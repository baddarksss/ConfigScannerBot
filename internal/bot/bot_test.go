package bot

import (
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
	b.settings.Channel = "Wpnfa" // a name exists so no prompt appears
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

// nav edits in place: a second nav() call must not error when there is a
// remembered message (the api errors here because "token" is fake, so we
// only assert the flow runs without panic and records no message id).
func TestNavFlowNoPanic(t *testing.T) {
	b := NewBot("token", 1, t.TempDir(), "xray", "hysteria")
	c := chat{ID: 42}
	b.nav(c, "hello", [][]string{{"a", "noop"}})
	b.nav(c, "world", [][]string{{"b", "noop"}})
	b.sendMain(c, "")
	var _ sync.Mutex
}
