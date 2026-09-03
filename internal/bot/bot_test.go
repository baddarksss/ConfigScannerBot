package bot

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// Regression (v1.0.7): cycleSetting used to mutate a SETTINGS COPY (from
// settingsLocked) and then save the ORIGINAL — so toggles like
// include_channel / include_unknown silently never persisted. The settings
// menu kept showing "خاموش" no matter how often the user toggled.
func TestCycleSettingPersists(t *testing.T) {
	b := NewBot("token", 1, t.TempDir(), "xray", "hysteria")
	b.mu.Lock()
	b.settings.Channel = "Wpnfa" // a name exists
	b.mu.Unlock()

	c := chat{ID: 1}
	b.cycleSetting(c, "channel")
	b.mu.Lock()
	on1 := b.settings.IncludeChannel
	b.mu.Unlock()
	if !on1 {
		t.Fatal("include_channel not set after first toggle")
	}

	b.cycleSetting(c, "unknown")
	b.mu.Lock()
	unk := b.settings.IncludeUnknown
	b.mu.Unlock()
	if !unk {
		t.Fatal("include_unknown not set after toggle")
	}

	b.cycleSetting(c, "channel")
	b.mu.Lock()
	on2 := b.settings.IncludeChannel
	par := b.settings.Parallel
	b.mu.Unlock()
	if on2 {
		t.Fatal("include_channel not cleared after second toggle")
	}

	// parallel cycles through the steps and must persist too
	b.cycleSetting(c, "parallel")
	b.mu.Lock()
	par2 := b.settings.Parallel
	b.mu.Unlock()
	if par2 == par {
		t.Fatal("parallel did not change")
	}
}

func TestReplyMenuStructure(t *testing.T) {
	b := NewBot("token", 1, t.TempDir(), "xray", "hysteria")
	c := chat{ID: 1}
	b.sendMain(c, "")

	menu := b.replyMainMenuFor(1, 5)
	if len(menu) == 0 {
		t.Fatal("main menu must not be empty")
	}
	if !strings.Contains(menu[0][0], "شروع اسکن (5 کانفیگ)") {
		t.Fatalf("first button should be start scan with count, got %s", menu[0][0])
	}

	sMenu := b.replySettingsMenu()
	if len(sMenu) == 0 {
		t.Fatal("settings menu must not be empty")
	}
	var _ sync.Mutex
}

func TestMessageForUsersPersists(t *testing.T) {
	dir := t.TempDir()
	b := NewBot("token", 1, dir, "xray", "hysteria")
	c := chat{ID: 1}
	customMsg := "🎯 Made by @wpnfa\nT.me/wpnfa"
	b.onSetMessageForUsers(c, customMsg)

	b2 := NewBot("token", 1, dir, "xray", "hysteria")
	s := b2.settingsLocked()
	if s.MessageForUsers != customMsg {
		t.Fatalf("expected message %q, got %q", customMsg, s.MessageForUsers)
	}

	b2.onClearMessageForUsers(c)
	s2 := b2.settingsLocked()
	if s2.MessageForUsers != "" {
		t.Fatalf("expected empty message, got %q", s2.MessageForUsers)
	}
}

func TestSetCodeQuickAndCallback(t *testing.T) {
	dir := t.TempDir()
	b := NewBot("token", 1, dir, "xray", "hysteria")
	c := chat{ID: 1}

	// Direct code set prompt and input
	b.onSetCode(c, "UZ", "5390843037349679256")
	s := b.settingsLocked()
	if s.EmojiCodes["UZ"] != "5390843037349679256" {
		t.Fatalf("expected UZ code saved, got %q", s.EmojiCodes["UZ"])
	}

	// Direct deletion
	b.onSetCode(c, "UZ", "delete")
	s = b.settingsLocked()
	if s.EmojiCodes["UZ"] != "" {
		t.Fatalf("expected UZ code deleted, got %q", s.EmojiCodes["UZ"])
	}
}

// Regression (v1.0.8): outputs were measured in BYTES, but Telegram limits
// messages in CHARACTERS — Persian text (2 bytes/rune) made small outputs
// spill into files too early.
func TestFitsMsgCountsCharacters(t *testing.T) {
	// ~3900 Persian chars = ~7800 bytes: must FIT (chars < 4000)
	persian := strings.Repeat("آ", 3900)
	if !fitsMsg(persian) {
		t.Fatal("3900 Persian chars must fit (old byte check rejected it)")
	}
	if fitsMsg(strings.Repeat("آ", 4500)) {
		t.Fatal("4500 chars must not fit")
	}
}

// Regression (v1.0.13): after Telegram's "start of the range" error the
// offset jumped to 2^60 and NEVER came back — every later getUpdates matched
// no update and the bot went deaf until restarted. The purge must be
// followed by a reset to 0.
func TestPollStateRecoversAfterRangeError(t *testing.T) {
	var ps pollState
	if ps.next() != 1 {
		t.Fatal("initial poll offset must be 1")
	}

	// the telegram error triggers the one-time purge jump
	todo, backoff := ps.feed(nil, errors.New("tg getUpdates: Bad Request: can't get updates: start of the range was skipped"))
	if todo != nil || backoff != 0 {
		t.Fatalf("range error must not back off: %v %v", todo, backoff)
	}
	if ps.next() != (1<<60)+1 {
		t.Fatalf("expected purge jump, offset=%d", ps.offset)
	}

	// the purge poll returns empty → offset must reset to 0
	todo, backoff = ps.feed(nil, nil)
	if len(todo) != 0 || backoff != 0 {
		t.Fatalf("purge poll should be clean: %v %v", todo, backoff)
	}
	if ps.next() != 1 {
		t.Fatalf("offset stuck at %d — the bot would never receive updates again", ps.offset)
	}

	// normal flow continues: track the last update id, confirm by +1
	ups := []update{{UpdateID: 500}, {UpdateID: 501}}
	todo, backoff = ps.feed(ups, nil)
	if len(todo) != 2 || backoff != 0 || ps.next() != 502 {
		t.Fatalf("normal flow broken: todo=%d offset=%d backoff=%v", len(todo), ps.offset, backoff)
	}

	// generic errors back off but keep the position
	_, backoff = ps.feed(nil, errors.New("connection reset"))
	if backoff == 0 {
		t.Fatal("generic error must back off")
	}
	if ps.next() != 502 {
		t.Fatalf("generic error moved the offset: %d", ps.offset)
	}
}

// Regression (v1.0.13): a multi-line "XX=code" list pasted directly used to
// be swallowed into ONE country's value (the rest of the list became part of
// the first code). It must import as a batch.
func TestHandleUpdateMultiLineCodesImport(t *testing.T) {
	b := NewBot("token", 1, t.TempDir(), "xray", "hysteria")
	var u update
	if err := json.Unmarshal([]byte(`{"update_id":1,"message":{"message_id":1,"chat":{"id":1},"text":"DE=123\nFR=456"}}`), &u); err != nil {
		t.Fatal(err)
	}
	b.handleUpdate(u)
	b.mu.Lock()
	de, fr := b.settings.EmojiCodes["DE"], b.settings.EmojiCodes["FR"]
	b.mu.Unlock()
	if de != "123" || fr != "456" {
		t.Fatalf("multi-line import broken: DE=%q FR=%q", de, fr)
	}
}

// Single-line quick set must still work (and "delete" removes).
func TestHandleUpdateSingleLineQuickSet(t *testing.T) {
	dir := t.TempDir()
	b := NewBot("token", 1, dir, "xray", "hysteria")
	var u update
	if err := json.Unmarshal([]byte(`{"update_id":1,"message":{"message_id":1,"chat":{"id":1},"text":"DE=999"}}`), &u); err != nil {
		t.Fatal(err)
	}
	b.handleUpdate(u)
	b.mu.Lock()
	de := b.settings.EmojiCodes["DE"]
	b.mu.Unlock()
	if de != "999" {
		t.Fatalf("quick set broken: DE=%q", de)
	}

	var d update
	if err := json.Unmarshal([]byte(`{"update_id":2,"message":{"message_id":2,"chat":{"id":1},"text":"DE=delete"}}`), &d); err != nil {
		t.Fatal(err)
	}
	b.handleUpdate(d)
	b.mu.Lock()
	_, exists := b.settings.EmojiCodes["DE"]
	b.mu.Unlock()
	if exists {
		t.Fatal("delete quick-set did not remove the code")
	}
}

// A config link containing "=" (vmess base64 padding) must never be treated
// as a country code.
func TestHandleUpdateConfigNotCode(t *testing.T) {
	b := NewBot("token", 1, t.TempDir(), "xray", "hysteria")
	var u update
	if err := json.Unmarshal([]byte(`{"update_id":1,"message":{"message_id":1,"chat":{"id":1},"text":"vmess://eyJhZGQiOiIxLjIuMy40IiwicG9ydCI6NDQzLCJpZCI6InUiLCJhaWQiOjB9"}}`), &u); err != nil {
		t.Fatal(err)
	}
	b.handleUpdate(u)
	b.mu.Lock()
	_, running := b.settings.EmojiCodes["vmess"]
	pending := len(b.pending)
	b.mu.Unlock()
	if running {
		t.Fatal("config link parsed as emoji code")
	}
	if pending != 1 {
		t.Fatalf("config link not queued: pending=%d", pending)
	}
}

func TestParseIDRejectsOverflow(t *testing.T) {
	if _, ok := parseID("123456789012345678901234567890"); ok {
		t.Fatal("30 digits must be rejected (int64 overflow)")
	}
	if id, ok := parseID("123456789"); !ok || id != 123456789 {
		t.Fatalf("normal id broken: %d %v", id, ok)
	}
	if _, ok := parseID(""); ok {
		t.Fatal("empty must be rejected")
	}
}

// The queue count shown to the user must match what the scan really parses
// (junk lines used to be counted, then dropped at run time).
func TestOnConfigInputCountsOnlyParseable(t *testing.T) {
	b := NewBot("token", 1, t.TempDir(), "xray", "hysteria")
	c := chat{ID: 1}
	b.onConfigInput(c, "vless://u@1.1.1.1:443#x\nthis is junk\n\nvmess://"+b64std(`{"add":"h","port":1,"id":"u"}`)+"\nmore junk")
	b.mu.Lock()
	pending := len(b.pending)
	b.mu.Unlock()
	if pending != 2 {
		t.Fatalf("pending = %d, want 2 (junk lines must not count)", pending)
	}
}

func b64std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
