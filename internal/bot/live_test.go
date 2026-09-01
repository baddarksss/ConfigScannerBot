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

// Opt-in live test: sends the REAL main menu to a real chat so the exact
// production payload (menuPayload) is verified against Telegram's API.
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

	intro := "📡 <b>ConfigScanner Bot</b> <code>v" + BotVersion + "</code>\n\n✅ تست زنده: اگه این پیام رو با دکمه‌ها می‌بینی، ربات درست جواب می‌ده."
	rows := [][]string{
		{"📡 اسکن کانفیگ‌ها | scan", "scan", "⚙️ تنظیمات | settings", "settings"},
		{"🏷️ کپشن و پرچم‌ها | caption", "caption", "ℹ️ درباره | about", "about"},
		{"👥 ادمین‌ها | admins", "admins"},
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
		OK     bool   `json:"ok"`
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
	t.Logf("OK: message %d delivered with %d keyboard rows",
		res.Result.MessageID, len(rows))
}
