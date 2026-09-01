package bot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cfgscanbot/internal/engine"
)

const (
	BotVersion = "1.0.5"
	// DefaultCaptionTemplate mirrors the app's caption template.
	DefaultCaptionTemplate = "NpvTunnel [6050626661043411760]  \n[5395616385734833119] لوکیشن | Location {{FLAGS}}\n\n[617260195842813119] @Wpnfa  \n\n[5206607083980820]  \n#npvtunnel #vpn #v2ray\n#فیلترشکن #vpn #پروکسی"
)

// Settings is the persisted bot configuration.
type Settings struct {
	Parallel       int    `json:"parallel"`
	TimeoutSec     int    `json:"timeout_sec"`
	OutLang        string `json:"out_lang"` // fa | en
	Channel        string `json:"channel"`
	IncludeChannel bool   `json:"include_channel"`
	IncludeUnknown bool   `json:"include_unknown"`
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
	awaitCaptionCodes
	awaitChannelName
	awaitAdminAdd
)

type Bot struct {
	api      *tgAPI
	ownerID  int64
	dataDir  string
	xrayBin  string

	mu        sync.Mutex
	settings  Settings
	running   bool
	pending   []string // raw config lines awaiting confirmation
	runMessage int      // progress message id
	runChat   int64
	awaiting  awaiting

	lastCodes []string
}

func NewBot(token string, ownerID int64, dataDir, xrayBin string) *Bot {
	if xrayBin == "" {
		xrayBin = "xray"
	}
	b := &Bot{
		api:     newAPI(token),
		ownerID: ownerID,
		dataDir: dataDir,
		xrayBin: xrayBin,
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
// fixed main menu (the admins row is owner-only)

func (b *Bot) menuFor(id int64) [][]string {
	// flat rows: [text1, cb1, text2, cb2]
	rows := [][]string{
		{"📡 اسکن کانفیگ‌ها | scan", "scan", "⚙️ تنظیمات | settings", "settings"},
		{"🏷️ کپشن و پرچم‌ها | caption", "caption", "ℹ️ درباره | about", "about"},
	}
	if id == b.ownerID {
		rows = append(rows, []string{"👥 ادمین‌ها | admins", "admins"})
	}
	return rows
}

func (b *Bot) sendMain(chatID int64, intro string) error {
	if intro == "" {
		intro = "📡 <b>ConfigScanner Bot</b> <code>v" + BotVersion + "</code>\n\nاسکنر خروجی کانفیگ‌ها — دقیقاً همون منطق اپ: هر سرور با xray جدا تست می‌شه، کشور خروجی با رأی‌گیری ۶ سرویس جیو تشخیص داده می‌شه، و خروجی با پرچم و اسم یکتا برمی‌گرده.\n\nیک دکمه بزن 👇"
	}
	_, err := b.api.sendMenu(chatID, intro, b.menuFor(chatID))
	return err
}

// ------------------------------------------------------------------
// scan flow

func (b *Bot) onScanRequest(c chat) {
	_, _ = b.api.sendWithKeyboard(c.ID,
		"📡 <b>اسکن کانفیگ‌ها</b>\n\nکانفیگ‌ها رو <b>پیست</b> کن یا <b>فایل .txt</b> بفرست.\nهر خط یه کانفیگ: vless, vmess, trojan, ss, hy2 …",
		[][]string{{"🗑️ بازگشت | back", "menu"}}, "sendMessage")
	b.mu.Lock()
	b.pending = nil
	b.mu.Unlock()
}

func (b *Bot) onConfigInput(c chat, raw string) {
	lines := splitConfigLines(raw)
	if len(lines) == 0 {
		_, _ = b.api.sendWithKeyboard(c.ID,
			"🤔 کانفیگی توی پیام پیدا نشد. هر خط باید با <code>vless://</code>، <code>trojan://</code>، <code>vmess://</code>، <code>ss://</code> یا <code>hysteria2://</code> شروع بشه.\n\nدوباره بفرست یا:",
			[][]string{{"🗑️ بازگشت | back", "menu"}}, "sendMessage")
		return
	}
	b.mu.Lock()
	b.pending = lines
	s := b.settingsLocked()
	b.mu.Unlock()
	_, _ = b.api.sendWithKeyboard(c.ID,
		fmt.Sprintf("✅ <b>%d کانفیگ</b> دریافت شد.\n\nبرای شروع اسکن دکمه‌ی زیر رو بزن (زمان تقریبی: %s):",
			len(lines), estimateDuration(len(lines), s.Parallel)),
		[][]string{
			{"🚀 شروع اسکن | start", "scan:start"},
			{"🗑️ لغو و بازگشت | cancel", "menu"},
		}, "sendMessage")
}

func (b *Bot) startRun(c chat) {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		_, _ = b.api.sendMessage(c.ID, "⏳ هنوز یه اسکن در حال اجراست — صبر کن تموم بشه.")
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
	b.runMessage = 0
	cfg := b.settingsLocked()
	b.mu.Unlock()

	servers := engine.ParseInput(raw)
	if len(servers) == 0 {
		b.finishRunGuard()
		_, _ = b.api.sendMessage(c.ID, "🤔 هیچ خط قابل شناسایی نبود.")
		return
	}

	id, _ := b.api.sendWithKeyboard(c.ID,
		fmt.Sprintf("🚀 <b>اسکن شروع شد</b>\n\n%d سرور · %d همزمان · تایم‌اوت %ds\n\n(دکمه‌ها تا پایان اسکن غیرفعال‌ان)",
			len(servers), cfg.Parallel, cfg.TimeoutSec),
		[][]string{{"⏳ در حال اجرا…", "noop"}}, "sendMessage")
	b.mu.Lock()
	b.runMessage = id
	b.mu.Unlock()

	eng := engine.NewEngine(engine.Config{
		XrayBin:        b.xrayBin,
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
	total := len(servers)

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
		_ = b.api.editText(msgID, chatID, fmt.Sprintf(
			"🔍 <b>اسکن در حال اجرا…</b> %d از %d\n\n%s\n\n⚙️ %d همزمان · تایم‌اوت %ds",
			d, tot, lastLine, cfg.Parallel, cfg.TimeoutSec))
	})

	b.mu.Lock()
	b.lastCodes = res.CountryCodes
	b.mu.Unlock()

	summary := res.Summary(cfg.OutLang)
	b.mu.Lock()
	msgID := b.runMessage
	chatID := b.runChat
	b.mu.Unlock()
	if msgID > 0 {
		_ = b.api.editText(msgID, chatID, fmt.Sprintf("✅ <b>اسکن تموم شد!</b>\n\n%s", summary))
	}

	// results file
	var sb strings.Builder
	for _, l := range res.Lines {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	_ = b.api.sendDocument(chatID, []byte(sb.String()),
		"cfgscan_results.txt", "📄 <b>خروجی کامل</b>\n"+summary)

	// working links only
	if len(res.Links) > 0 {
		cap := fmt.Sprintf("🔗 <b>%d لینک سالم</b>", len(res.Links))
		if cfg.IncludeUnknown {
			cap += " (شامل " + itoaSafe(res.NoCountry) + " بدون کشور)"
		}
		_ = b.api.sendDocument(chatID, []byte(strings.Join(res.Links, "\n")),
			"cfgscan_links.txt", cap)
	}

	// caption (if we have countries)
	if res.CountryCodes != nil && cfg.CaptionTemplate != "" {
		caption := b.buildCaption(res.CountryCodes)
		if caption != "" {
			_ = b.api.sendDocument(chatID, []byte(caption), "caption.txt",
				"🏷️ <b>کپشن آماده</b> — کپی کن و با فایل کانفیگ‌ها پست کن")
		}
	}

	_, _ = b.api.sendMenu(chatID, "تمام شد ✅\n\n" + summary + "\n\n🚀 اسکن بعدی؟", b.menuFor(chatID))
	b.finishRunGuard()
	_ = total
}

func (b *Bot) finishRunGuard() {
	b.mu.Lock()
	b.running = false
	b.mu.Unlock()
}

func (b *Bot) runLog(line string) {
	fmt.Println("[" + time.Now().Format("15:04:05") + "] " + line)
}

// ------------------------------------------------------------------
// caption

func (b *Bot) onCaptionMenu(c chat) {
	b.mu.Lock()
	_ = b.settingsLocked()
	hasLast := b.lastCodes != nil
	b.mu.Unlock()
	rows := [][]string{
		{"✏️ ویرایش قالب کپشن", "cap:edit"},
		{"🎨 کد ایموجی کشورها", "cap:codes"},
	}
	if hasLast {
		rows = append(rows, []string{"👁️ پیش‌نمایش کپشن", "cap:preview"})
	}
	rows = append(rows,
		[]string{"↩️ قالب پیش‌فرض", "cap:default"},
		[]string{"🗑️ بازگشت | back", "menu"})
	_, _ = b.api.sendWithKeyboard(c.ID,
		"🏷️ <b>کپشن و پرچم‌ها</b>\n\nقالب با <code>{{FLAGS}}</code> پر از پرچم‌های همان ران می‌شه؛ کد ایموجی = اعدادی که توی تپ Caption اپ وارد می‌کنی (custom emoji id).",
		rows, "sendMessage")
}

func (b *Bot) captionEditPrompt(c chat) {
	b.mu.Lock()
	s := b.settingsLocked()
	b.awaiting = awaitCaptionTemplate
	b.mu.Unlock()
	_, _ = b.api.sendWithKeyboard(c.ID,
		"✏️ <b>قالب فعلی:</b>\n<code>"+escapeHTML(s.CaptionTemplate)+"</code>\n\nقالب جدید رو <b>پیست</b> کن (باید <code>{{FLAGS}}</code> داشته باشه).",
		[][]string{{"✖️ لغو", "caption"}}, "sendMessage")
}

func (b *Bot) captionCodesPrompt(c chat) {
	b.mu.Lock()
	b.awaiting = awaitCaptionCodes
	b.mu.Unlock()
	_, _ = b.api.sendWithKeyboard(c.ID,
		"🎨 <b>کد ایموجی کشورها</b>\n\nبه هر خط یه کشور و کد بده، مثلاً:\n<code>DE=6050626661043411760\nFR=5395616385734833119</code>\n\n(همین کدهایی که توی تپ Caption اپ ثبت می‌کنی)",
		[][]string{{"✖️ لغو", "caption"}}, "sendMessage")
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
	_, _ = b.api.sendWithKeyboard(c.ID, "✅ قالب کپشن ذخیره شد.",
		[][]string{{"🏷️ منوی کپشن", "caption"}, {"🗑️ بازگشت | back", "menu"}}, "sendMessage")
}

func (b *Bot) onCaptionCodes(c chat, text string) {
	b.mu.Lock()
	if b.settings.EmojiCodes == nil {
		b.settings.EmojiCodes = map[string]string{}
	}
	added := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.ToUpper(strings.TrimSpace(kv[0]))
		v := strings.TrimSpace(kv[1])
		if len(k) == 2 && v != "" {
			b.settings.EmojiCodes[k] = v
			added++
		}
	}
	total := len(b.settings.EmojiCodes)
	b.save()
	b.mu.Unlock()
	if added == 0 {
		_, _ = b.api.sendWithKeyboard(c.ID,
			"🤔 هیچ خط معتبری نبود (فرمت: <code>DE=123456789</code>).",
			[][]string{{"🏷️ منوی کپشن", "caption"}}, "sendMessage")
		return
	}
	_, _ = b.api.sendWithKeyboard(c.ID,
		fmt.Sprintf("✅ %d کد ذخیره شد. کل کدهای ثبت‌شده: %d", added, total),
		[][]string{{"🏷️ منوی کپشن", "caption"}, {"🗑️ بازگشت | back", "menu"}}, "sendMessage")
}

func (b *Bot) captionPreview(c chat) {
	b.mu.Lock()
	codes := b.lastCodes
	b.mu.Unlock()
	if codes == nil {
		_, _ = b.api.sendMessage(c.ID, "اول یه اسکن انجام بده تا کشورها ثبت بشن.")
		return
	}
	caption := b.buildCaption(codes)
	if caption == "" {
		_, _ = b.api.sendMessage(c.ID, "کپشنی ساخته نشد.")
		return
	}
	_ = b.api.sendDocument(c.ID, []byte(caption), "caption_preview.txt",
		"👁️ <b>پیش‌نمایش</b> (کشورهای آخرین ران)")
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
		chState = "روشن"
	}
	if s.IncludeChannel && s.Channel == "" {
		chState = "روشن (بدون اسم!)"
	}
	unkState := "خاموش"
	if s.IncludeUnknown {
		unkState = "روشن"
	}
	rows := [][]string{
		{fmt.Sprintf("🔢 همزمانی: %d", s.Parallel), "set:parallel"},
		{fmt.Sprintf("⏱️ تایم‌اوت: %ds", s.TimeoutSec), "set:timeout"},
		{fmt.Sprintf("🌐 زبان اسم: %s", langName), "set:lang"},
		{fmt.Sprintf("📢 سافیکس کانال: %s", chState), "set:channel"},
		{fmt.Sprintf("🔗 لینک بدون کشور در خروجی: %s", unkState), "set:unknown"},
		{"🗑️ بازگشت | back", "menu"},
	}
	_, _ = b.api.sendWithKeyboard(c.ID,
		"⚙️ <b>تنظیمات</b>\n\nروی هر خط بزن تا عوض بشه. همه چیز ذخیره می‌شه.",
		rows, "sendMessage")
}

func (b *Bot) cycleSetting(c chat, key string) {
	b.mu.Lock()
	s := b.settingsLocked()
	switch key {
	case "parallel":
		idx := indexInt(parallelSteps, s.Parallel)
		s.Parallel = parallelSteps[(idx+1)%len(parallelSteps)]
	case "timeout":
		idx := indexInt(timeoutSteps, s.TimeoutSec)
		s.TimeoutSec = timeoutSteps[(idx+1)%len(timeoutSteps)]
	case "lang":
		if s.OutLang == "fa" {
			s.OutLang = "en"
		} else {
			s.OutLang = "fa"
		}
	case "channel":
		s.IncludeChannel = !s.IncludeChannel
		b.save()
		b.mu.Unlock()
		if s.IncludeChannel && s.Channel == "" {
			_, _ = b.api.sendWithKeyboard(c.ID,
				"📢 سافیکس کانال روشن شد.\n\nاسم کانال رو <b>پیست</b> کن (مثلاً <code>Wpnfa</code>) — آخر اسم همه کانفیگ‌ها اضافه می‌شه:",
				[][]string{{"✖️ لغو", "settings"}}, "sendMessage")
			b.mu.Lock()
			b.awaiting = awaitChannelName
		} else {
			_, _ = b.api.sendWithKeyboard(c.ID, "📢 سافیکس کانال خاموش شد.",
				[][]string{{"⚙️ تنظیمات", "settings"}}, "sendMessage")
		}
		b.mu.Unlock()
		return
	case "unknown":
		s.IncludeUnknown = !s.IncludeUnknown
	}
	b.save()
	b.mu.Unlock()
	b.onSettingsMenu(c)
}

// ------------------------------------------------------------------
// admins (owner only)

func (b *Bot) onAdminMenu(c chat) {
	if c.ID != b.ownerID {
		b.sendMain(c.ID, "")
		return
	}
	b.mu.Lock()
	s := b.settingsLocked()
	b.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("👥 <b>ادمین‌ها</b>\n\n")
	sb.WriteString("شخص‌هایی که اجازه استفاده از ربات رو دارن. صاحب ربات همیشه دسترسی کامل داره.\n\n")
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
		[]string{"🗑️ بازگشت | back", "menu"})
	_, _ = b.api.sendWithKeyboard(c.ID, sb.String(), rows, "sendMessage")
}

func (b *Bot) adminAddPrompt(c chat) {
	if c.ID != b.ownerID {
		b.sendMain(c.ID, "")
		return
	}
	b.mu.Lock()
	b.awaiting = awaitAdminAdd
	b.mu.Unlock()
	_, _ = b.api.sendWithKeyboard(c.ID,
		"➕ <b>افزودن ادمین</b>\n\n<b>ID عددی</b> شخص رو بفرست.\n(شخص می‌تونه با /id توی همین ربات ID خودش رو بگیره)",
		[][]string{{"✖️ لغو", "admins"}}, "sendMessage")
}

func (b *Bot) onAdminAdd(c chat, text string) {
	if c.ID != b.ownerID {
		b.sendMain(c.ID, "")
		return
	}
	idStr := strings.TrimSpace(strings.TrimPrefix(text, "@"))
	id, ok := parseID(idStr)
	if !ok || id <= 0 {
		_, _ = b.api.sendWithKeyboard(c.ID,
			"🤔 این یه ID عددی معتبر نیست. عددی که /id می‌ده رو بفرست (مثلاً <code>123456789</code>).",
			[][]string{{"👥 ادمین‌ها", "admins"}}, "sendMessage")
		return
	}
	b.mu.Lock()
	if id == b.ownerID {
		b.mu.Unlock()
		_, _ = b.api.sendWithKeyboard(c.ID, "شما خودت صاحب رباتی و همیشه دسترسی داری 🙂",
			[][]string{{"👥 ادمین‌ها", "admins"}}, "sendMessage")
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
		_, _ = b.api.sendWithKeyboard(c.ID, "این ID قبلاً ثبت شده بود.",
			[][]string{{"👥 ادمین‌ها", "admins"}}, "sendMessage")
		return
	}
	_, _ = b.api.sendWithKeyboard(c.ID,
		"✅ <code>"+itoaSafe(int(id))+"</code> به‌عنوان ادمین اضافه شد. از این به بعد می‌تونه از همه امکانات ربات استفاده کنه.",
		[][]string{{"👥 ادمین‌ها", "admins"}}, "sendMessage")
}

func (b *Bot) onAdminRemove(c chat, idStr string) {
	if c.ID != b.ownerID {
		b.sendMain(c.ID, "")
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
		_, _ = b.api.sendWithKeyboard(c.ID, "این ID توی لیست نبود.",
			[][]string{{"👥 ادمین‌ها", "admins"}}, "sendMessage")
		return
	}
	_, _ = b.api.sendWithKeyboard(c.ID,
		"🗑️ <code>"+itoaSafe(int(id))+"</code> از ادمین‌ها حذف شد و دسترسی‌اش قطع شد.",
		[][]string{{"👥 ادمین‌ها", "admins"}}, "sendMessage")
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
	_, _ = b.api.sendWithKeyboard(c.ID,
		"ℹ️ <b>ConfigScanner Bot</b> v"+BotVersion+"\n\n"+
			"• هر سرور با یه پروسس xray جدا و پورت اختصاصی تست می‌شه\n"+
			"• پروب «تونل مرده» قبل از جیو (ریست فوری = غیرقابل دسترس)\n"+
			"• ۶ سرویس جیو در موج‌های موازی + رأی‌گیری (۲ رأی = مطمئن)\n"+
			"• fallback HTTP ساده (ip-api) برای خروجی‌هایی که TLS رو می‌بندن\n"+
			"• تلاش نهایی ۲۰ ثانیه‌ای برای خروجی‌های کند (فقط timeout)\n"+
			"• اسم یکتا برای هر خروجی (dedup پنل‌ها چیزی حذف نمی‌کنه)\n"+
			"• کپشن با custom emoji — همون تپ Caption اپ\n\n"+
			"⚙️ فعلی: "+itoaSafe(s.Parallel)+" همزمان · "+itoaSafe(s.TimeoutSec)+"s · "+langName+
			"\n\n⚠️ نکته: نتیجه‌ها از دید IP سرور (Railway) هستن؛ بعضی سرورها دیتاسنتر رو فیلتر می‌کنن و از ایران سالم‌ان.",
		[][]string{{"🗑️ بازگشت | back", "menu"}}, "sendMessage")
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
			_, _ = b.api.sendMessage(c.ID, "⛔ این ربات خصوصی‌ه.")
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
			_, _ = b.api.sendMessage(c.ID, "⏳ اسکن در حال اجراست — صبر کن تموم بشه.")
			return
		}
		switch {
		case data == "menu":
			b.sendMain(c.ID, "")
		case data == "scan":
			b.onScanRequest(c)
		case data == "scan:start":
			b.startRun(c)
		case data == "settings":
			b.onSettingsMenu(c)
		case data == "caption":
			b.onCaptionMenu(c)
		case data == "cap:edit":
			b.captionEditPrompt(c)
		case data == "cap:codes":
			b.captionCodesPrompt(c)
		case data == "cap:preview":
			b.captionPreview(c)
		case data == "cap:default":
			b.mu.Lock()
			b.settings.CaptionTemplate = DefaultCaptionTemplate
			b.save()
			b.mu.Unlock()
			_, _ = b.api.sendWithKeyboard(c.ID, "↩️ قالب پیش‌فرض برگشت.",
				[][]string{{"🏷️ منوی کپشن", "caption"}}, "sendMessage")
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
		b.sendMain(c.ID, "")
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
			_, _ = b.api.sendMessage(c.ID, "✖️ لغو شد.")
			return
		}
		b.sendMain(c.ID, "")
		return
	}

	// awaiting input
	b.mu.Lock()
	aw := b.awaiting
	b.mu.Unlock()
	switch aw {
	case awaitCaptionTemplate:
		b.mu.Lock()
		b.awaiting = awaitNone
		b.mu.Unlock()
		b.onCaptionTemplate(c, text)
		return
	case awaitCaptionCodes:
		b.mu.Lock()
		b.awaiting = awaitNone
		b.mu.Unlock()
		b.onCaptionCodes(c, text)
		return
	case awaitChannelName:
		b.mu.Lock()
		b.awaiting = awaitNone
		name := strings.TrimSpace(strings.TrimPrefix(text, "@"))
		if name == "" {
			b.settings.IncludeChannel = false
			b.save()
			b.mu.Unlock()
			_, _ = b.api.sendMessage(c.ID, "📢 لغو شد (اسم خالی).")
		} else {
			b.settings.Channel = name
			b.save()
			b.mu.Unlock()
			_, _ = b.api.sendWithKeyboard(c.ID,
				"✅ سافیکس کانال: <b> | "+escapeHTML(name)+"</b>",
				[][]string{{"⚙️ تنظیمات", "settings"}}, "sendMessage")
		}
		return
	case awaitAdminAdd:
		b.mu.Lock()
		b.awaiting = awaitNone
		b.mu.Unlock()
		b.onAdminAdd(c, text)
		return
	}

	// default: treat as config input when it looks like configs
	t := strings.TrimSpace(text)
	if strings.Contains(t, "://") {
		b.onConfigInput(c, text)
		return
	}
	b.sendMain(c.ID, "")
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
		t := strings.TrimSpace(l)
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
