package bot

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cfgscanbot/internal/countries"
	"cfgscanbot/internal/engine"
)

const (
	BotVersion = "1.1.0"
	// DefaultCaptionTemplate mirrors the app's caption template.
	DefaultCaptionTemplate = "NpvTunnel [6050626661043411760]  \n[5395616385734833119] لوکیشن | Location {{FLAGS}}\n\n[617260195842813119] @Wpnfa  \n\n[5206607083980820]  \n#npvtunnel #vpn #v2ray\n#فیلترشکن #vpn #پروکسی"
	// tgTextLimit is Telegram's 4096 message cap minus a safety margin.
	tgTextLimit = 4000
)

// Settings is the persisted bot configuration.
type Settings struct {
	Parallel       int     `json:"parallel"`
	TimeoutSec     int     `json:"timeout_sec"`
	OutLang        string  `json:"out_lang"` // fa | en
	Channel        string  `json:"channel"`
	IncludeChannel bool    `json:"include_channel"`
	IncludeUnknown bool    `json:"include_unknown"`
	Admins         []int64 `json:"admins"` // legacy list — kept in sync for rollback

	AdminsV2        []AdminUser       `json:"admins_v2"` // per-admin permission level
	CaptionTemplate string            `json:"caption_template"`
	EmojiCodes      map[string]string `json:"emoji_codes"` // ISO -> custom emoji id
	MessageForUsers string            `json:"message_for_users"`
}

// AdminUser is one extra admin with a permission level.
type AdminUser struct {
	ID   int64  `json:"id"`
	Perm string `json:"perm"` // PermFull | PermScan
}

const (
	// PermFull: every section (settings, caption, users message…) except
	// owner-only admin management.
	PermFull = "full"
	// PermScan: only the scan flow — submit configs, run, clear, log —
	// plus the scan output itself (links, caption, users message).
	PermScan = "scan"
)

func adminPermLabel(perm string) string {
	if perm == PermScan {
		return "📡 فقط اسکن"
	}
	return "👑 کامل"
}

func (s *Settings) defaults() {
	if s.Parallel <= 0 {
		s.Parallel = 4
	}
	if s.TimeoutSec <= 0 {
		s.TimeoutSec = 10
	}
	if s.OutLang != "fa" && s.OutLang != "en" {
		s.OutLang = "fa"
	}
	if s.EmojiCodes == nil {
		s.EmojiCodes = map[string]string{}
	}
	if s.CaptionTemplate == "" {
		s.CaptionTemplate = DefaultCaptionTemplate
	}
}

type awaiting int

const (
	awaitNone awaiting = iota
	awaitCaptionTemplate
	awaitChannelName
	awaitAdminAdd
	awaitCodesImport
	awaitCodeSet
	awaitMessageForUsers
)

type Bot struct {
	api     *tgAPI
	ownerID int64
	dataDir string
	xrayBin string
	hyBin   string

	mu         sync.Mutex
	settings   Settings
	running    bool
	pending    []string // raw config lines awaiting confirmation
	runMessage int      // progress message id
	runChat    int64
	awaiting   awaiting
	awaitISO   string // target country for awaitCodeSet
	awaitAdmin int64  // admin id awaiting a permission choice

	lastCodes []string
	lastFlags []string
	lastLog   []string
}

func NewBot(token string, ownerID int64, dataDir, xrayBin, hyBin string) *Bot {
	if xrayBin == "" {
		xrayBin = "xray"
	}
	if hyBin == "" {
		hyBin = "hysteria"
	}
	b := &Bot{
		api:     newAPI(token),
		ownerID: ownerID,
		dataDir: dataDir,
		xrayBin: xrayBin,
		hyBin:   hyBin,
	}
	b.load()
	return b
}

func (b *Bot) load() {
	if b.dataDir == "" {
		return
	}
	bb, err := os.ReadFile(filepath.Join(b.dataDir, "settings.json"))
	if err != nil {
		return
	}
	_ = json.Unmarshal(bb, &b.settings)
	// migrate the legacy flat admin list to per-admin permission levels
	if len(b.settings.AdminsV2) == 0 {
		for _, id := range b.settings.Admins {
			b.settings.AdminsV2 = append(b.settings.AdminsV2, AdminUser{ID: id, Perm: PermFull})
		}
	}
	b.settings.defaults()
}

func (b *Bot) save() {
	if b.dataDir == "" {
		return
	}
	_ = os.MkdirAll(b.dataDir, 0o755)
	bb, _ := json.MarshalIndent(b.settings, "", "  ")
	_ = os.WriteFile(filepath.Join(b.dataDir, "settings.json"), bb, 0o644)
}

func (b *Bot) settingsLocked() Settings {
	b.settings.defaults()
	return b.settings
}

// ------------------------------------------------------------------
// access control

// isAllowed: owner or any admin (either permission level) — i.e. the bot
// answers to them at all.
func (b *Bot) isAllowed(id int64) bool {
	if id == b.ownerID {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range b.settings.Admins {
		if a == id {
			return true
		}
	}
	for _, a := range b.settings.AdminsV2 {
		if a.ID == id {
			return true
		}
	}
	return false
}

// isFullAdmin: owner or an admin with full permission — everything except
// owner-only admin management. Only the v2 list decides: the legacy flat
// list is kept on disk purely so an older binary still boots.
func (b *Bot) isFullAdmin(id int64) bool {
	if id == b.ownerID {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range b.settings.AdminsV2 {
		if a.ID == id && a.Perm != PermScan {
			return true
		}
	}
	return false
}

// adminUser returns the AdminUser entry for id (false when not in the v2 list).
func (b *Bot) adminUser(id int64) (AdminUser, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range b.settings.AdminsV2 {
		if a.ID == id {
			return a, true
		}
	}
	return AdminUser{}, false
}

// addOrUpdateAdmin inserts or updates one admin and keeps the legacy flat
// list in sync (older binaries keep working after a rollback).
func (b *Bot) addOrUpdateAdmin(id int64, perm string) {
	b.mu.Lock()
	replaced := false
	for i := range b.settings.AdminsV2 {
		if b.settings.AdminsV2[i].ID == id {
			b.settings.AdminsV2[i].Perm = perm
			replaced = true
		}
	}
	if !replaced {
		b.settings.AdminsV2 = append(b.settings.AdminsV2, AdminUser{ID: id, Perm: perm})
	}
	legacy := false
	for _, a := range b.settings.Admins {
		if a == id {
			legacy = true
		}
	}
	if !legacy {
		b.settings.Admins = append(b.settings.Admins, id)
	}
	b.save()
	b.mu.Unlock()
}

// removeAdminID drops the admin from both lists; false when it wasn't there.
func (b *Bot) removeAdminID(id int64) bool {
	b.mu.Lock()
	found := false
	out := b.settings.AdminsV2[:0]
	for _, a := range b.settings.AdminsV2 {
		if a.ID == id {
			found = true
			continue
		}
		out = append(out, a)
	}
	b.settings.AdminsV2 = out
	lout := b.settings.Admins[:0]
	for _, a := range b.settings.Admins {
		if a == id {
			found = true
			continue
		}
		lout = append(lout, a)
	}
	b.settings.Admins = lout
	b.save()
	b.mu.Unlock()
	return found
}

// ------------------------------------------------------------------
// Reply Keyboards (Fixed Persistent Bottom Buttons)

func (b *Bot) replyMainMenuFor(id int64, pendingCount int) [][]string {
	if !b.isFullAdmin(id) {
		// scan-only admin: only the scan flow
		return b.replyScanMainMenu(pendingCount)
	}
	var rows [][]string
	if pendingCount > 0 {
		rows = append(rows, []string{
			fmt.Sprintf("▶️ شروع اسکن (%d کانفیگ)", pendingCount),
			"🗑️ پاک کردن لیست",
		})
	}
	rows = append(rows, []string{"📡 اسکن کانفیگ", "⚙️ تنظیمات"})
	rows = append(rows, []string{"🏷️ کپشن و پرچم", "✏️ پیام برای کاربران"})
	rows = append(rows, []string{"📊 گزارش / لاگ", "ℹ️ راهنما و درباره"})
	if id == b.ownerID {
		rows = append(rows, []string{"👥 ادمین‌ها"})
	}
	return rows
}

// replyScanMainMenu is the reduced persistent keyboard for scan-only admins.
func (b *Bot) replyScanMainMenu(pendingCount int) [][]string {
	var rows [][]string
	if pendingCount > 0 {
		rows = append(rows, []string{
			fmt.Sprintf("▶️ شروع اسکن (%d کانفیگ)", pendingCount),
			"🗑️ پاک کردن لیست",
		})
	}
	rows = append(rows, []string{"📥 ارسال کانفیگ", "📊 گزارش / لاگ"})
	return rows
}

// replyScanMenu is the section keyboard shown while the user is inside the
// scan section — the main-menu buttons disappear until they go back.
func (b *Bot) replyScanMenu() [][]string {
	return [][]string{
		{"▶️ شروع اسکن", "🗑️ پاک کردن لیست"},
		{"📥 ارسال کانفیگ", "📊 گزارش / لاگ"},
		{"↩️ بازگشت به منوی اصلی"},
	}
}

func (b *Bot) replySettingsMenu() [][]string {
	b.mu.Lock()
	s := b.settingsLocked()
	b.mu.Unlock()

	langName := "فارسی"
	if s.OutLang == "en" {
		langName = "English"
	}
	chState := "خاموش"
	if s.IncludeChannel {
		if s.Channel != "" {
			chState = "روشن (" + s.Channel + ")"
		} else {
			chState = "روشن (بدون نام)"
		}
	}
	unkState := "خاموش"
	if s.IncludeUnknown {
		unkState = "روشن"
	}

	return [][]string{
		{fmt.Sprintf("🔢 همزمانی: %d", s.Parallel), fmt.Sprintf("⏱️ تایم‌اوت: %d ثانیه", s.TimeoutSec)},
		{fmt.Sprintf("🌐 زبان: %s", langName), fmt.Sprintf("📢 سافیکس: %s", chState)},
		{fmt.Sprintf("🔗 خروجی بدون کشور: %s", unkState), "📢 تنظیم نام کانال"},
		{"↩️ بازگشت به منوی اصلی"},
	}
}

func (b *Bot) replyCaptionMenu() [][]string {
	return [][]string{
		{"✏️ ویرایش قالب کپشن", "🎨 کدهای ایموجی کشورها"},
		{"👁️ پیش‌نمایش کپشن", "↩️ بازگردانی قالب پیش‌فرض"},
		{"📥 وارد کردن دسته‌ای کدها", "📤 خروجی گرفتن کدها"},
		{"↩️ بازگشت به منوی اصلی"},
	}
}

func (b *Bot) replyMsgForUsersMenu() [][]string {
	return [][]string{
		{"✏️ تنظیم / ویرایش متن", "👁️ مشاهده متن فعلی"},
		{"🗑️ حذف متن پیام", "↩️ بازگشت به منوی اصلی"},
	}
}

func (b *Bot) replyAdminMenu() [][]string {
	return [][]string{
		{"➕ افزودن ادمین", "📋 لیست ادمین‌ها"},
		{"↩️ بازگشت به منوی اصلی"},
	}
}

// adminListKeyboard: one inline row per admin — toggle permission + remove.
func (b *Bot) adminListKeyboard() [][]string {
	b.mu.Lock()
	admins := append([]AdminUser(nil), b.settings.AdminsV2...)
	b.mu.Unlock()
	var rows [][]string
	for _, a := range admins {
		id := fmt.Sprintf("%d", a.ID)
		if a.Perm == PermScan {
			rows = append(rows, []string{
				"👑 تغییر به کامل", "admtoggle:" + id,
				"🗑️ حذف", "admdel:" + id,
			})
		} else {
			rows = append(rows, []string{
				"📡 تغییر به فقط‌اسکن", "admtoggle:" + id,
				"🗑️ حذف", "admdel:" + id,
			})
		}
	}
	return rows
}

func (b *Bot) mainIntro() string {
	b.mu.Lock()
	p := len(b.pending)
	b.mu.Unlock()

	status := ""
	if p > 0 {
		status = fmt.Sprintf("\n📥 <b>%d کانفیگ در صف آماده اسکن است.</b> دکمه‌ی «▶️ شروع اسکن» را بزنید.\n", p)
	}

	return "📡 <b>ConfigScanner Bot</b> <code>v" + BotVersion + "</code>\n\n" +
		"اسکنر خروجی کانفیگ‌ها با هسته رسمی Xray و Hysteria:\n" +
		"• تست کامل اتصال و تخصیص اتمیک پورت\n" +
		"• استعلام موازی ۶ سرویس جیو و نام‌گذاری دقیق کشورها\n" +
		"• ارسال لینک‌های سالم، پرچم‌های استاندارد، کپشن و پیام کاربران\n" +
		status + "\n" +
		"کانفیگ‌ها را پیست کنید یا از منوی پایین انتخاب فرمایید 👇"
}

func (b *Bot) sendMain(c chat, intro string) {
	if intro == "" {
		intro = b.mainIntro()
	}
	b.mu.Lock()
	b.awaiting = awaitNone
	b.awaitISO = ""
	b.awaitAdmin = 0
	p := len(b.pending)
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID, intro, b.replyMainMenuFor(c.ID, p))
}

// ------------------------------------------------------------------
// scan flow

func (b *Bot) onScanRequest(c chat) {
	b.mu.Lock()
	p := len(b.pending)
	b.mu.Unlock()

	msg := "📡 <b>اسکن کانفیگ</b>\n\n" +
		"کانفیگ‌ها را <b>پیست</b> کنید یا <b>فایل .txt</b> بفرستید — هر خط یک کانفیگ:\n" +
		"<code>vless · vmess · trojan · ss · hy2 · ssr · tuic</code>\n\n" +
		"💡 می‌توانید چند لیست را پشت‌سرهم بفرستید؛ همه با هم جمع می‌شوند."
	if p > 0 {
		msg += fmt.Sprintf("\n\n📥 <b>هم‌اکنون %d کانفیگ در صف آماده است.</b>", p)
	}
	// scan section keyboard: only scan buttons until «بازگشت به منوی اصلی»
	_, _ = b.api.sendWithReplyKeyboard(c.ID, msg, b.replyScanMenu())
}

func (b *Bot) onConfigInput(c chat, raw string) {
	// keep only lines that actually parse — the queue count shown to the
	// user must match what the scan will really test (ParseInput drops
	// unrecognized lines at run time, which used to cause "5 کانفیگ آماده"
	// followed by "هیچ خط قابل شناسایی نبود")
	var lines []string
	for _, l := range splitConfigLines(raw) {
		if engine.ParseOne(l) != nil {
			lines = append(lines, l)
		}
	}
	b.mu.Lock()
	running := b.running
	b.mu.Unlock()
	if running {
		_, _ = b.api.sendMessage(c.ID, "⏳ یک اسکن در حال اجراست — لطفاً تا پایان صبر کنید.")
		return
	}
	if len(lines) == 0 {
		_, _ = b.api.sendWithReplyKeyboard(c.ID,
			"🤔 کانفیگی در متن پیدا نشد.\nهر خط باید با یکی از این پروتکل‌ها شروع شود:\n<code>vless:// · vmess:// · trojan:// · ss:// · hysteria2://</code>",
			b.replyScanMenu())
		return
	}

	b.mu.Lock()
	b.pending = append(b.pending, lines...)
	total := len(b.pending)
	s := b.settingsLocked()
	b.mu.Unlock()

	confirm := fmt.Sprintf("✅ <b>%d کانفیگ آماده اسکن</b>\n\n"+
		"⏱️ زمان تقریبی: %s\n\n"+
		"👇 برای شروع اسکن، دکمه‌ی ثابت <b>«▶️ شروع اسکن»</b> را در پایین صفحه لمس کنید:",
		total, estimateDuration(total, s.Parallel))

	_, _ = b.api.sendWithReplyKeyboard(c.ID, confirm, b.replyScanMenu())
}

func (b *Bot) cancelPending(c chat) {
	b.mu.Lock()
	b.pending = nil
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID, "🗑️ لیست کانفیگ‌ها پاک شد.", b.replyMainMenuFor(c.ID, 0))
}

func (b *Bot) startRun(c chat) {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		_, _ = b.api.sendMessage(c.ID, "⏳ اسکن در حال اجراست — لطفاً صبر کنید.")
		return
	}
	if len(b.pending) == 0 {
		b.mu.Unlock()
		_, _ = b.api.sendWithReplyKeyboard(c.ID, "🤔 ابتدا کانفیگ‌ها را بفرستید یا پیست کنید.", b.replyScanMenu())
		return
	}
	raw := strings.Join(b.pending, "\n")
	b.pending = nil
	b.running = true
	b.runChat = c.ID
	b.lastLog = nil
	cfg := b.settingsLocked()
	b.mu.Unlock()

	servers := engine.ParseInput(raw)
	if len(servers) == 0 {
		b.finishRunGuard()
		_, _ = b.api.sendWithReplyKeyboard(c.ID, "🤔 هیچ خط قابل شناسایی نبود.", b.replyScanMenu())
		return
	}

	intro := fmt.Sprintf("🚀 <b>اسکن شروع شد</b>\n\n"+
		"📦 %d سرور · ⚙️ %d همزمان · ⏱️ تایم‌اوت %d ثانیه\n\n"+
		"%s\n\n⏳ در حال اجرا…",
		len(servers), cfg.Parallel, cfg.TimeoutSec, progressBar(0, len(servers)))

	progMsgID, err := b.api.sendMessage(c.ID, intro)
	if err != nil {
		b.runLog("error sending start message: " + err.Error())
	}
	b.mu.Lock()
	b.runMessage = progMsgID
	b.mu.Unlock()

	eng := engine.NewEngine(engine.Config{
		XrayBin:        b.xrayBin,
		HysteriaBin:    b.hyBin,
		WorkDir:        filepath.Join(os.TempDir(), "cfgscanbot"),
		Parallel:       cfg.Parallel,
		TimeoutSec:     cfg.TimeoutSec,
		OutLang:        cfg.OutLang,
		Channel:        cfg.Channel,
		IncludeChannel: cfg.IncludeChannel,
		IncludeUnknown: cfg.IncludeUnknown,
		Logf:           b.runLog,
	})

	var (
		lastEdit time.Time
		lastLine string
		editMu   sync.Mutex
	)

	res := eng.Run(servers, func(d, tot int, hostport, mark string) {
		editMu.Lock()
		defer editMu.Unlock()
		lastLine = mark + " " + hostport
		if time.Since(lastEdit) < 1500*time.Millisecond && d < tot {
			return
		}
		lastEdit = time.Now()
		b.mu.Lock()
		msgID := b.runMessage
		chatID := b.runChat
		b.mu.Unlock()
		if msgID <= 0 {
			return
		}
		pct := 0
		if tot > 0 {
			pct = d * 100 / tot
		}
		text := fmt.Sprintf(
			"🔍 <b>در حال اسکن…</b> %d%%\n\n%s\n\n📦 %d از %d\n%s\n\n⚙️ %d همزمان · ⏱️ %d ثانیه",
			pct, progressBar(d, tot), d, tot, escapeHTML(lastLine), cfg.Parallel, cfg.TimeoutSec)
		_ = b.api.editText(msgID, chatID, text)
	})

	b.mu.Lock()
	b.lastCodes = res.CountryCodes
	b.lastFlags = res.Flags
	msgID := b.runMessage
	chatID := b.runChat
	b.mu.Unlock()

	if msgID > 0 {
		_ = b.api.editText(msgID, chatID, fmt.Sprintf(
			"✅ <b>اسکن تمام شد</b>\n\n%s", res.Summary(cfg.OutLang)))
	}

	// 1) Summary Table / Box (Clean Modern Table Layout in blockquote)
	summaryMsg := fmt.Sprintf(
		"<blockquote>\n"+
			"📊 <b>گزارش نتیجه اسکن</b>\n"+
			"─────────────────────\n"+
			"🟢 <b>سالم و متصل:</b> %d سرور\n"+
			"🟡 <b>بدون لوکیشن:</b> %d سرور\n"+
			"🔴 <b>غیرقابل دسترس:</b> %d سرور\n"+
			"⚪ <b>ردشده:</b> %d سرور\n"+
			"─────────────────────\n"+
			"🌍 <b>تعداد کشورها:</b> %d کشور\n"+
			"⚙️ <b>همزمانی:</b> %d موازی · ⏱️ <b>تایم‌اوت:</b> %d ثانیه\n"+
			"</blockquote>",
		res.OK, res.NoCountry, res.Unreachable, res.Skipped,
		len(res.CountryCodes), cfg.Parallel, cfg.TimeoutSec)
	_, _ = b.api.sendMessage(chatID, summaryMsg)

	// 2) Working links only (clean output in <pre> or .txt file)
	if len(res.Links) > 0 {
		linksBody := strings.Join(res.Links, "\n")
		if fitsMsg(linksBody) {
			_, _ = b.api.sendMessage(chatID, "<pre>"+escapeHTML(linksBody)+"</pre>")
		} else {
			_ = b.api.sendDocument(chatID, []byte(linksBody), "configs.txt", fmt.Sprintf("🔗 %d لینک سالم", len(res.Links)))
		}
	} else {
		_, _ = b.api.sendMessage(chatID, "❌ هیچ کانفیگ سالمی در این اسکن یافت نشد.")
	}

	// 3) Standard Country Emojis (اموجی معمولی کشور)
	if len(res.CountryCodes) > 0 {
		var flagList []string
		for _, iso := range res.CountryCodes {
			flagList = append(flagList, engine.Flag(iso))
		}
		flagsText := strings.Join(flagList, " ")
		_, _ = b.api.sendMessage(chatID, "<code>"+flagsText+"</code>")
	}

	// 4) Caption — Sent ALONE without header text so copying copies ONLY the caption
	if len(res.CountryCodes) > 0 && cfg.CaptionTemplate != "" {
		caption := b.buildCaption(res.CountryCodes)
		if caption != "" {
			_, _ = b.api.sendMessage(chatID, escapeHTML(caption))
		}
	}

	// 5) Message for Users — Sent ALONE without header text
	if strings.TrimSpace(cfg.MessageForUsers) != "" {
		_, _ = b.api.sendMessage(chatID, escapeHTML(cfg.MessageForUsers))
	}

	// 6) Missing emoji codes report with DIRECT ACTION BUTTONS underneath
	var missing []string
	if res.CountryCodes != nil {
		b.mu.Lock()
		codes := b.settings.EmojiCodes
		b.mu.Unlock()
		for _, iso := range res.CountryCodes {
			if codes[iso] == "" {
				missing = append(missing, iso)
			}
		}
	}
	if len(missing) > 0 {
		var lines []string
		var btnRows [][]string
		var currentRow []string

		for i, iso := range missing {
			flag := engine.Flag(iso)
			nm, ok := countries.Names(iso, cfg.OutLang)
			if !ok {
				nm = iso
			}
			lines = append(lines, fmt.Sprintf("%d. %s %s (<code>%s</code>)", i+1, flag, nm, iso))

			currentRow = append(currentRow, "✏️ "+flag+" "+nm, "setcode:"+iso)
			if len(currentRow) == 4 { // 2 buttons per row
				btnRows = append(btnRows, currentRow)
				currentRow = nil
			}
		}
		if len(currentRow) > 0 {
			btnRows = append(btnRows, currentRow)
		}
		btnRows = append(btnRows, []string{"📥 وارد کردن دسته‌ای کدها", "cap:import"})

		msg := "🎨 <b>این کشورها کد ایموجی ندارند:</b>\n\n" +
			strings.Join(lines, "\n") + "\n\n" +
			"در کپشن برای این‌ها <code>[]</code> خالی قرار گرفته است.\n" +
			"👇 روی دکمه‌ی هر کشور در زیر بزنید تا کد آن را مستقیماً وارد کنید:"

		_, _ = b.api.sendWithKeyboard(chatID, msg, btnRows, "sendMessage")
	}

	b.finishRunGuard()
	b.sendMain(c, "✅ <b>پایان اسکن.</b> برای اسکن بعدی کانفیگ‌ها را بفرستید یا از منو انتخاب کنید:")
}

func fitsMsg(s string) bool {
	return len([]rune(s)) <= tgTextLimit
}

func progressBar(done, total int) string {
	if total <= 0 {
		return ""
	}
	const cells = 20
	filled := (done*cells + total/2) / total
	if filled > cells {
		filled = cells
	}
	return strings.Repeat("🟩", filled) + strings.Repeat("⬜", cells-filled)
}

func (b *Bot) finishRunGuard() {
	b.mu.Lock()
	b.running = false
	b.mu.Unlock()
}

func (b *Bot) runLog(line string) {
	b.mu.Lock()
	b.lastLog = append(b.lastLog, "["+time.Now().Format("15:04:05")+"] "+line)
	b.mu.Unlock()
	fmt.Println("[" + time.Now().Format("15:04:05") + "] " + line)
}

func (b *Bot) sendRunLog(c chat) {
	b.mu.Lock()
	logs := append([]string(nil), b.lastLog...)
	b.mu.Unlock()
	if len(logs) == 0 {
		_, _ = b.api.sendMessage(c.ID, "📄 لاگی ثبت نشده — ابتدا یک اسکن انجام دهید.")
		return
	}
	body := strings.Join(logs, "\n")
	if fitsMsg(body) {
		_, _ = b.api.sendMessage(c.ID, "📄 <b>لاگ آخرین اجرا:</b>\n\n<pre>"+escapeHTML(body)+"</pre>")
		return
	}
	_ = b.api.sendDocument(c.ID, []byte(body), "cfgscan_run.log",
		"📄 <b>لاگ آخرین اجرا</b> — به‌صورت فایل")
}

// ------------------------------------------------------------------
// caption

func (b *Bot) showCaptionMenu(c chat) {
	b.mu.Lock()
	b.awaiting = awaitNone
	b.awaitISO = ""
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		"🏷️ <b>کپشن و پرچم</b>\n\n"+
			"• <b>قالب کپشن</b>: متنی که زیر پست کانفیگ‌ها قرار می‌گیرد (جای <code>{{FLAGS}}</code> با پرچم‌ها پر می‌شود).\n"+
			"• <b>کد ایموجی</b>: کدهای عددی پرچم‌های متحرک هر کشور (همان تب Caption اپلیکیشن).\n"+
			"• <b>پیش‌نمایش</b>: مشاهده کپشن نهایی با کشورهای اسکن قبلی.\n\n"+
			"یکی از گزینه‌های زیر را انتخاب کنید:",
		b.replyCaptionMenu())
}

func (b *Bot) captionEditPrompt(c chat) {
	b.mu.Lock()
	s := b.settingsLocked()
	b.awaiting = awaitCaptionTemplate
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		"✏️ <b>ویرایش قالب کپشن</b>\n\n"+
			"قالب فعلی:\n<pre>"+escapeHTML(s.CaptionTemplate)+"</pre>\n\n"+
			"متن قالب جدید را بفرستید (باید شامل <code>{{FLAGS}}</code> باشد).\n"+
			"برای لغو، «↩️ بازگشت به منوی اصلی» را بزنید.",
		[][]string{{"↩️ بازگشت به منوی اصلی"}})
}

func (b *Bot) onCaptionTemplate(c chat, text string) {
	b.mu.Lock()
	if strings.Contains(text, "{{FLAGS}}") {
		b.settings.CaptionTemplate = text
	} else {
		b.settings.CaptionTemplate = text + "\n{{FLAGS}}"
	}
	b.save()
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID, "✅ قالب کپشن با موفقیت ذخیره شد.", b.replyCaptionMenu())
}

func (b *Bot) captionPreview(c chat) {
	b.mu.Lock()
	codes := b.lastCodes
	b.mu.Unlock()
	if len(codes) == 0 {
		_, _ = b.api.sendMessage(c.ID, "ابتدا یک اسکن انجام دهید تا کشورهای فعال مشخص شوند.")
		return
	}
	caption := b.buildCaption(codes)
	if caption == "" {
		_, _ = b.api.sendMessage(c.ID, "کپشنی ساخته نشد.")
		return
	}
	if fitsMsg(caption) {
		_, _ = b.api.sendMessage(c.ID, "👁️ <b>پیش‌نمایش کپشن:</b>\n\n"+escapeHTML(caption))
		return
	}
	_ = b.api.sendDocument(c.ID, []byte(caption), "caption_preview.txt", "👁️ پیش‌نمایش کپشن")
}

func (b *Bot) buildCaption(codes []string) string {
	b.mu.Lock()
	s := b.settingsLocked()
	b.mu.Unlock()
	var sb strings.Builder
	for _, iso := range codes {
		if id, ok := s.EmojiCodes[iso]; ok && id != "" {
			sb.WriteString("[")
			sb.WriteString(id)
			sb.WriteString("]")
		}
	}
	flagsLine := sb.String()
	if strings.Contains(s.CaptionTemplate, "{{FLAGS}}") {
		return strings.ReplaceAll(s.CaptionTemplate, "{{FLAGS}}", flagsLine)
	}
	return s.CaptionTemplate + "\n" + flagsLine
}

func (b *Bot) onCodesMenu(c chat) {
	b.mu.Lock()
	s := b.settingsLocked()
	codes := b.lastCodes
	b.mu.Unlock()

	var sb strings.Builder
	sb.WriteString("🎨 <b>کد ایموجی کشورها</b>\n\n")
	sb.WriteString("📊 تعداد کدهای ثبت‌شده: <b>" + itoaSafe(len(s.EmojiCodes)) + "</b> کشور\n\n")
	if len(codes) > 0 {
		sb.WriteString("🌍 <b>کشورهای آخرین اسکن:</b>\n")
		for _, iso := range codes {
			flag := engine.Flag(iso)
			name, ok := countries.Names(iso, s.OutLang)
			if !ok {
				name = iso
			}
			if v, has := s.EmojiCodes[iso]; has && v != "" {
				sb.WriteString(fmt.Sprintf("• %s %s (%s) ➔ <code>%s</code>\n", flag, name, iso, v))
			} else {
				sb.WriteString(fmt.Sprintf("• %s %s (%s) ➔ <b>بدون کد</b>\n", flag, name, iso))
			}
		}
		sb.WriteString("\nبرای ثبت یا تغییر کد، می‌توانید پیام را با فرمت <code>XX=code</code> بفرستید (مثلاً <code>DE=5390843037349679256</code>).\n")
	} else {
		sb.WriteString("می‌توانید کدهای بکاپ اپ را با دکمه‌ی «📥 وارد کردن دسته‌ای کدها» وارد کنید یا کد هر کشور را با فرمت <code>XX=code</code> بفرستید.\n")
	}

	_, _ = b.api.sendWithReplyKeyboard(c.ID, sb.String(), b.replyCaptionMenu())
}

func (b *Bot) importPrompt(c chat) {
	b.mu.Lock()
	b.awaiting = awaitCodesImport
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		"📥 <b>وارد کردن دسته‌ای کدها</b>\n\n"+
			"در اپلیکیشن: تب <b>Caption</b> ➔ دکمه‌ی «کپی کدها» ➔ اینجا <b>پیست</b> کنید — یا فایل .txt را بفرستید.\n\n"+
			"فرمت: هر خط <code>XX=کد</code>، مانند:\n<code>DE=6050626661043411760\nFR=5395616385734833119</code>",
		[][]string{{"↩️ بازگشت به منوی اصلی"}})
}

func (b *Bot) onCodesImport(c chat, text string) {
	b.mu.Lock()
	if b.settings.EmojiCodes == nil {
		b.settings.EmojiCodes = map[string]string{}
	}
	added := 0
	for _, line := range strings.Split(html.UnescapeString(text), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.ToUpper(strings.TrimSpace(kv[0]))
		v := strings.TrimSpace(kv[1])
		if len(k) == 2 && v != "" {
			if _, had := b.settings.EmojiCodes[k]; !had {
				added++
			}
			b.settings.EmojiCodes[k] = v
		}
	}
	total := len(b.settings.EmojiCodes)
	b.save()
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		fmt.Sprintf("✅ <b>%d کد</b> ذخیره شد. مجموع کدهای ثبت‌شده: <b>%d</b>", added, total),
		b.replyCaptionMenu())
}

// setEmojiCodeDirect handles the single-line quick setting "DE=5390843037349679256".
func (b *Bot) setEmojiCodeDirect(c chat, iso, val string) {
	b.mu.Lock()
	if b.settings.EmojiCodes == nil {
		b.settings.EmojiCodes = map[string]string{}
	}
	del := strings.EqualFold(val, "delete")
	if del {
		delete(b.settings.EmojiCodes, iso)
	} else {
		b.settings.EmojiCodes[iso] = val
	}
	b.save()
	b.mu.Unlock()
	flag := engine.Flag(iso)
	nm, _ := countries.Names(iso, "fa")
	var msg string
	if del {
		msg = fmt.Sprintf("🗑️ کد ایموجی %s %s (%s) حذف شد.", flag, nm, iso)
	} else {
		msg = fmt.Sprintf("✅ کد ایموجی %s %s (%s) ذخیره شد: <code>%s</code>", flag, nm, iso, escapeHTML(val))
	}
	_, _ = b.api.sendMessage(c.ID, msg)
}

// looksLikeCodesList reports whether the text contains at least one
// "XX=value" line (a batch country-codes import pasted directly).
func looksLikeCodesList(s string) bool {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		kv := strings.SplitN(l, "=", 2)
		if len(kv) == 2 && len(strings.TrimSpace(kv[0])) == 2 {
			return true
		}
	}
	return false
}

func (b *Bot) exportCodesText() string {
	b.mu.Lock()
	s := b.settingsLocked()
	b.mu.Unlock()
	if len(s.EmojiCodes) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# ConfigScanner country emoji codes\n")
	for _, c := range countries.All() {
		if v, ok := s.EmojiCodes[c.Code]; ok && v != "" {
			sb.WriteString(c.Code + "=" + v + "\n")
		}
	}
	return sb.String()
}

func (b *Bot) onCodesExport(c chat) {
	t := b.exportCodesText()
	if t == "" {
		_, _ = b.api.sendWithReplyKeyboard(c.ID, "🤔 هنوز کدی ثبت نشده است.", b.replyCaptionMenu())
		return
	}
	_, _ = b.api.sendMessage(c.ID,
		"📤 <b>خروجی کدهای ایموجی</b> (فرمت مشترک اپ و ربات):\n\n<pre>"+escapeHTML(t)+"</pre>")
}

func (b *Bot) setCodePrompt(c chat, iso string) {
	iso = strings.ToUpper(strings.TrimSpace(iso))
	b.mu.Lock()
	b.awaiting = awaitCodeSet
	b.awaitISO = iso
	b.mu.Unlock()

	flag := engine.Flag(iso)
	name, _ := countries.Names(iso, "fa")
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		fmt.Sprintf("✏️ <b>ثبت کد ایموجی برای %s %s (%s)</b>\n\n"+
			"کد عددی ایموجی را ارسال کنید (مثلاً عدد را از تب Caption اپلیکیشن کپی کرده و بفرستید).\n"+
			"برای حذف کد این کشور، عبارت <code>delete</code> را بفرستید.\n\n"+
			"برای لغو، دکمه‌ی «↩️ بازگشت به منوی اصلی» را بزنید.", flag, name, iso),
		[][]string{{"↩️ بازگشت به منوی اصلی"}})
}

func (b *Bot) onSetCode(c chat, iso, text string) {
	iso = strings.ToUpper(strings.TrimSpace(iso))
	text = strings.TrimSpace(text)
	b.mu.Lock()
	if b.settings.EmojiCodes == nil {
		b.settings.EmojiCodes = map[string]string{}
	}
	if strings.EqualFold(text, "delete") {
		delete(b.settings.EmojiCodes, iso)
	} else if text != "" {
		b.settings.EmojiCodes[iso] = text
	}
	b.save()
	b.mu.Unlock()

	flag := engine.Flag(iso)
	name, _ := countries.Names(iso, "fa")
	var msg string
	if strings.EqualFold(text, "delete") {
		msg = fmt.Sprintf("🗑️ کد ایموجی %s %s (%s) حذف شد.", flag, name, iso)
	} else {
		msg = fmt.Sprintf("✅ کد ایموجی %s %s (%s) با موفقیت ذخیره شد: <code>%s</code>", flag, name, iso, escapeHTML(text))
	}
	_, _ = b.api.sendWithReplyKeyboard(c.ID, msg, b.replyMainMenuFor(c.ID, 0))
}

// ------------------------------------------------------------------
// Message For Users (پیام برای کاربران / داخل کانفیگ)

func (b *Bot) showMsgForUsersMenu(c chat) {
	b.mu.Lock()
	b.awaiting = awaitNone
	b.awaitISO = ""
	s := b.settingsLocked()
	b.mu.Unlock()

	msg := "✏️ <b>پیام برای کاربران | Message for users</b>\n\n" +
		"این پیام پس از اتمام اسکن، به عنوان یک پیام جداگانه ارسال می‌شود تا بتوانید آن را به راحتی کپی کنید و در کانال یا توضیحات کانفیگ قرار دهید.\n\n"
	if strings.TrimSpace(s.MessageForUsers) != "" {
		msg += "📄 <b>متن فعلی:</b>\n<pre>" + escapeHTML(s.MessageForUsers) + "</pre>"
	} else {
		msg += "<i>(هنوز متنی ثبت نشده است)</i>"
	}

	_, _ = b.api.sendWithReplyKeyboard(c.ID, msg, b.replyMsgForUsersMenu())
}

func (b *Bot) msgForUsersPrompt(c chat) {
	b.mu.Lock()
	b.awaiting = awaitMessageForUsers
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		"✏️ <b>تنظیم متن پیام برای کاربران</b>\n\n"+
			"متن دلخواه خود را ارسال کنید (مثلاً لینک کانال، توضیحات، آیدی و ...):\n\n"+
			"<pre>🎯 ساخته شده توسط کانال | Made by @wpnfa\n\nT.me/wpnfa\n\n🌍 آپدیت روزانه | Daily update</pre>",
		[][]string{{"↩️ بازگشت به منوی اصلی"}})
}

func (b *Bot) onSetMessageForUsers(c chat, text string) {
	b.mu.Lock()
	b.settings.MessageForUsers = text
	b.save()
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		"✅ <b>متن پیام برای کاربران ذخیره شد:</b>\n\n<pre>"+escapeHTML(text)+"</pre>",
		b.replyMsgForUsersMenu())
}

func (b *Bot) onClearMessageForUsers(c chat) {
	b.mu.Lock()
	b.settings.MessageForUsers = ""
	b.save()
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID, "🗑️ پیام برای کاربران حذف شد.", b.replyMsgForUsersMenu())
}

// ------------------------------------------------------------------
// settings

var (
	parallelSteps = []int{2, 3, 4, 6, 8}
	timeoutSteps  = []int{5, 10, 15, 20, 30}
)

func (b *Bot) showSettingsMenu(c chat) {
	b.mu.Lock()
	b.awaiting = awaitNone
	b.awaitISO = ""
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		"⚙️ <b>تنظیمات ربات</b>\n\n"+
			"• <b>همزمانی</b>: تعداد سرورهایی که همزمان اسکن می‌شوند.\n"+
			"• <b>تایم‌اوت</b>: حداکثر زمان انتظار برای پاسخ هر سرور.\n"+
			"• <b>زبان اسم</b>: فارسی یا English برای نام کشورها.\n"+
			"• <b>سافیکس کانال</b>: اضافه کردن « | اسم_کانال» به انتهای نام کانفیگ‌ها.\n"+
			"• <b>خروجی بدون کشور</b>: اضافه کردن لینک‌هایی که کشورشان مشخص نشده در لیست خروجی.\n\n"+
			"روی گزینه‌های زیر لمس کنید تا تغییر کنند:",
		b.replySettingsMenu())
}

func (b *Bot) cycleSetting(c chat, key string) {
	b.mu.Lock()
	switch key {
	case "parallel":
		idx := indexInt(parallelSteps, b.settings.Parallel)
		b.settings.Parallel = parallelSteps[(idx+1)%len(parallelSteps)]
	case "timeout":
		idx := indexInt(timeoutSteps, b.settings.TimeoutSec)
		b.settings.TimeoutSec = timeoutSteps[(idx+1)%len(timeoutSteps)]
	case "lang":
		if b.settings.OutLang == "fa" {
			b.settings.OutLang = "en"
		} else {
			b.settings.OutLang = "fa"
		}
	case "channel":
		b.settings.IncludeChannel = !b.settings.IncludeChannel
	case "unknown":
		b.settings.IncludeUnknown = !b.settings.IncludeUnknown
	}
	b.save()
	b.mu.Unlock()
	b.showSettingsMenu(c)
	// every full admin sees the change immediately, not just the changer
	b.broadcastSettings(c.ID)
}

func (b *Bot) channelNamePrompt(c chat) {
	b.mu.Lock()
	b.awaiting = awaitChannelName
	s := b.settingsLocked()
	b.mu.Unlock()
	cur := s.Channel
	if cur == "" {
		cur = "(خالی)"
	}
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		"📢 <b>تنظیم نام کانال (سافیکس)</b>\n\n"+
			"نام فعلی: <code>"+escapeHTML(cur)+"</code>\n\n"+
			"نام جدید را بفرستید (مثلاً <code>Wpnfa</code>) تا به انتهای نام همه کانفیگ‌ها اضافه شود:\n"+
			"<pre>🇩🇪 آلمان | Wpnfa</pre>\n\n"+
			"برای حذف نام، عبارت <code>delete</code> را بفرستید.",
		[][]string{{"↩️ بازگشت به منوی اصلی"}})
}

// ------------------------------------------------------------------
// admins (owner only)

func (b *Bot) onAdminMenu(c chat) {
	if c.ID != b.ownerID {
		b.sendMain(c, "")
		return
	}
	b.mu.Lock()
	s := b.settingsLocked()
	admins := append([]AdminUser(nil), s.AdminsV2...)
	b.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("👥 <b>ادمین‌های ربات</b>\n\n")
	sb.WriteString("👑 مالک اصلی: <code>" + itoaSafe(int(b.ownerID)) + "</code>\n\n")
	if len(admins) == 0 {
		sb.WriteString("<i>(هیچ ادمین اضافی ثبت نشده است)</i>\n")
	} else {
		sb.WriteString("<b>لیست ادمین‌ها:</b>\n")
		for i, a := range admins {
			sb.WriteString(fmt.Sprintf("%d. <code>%d</code> — %s\n", i+1, a.ID, adminPermLabel(a.Perm)))
		}
	}
	sb.WriteString("\n📡 <b>فقط اسکن</b>: فقط ارسال کانفیگ، شروع اسکن و دریافت خروجی — بدون دسترسی به تنظیمات و بقیه بخش‌ها.\n" +
		"👑 <b>کامل</b>: همه بخش‌ها به‌جز مدیریت ادمین‌ها.\n\n" +
		"برای حذف سریع، <code>/rmadmin ID</code> را هم می‌توانید ارسال کنید.\n" +
		"برای تغییر سطح یا حذف، از دکمه‌های زیر استفاده کنید:")
	_, _ = b.api.sendWithKeyboard(c.ID, sb.String(), b.adminListKeyboard(), "sendMessage")
	_, _ = b.api.sendWithReplyKeyboard(c.ID, "👇 برای افزودن ادمین جدید روی «➕ افزودن ادمین» بزنید یا از «↩️ بازگشت» استفاده کنید.", b.replyAdminMenu())
}

func (b *Bot) adminAddPrompt(c chat) {
	if c.ID != b.ownerID {
		b.sendMain(c, "")
		return
	}
	b.mu.Lock()
	b.awaiting = awaitAdminAdd
	b.mu.Unlock()
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		"➕ <b>افزودن ادمین جدید</b>\n\n"+
			"<b>آیدی عددی</b> شخص مورد نظر را بفرستید.\n"+
			"(آن شخص می‌تواند با ارسال <code>/id</code> به همین ربات، آیدی خود را دریافت کند)\n\n"+
			"بعد از آیدی، <b>سطح دسترسی</b> را انتخاب می‌کنید:\n"+
			"👑 کامل — همه بخش‌ها\n"+
			"📡 فقط اسکن — فقط ارسال کانفیگ و دریافت خروجی",
		[][]string{{"↩️ بازگشت به منوی اصلی"}})
}

func (b *Bot) onAdminAdd(c chat, text string) {
	if c.ID != b.ownerID {
		b.sendMain(c, "")
		return
	}
	idStr := strings.TrimSpace(strings.TrimPrefix(text, "@"))
	id, ok := parseID(idStr)
	if !ok || id <= 0 {
		_, _ = b.api.sendWithReplyKeyboard(c.ID, "🤔 آیدی عددی معتبر نیست.", b.replyAdminMenu())
		return
	}
	b.mu.Lock()
	if id == b.ownerID {
		b.mu.Unlock()
		_, _ = b.api.sendWithReplyKeyboard(c.ID, "شما مالک ربات هستید و دسترسی کامل دارید.", b.replyAdminMenu())
		return
	}
	if ex, has := b.adminUserLocked(id); has {
		b.awaiting = awaitNone
		b.awaitAdmin = id
		b.mu.Unlock()
		_, _ = b.api.sendWithKeyboard(c.ID,
			fmt.Sprintf("این کاربر از قبل با سطح %s ثبت شده — سطح جدید را انتخاب کنید:", adminPermLabel(ex.Perm)),
			permPickerRows(id), "sendMessage")
		return
	}
	b.awaiting = awaitNone
	b.awaitAdmin = id
	b.mu.Unlock()
	_, _ = b.api.sendWithKeyboard(c.ID,
		fmt.Sprintf("✏️ سطح دسترسی ادمین <code>%d</code> را انتخاب کنید:", id),
		permPickerRows(id), "sendMessage")
}

func permPickerRows(id int64) [][]string {
	return [][]string{
		{"👑 کامل", fmt.Sprintf("admperm:%d:full", id)},
		{"📡 فقط اسکن", fmt.Sprintf("admperm:%d:scan", id)},
	}
}

// adminUserLocked is adminUser without re-locking (caller holds b.mu).
func (b *Bot) adminUserLocked(id int64) (AdminUser, bool) {
	for _, a := range b.settings.AdminsV2 {
		if a.ID == id {
			return a, true
		}
	}
	return AdminUser{}, false
}

func (b *Bot) onAdminRemove(c chat, idStr string) {
	if c.ID != b.ownerID {
		b.sendMain(c, "")
		return
	}
	id, ok := parseID(idStr)
	if !ok {
		return
	}
	if !b.removeAdminID(id) {
		_, _ = b.api.sendWithReplyKeyboard(c.ID, "این آیدی در لیست ادمین‌ها نبود.", b.replyAdminMenu())
		return
	}
	_, _ = b.api.sendWithReplyKeyboard(c.ID,
		fmt.Sprintf("🗑️ دسترسی کاربر <code>%d</code> قطع شد.", id),
		b.replyAdminMenu())
}

// routeScanOnly handles (or rejects) input from a scan-only admin: the scan
// flow plus the log — nothing else, not even via old buttons or commands.
func (b *Bot) routeScanOnly(c chat, cleanText, lowerText string) {
	switch {
	case lowerText == "/start" || lowerText == "/help" || cleanText == "منو" ||
		cleanText == "↩️ بازگشت به منوی اصلی" || cleanText == "↩️ منوی اصلی":
		b.sendMain(c, "")
	case cleanText == "📡 اسکن کانفیگ" || cleanText == "📡 اسکن" ||
		cleanText == "📥 ارسال کانفیگ" || lowerText == "/scan":
		b.onScanRequest(c)
	case strings.HasPrefix(cleanText, "▶️ شروع اسکن") || cleanText == "شروع اسکن" ||
		cleanText == "شروع" || lowerText == "/run" || lowerText == "/start_scan":
		b.startRun(c)
	case cleanText == "🗑️ پاک کردن لیست" || cleanText == "پاک کردن لیست" || cleanText == "حذف لیست":
		b.cancelPending(c)
	case cleanText == "📊 گزارش / لاگ" || lowerText == "/log":
		b.sendRunLog(c)
	case lowerText == "/id":
		_, _ = b.api.sendMessage(c.ID, fmt.Sprintf("ID شما: <code>%d</code>", c.ID))
	case lowerText == "/cancel" || cleanText == "لغو":
		b.mu.Lock()
		b.awaiting = awaitNone
		b.awaitISO = ""
		b.awaitAdmin = 0
		b.pending = nil
		b.mu.Unlock()
		b.sendMain(c, "✖️ عملیات لغو شد.")
	default:
		if strings.Contains(cleanText, "://") {
			b.onConfigInput(c, cleanText)
			return
		}
		_, _ = b.api.sendMessage(c.ID,
			"⛔ این بخش فقط برای مالک ربات است — دسترسی شما فقط به بخش اسکن است.")
	}
}

// onAdminCallback runs the owner's admin-management buttons
// (admperm:<id>:<perm> | admdel:<id> | admtoggle:<id>).
func (b *Bot) onAdminCallback(chatID int64, msgID int, data string) {
	switch {
	case strings.HasPrefix(data, "admperm:"):
		parts := strings.Split(strings.TrimPrefix(data, "admperm:"), ":")
		if len(parts) != 2 {
			return
		}
		id, ok := parseID(parts[0])
		if !ok {
			return
		}
		perm := parts[1]
		if perm != PermFull && perm != PermScan {
			return
		}
		b.addOrUpdateAdmin(id, perm)
		// inform the admin (silently ignored if they never started the bot)
		_, _ = b.api.sendMessage(id, fmt.Sprintf("✅ دسترسی شما به ربات ثبت شد: <b>%s</b>", adminPermLabel(perm)))
		if msgID > 0 {
			_ = b.api.editText(msgID, chatID, fmt.Sprintf("✅ ادمین <code>%d</code> با سطح <b>%s</b> ثبت شد.", id, adminPermLabel(perm)))
		}
	case strings.HasPrefix(data, "admdel:"):
		id, ok := parseID(strings.TrimPrefix(data, "admdel:"))
		if !ok {
			return
		}
		b.removeAdminID(id)
		_, _ = b.api.sendMessage(id, "🗑️ دسترسی شما به ربات حذف شد.")
		if msgID > 0 {
			_ = b.api.editText(msgID, chatID, fmt.Sprintf("🗑️ ادمین <code>%d</code> حذف شد.", id))
		}
	case strings.HasPrefix(data, "admtoggle:"):
		id, ok := parseID(strings.TrimPrefix(data, "admtoggle:"))
		if !ok {
			return
		}
		a, has := b.adminUser(id)
		if !has {
			return
		}
		np := PermScan
		if a.Perm == PermScan {
			np = PermFull
		}
		b.addOrUpdateAdmin(id, np)
		_, _ = b.api.sendMessage(id, fmt.Sprintf("🔁 سطح دسترسی شما تغییر کرد: <b>%s</b>", adminPermLabel(np)))
		if msgID > 0 {
			_ = b.api.editText(msgID, chatID, fmt.Sprintf("🔁 سطح ادمین <code>%d</code> به <b>%s</b> تغییر کرد.", id, adminPermLabel(np)))
		}
	}
}

// broadcastSettings pushes the refreshed settings menu to the owner and the
// other FULL admins, so a change made by one is immediately visible to all
// (their persistent keyboard would otherwise keep the stale labels).
func (b *Bot) broadcastSettings(exceptChat int64) {
	b.mu.Lock()
	var targets []int64
	seen := map[int64]bool{}
	add := func(id int64) {
		if id == exceptChat || id <= 0 || seen[id] {
			return
		}
		seen[id] = true
		targets = append(targets, id)
	}
	for _, a := range b.settings.AdminsV2 {
		if a.Perm != PermScan {
			add(a.ID)
		}
	}
	for _, a := range b.settings.Admins {
		add(a)
	}
	b.mu.Unlock()
	for _, t := range targets {
		if !b.isFullAdmin(t) {
			continue
		}
		_, _ = b.api.sendMessage(t, "🔄 <b>تنظیمات به‌روزرسانی شد.</b>")
		_, _ = b.api.sendWithReplyKeyboard(t, b.settingsSummaryText(), b.replySettingsMenu())
	}
}

// settingsSummaryText renders the current settings as a short text block.
func (b *Bot) settingsSummaryText() string {
	b.mu.Lock()
	s := b.settingsLocked()
	b.mu.Unlock()
	langName := "فارسی"
	if s.OutLang == "en" {
		langName = "English"
	}
	chState := "خاموش"
	if s.IncludeChannel {
		if s.Channel != "" {
			chState = "روشن (" + s.Channel + ")"
		} else {
			chState = "روشن (بدون نام)"
		}
	}
	unkState := "خاموش"
	if s.IncludeUnknown {
		unkState = "روشن"
	}
	return "⚙️ <b>تنظیمات فعلی ربات</b>\n\n" +
		fmt.Sprintf("🔢 همزمانی: <b>%d</b>\n", s.Parallel) +
		fmt.Sprintf("⏱️ تایم‌اوت: <b>%d ثانیه</b>\n", s.TimeoutSec) +
		fmt.Sprintf("🌐 زبان: <b>%s</b>\n", langName) +
		fmt.Sprintf("📢 سافیکس کانال: <b>%s</b>\n", chState) +
		fmt.Sprintf("🔗 خروجی بدون کشور: <b>%s</b>", unkState)
}

func parseID(s string) (int64, bool) {
	// 19 digits can overflow int64 (max 9223372036854775807) and silently
	// wrap into a wrong id
	if s == "" || len(s) > 18 {
		return 0, false
	}
	n := int64(0)
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int64(ch-'0')
	}
	return n, true
}

// ------------------------------------------------------------------
// about

func (b *Bot) onAbout(c chat) {
	b.mu.Lock()
	s := b.settingsLocked()
	b.mu.Unlock()
	langName := "فارسی"
	if s.OutLang == "en" {
		langName = "English"
	}
	msg := "ℹ️ <b>ConfigScanner Bot</b> <code>v" + BotVersion + "</code>\n\n" +
		"• 📡 تست هر سرور با پروسس اختصاصی Xray و تخصیص اتمیک پورت\n" +
		"• 💨 هسته بومی Hysteria 2 برای لینک‌های سلامندر و جکوهو\n" +
		"• 🔒 پین کردن خودکار گواهی TLS برای سرورهای insecure\n" +
		"• 🧩 پشتیبانی از TCP Fragment، ECH و تنظیمات پیشرفته xhttp\n" +
		"• 🌐 استعلام ۶ سرویس جیو به‌صورت موازی و نام‌گذاری استاندارد\n" +
		"• 🏷️ خروجی تمیز بدون ارقام و شماره‌های اضافی\n" +
		"• 🎨 کپشن، پیام برای کاربران و سازگاری کامل با اپلیکیشن اندروید\n\n" +
		"⚙️ تنظیمات فعلی: " + itoaSafe(s.Parallel) + " همزمان · " + itoaSafe(s.TimeoutSec) + " ثانیه · " + langName
	_, _ = b.api.sendWithReplyKeyboard(c.ID, msg, b.replyMainMenuFor(c.ID, 0))
}

// ------------------------------------------------------------------
// update routing

func (b *Bot) handleUpdate(u update) {
	c := u.chat()
	if c.ID == 0 {
		return
	}
	if !b.isAllowed(c.ID) {
		if u.Message != nil {
			_, _ = b.api.sendMessage(c.ID, "⛔ این ربات خصوصی است.")
		}
		return
	}

	if u.CallbackQuery != nil {
		cq := u.CallbackQuery
		data := cq.Data
		b.api.answerCallback(cq.ID, "")

		// owner-only admin-management callbacks
		if strings.HasPrefix(data, "admperm:") || strings.HasPrefix(data, "admdel:") ||
			strings.HasPrefix(data, "admtoggle:") {
			if cq.From == nil || cq.From.ID != b.ownerID {
				return
			}
			var chatID int64
			var msgID int
			if cq.Message != nil {
				msgID = cq.Message.MessageID
				if cq.Message.Chat != nil {
					chatID = cq.Message.Chat.ID
				}
			}
			b.onAdminCallback(chatID, msgID, data)
			return
		}

		// caption/codes callbacks are not for scan-only admins
		if !b.isFullAdmin(c.ID) {
			return
		}
		if strings.HasPrefix(data, "setcode:") {
			iso := strings.TrimPrefix(data, "setcode:")
			b.setCodePrompt(c, iso)
			return
		}
		if data == "cap:import" {
			b.importPrompt(c)
			return
		}
		return
	}

	m := u.Message
	if m == nil {
		return
	}
	text := m.Text
	if text == "" && m.Caption != "" {
		text = m.Caption
	}
	if text == "" && m.Document != nil {
		bb, err := b.api.getFileBytes(m.Document.FileID)
		if err != nil {
			// never echo the raw error — it can contain the bot token inside
			// the file/download URL
			_, _ = b.api.sendMessage(c.ID,
				"⚠️ دریافت فایل ناموفق بود — حجم فایل باید کمتر از ۲۰ مگابایت باشد یا دوباره تلاش کنید.")
			return
		}
		text = string(bb)
	}
	if text == "" {
		_, _ = b.api.sendMessage(c.ID, "لطفاً متن کانفیگ یا فایل .txt ارسال کنید.")
		return
	}

	cleanText := strings.TrimSpace(text)
	lowerText := strings.ToLower(cleanText)

	// scan-only admins: only the scan flow. Everything else — settings,
	// caption, users message, admins — is rejected before any other route.
	if !b.isFullAdmin(c.ID) {
		b.routeScanOnly(c, cleanText, lowerText)
		return
	}

	switch {
	case lowerText == "/start" || lowerText == "/help" || cleanText == "منو" || cleanText == "↩️ بازگشت به منوی اصلی" || cleanText == "↩️ منوی اصلی":
		b.sendMain(c, "")
		return
	case cleanText == "📡 اسکن کانفیگ" || cleanText == "📡 اسکن" || cleanText == "📥 ارسال کانفیگ" || lowerText == "/scan":
		b.onScanRequest(c)
		return
	case strings.HasPrefix(cleanText, "▶️ شروع اسکن") || cleanText == "شروع اسکن" || cleanText == "شروع" || lowerText == "/run" || lowerText == "/start_scan":
		b.startRun(c)
		return
	case cleanText == "🗑️ پاک کردن لیست" || cleanText == "پاک کردن لیست" || cleanText == "حذف لیست":
		b.cancelPending(c)
		return
	case cleanText == "⚙️ تنظیمات" || lowerText == "/settings":
		b.showSettingsMenu(c)
		return
	case strings.HasPrefix(cleanText, "🔢 همزمانی"):
		b.cycleSetting(c, "parallel")
		return
	case strings.HasPrefix(cleanText, "⏱️ تایم‌اوت"):
		b.cycleSetting(c, "timeout")
		return
	case strings.HasPrefix(cleanText, "🌐 زبان"):
		b.cycleSetting(c, "lang")
		return
	case strings.HasPrefix(cleanText, "📢 سافیکس"):
		b.cycleSetting(c, "channel")
		return
	case cleanText == "📢 تنظیم نام کانال":
		b.channelNamePrompt(c)
		return
	case strings.HasPrefix(cleanText, "🔗 خروجی بدون کشور"):
		b.cycleSetting(c, "unknown")
		return
	case cleanText == "🏷️ کپشن و پرچم" || lowerText == "/caption":
		b.showCaptionMenu(c)
		return
	case cleanText == "✏️ ویرایش قالب کپشن":
		b.captionEditPrompt(c)
		return
	case cleanText == "🎨 کدهای ایموجی کشورها":
		b.onCodesMenu(c)
		return
	case cleanText == "👁️ پیش‌نمایش کپشن":
		b.captionPreview(c)
		return
	case cleanText == "↩️ بازگردانی قالب پیش‌فرض":
		b.mu.Lock()
		b.settings.CaptionTemplate = DefaultCaptionTemplate
		b.save()
		b.mu.Unlock()
		_, _ = b.api.sendWithReplyKeyboard(c.ID, "↩️ قالب کپشن به حالت پیش‌فرض بازگشت.", b.replyCaptionMenu())
		return
	case cleanText == "📥 وارد کردن دسته‌ای کدها":
		b.importPrompt(c)
		return
	case cleanText == "📤 خروجی گرفتن کدها":
		b.onCodesExport(c)
		return
	case cleanText == "✏️ پیام برای کاربران" || lowerText == "/message":
		b.showMsgForUsersMenu(c)
		return
	case cleanText == "✏️ تنظیم / ویرایش متن":
		b.msgForUsersPrompt(c)
		return
	case cleanText == "👁️ مشاهده متن فعلی":
		b.mu.Lock()
		s := b.settingsLocked()
		b.mu.Unlock()
		if strings.TrimSpace(s.MessageForUsers) == "" {
			_, _ = b.api.sendWithReplyKeyboard(c.ID, "<i>(هنوز متنی ثبت نشده است)</i>", b.replyMsgForUsersMenu())
		} else {
			_, _ = b.api.sendWithReplyKeyboard(c.ID, "📄 <b>متن فعلی پیام برای کاربران:</b>\n\n<pre>"+escapeHTML(s.MessageForUsers)+"</pre>", b.replyMsgForUsersMenu())
		}
		return
	case cleanText == "🗑️ حذف متن پیام":
		b.onClearMessageForUsers(c)
		return
	case cleanText == "📊 گزارش / لاگ" || lowerText == "/log":
		b.sendRunLog(c)
		return
	case cleanText == "ℹ️ راهنما و درباره" || cleanText == "ℹ️ درباره" || lowerText == "/about":
		b.onAbout(c)
		return
	case cleanText == "👥 ادمین‌ها" || cleanText == "📋 لیست ادمین‌ها" || lowerText == "/admins":
		b.onAdminMenu(c)
		return
	case cleanText == "➕ افزودن ادمین":
		b.adminAddPrompt(c)
		return
	case strings.HasPrefix(lowerText, "/rmadmin "):
		b.onAdminRemove(c, strings.TrimPrefix(lowerText, "/rmadmin "))
		return
	case lowerText == "/id":
		_, _ = b.api.sendMessage(c.ID, fmt.Sprintf("ID شما: <code>%d</code>", c.ID))
		return
	case lowerText == "/cancel" || cleanText == "لغو":
		b.mu.Lock()
		b.awaiting = awaitNone
		b.awaitISO = ""
		b.awaitAdmin = 0
		b.pending = nil
		b.mu.Unlock()
		b.sendMain(c, "✖️ عملیات لغو شد.")
		return
	}

	// Awaiting state
	b.mu.Lock()
	aw := b.awaiting
	iso := b.awaitISO
	b.mu.Unlock()

	switch aw {
	case awaitCaptionTemplate:
		b.mu.Lock()
		b.awaiting = awaitNone
		b.mu.Unlock()
		b.onCaptionTemplate(c, text)
		return
	case awaitChannelName:
		b.mu.Lock()
		b.awaiting = awaitNone
		name := strings.TrimSpace(strings.TrimPrefix(text, "@"))
		if name == "" || strings.EqualFold(name, "delete") {
			b.settings.Channel = ""
			b.settings.IncludeChannel = false
			b.save()
			b.mu.Unlock()
			_, _ = b.api.sendWithReplyKeyboard(c.ID, "📢 نام کانال حذف شد و سافیکس خاموش گردید.", b.replySettingsMenu())
		} else {
			b.settings.Channel = name
			b.settings.IncludeChannel = true
			b.save()
			b.mu.Unlock()
			_, _ = b.api.sendWithReplyKeyboard(c.ID,
				"✅ سافیکس کانال <b>روشن</b> شد:\n<pre>🇩🇪 آلمان | "+escapeHTML(name)+"</pre>",
				b.replySettingsMenu())
		}
		b.broadcastSettings(c.ID)
		return
	case awaitAdminAdd:
		b.mu.Lock()
		b.awaiting = awaitNone
		b.mu.Unlock()
		b.onAdminAdd(c, text)
		return
	case awaitCodesImport:
		b.mu.Lock()
		b.awaiting = awaitNone
		b.mu.Unlock()
		b.onCodesImport(c, text)
		return
	case awaitCodeSet:
		b.mu.Lock()
		b.awaiting = awaitNone
		b.awaitISO = ""
		b.mu.Unlock()
		b.onSetCode(c, iso, text)
		return
	case awaitMessageForUsers:
		b.mu.Lock()
		b.awaiting = awaitNone
		b.mu.Unlock()
		b.onSetMessageForUsers(c, text)
		return
	}

	// Country emoji codes: a single line "DE=5390843037349679256" sets one
	// code directly; a multi-line list is a batch import. This must run AFTER
	// the awaiting switch (so it never hijacks a prompt like the caption
	// template) and the multi-line case must be handled as a list — the old
	// quick-set path swallowed the whole list into ONE country's value.
	if strings.Contains(cleanText, "=") && !strings.Contains(cleanText, "://") {
		if !strings.Contains(cleanText, "\n") {
			parts := strings.SplitN(cleanText, "=", 2)
			iso := strings.ToUpper(strings.TrimSpace(parts[0]))
			val := strings.TrimSpace(parts[1])
			if len(iso) == 2 && val != "" {
				b.setEmojiCodeDirect(c, iso, val)
				return
			}
		} else if looksLikeCodesList(cleanText) {
			b.onCodesImport(c, text)
			return
		}
	}

	// Config input
	t := strings.TrimSpace(text)
	if strings.Contains(t, "://") {
		b.onConfigInput(c, text)
		return
	}

	b.sendMain(c, "")
}

// pollState tracks the getUpdates offset across the polling loop.
type pollState struct {
	offset  int
	purging bool
}

func (p *pollState) next() int { return p.offset + 1 }

// feed handles one getUpdates outcome and returns the updates to process
// plus a back-off. On Telegram's "start of the range" error the stale range
// is purged with one huge-offset poll — and the offset is then reset to 0
// after the clean (empty) reply. The old code kept the huge offset forever,
// so every later poll matched no update and the bot went deaf until restart.
func (p *pollState) feed(ups []update, err error) (todo []update, backoff time.Duration) {
	if err != nil {
		if strings.Contains(err.Error(), "start of the range") {
			p.purging = true
			p.offset = 1 << 60
			return nil, 0
		}
		return nil, 3 * time.Second
	}
	if p.purging {
		p.purging = false
		if len(ups) == 0 {
			p.offset = 0
			return nil, 0
		}
	}
	for _, u := range ups {
		p.offset = u.UpdateID
	}
	return ups, 0
}

func (b *Bot) Loop() error {
	name, err := b.getMeName()
	if err != nil {
		return err
	}
	fmt.Printf("bot started as @%s, owner=%d (v%s)\n", name, b.ownerID, BotVersion)
	var ps pollState
	for {
		ups, err := b.api.getUpdates(ps.next(), 50)
		todo, backoff := ps.feed(ups, err)
		if err != nil {
			fmt.Println("getUpdates error:", err)
		}
		if backoff > 0 {
			time.Sleep(backoff)
			continue
		}
		for _, u := range todo {
			uc := u.chat()
			if u.Message != nil {
				fmt.Printf("update %d: chat=%d msg=%q\n",
					u.UpdateID, uc.ID, clip(u.Message.Text, 60))
			} else if u.CallbackQuery != nil {
				fmt.Printf("update %d: callback=%q\n",
					u.UpdateID, clip(u.CallbackQuery.Data, 60))
			}
			go b.handleUpdate(u)
		}
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (b *Bot) getMeName() (string, error) {
	var out struct {
		UserName string `json:"username"`
	}
	if err := b.api.call("getMe", map[string]any{}, &out); err != nil {
		return "", err
	}
	return out.UserName, nil
}

// ------------------------------------------------------------------

func splitConfigLines(raw string) []string {
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		t := html.UnescapeString(strings.TrimSpace(l))
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}

func estimateDuration(n, parallel int) string {
	secs := int((float64(n)/float64(parallel))*30*0.45) + 20
	if secs < 25 {
		secs = 25
	}
	min := secs / 60
	rem := secs % 60
	if min > 0 {
		return fmt.Sprintf("~%d دقیقه و %d ثانیه", min, rem)
	}
	return fmt.Sprintf("~%d ثانیه", rem)
}

func indexInt(steps []int, v int) int {
	for i, s := range steps {
		if s == v {
			return i
		}
	}
	return 0
}

func itoaSafe(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
