package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Nyxnix/crow/internal/config"
)

// fakeFactory returns a bare model for a channel and a no-op close.
func fakeFactory() TabFactory {
	return func(channel string) (*Model, func()) {
		return NewModel(Options{Channel: channel}), func() {}
	}
}

func newTestApp(t *testing.T, channels ...string) *App {
	t.Helper()
	a := NewApp(AppOptions{Factory: fakeFactory(), Config: config.Default(), Channels: channels})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return a
}

func typeKeys(a *App, s string) {
	for _, r := range s {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// Typing on the splash must fill the channel input.
func TestSplashTypingFillsInput(t *testing.T) {
	a := newTestApp(t) // no channels -> splash
	if a.mode != modeSplash {
		t.Fatalf("mode = %v, want splash", a.mode)
	}
	typeKeys(a, "caedrel")
	if got := a.splash.input.Value(); got != "caedrel" {
		t.Fatalf("input = %q, want caedrel — is it focused? %v", got, a.splash.input.Focused())
	}
}

// Enter on the splash opens the typed channel as a tab and switches to chat.
func TestSplashEnterOpensTab(t *testing.T) {
	a := newTestApp(t)
	typeKeys(a, "caedrel")
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if a.mode != modeChat {
		t.Fatalf("mode = %v, want chat after opening a channel", a.mode)
	}
	if len(a.tabs) != 1 || a.tabs[0].channel != "caedrel" {
		t.Fatalf("tabs = %+v, want one caedrel tab", a.tabs)
	}
}

// Startup channels open directly into tabbed chat, skipping the splash.
func TestStartupChannelsOpenTabs(t *testing.T) {
	a := newTestApp(t, "a", "b")
	if a.mode != modeChat {
		t.Fatalf("mode = %v, want chat", a.mode)
	}
	if len(a.tabs) != 2 {
		t.Fatalf("got %d tabs, want 2", len(a.tabs))
	}
	if a.active != 1 {
		t.Errorf("active = %d, want the last opened", a.active)
	}
}

func TestTabSwitching(t *testing.T) {
	a := newTestApp(t, "a", "b", "c")
	a.switchTab(0)
	if a.active != 0 {
		t.Errorf("active = %d, want 0", a.active)
	}
	// Wraps around.
	a.switchTab(-1)
	if a.active != 2 {
		t.Errorf("active = %d, want wrap to 2", a.active)
	}
	a.switchTab(3)
	if a.active != 0 {
		t.Errorf("active = %d, want wrap to 0", a.active)
	}
}

func TestCloseTab(t *testing.T) {
	a := newTestApp(t, "a", "b")
	closed := false
	a.tabs[1].close = func() { closed = true }
	a.active = 1
	a.closeActiveTab()
	if !closed {
		t.Error("closing a tab did not run its teardown")
	}
	if len(a.tabs) != 1 || a.tabs[0].channel != "a" {
		t.Errorf("tabs = %+v, want just 'a'", a.tabs)
	}
	// Closing the last tab returns to the splash.
	a.closeActiveTab()
	if a.mode != modeSplash {
		t.Errorf("mode = %v after closing all tabs, want splash", a.mode)
	}
}

// Opening a channel that is already open just focuses it, no duplicate tab.
func TestOpenDuplicateFocuses(t *testing.T) {
	a := newTestApp(t, "a", "b")
	a.active = 0
	a.openTab("b")
	if len(a.tabs) != 2 {
		t.Errorf("got %d tabs, want no duplicate", len(a.tabs))
	}
	if a.tabs[a.active].channel != "b" {
		t.Errorf("active channel = %q, want b focused", a.tabs[a.active].channel)
	}
}

// Ctrl+S opens settings; Esc returns to chat.
func TestSettingsToggle(t *testing.T) {
	a := newTestApp(t, "a")
	a.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if a.mode != modeSettings {
		t.Fatalf("mode = %v, want settings after ctrl+s", a.mode)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.mode != modeChat {
		t.Errorf("mode = %v, want chat after esc", a.mode)
	}
}

func TestAppearanceSizeSavesPerKeystroke(t *testing.T) {
	var saved []int
	a := NewApp(AppOptions{Config: config.Default(), Save: func(c config.Config) { saved = append(saved, c.Overlay.Size) }})
	a.mode = modeSettings
	a.settings = newSettingsState("", &a.cfg)
	a.settings.page = pageOverlay

	rows := a.settings.rows[pageOverlay]
	idx := -1
	for i, r := range rows {
		if r.label == "size (px)" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no size row on the overlay page")
	}
	a.settings.sel[pageOverlay] = idx
	a.settings.refocus()

	// Type "8" into the size field (default "20" -> "208"): the very keystroke
	// must commit and save, so the overlay restyles live.
	a.settingsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'8'}})
	if a.cfg.Overlay.Size != 208 {
		t.Errorf("size = %d after keystroke, want 208", a.cfg.Overlay.Size)
	}
	if len(saved) == 0 || saved[len(saved)-1] != 208 {
		t.Errorf("save calls = %v, want last one 208", saved)
	}
}
