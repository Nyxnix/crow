package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Nyxnix/typetype/internal/chat"
)

// newTestModel builds a model at a fixed size with the given messages already
// in the buffer, then renders once so the hit map is populated.
func newTestModel(t *testing.T, w, h int, msgs ...chat.Message) *Model {
	t.Helper()
	m := NewModel(Options{Channel: "buh", Incoming: make(chan chat.Message)})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	for _, msg := range msgs {
		m.append(msg)
	}
	m.View() // View is what records hits
	return m
}

func click(m *Model, x, y int) {
	m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
}

func TestClickOnNameOpensCard(t *testing.T) {
	m := newTestModel(t, 80, 10,
		chat.Message{AuthorID: "1", Author: "alice", AuthorLogin: "alice", Text: "hi"},
		chat.Message{AuthorID: "2", Author: "bob", AuthorLogin: "bob", Text: "yo"},
	)

	// The buffer is short, so it renders at the top of the viewport.
	click(m, 1, 0)
	if m.card == nil {
		t.Fatal("clicking a name did not open the card")
	}
	if m.card.userID != "1" {
		t.Errorf("card is for %q, want alice's id", m.card.userID)
	}
}

func TestClickPicksTheClickedUser(t *testing.T) {
	m := newTestModel(t, 80, 10,
		chat.Message{AuthorID: "1", Author: "alice", Text: "hi"},
		chat.Message{AuthorID: "2", Author: "bob", Text: "yo"},
	)
	click(m, 1, 1) // second row
	if m.card == nil {
		t.Fatal("no card")
	}
	if m.card.userID != "2" {
		t.Errorf("card is for %q, want bob's id", m.card.userID)
	}
}

// Clicking the message text, not the name, must do nothing: the whole line
// being clickable would make the card impossible to avoid.
func TestClickOnTextDoesNotOpenCard(t *testing.T) {
	m := newTestModel(t, 80, 10,
		chat.Message{AuthorID: "1", Author: "alice", Text: "hello there"},
	)
	click(m, 30, 0)
	if m.card != nil {
		t.Errorf("clicking message text opened a card for %q", m.card.userID)
	}
}

// The name ends where the hit box ends; one column past it is the colon.
func TestClickBoundaries(t *testing.T) {
	m := newTestModel(t, 80, 10,
		chat.Message{AuthorID: "1", Author: "alice", Text: "hi"},
	)
	click(m, 4, 0) // last column of "alice"
	if m.card == nil {
		t.Error("click on the last column of the name missed")
	}
	m.card = nil

	click(m, 5, 0) // the ":"
	if m.card != nil {
		t.Error("click past the name opened a card")
	}
}

func TestClickWhileCardOpenDismissesIt(t *testing.T) {
	m := newTestModel(t, 80, 10, chat.Message{AuthorID: "1", Author: "alice", Text: "hi"})
	click(m, 1, 0)
	if m.card == nil {
		t.Fatal("no card to dismiss")
	}
	click(m, 40, 5)
	if m.card != nil {
		t.Error("card survived a click elsewhere")
	}
}

// Hits are recorded in screen rows. After scrolling, a click must resolve
// against what is actually on screen, not the absolute message list.
func TestHitsFollowScroll(t *testing.T) {
	var msgs []chat.Message
	for i := 0; i < 40; i++ {
		msgs = append(msgs, chat.Message{
			AuthorID: string(rune('a' + i%26)),
			Author:   "user" + strings.Repeat("x", i%3),
			Text:     "message",
		})
	}
	m := newTestModel(t, 80, 10, msgs...)

	// At the bottom, row 0 is not the first message.
	click(m, 1, 0)
	if m.card == nil {
		t.Fatal("no card at the bottom of a full buffer")
	}
	bottomUser := m.card.userID
	m.card = nil

	m.scrollBy(5)
	m.View()
	click(m, 1, 0)
	if m.card == nil {
		t.Fatal("no card after scrolling")
	}
	if m.card.userID == bottomUser {
		t.Error("scrolling did not change what row 0 points at")
	}
}

func TestCardShowsOnlyThatUsersMessages(t *testing.T) {
	m := newTestModel(t, 80, 20,
		chat.Message{AuthorID: "1", Author: "alice", Text: "first"},
		chat.Message{AuthorID: "2", Author: "bob", Text: "bobs message"},
		chat.Message{AuthorID: "1", Author: "alice", Text: "second"},
	)
	got := m.userMessages("1")
	if len(got) != 2 {
		t.Fatalf("got %d messages, want alice's 2", len(got))
	}
	if got[0].Text != "first" || got[1].Text != "second" {
		t.Errorf("got %q, want chronological order", []string{got[0].Text, got[1].Text})
	}
}

func TestCardHistoryIsCapped(t *testing.T) {
	var msgs []chat.Message
	for i := 0; i < cardHistory+10; i++ {
		msgs = append(msgs, chat.Message{AuthorID: "1", Author: "alice", Text: "spam"})
	}
	m := newTestModel(t, 80, 20, msgs...)
	if got := len(m.userMessages("1")); got != cardHistory {
		t.Errorf("got %d, want capped at %d", got, cardHistory)
	}
}

// The card must not push the view past the terminal width, and the chat column
// must stay put regardless of how long the messages happen to be — otherwise
// the card drifts as chat changes.
func TestCardLayoutFitsAndIsStable(t *testing.T) {
	measure := func(msgs ...chat.Message) (width int, cardCol int) {
		m := newTestModel(t, 100, 18, msgs...)
		click(m, 1, 0)
		if m.card == nil {
			t.Fatal("no card")
		}
		out := m.View()
		for _, l := range strings.Split(out, "\n") {
			if w := lipglossWidth(l); w > width {
				width = w
			}
			// The card's top border is the first thing on its column.
			if i := strings.Index(l, "╭"); i >= 0 {
				cardCol = lipglossWidth(l[:i])
			}
		}
		return width, cardCol
	}

	shortW, shortCol := measure(chat.Message{AuthorID: "1", Author: "alice", Text: "hi"})
	longW, longCol := measure(
		chat.Message{AuthorID: "1", Author: "alice", Text: "hi"},
		chat.Message{AuthorID: "2", Author: "bob", Text: strings.Repeat("long ", 40)},
	)

	if shortW > 100 || longW > 100 {
		t.Errorf("rendered %d and %d columns wide, want <= 100", shortW, longW)
	}
	if shortCol != longCol {
		t.Errorf("card sits at column %d with short messages but %d with long ones; it must not drift",
			shortCol, longCol)
	}
}

func TestEscClosesCard(t *testing.T) {
	m := newTestModel(t, 80, 10, chat.Message{AuthorID: "1", Author: "alice", Text: "hi"})
	click(m, 1, 0)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.card != nil {
		t.Error("esc did not close the card")
	}
}

// --- moderation ------------------------------------------------------------

type fakeMod struct {
	timeoutSecs int
	timeoutUser string
	banned      string
	unbanned    string
	deleted     string
	err         error
}

func (f *fakeMod) Timeout(_ context.Context, userID string, secs int, _ string) error {
	f.timeoutUser, f.timeoutSecs = userID, secs
	return f.err
}
func (f *fakeMod) Ban(_ context.Context, userID, _ string) error { f.banned = userID; return f.err }
func (f *fakeMod) Unban(_ context.Context, userID string) error  { f.unbanned = userID; return f.err }
func (f *fakeMod) DeleteMessage(_ context.Context, id string) error {
	f.deleted = id
	return f.err
}

func openCardWithMod(t *testing.T, mod Moderator) (*Model, *card) {
	t.Helper()
	m := NewModel(Options{Channel: "buh", Incoming: make(chan chat.Message), Mod: mod})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m.append(chat.Message{ID: "msg-1", AuthorID: "42", Author: "alice", AuthorLogin: "alice", Text: "hi"})
	m.View()
	click(m, 1, 0)
	if m.card == nil {
		t.Fatal("no card")
	}
	return m, m.card
}

// Timeout presets fire immediately: they are reversible, so a confirm step
// would just be friction on the action mods take most.
func TestTimeoutPresetFires(t *testing.T) {
	f := &fakeMod{}
	m, _ := openCardWithMod(t, f)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if cmd == nil {
		t.Fatal("no command issued for the 10m preset")
	}
	cmd() // the command is what performs the call

	if f.timeoutUser != "42" || f.timeoutSecs != 600 {
		t.Errorf("timeout(%q, %d), want (42, 600)", f.timeoutUser, f.timeoutSecs)
	}
}

// A ban is not practically reversible, so it must not happen on one keypress.
func TestBanRequiresConfirmation(t *testing.T) {
	f := &fakeMod{}
	m, c := openCardWithMod(t, f)

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if f.banned != "" {
		t.Fatal("ban fired without confirmation")
	}
	if c.confirm != "ban" {
		t.Fatalf("confirm = %q, want a pending ban", c.confirm)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("confirming produced no command")
	}
	cmd()
	if f.banned != "42" {
		t.Errorf("banned %q, want 42", f.banned)
	}
}

func TestBanCancelled(t *testing.T) {
	f := &fakeMod{}
	m, _ := openCardWithMod(t, f)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if f.banned != "" {
		t.Error("ban fired after being cancelled")
	}
}

func TestDeleteTargetsTheClickedMessage(t *testing.T) {
	f := &fakeMod{}
	m, _ := openCardWithMod(t, f)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd == nil {
		t.Fatal("no command for delete")
	}
	cmd()
	if f.deleted != "msg-1" {
		t.Errorf("deleted %q, want msg-1", f.deleted)
	}
}

// Without a login there is nothing to moderate with; keys must not silently do
// nothing, they must say why.
func TestActionsWithoutLoginExplain(t *testing.T) {
	m := NewModel(Options{Channel: "buh", Incoming: make(chan chat.Message)})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m.append(chat.Message{ID: "m", AuthorID: "42", Author: "alice", Text: "hi"})
	m.View()
	click(m, 1, 0)

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if m.card.confirm != "" {
		t.Error("a ban was staged with no way to perform it")
	}
	if !m.card.statusErr || m.card.status == "" {
		t.Error("card gave no reason for doing nothing")
	}
}

func TestActionErrorSurfaces(t *testing.T) {
	f := &fakeMod{err: errors.New("401 unauthorized")}
	m, _ := openCardWithMod(t, f)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	msg := cmd()
	m.Update(msg)

	if !m.card.statusErr || !strings.Contains(m.card.status, "401") {
		t.Errorf("status = %q err=%v, want the error shown", m.card.status, m.card.statusErr)
	}
}
