package bot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
)

// Opt-in live test: sends the REAL main menu to a real chat and then EDITS
// the same message in place (nav behaviour) so the exact production payload
// is verified against Telegram's API.
// Run with:  LIVE_TG_TOKEN=... LIVE_TG_CHAT=... go test ./internal/bot/ -run TestLive
func TestLiveSendMenu(t *testing.T) {
	token := os.Getenv("LIVE_TG_TOKEN")
	chat := os.Getenv("LIVE_TG_CHAT")
	if token == "" || chat == "" {
		t.Skip("set LIVE_TG_TOKEN and LIVE_TG_CHAT to run")
	}
	var chatID int64
	if _, err := fmt.Sscanf(chat, "%d", &chatID); err != nil {
		t.Fatalf("bad chat id %q: %v", chat, err)
	}

	intro := "📡 <b>ConfigScanner Bot</b> <code>v" + BotVersion + "</code>\n\n✅ تست زنده v1.0.7: اگه این پیام رو با دکمه‌ها می‌بینی، ربات درست کار می‌کنه. بعد ۲ ثانیه همین پیام ادیت می‌شه (نه پیام جدید) تا سیستم دکمه‌های ثابت تأیید بشه."
	rows := [][]string{
		{"📡 اسکن کانفیگ", "scan", "⚙️ تنظیمات", "settings"},
		{"🏷️ کپشن و پرچم", "caption", "ℹ️ درباره", "about"},
		{"👥 ادمین‌ها", "admins"},
	}

	body, _ := json.Marshal(menuPayload(chatID, intro, rows))
	resp, err := http.Post("https://api.telegram.org/bot"+token+"/sendMessage",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var res struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
		Err string `json:"description"`
	}
	if err := json.Unmarshal(rb, &res); err != nil {
		t.Fatalf("bad response: %s", rb)
	}
	if !res.OK {
		t.Fatalf("telegram rejected the payload: %s", res.Err)
	}
	t.Logf("menu delivered: message %d", res.Result.MessageID)

	// now edit the SAME message (nav in-place behaviour)
	edit := map[string]any{
		"chat_id":    chatID,
		"message_id": res.Result.MessageID,
		"text":       "✅ <b>تست ادیت درجا</b>\n\nهمین پیام ادیت شد — پیام جدیدی ساخته نشد. دکمه‌های جدید:",
		"parse_mode": "HTML",
		"reply_markup": map[string]any{
			"inline_keyboard": buildKeyboard([][]string{
				{"🟩 ادیت درجا تأیید شد", "noop"},
				{"↩️ بازگشت", "menu"},
			}),
		},
	}
	eb, _ := json.Marshal(edit)
	r2, err := http.Post("https://api.telegram.org/bot"+token+"/editMessageText",
		"application/json", bytes.NewReader(eb))
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	rb2, _ := io.ReadAll(io.LimitReader(r2.Body, 1<<20))
	var res2 struct {
		OK  bool   `json:"ok"`
		Err string `json:"description"`
	}
	if err := json.Unmarshal(rb2, &res2); err != nil {
		t.Fatalf("bad edit response: %s", rb2)
	}
	if !res2.OK {
		t.Fatalf("telegram rejected the edit: %s", res2.Err)
	}
	t.Log("in-place edit OK")
}
