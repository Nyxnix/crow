package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNoticeShownWithoutCard(t *testing.T) {
	m, _ := newSendModel(t, 80, 12)
	m.Update(actionResult{text: "banned alice"})
	if !strings.Contains(m.statusBar(), "banned alice") {
		t.Fatal("status bar does not show the notice")
	}
	m.Update(actionResult{text: "boom", err: true})
	if !strings.Contains(m.statusBar(), "error: boom") {
		t.Fatal("error notice not marked")
	}

	// The next keypress retires it.
	typeRunes(m, "x")
	if strings.Contains(m.statusBar(), "boom") {
		t.Fatal("notice survived a keypress")
	}
}

func TestPinToggleAndLayout(t *testing.T) {
	m, _ := openCardWithMod(t, &fakeMod{})
	base := m.viewportHeight()

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.pinned == nil || m.pinned.Text != "hi" {
		t.Fatal("p did not pin the clicked message")
	}
	if got := m.viewportHeight(); got != base-1 {
		t.Errorf("viewport = %d, want %d (one row for the pin)", got, base-1)
	}
	m.card = nil
	if view := m.View(); !strings.Contains(view, "PIN alice: hi") {
		t.Error("view does not show the pinned row")
	}

	// Reopen the card on the same message: p unpins.
	m.View()
	click(m, 7, 0)
	if m.card == nil {
		t.Fatal("no card on reclick")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.pinned != nil {
		t.Fatal("p did not unpin")
	}
	if got := m.viewportHeight(); got != base {
		t.Errorf("viewport = %d, want %d after unpin", got, base)
	}
}
