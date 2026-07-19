package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Nyxnix/crow/internal/chat"
	"github.com/Nyxnix/crow/internal/emote"
)

func pressTab(m *Model) { m.Update(tea.KeyMsg{Type: tea.KeyTab}) }

// completeModel is a logged-in model with some chatters and emotes to
// complete against.
func completeModel(t *testing.T, emotes ...string) *Model {
	t.Helper()
	reg := emote.New()
	for _, name := range emotes {
		reg.Add(emote.Emote{Name: name, URL: "http://x/" + name})
	}
	m := NewModel(Options{Channel: "buh", Send: func(string) {}, Emotes: reg})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m.Append(chat.Message{Author: "Alice", AuthorLogin: "alice", AuthorID: "1", Text: "hi"})
	m.Append(chat.Message{Author: "ボブBob", AuthorLogin: "bob_jp", AuthorID: "2", Text: "yo"})
	return m
}

func TestTabCompletesUsername(t *testing.T) {
	m := completeModel(t)
	typeRunes(m, "hey al")
	pressTab(m)
	if got := m.input.Value(); got != "hey Alice" {
		t.Fatalf("value = %q, want %q", got, "hey Alice")
	}
	if m.input.Position() != len([]rune("hey Alice")) {
		t.Errorf("cursor = %d, want end of input", m.input.Position())
	}

	// A multibyte display name must complete without mangling offsets.
	m.input.Reset()
	typeRunes(m, "ボブ")
	pressTab(m)
	if got := m.input.Value(); got != "ボブBob" {
		t.Errorf("value = %q, want the full multibyte name", got)
	}
}

func TestTabAtWordKeepsAtAndSkipsEmotes(t *testing.T) {
	m := completeModel(t, "alpha") // an emote that would match "al"
	typeRunes(m, "@al")
	pressTab(m)
	if got := m.input.Value(); got != "@Alice" {
		t.Fatalf("value = %q, want @Alice (users only after @)", got)
	}
}

func TestTabCyclesAndOtherKeyEnds(t *testing.T) {
	m := completeModel(t)
	m.Append(chat.Message{Author: "alan", AuthorLogin: "alan", AuthorID: "3", Text: "x"})
	typeRunes(m, "al")
	pressTab(m)
	firstVal := m.input.Value()
	pressTab(m)
	secondVal := m.input.Value()
	if firstVal != "Alice" || secondVal != "alan" {
		t.Fatalf("cycle = %q -> %q, want Alice -> alan (sorted)", firstVal, secondVal)
	}
	pressTab(m)
	if got := m.input.Value(); got != "Alice" {
		t.Errorf("third tab = %q, want wrap back to Alice", got)
	}

	// Any other key ends the cycle; the next Tab completes the new word.
	typeRunes(m, "x")
	if m.comp != nil {
		t.Fatal("typing did not end the completion cycle")
	}
	pressTab(m)
	if got := m.input.Value(); got != "Alicex" {
		t.Errorf("tab on no-match word changed input to %q", got)
	}
}

func TestBareWordPrefersEmotes(t *testing.T) {
	m := completeModel(t, "Kappa")
	m.Append(chat.Message{Author: "kappaman", AuthorLogin: "kappaman", AuthorID: "9", Text: "x"})
	typeRunes(m, "ka")
	pressTab(m)
	if got := m.input.Value(); got != "Kappa" {
		t.Fatalf("first candidate = %q, want the emote Kappa", got)
	}
	pressTab(m)
	if got := m.input.Value(); got != "kappaman" {
		t.Errorf("second candidate = %q, want the user kappaman", got)
	}
}

func TestTabNilRegistry(t *testing.T) {
	m := NewModel(Options{Channel: "buh", Send: func(string) {}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m.Append(chat.Message{Author: "Alice", AuthorLogin: "alice", AuthorID: "1", Text: "hi"})
	typeRunes(m, "al")
	pressTab(m) // must not panic on the nil registry
	if got := m.input.Value(); got != "Alice" {
		t.Fatalf("value = %q, want Alice", got)
	}
}
