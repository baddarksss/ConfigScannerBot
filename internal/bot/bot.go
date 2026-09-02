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
	BotVersion = "1.0.7"
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
	Admins         []int64 `json:"admins"` // extra users allowed to use the bot

	CaptionTemplate string            `json:"caption_template"`
	EmojiCodes      map[string]string `json:"emoji_codes"` // ISO -> custom emoji id
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
)

type Bot struct {
	api     *tgAPI
	ownerID int64
	dataDir string
	xrayBin string
	hyBin   string

	mu        sync.Mutex
	settings  Settings
	running   bool
	pending   []string // raw config lines awaiting confirmation
	runMessage int      // progress message id (the nav message itself)
	runChat   int64
	awaiting  awaiting
	awaitISO  string // target country for awaitCodeSet
	navMsg    map[int64]int // per-chat id of the current interactive message

	lastCodes []string
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
		navMsg:  map[int64]int{},
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

// settingsLocked returns a SNAPSHOT copy — mutation-safe for readers.
// Writers must lock and modify b.settings directly.
func (b *Bot) settingsLocked() Settings {
	b.settings.defaults()
	return b.settings
}

// ------------------------------------------------------------------
// access control

// isAllowed: the owner or any user on the admins list.
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
	return false
}

// ------------------------------------------------------------------
// navigation
//
// nav renders text+buttons by EDITING this chat's current interactive
// message in place (one stable message, like the app's screens); a new
// message is sent only when there is none to edit (or the edit fails).

func (b *Bot) nav(c chat, text string, rows [][]string) {
	b.mu.Lock()
	msgID := b.navMsg[c.ID]
	b.mu.Unlock()
	if msgID > 0 {
		if err := b.api.editKeyboard(msgID, c.ID, text, rows); err == nil {
			return
		}
	}
	id, err := b.api.sendWithKeyboard(c.ID, text, rows, "sendMessage")
	if err == nil {
		b.mu.Lock()
		b.navMsg[c.ID] = id
		b.mu.Unlock()
	}
}

// navID returns the current interactive message id for this chat (0 if none).
func (b *Bot) navID(chatID int64) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.navMsg[chatID]
}

// ------------------------------------------------------------------
// fixed main menu (the admins row is owner-only)

func (b *Bot) menuFor(id int64) [][]string {
	// flat rows: [text1, cb1, text2, cb2]
	rows := [][]string{
		{"📡 اسکن کانفیگ", "scan", "⚙️ تنظیمات", "settings"},
		{"🏷️ کپشن و پرچم", "caption", "ℹ️ درباره", "about"},
	}
	if id == b.ownerID {
		rows = append(rows, []string{"👥 ادمین‌ها", "admins"})
	}
	return rows
}

func (b *Bot) mainIntro() string {
	return "📡 <b>ConfigScanner Bot</b> <code>v" + BotVersion + "</code>\n\n" +
		"اسکنر خروجی کانفیگ‌ها — دقیقاً همون منطق اپ:\n" +
		"• هر سرور با xray جدا تست می‌شه (hy2 با هسته‌ی اصلی hysteria)\n" +
		"• کشور خروجی با رأی‌گیری ۶ سرویس جیو\n" +
		"• خروجی با پرچم و اسم یکتا + کپشن custom emoji\n\n" +
		"یک دکمه بزن 👇"
}

func (b *Bot) sendMain(c chat, intro string) {
	if intro == "" {
		intro = b.mainIntro()
	}
	b.nav(c, intro, b.menuFor(c.ID))
}

// ------------------------------------------------------------------
// scan flow

func (b *Bot) onScanRequest(c chat) {
	b.mu.Lock()
	b.pending = nil
	b.mu.Unlock()
	b.nav(c,
		"📡 <b>اسکن کانفیگ</b>\n\n"+
			"کانفیگ‌ها رو <b>پیست</b> کن یا <b>فایل .txt</b> بفرست — هر خط یک کانفیگ:\n"+
			"<code>vless · vmess · trojan · ss · hy2 · ssr · tuic</code>\n\n"+
			"💡 چند لیست هم می‌تونی پشت‌سرهم بفرستی؛ همه جمع می‌شن و با یک دکمه شروع می‌کنی.",
		[][]string{{"↩️ بازگشت", "menu"}})
}

// onConfigInput collects config lines. Successive messages are APPENDED to
// the pending list (the user may paste several lists before pressing start).
func (b *Bot) onConfigInput(c chat, raw string) {
	lines := splitConfigLines(raw)
	b.mu.Lock()
	running := b.running
	b.mu.Unlock()
	if running {
		_, _ = b.api.sendMessage(c.ID, "⏳ هنوز یک اسکن در حال اجراست — صبر کن تمام شود.")
		return
	}
	if len(lines) == 0 {
		b.nav(c,
			"🤔 کانفیگی توی پیام پیدا نشد.\nهر خط باید با یکی از این‌ها شروع بشه:\n<code>vless:// · vmess:// · trojan:// · ss:// · hysteria2://</code>\n\nدوباره بفرست یا:",
			[][]string{{"↩️ بازگشت", "menu"}})
		return
	}

	b.mu.Lock()
	extra := len(b.pending) > 0
	b.pending = append(b.pending, lines...)
	total := len(b.pending)
	s := b.settingsLocked()
	b.mu.Unlock()

	confirm := fmt.Sprintf("✅ <b>%d کانفیگ</b> آماده‌ست", total)
	if extra {
		confirm += "\n\n📥 لیست جدید هم جمع شد"
	}
	confirm += fmt.Sprintf("\n\n⏱️ زمان تقریبی: %s\n\nبرای شروع اسکن دکمه‌ی زیر رو بزن:",
		estimateDuration(total, s.Parallel))
	rows := [][]string{
		{"🚀 شروع اسکن", "scan:start", "🗑️ لغو و پاک‌سازی", "scan:cancel"},
	}
	b.nav(c, confirm, rows)
}

func (b *Bot) cancelPending(c chat) {
	b.mu.Lock()
	b.pending = nil
	b.mu.Unlock()
	b.nav(c, "✖️ لیست کانفیگ‌ها پاک شد.",
		[][]string{{"📡 اسکن کانفیگ", "scan"}})
}

func (b *Bot) startRun(c chat) {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		_, _ = b.api.sendMessage(c.ID, "⏳ هنوز یک اسکن در حال اجراست — صبر کن تمام شود.")
		return
	}
	if len(b.pending) == 0 {
		b.mu.Unlock()
		_, _ = b.api.sendMessage(c.ID, "اول کانفیگ‌ها رو بفرست.")
		return
	}
	raw := strings.Join(b.pending, "\n")
	b.pending = nil
	b.running = true
	b.runChat = c.ID
	b.lastLog = nil // each run starts with a clean log
	cfg := b.settingsLocked()
	b.mu.Unlock()

	servers := engine.ParseInput(raw)
	if len(servers) == 0 {
		b.finishRunGuard()
		b.nav(c, "🤔 هیچ خط قابل شناسایی نبود.", [][]string{{"↩️ بازگشت", "menu"}})
		return
	}

	intro := fmt.Sprintf("🚀 <b>اسکن شروع شد</b>\n\n"+
		"📦 %d سرور · ⚙️ %d همزمان · ⏱️ تایم‌اوت %d ثانیه\n\n"+
		"%s\n\n⏳ در حال اجرا…",
		len(servers), cfg.Parallel, cfg.TimeoutSec, progressBar(0, len(servers)))
	b.nav(c, intro, nil)
	b.mu.Lock()
	b.runMessage = b.navMsg[c.ID]
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

	var lastEdit time.Time
	var lastLine string

	res := eng.Run(servers, func(d, tot int, hostport, mark string) {
		lastLine = mark + " " + hostport
		if time.Since(lastEdit) < 2*time.Second {
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
		_ = b.api.editKeyboard(msgID, chatID, fmt.Sprintf(
			"🔍 <b>در حال اسکن…</b> %d%%\n\n%s\n\n📦 %d از %d\n%s\n\n⚙️ %d همزمان · ⏱️ %d ثانیه",
			d*100/tot, progressBar(d, tot), d, tot, lastLine, cfg.Parallel, cfg.TimeoutSec), nil)
	})

	b.mu.Lock()
	b.lastCodes = res.CountryCodes
	msgID := b.runMessage
	chatID := b.runChat
	b.mu.Unlock()
	if msgID > 0 {
		_ = b.api.editKeyboard(msgID, chatID, fmt.Sprintf(
			"✅ <b>اسکن تمام شد</b>\n\n%s", res.Summary(cfg.OutLang)), nil)
	}

	// 1) full output — text first, file only when it overflows one message
	var out strings.Builder
	for _, l := range res.Lines {
		out.WriteString(l)
		out.WriteString("\n")
	}
	body := out.String()
	if len(body) <= tgTextLimit {
		_, _ = b.api.sendMessage(chatID, "📄 <b>خروجی کامل</b>\n\n<pre>"+escapeHTML(body)+"</pre>")
	} else {
		_ = b.api.sendDocument(chatID, []byte(body), "cfgscan_results.txt",
			"📄 <b>خروجی کامل</b> — بیشتر از یک پیام بود، به‌صورت فایل\n"+res.Summary(cfg.OutLang))
	}

	// 2) working links only (file — long)
	if len(res.Links) > 0 {
		cap := fmt.Sprintf("🔗 <b>%d لینک سالم</b> — فقط لینک‌ها، آماده کپی", len(res.Links))
		if cfg.IncludeUnknown {
			cap += " (شامل " + itoaSafe(res.NoCountry) + " بدون کشور)"
		}
		_ = b.api.sendDocument(chatID, []byte(strings.Join(res.Links, "\n")),
			"cfgscan_links.txt", cap)
	}

	// 3) caption — always as text
	if res.CountryCodes != nil && cfg.CaptionTemplate != "" {
		caption := b.buildCaption(res.CountryCodes)
		if caption != "" {
			if len(caption) <= tgTextLimit {
				_, _ = b.api.sendMessage(chatID, "🏷️ <b>کپشن آماده</b> — کپی کن و با فایل کانفیگ‌ها پست کن\n\n"+escapeHTML(caption))
			} else {
				_ = b.api.sendDocument(chatID, []byte(caption), "caption.txt",
					"🏷️ <b>کپشن آماده</b> (بسیار طولانی بود)")
			}
		}
	}

	// 4) countries from this run that have no emoji code yet
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
		var names []string
		for _, iso := range missing {
			flag := engine.Flag(iso)
			nm, ok := countries.Names(iso, cfg.OutLang)
			if !ok {
				nm = iso
			}
			names = append(names, flag+" "+nm)
		}
		_, _ = b.api.sendWithKeyboard(chatID,
			"🎨 <b>این کشورها کد ایموجی ندارند</b>\n\n"+
				strings.Join(names, "  ·  ")+"\n\n"+
				"در کپشن برای این‌ها <code>[]</code> خالی جا می‌گیره. کدشون رو از تپ Caption اپ بردار و اینجا وارد کن:",
			[][]string{{"🎨 کد ایموجی کشورها", "cap:codes"}}, "sendMessage")
	}

	// 5) main menu + run log (same nav message, back in place)
	rows := b.menuFor(chatID)
	rows = append([][]string{{"📄 لاگ این اجرا", "runlog"}}, rows...)
	b.nav(c, "تمام شد ✅\n\n"+res.Summary(cfg.OutLang)+"\n\n🚀 اسکن بعدی؟", rows)
	b.finishRunGuard()
}

// progressBar renders "🟩⬜" (max 20 cells), rounded to the nearest cell.
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
		_, _ = b.api.sendMessage(c.ID, "📄 لاگی ثبت نشده — اول یک اسکن انجام بده.")
		return
	}
	body := strings.Join(logs, "\n")
	if len(body) <= tgTextLimit {
		_, _ = b.api.sendMessage(c.ID, "📄 <b>لاگ آخرین اجرا</b>\n\n<pre>"+escapeHTML(body)+"</pre>")
		return
	}
	_ = b.api.sendDocument(c.ID, []byte(body), "cfgscan_run.log",
		"📄 <b>لاگ آخرین اجرا</b> — بیشتر از یک پیام بود، به‌صورت فایل")
}

// ------------------------------------------------------------------
// caption

func (b *Bot) onCaptionMenu(c chat) {
	b.mu.Lock()
	hasLast := b.lastCodes != nil
	b.mu.Unlock()
	rows := [][]string{
		{"✏️ قالب کپشن", "cap:edit"},
		{"🎨 کد ایموجی کشورها", "cap:codes"},
	}
	if hasLast {
		rows = append(rows, []string{"👁️ پیش‌نمایش کپشن", "cap:preview"})
	}
	rows = append(rows,
		[]string{"↩️ قالب پیش‌فرض", "cap:default"},
		[]string{"↩️ بازگشت", "menu"})
	b.nav(c,
		"🏷️ <b>کپشن و پرچم</b>\n\n"+
			"جای <code>{{FLAGS}}</code> با پرچم‌های همان ران پر می‌شه.\n"+
			"کد ایموجی = همون اعدادی که توی تپ Caption اپ می‌زنی (custom emoji id).",
		rows)
}

func (b *Bot) captionEditPrompt(c chat) {
	b.mu.Lock()
	s := b.settingsLocked()
	b.awaiting = awaitCaptionTemplate
	b.mu.Unlock()
	b.nav(c,
		"✏️ <b>ویرایش قالب کپشن</b>\n\n"+
			"قالب فعلی:\n<code>"+escapeHTML(s.CaptionTemplate)+"</code>\n\n"+
			"قالب جدید رو <b>پیست</b> کن (باید <code>{{FLAGS}}</code> داشته باشه).",
		[][]string{{"✖️ لغو", "caption"}})
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
	b.nav(c, "✅ قالب کپشن ذخیره شد.",
		[][]string{{"🏷️ کپشن و پرچم", "caption"}, {"↩️ بازگشت", "menu"}})
}

func (b *Bot) captionPreview(c chat) {
	b.mu.Lock()
	codes := b.lastCodes
	b.mu.Unlock()
	if codes == nil {
		_, _ = b.api.sendMessage(c.ID, "اول یک اسکن انجام بده تا کشورها ثبت بشن.")
		return
	}
	caption := b.buildCaption(codes)
	if caption == "" {
		_, _ = b.api.sendMessage(c.ID, "کپشنی ساخته نشد.")
		return
	}
	if len(caption) <= tgTextLimit {
		_, _ = b.api.sendMessage(c.ID, "👁️ <b>پیش‌نمایش کپشن</b> (کشورهای آخرین ران)\n\n"+escapeHTML(caption))
		return
	}
	_ = b.api.sendDocument(c.ID, []byte(caption), "caption_preview.txt",
		"👁️ <b>پیش‌نمایش کپشن</b> (بسیار طولانی بود)")
}

// buildCaption mirrors the app: for each run country (in order), emit the
// raw [custom_emoji_id] marker when one is registered.
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

// onCodesMenu is the app-style codes screen: the last run's countries with
// flag + name + current code, per-country edit buttons, import/export.
func (b *Bot) onCodesMenu(c chat) {
	b.mu.Lock()
	s := b.settingsLocked()
	codes := b.lastCodes
	b.mu.Unlock()

	var sb strings.Builder
	sb.WriteString("🎨 <b>کد ایموجی کشورها</b>\n\n")
	sb.WriteString("📊 " + itoaSafe(len(s.EmojiCodes)) + " کد ثبت شده\n")
	if codes != nil {
		sb.WriteString("\n🌍 <b>کشورهای آخرین ران:</b>\n")
		for _, iso := range codes {
			flag := engine.Flag(iso)
			name, ok := countries.Names(iso, s.OutLang)
			if !ok {
				name = iso
			}
			if v, has := s.EmojiCodes[iso]; has && v != "" {
				sb.WriteString(fmt.Sprintf("%s %s — <code>%s</code>\n", flag, name, v))
			} else {
				sb.WriteString(fmt.Sprintf("%s %s — <b>کد ندارد</b>\n", flag, name))
			}
		}
	}
	sb.WriteString("\nروی دکمه‌ی هر کشور بزن تا کدش رو ثبت یا عوض کنی.\n")
	sb.WriteString("📥/📤 همون فایل بکاپ تپ Caption اپ هست — هر دو طرف راحت می‌خونه و آپدیت می‌مونه.")

	rows := [][]string{}
	for _, iso := range codes {
		flag := engine.Flag(iso)
		name, ok := countries.Names(iso, s.OutLang)
		if !ok {
			name = iso
		}
		rows = append(rows, []string{"✏️ " + flag + " " + name, "cap:set:" + iso})
	}
	rows = append(rows,
		[]string{"📥 وارد کردن کدها", "cap:import"},
		[]string{"📤 خروجی گرفتن کدها", "cap:export"},
		[]string{"↩️ بازگشت", "caption"})
	b.nav(c, sb.String(), rows)
}

// setCodePrompt asks for one country's numeric custom-emoji id.
func (b *Bot) setCodePrompt(c chat, iso string) {
	b.mu.Lock()
	b.awaiting = awaitCodeSet
	b.awaitISO = iso
	b.mu.Unlock()
	flag := engine.Flag(iso)
	name, _ := countries.Names(iso, "fa")
	b.nav(c,
		"✏️ <b>کد ایموجی: "+flag+" "+name+"</b>\n\n"+
			"کد عددی ایموجی رو <b>پیست</b> کن (همون عددی که توی تپ Caption اپ می‌زنی).\n"+
			"برای حذف کد بنویس: <code>delete</code>",
		[][]string{{"✖️ لغو", "cap:codes"}})
}

func (b *Bot) onSetCode(c chat, iso, text string) {
	text = strings.TrimSpace(text)
	b.mu.Lock()
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
		msg = "🗑️ کد "+flag+" "+name+" حذف شد."
	} else {
		msg = "✅ کد "+flag+" "+name+": <code>"+escapeHTML(text)+"</code>"
	}
	b.nav(c, msg,
		[][]string{{"🎨 کد ایموجی کشورها", "cap:codes"}, {"↩️ بازگشت", "menu"}})
}

// importPrompt asks for the app's exported codes text (paste or .txt file).
func (b *Bot) importPrompt(c chat) {
	b.mu.Lock()
	b.awaiting = awaitCodesImport
	b.mu.Unlock()
	b.nav(c,
		"📥 <b>وارد کردن کدها</b>\n\n"+
			"توی اپ: تپ <b>Caption</b> → دکمه‌ی «کپی کدها» → اینجا <b>پیست</b> کن —\n"+
			"یا مستقیم <b>فایل بکاپ .txt</b> اپ رو بفرست.\n\n"+
			"فرمت: هر خط <code>XX=کد</code>، مثلاً:\n<code>DE=6050626661043411760\nFR=5395616385734833119</code>",
		[][]string{{"✖️ لغو", "cap:codes"}})
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
	b.nav(c,
		fmt.Sprintf("✅ <b>%d کد</b> وارد شد. کل کدهای ثبت‌شده: <b>%d</b>", added, total),
		[][]string{{"🎨 کد ایموجی کشورها", "cap:codes"}, {"↩️ بازگشت", "menu"}})
}

// exportCodesText produces exactly the app's backup format, so the app's
// «بازیابی از فایل» accepts it as-is (and vice versa).
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
		b.nav(c,
			"🤔 هنوز کدی ثبت نشده.\nاول کدها رو وارد کن (📥) یا تک‌تک ثبت کن.",
			[][]string{{"🎨 کد ایموجی کشورها", "cap:codes"}})
		return
	}
	_, _ = b.api.sendMessage(c.ID,
		"📤 <b>خروجی گرفتن کدها</b> — فرمت مشترک اپ و ربات\n\n"+
			"کپی کن؛ توی اپ: تپ Caption → <b>بازیابی از فایل</b> → پیست کن:\n\n<pre>"+escapeHTML(t)+"</pre>")
}

// ------------------------------------------------------------------
// settings

var parallelSteps = []int{2, 3, 4, 6, 8}
var timeoutSteps = []int{5, 10, 15, 20, 30}

func (b *Bot) onSettingsMenu(c chat) {
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
			chState = "روشن (| " + s.Channel + ")"
		} else {
			chState = "روشن — بدون اسم!"
		}
	}
	unkState := "خاموش"
	if s.IncludeUnknown {
		unkState = "روشن"
	}
	rows := [][]string{
		{fmt.Sprintf("🔢 همزمانی: %d", s.Parallel), "set:parallel"},
		{fmt.Sprintf("⏱️ تایم‌اوت: %d ثانیه", s.TimeoutSec), "set:timeout"},
		{fmt.Sprintf("🌐 زبان اسم: %s", langName), "set:lang"},
		{fmt.Sprintf("📢 سافیکس کانال: %s", chState), "set:channel"},
		{fmt.Sprintf("🔗 خروجی بدون کشور: %s", unkState), "set:unknown"},
		{"↩️ بازگشت", "menu"},
	}
	b.nav(c,
		"⚙️ <b>تنظیمات</b>\n\n"+
			"روی هر گزینه بزن تا تغییر کنه — همه ذخیره می‌شن.\n\n"+
			"📢 سافیکس کانال = به آخر اسم هر کانفیگ « | اسم_کانال» اضافه بشه (مثل اپ).\n"+
			"🔗 خروجی بدون کشور = لینک‌های «وصل شد ولی کشور نگرفت» توی لیست لینک‌ها بیان یا نه.",
		rows)
}

// cycleSetting flips one setting. IMPORTANT: mutate b.settings directly under
// the lock — settingsLocked() returns a copy and saving after mutating the
// copy silently loses the change (v1.0.7 regression fix).
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
		on := b.settings.IncludeChannel
		noName := b.settings.Channel == ""
		if on && noName {
			b.awaiting = awaitChannelName
		}
		b.save()
		b.mu.Unlock()
		if on && noName {
			b.nav(c,
				"📢 <b>سافیکس کانال روشن شد</b>\n\n"+
					"اسم کانال رو <b>پیست</b> کن (مثلاً <code>Wpnfa</code>) — به آخر اسم همه کانفیگ‌ها اضافه می‌شه:\n\n"+
					"<pre>🇩 آلمان | Wpnfa</pre>",
				[][]string{{"✖️ لغو و خاموش کردن", "set:channel"}})
			return
		}
		state := "خاموش"
		if on {
			state = "روشن"
		}
		b.nav(c, fmt.Sprintf("📢 سافیکس کانال <b>%s</b> شد.", state),
			[][]string{{"⚙️ تنظیمات", "settings"}})
		return
	case "unknown":
		b.settings.IncludeUnknown = !b.settings.IncludeUnknown
	}
	b.save()
	b.mu.Unlock()
	b.onSettingsMenu(c)
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
	b.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("👥 <b>ادمین‌ها</b>\n\n")
	sb.WriteString("افزودن / حذف افرادی که به ربات دسترسی دارن.\nصاحب ربات همیشه دسترسی کامل داره و توی لیست نیازی به ثبت نداره.\n\n")
	sb.WriteString("👑 صاحب: <code>" + itoaSafe(int(b.ownerID)) + "</code>\n")
	if len(s.Admins) == 0 {
		sb.WriteString("\n(ادمین اضافه‌ای ثبت نشده)\n")
	}
	rows := [][]string{}
	var row []string
	for _, a := range s.Admins {
		row = append(row, "❌ "+itoaSafe(int(a)), "admin:rm:"+itoaSafe(int(a)))
		if len(row) == 4 { // two buttons per row
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows,
		[]string{"➕ افزودن ادمین", "admin:add"},
		[]string{"↩️ بازگشت", "menu"})
	b.nav(c, sb.String(), rows)
}

func (b *Bot) adminAddPrompt(c chat) {
	if c.ID != b.ownerID {
		b.sendMain(c, "")
		return
	}
	b.mu.Lock()
	b.awaiting = awaitAdminAdd
	b.mu.Unlock()
	b.nav(c,
		"➕ <b>افزودن ادمین</b>\n\n"+
			"<b>ID عددی</b> اون شخص رو بفرست.\n"+
			"روش گرفتن ID: اون شخص کافیه <code>/id</code> رو به همین ربات بفرسته تا IDش برگرده.",
		[][]string{{"✖️ لغو", "admins"}})
}

func (b *Bot) onAdminAdd(c chat, text string) {
	if c.ID != b.ownerID {
		b.sendMain(c, "")
		return
	}
	idStr := strings.TrimSpace(strings.TrimPrefix(text, "@"))
	id, ok := parseID(idStr)
	if !ok || id <= 0 {
		b.nav(c,
			"🤔 این یک ID عددی معتبر نیست.\nعدد را از دستورات <code>/id</code> بگیرید (مثلاً <code>123456789</code>).",
			[][]string{{"👥 ادمین‌ها", "admins"}})
		return
	}
	b.mu.Lock()
	if id == b.ownerID {
		b.mu.Unlock()
		b.nav(c, "شما خودتان صاحب ربات هستید و همیشه دسترسی کامل دارید 🙂",
			[][]string{{"👥 ادمین‌ها", "admins"}})
		return
	}
	dup := false
	for _, a := range b.settings.Admins {
		if a == id {
			dup = true
		}
	}
	if !dup {
		b.settings.Admins = append(b.settings.Admins, id)
	}
	b.save()
	b.mu.Unlock()
	if dup {
		b.nav(c, "این ID قبلاً ثبت شده بود.",
			[][]string{{"👥 ادمین‌ها", "admins"}})
		return
	}
	b.nav(c,
		"✅ <code>"+itoaSafe(int(id))+"</code> به‌عنوان ادمین اضافه شد.\n"+
			"از این به بعد تمام امکانات ربات (اسکن، تنظیمات، کپشن) در دسترس اوست.",
		[][]string{{"👥 ادمین‌ها", "admins"}})
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
	b.mu.Lock()
	out := b.settings.Admins[:0]
	removed := false
	for _, a := range b.settings.Admins {
		if a == id {
			removed = true
			continue
		}
		out = append(out, a)
	}
	b.settings.Admins = out
	b.save()
	b.mu.Unlock()
	if !removed {
		b.nav(c, "این ID توی لیست نبود.",
			[][]string{{"👥 ادمین‌ها", "admins"}})
		return
	}
	b.nav(c,
		"🗑️ <code>"+itoaSafe(int(id))+"</code> از ادمین‌ها حذف شد و دسترسی‌اش قطع شد.",
		[][]string{{"👥 ادمین‌ها", "admins"}})
}

func parseID(s string) (int64, bool) {
	if s == "" {
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
	b.nav(c,
		"ℹ️ <b>ConfigScanner Bot</b> <code>v"+BotVersion+"</code>\n\n"+
			"• 📡 هر سرور با یک پروسس xray جدا و پورت اختصاصی تست می‌شه\n"+
			"• 💨 hy2 (سالمندر/جکوهو) با هسته‌ی اصلی hysteria — دقیقاً مثل اپ\n"+
			"• 🔒 لینک‌های <code>insecure=1</code> با پین کردن گواهی تست می‌شن (مثل اپ)\n"+
			"• 💀 پروب «تونل مرده» قبل از جیو (ریست فوری = غیرقابل دسترس)\n"+
			"• 🌐 ۶ سرویس جیو در موج‌های موازی + رأی‌گیری (۲ رأی = مطمئن)\n"+
			"• 📉 fallback HTTP ساده (ip-api) برای خروجی‌هایی که TLS رو می‌بندن\n"+
			"• ⏳ تلاش نهایی ۲۰ ثانیه‌ای برای خروجی‌های کند (فقط timeout)\n"+
			"• 🏷️ اسم یکتا برای هر خروجی (dedup پنل‌ها چیزی حذف نمی‌کنه)\n"+
			"• 🎨 کپشن با custom emoji + ورودی/خروجی کدها با اپ\n\n"+
			"⚙️ فعلی: "+itoaSafe(s.Parallel)+" همزمان · "+itoaSafe(s.TimeoutSec)+" ثانیه · "+langName+
			"\n\n⚠️ نکته: نتیجه‌ها از دید IP سرور (Railway) هستن؛ بعضی سرورها دیتاسنتر رو فیلتر می‌کنن و از ایران سالم‌ان.",
		[][]string{{"↩️ بازگشت", "menu"}})
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
		if cq.From != nil && !b.isAllowed(cq.From.ID) {
			return
		}
		b.mu.Lock()
		running := b.running
		b.mu.Unlock()
		data := cq.Data
		b.api.answerCallback(cq.ID, "")
		if running {
			if data == "noop" {
				return
			}
			_, _ = b.api.sendMessage(c.ID, "⏳ اسکن در حال اجراست — صبر کن تمام شود.")
			return
		}
		switch {
		case data == "menu":
			b.sendMain(c, "")
		case data == "scan":
			b.onScanRequest(c)
		case data == "scan:start":
			b.startRun(c)
		case data == "scan:cancel":
			b.cancelPending(c)
		case data == "settings":
			b.onSettingsMenu(c)
		case data == "caption":
			b.onCaptionMenu(c)
		case data == "cap:edit":
			b.captionEditPrompt(c)
		case data == "cap:codes":
			b.onCodesMenu(c)
		case strings.HasPrefix(data, "cap:set:"):
			b.setCodePrompt(c, strings.TrimPrefix(data, "cap:set:"))
		case data == "cap:import":
			b.importPrompt(c)
		case data == "cap:export":
			b.onCodesExport(c)
		case data == "cap:preview":
			b.captionPreview(c)
		case data == "cap:default":
			b.mu.Lock()
			b.settings.CaptionTemplate = DefaultCaptionTemplate
			b.save()
			b.mu.Unlock()
			b.nav(c, "↩️ قالب پیش‌فرض برگشت.",
				[][]string{{"🏷️ کپشن و پرچم", "caption"}})
		case data == "runlog":
			b.sendRunLog(c)
		case strings.HasPrefix(data, "set:"):
			b.cycleSetting(c, strings.TrimPrefix(data, "set:"))
		case data == "about":
			b.onAbout(c)
		case data == "admins":
			b.onAdminMenu(c)
		case data == "admin:add":
			b.adminAddPrompt(c)
		case strings.HasPrefix(data, "admin:rm:"):
			b.onAdminRemove(c, strings.TrimPrefix(data, "admin:rm:"))
		case data == "noop":
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
		if err == nil {
			text = string(bb)
		}
	}
	if text == "" {
		_, _ = b.api.sendMessage(c.ID, "فقط متن یا فایل .txt بفرست.")
		return
	}

	switch strings.ToLower(strings.TrimSpace(text)) {
	case "/start", "/help":
		b.sendMain(c, "")
		return
	case "/settings":
		b.onSettingsMenu(c)
		return
	case "/scan":
		b.onScanRequest(c)
		return
	case "/caption":
		b.onCaptionMenu(c)
		return
	case "/about":
		b.onAbout(c)
		return
	case "/id":
		_, _ = b.api.sendMessage(c.ID, fmt.Sprintf("ID شما: <code>%d</code>", c.ID))
		return
	case "/cancel":
		b.mu.Lock()
		aw := b.awaiting
		b.awaiting = awaitNone
		b.mu.Unlock()
		if aw != awaitNone {
			b.nav(c, "✖️ لغو شد.", [][]string{{"↩️ بازگشت", "menu"}})
			return
		}
		b.sendMain(c, "")
		return
	}

	// awaiting input
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
		if name == "" {
			// empty name: turn the feature back off so the state is honest
			b.settings.IncludeChannel = false
			b.save()
			b.mu.Unlock()
			b.nav(c, "📢 لغو شد (اسم خالی) — سافیکس کانال خاموش است.",
				[][]string{{"⚙️ تنظیمات", "settings"}})
		} else {
			b.settings.Channel = name
			b.save()
			b.mu.Unlock()
			b.nav(c,
				"✅ سافیکس کانال <b>روشن</b>: به آخر همه اسم‌ها <b> | "+escapeHTML(name)+"</b> اضافه می‌شه",
				[][]string{{"⚙️ تنظیمات", "settings"}})
		}
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
	}

	// default: treat as config input when it looks like configs
	t := strings.TrimSpace(text)
	if strings.Contains(t, "://") {
		b.onConfigInput(c, text)
		return
	}
	b.sendMain(c, "")
}

func (b *Bot) Loop() error {
	name, err := b.getMeName()
	if err != nil {
		return err
	}
	fmt.Printf("bot started as @%s, owner=%d (v%s)\n", name, b.ownerID, BotVersion)
	offset := 0
	for {
		ups, err := b.api.getUpdates(offset+1, 50)
		if err != nil {
			// Telegram only retains ~24h of updates. After a restart the
			// offset starts at 0; when the bot's last updates are older
			// than that window the API answers "You reached the start of
			// the range" and polling would loop forever on the same stale
			// offset. Jump to the tail: an offset beyond the latest update
			// returns an empty list, and polling resumes from the present.
			if strings.Contains(err.Error(), "start of the range") {
				fmt.Println("getUpdates: start of the range - resetting offset to tail")
				offset = 1 << 60
				continue
			}
			fmt.Println("getUpdates error:", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID
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
