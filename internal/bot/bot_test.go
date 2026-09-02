package bot

import (
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
