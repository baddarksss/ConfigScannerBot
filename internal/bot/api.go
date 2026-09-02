package bot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// tgAPI is a minimal Telegram Bot API client (long polling, HTML, files).
type tgAPI struct {
	token string
	http  *http.Client
}

func newAPI(token string) *tgAPI {
	return &tgAPI{token: token, http: &http.Client{Timeout: 90 * time.Second}}
}

type update struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int    `json:"message_id"`
		Text      string `json:"text"`
		Caption   string `json:"caption"`
		// Telegram nests the chat inside the message: "chat": {"id": ...}
		Chat *struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Document *struct {
			FileID   string `json:"file_id"`
			FileSize int    `json:"file_size"`
			FileName string `json:"file_name"`
		} `json:"document"`
		From *struct {
			ID   int64  `json:"id"`
			Name string `json:"first_name"`
		} `json:"from"`
	} `json:"message"`
	CallbackQuery *struct {
		ID   string `json:"id"`
		Data string `json:"data"`
		// a callback carries its chat inside "message.chat" (not a flat field)
		Message *struct {
			Chat *struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
		From *struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"callback_query"`
}

type chat struct {
	ID   int64
	Name string
}

func (u *update) chat() chat {
	if u.Message != nil && u.Message.Chat != nil {
		name := ""
		if u.Message.From != nil {
			name = u.Message.From.Name
		}
		return chat{ID: u.Message.Chat.ID, Name: name}
	}
	if u.CallbackQuery != nil && u.CallbackQuery.Message != nil &&
		u.CallbackQuery.Message.Chat != nil {
		return chat{ID: u.CallbackQuery.Message.Chat.ID}
	}
	return chat{}
}

func (a *tgAPI) call(method string, payload map[string]any, out any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := a.http.Post(
		"https://api.telegram.org/bot"+a.token+"/"+method,
		"application/json", bytes.NewReader(b))
	if err != nil {
		fmt.Printf("TG API %s: %v\n", method, err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var res struct {
		OK      bool `json:"ok"`
		Result  json.RawMessage `json:"result"`
		Err     string `json:"description"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return fmt.Errorf("tg %s: bad response: %s", method, body[:min(len(body), 300)])
	}
	if !res.OK {
		fmt.Printf("TG API %s error: %s\n", method, res.Err)
		return fmt.Errorf("tg %s: %s", method, res.Err)
	}
	if out != nil && len(res.Result) > 0 {
		return json.Unmarshal(res.Result, out)
	}
	return nil
}

type tgMessage struct {
	MessageID int `json:"message_id"`
}

func (a *tgAPI) sendMessage(chatID int64, text string) (int, error) {
	var m tgMessage
	err := a.call("sendMessage", map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
		"disable_web_page_preview": true,
	}, &m)
	return m.MessageID, err
}

func (a *tgAPI) sendMenu(chatID int64, text string, rows [][]string) (int, error) {
	return a.sendWithKeyboard(chatID, text, rows, "sendMessage")
}

func (a *tgAPI) sendWithReplyKeyboard(chatID int64, text string, rows [][]string) (int, error) {
	keyboard := make([][]map[string]any, 0, len(rows))
	for _, row := range rows {
		rowBtns := make([]map[string]any, 0, len(row))
		for _, btnText := range row {
			rowBtns = append(rowBtns, map[string]any{"text": btnText})
		}
		if len(rowBtns) > 0 {
			keyboard = append(keyboard, rowBtns)
		}
	}
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup": map[string]any{
			"keyboard":        keyboard,
			"resize_keyboard": true,
			"is_persistent":   true,
		},
	}
	var m tgMessage
	if err := a.call("sendMessage", payload, &m); err != nil {
		return 0, err
	}
	return m.MessageID, nil
}

func menuPayload(chatID int64, text string, rows [][]string) map[string]any {
	return map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
		"disable_web_page_preview": true,
		"reply_markup": map[string]any{
			"inline_keyboard": buildKeyboard(rows),
		},
	}
}

// Rows are passed flat as [text, callbackData, text, callbackData, ...]
// per row. Telegram needs each button as an object:
// {"text": "...", "callback_data": "..."}.
func buildKeyboard(rows [][]string) [][]map[string]any {
	keyboard := make([][]map[string]any, 0, len(rows))
	for _, row := range rows {
		btns := make([]map[string]any, 0, (len(row)+1)/2)
		for i := 0; i+1 < len(row); i += 2 {
			btns = append(btns, map[string]any{
				"text":          row[i],
				"callback_data": row[i+1],
			})
		}
		if len(btns) > 0 {
			keyboard = append(keyboard, btns)
		}
	}
	return keyboard
}

func (a *tgAPI) sendWithKeyboard(chatID int64, text string, rows [][]string, method string) (int, error) {
	payload := menuPayload(chatID, text, rows)
	var m tgMessage
	if err := a.call(method, payload, &m); err != nil {
		return 0, err
	}
	return m.MessageID, nil
}

func (a *tgAPI) editText(messageID int, chatID int64, text string) error {
	return a.call("editMessageText", map[string]any{
		"chat_id":      chatID,
		"message_id":   messageID,
		"text":         text,
		"parse_mode":   "HTML",
		"disable_web_page_preview": true,
	}, nil)
}

func (a *tgAPI) editKeyboard(messageID int, chatID int64, text string, rows [][]string) error {
	payload := map[string]any{
		"chat_id":      chatID,
		"message_id":   messageID,
		"text":         text,
		"parse_mode":   "HTML",
		"disable_web_page_preview": true,
	}
	if kb := buildKeyboard(rows); len(kb) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": kb}
	} else {
		payload["reply_markup"] = nil // clears the keyboard
	}
	return a.call("editMessageText", payload, nil)
}

func (a *tgAPI) answerCallback(cbID string, text string) {
	_ = a.call("answerCallbackQuery", map[string]any{
		"callback_query_id": cbID,
		"text":              text,
	}, nil)
}

func (a *tgAPI) sendDocument(chatID int64, fileBytes []byte, fileName, caption string) error {
	// multipart upload
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, _ := w.CreateFormFile("document", fileName)
	fw.Write(fileBytes)
	w.WriteField("chat_id", fmt.Sprint(chatID))
	if caption != "" {
		w.WriteField("caption", caption)
		w.WriteField("parse_mode", "HTML")
	}
	w.Close()
	resp, err := a.http.Post(
		"https://api.telegram.org/bot"+a.token+"/sendDocument",
		w.FormDataContentType(), &body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var res struct {
		OK  bool `json:"ok"`
		Err string `json:"description"`
	}
	json.Unmarshal(rb, &res)
	if !res.OK {
		return fmt.Errorf("tg sendDocument: %s", res.Err)
	}
	return nil
}

func (a *tgAPI) getFileBytes(fileID string) ([]byte, error) {
	var out struct {
		FilePath string `json:"file_path"`
	}
	if err := a.call("getFile", map[string]any{"file_id": fileID}, &out); err != nil {
		return nil, err
	}
	u := "https://api.telegram.org/file/bot" + a.token + "/" + out.FilePath
	resp, err := a.http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 5<<20))
}

// getUpdates long-polls.
func (a *tgAPI) getUpdates(offset int, timeoutSec int) ([]update, error) {
	var out []update
	err := a.call("getUpdates", map[string]any{
		"offset":  offset,
		"timeout": timeoutSec,
	}, &out)
	return out, err
}

func (a *tgAPI) deleteMessage(chatID int64, messageID int) error {
	return a.call("deleteMessage", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}, nil)
}


