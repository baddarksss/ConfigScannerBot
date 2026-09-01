package bot

import (
	"encoding/json"
	"testing"
)

// Realistic Bot API payloads — these caught a regression where the chat id
// was read from a non-existent "chat_id" field instead of "chat.id",
// making every update resolve to chat=0 and get silently dropped.
func TestUpdateParseMessage(t *testing.T) {
	raw := `{
	  "update_id": 724100985,
	  "message": {
	    "message_id": 2001,
	    "date": 1787000000,
	    "chat": {"id": 1313999046, "type": "private", "first_name": "N"},
	    "from": {"id": 1313999046, "is_bot": false, "first_name": "N"},
	    "text": "/start"
	  }
	}`
	var u update
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	c := u.chat()
	if c.ID != 1313999046 {
		t.Fatalf("message chat id = %d, want 1313999046", c.ID)
	}
	if u.Message.Text != "/start" {
		t.Fatalf("text = %q", u.Message.Text)
	}
}

func TestUpdateParseCallback(t *testing.T) {
	raw := `{
	  "update_id": 724100986,
	  "callback_query": {
	    "id": "cb-1",
	    "from": {"id": 1313999046, "is_bot": false, "first_name": "N"},
	    "chat_instance": "123",
	    "message": {
	      "message_id": 2001,
	      "date": 1787000000,
	      "chat": {"id": 1313999046, "type": "private", "first_name": "N"},
	      "text": "menu",
	      "reply_markup": {"inline_keyboard": [[{"text": "x", "callback_data": "scan"}]]}
	    },
	    "data": "scan"
	  }
	}`
	var u update
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	c := u.chat()
	if c.ID != 1313999046 {
		t.Fatalf("callback chat id = %d, want 1313999046", c.ID)
	}
	if u.CallbackQuery.Data != "scan" {
		t.Fatalf("data = %q", u.CallbackQuery.Data)
	}
	if u.CallbackQuery.From.ID != 1313999046 {
		t.Fatalf("from id = %d", u.CallbackQuery.From.ID)
	}
}
