package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	click(m, 7, 0)
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
	click(m, 7, 1) // second row
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
	click(m, 20, 0)
	if m.card != nil {
		t.Errorf("clicking message text opened a card for %q", m.card.userID)
	}
}

// The name ends where the hit box ends; one column past it is the colon.
func TestClickBoundaries(t *testing.T) {
	m := newTestModel(t, 80, 10,
		chat.Message{AuthorID: "1", Author: "alice", Text: "hi"},
	)
	click(m, 10, 0) // last column of "alice" (name starts at col 6, after timestamp)
	if m.card == nil {
		t.Error("click on the last column of the name missed")
	}
	m.card = nil

	click(m, 11, 0) // the ":"
	if m.card != nil {
		t.Error("click past the name opened a card")
	}
}

func TestClickWhileCardOpenDismissesIt(t *testing.T) {
	m := newTestModel(t, 80, 10, chat.Message{AuthorID: "1", Author: "alice", Text: "hi"})
	click(m, 7, 0)
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
	click(m, 7, 0)
	if m.card == nil {
		t.Fatal("no card at the bottom of a full buffer")
	}
	bottomUser := m.card.userID
	m.card = nil

	m.scrollBy(5)
	m.View()
	click(m, 7, 0)
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
		click(m, 7, 0)
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

func TestStatusBarShowsStats(t *testing.T) {
	m := NewModel(Options{
		Channel:  "buh",
		Incoming: make(chan chat.Message),
		Stats: func() StreamStats {
			return StreamStats{Live: true, Viewers: 1234, AvgViewers: 1000, Uptime: 3*time.Hour + 24*time.Minute}
		},
	})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	bar := m.statusBar()
	for _, want := range []string{"1.2k viewers", "avg 1.0k", "up 3h 24m"} {
		if !strings.Contains(bar, want) {
			t.Errorf("status bar missing %q:\n%s", want, bar)
		}
	}
}

func TestStatusBarOffline(t *testing.T) {
	m := NewModel(Options{
		Channel:  "buh",
		Incoming: make(chan chat.Message),
		Stats:    func() StreamStats { return StreamStats{Live: false} },
	})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	if !strings.Contains(m.statusBar(), "offline") {
		t.Errorf("status bar should show offline:\n%s", m.statusBar())
	}
}

func TestHumanCount(t *testing.T) {
	cases := map[int]string{0: "0", 42: "42", 999: "999", 1200: "1.2k", 1_500_000: "1.5M"}
	for n, want := range cases {
		if got := humanCount(n); got != want {
			t.Errorf("humanCount(%d) = %q, want %q", n, got, want)
		}
	}
}

// --- card info section -----------------------------------------------------

type fakeInfo struct {
	info UserInfo
	err  error
	got  struct{ login, channel string }
}

func (f *fakeInfo) CardInfo(_ context.Context, login, channel string) (UserInfo, error) {
	f.got.login, f.got.channel = login, channel
	return f.info, f.err
}

func openCardWithInfo(t *testing.T, f *fakeInfo) *Model {
	t.Helper()
	m := NewModel(Options{Channel: "buh", Incoming: make(chan chat.Message), Info: f})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m.append(chat.Message{ID: "m", AuthorID: "42", Author: "alice", AuthorLogin: "alice", Text: "hi"})
	m.View()
	_, cmd := m.Update(tea.MouseMsg{X: 7, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.card == nil {
		t.Fatal("card did not open")
	}
	if cmd == nil {
		t.Fatal("clicking a name with an info provider produced no fetch command")
	}
	m.Update(cmd()) // run the fetch and apply the result
	return m
}

func TestCardFetchesInfoForClickedUser(t *testing.T) {
	f := &fakeInfo{info: UserInfo{SubTier: "3", SubMonths: 78}}
	openCardWithInfo(t, f)
	if f.got.login != "alice" || f.got.channel != "buh" {
		t.Errorf("fetched for %q in %q, want alice in buh", f.got.login, f.got.channel)
	}
}

func TestCardShowsSubInfo(t *testing.T) {
	created := time.Date(2018, 7, 20, 0, 0, 0, 0, time.UTC)
	f := &fakeInfo{info: UserInfo{CreatedAt: created, SubTier: "3", SubMonths: 78}}
	m := openCardWithInfo(t, f)

	view := m.View()
	if !strings.Contains(view, "Tier 3") || !strings.Contains(view, "78 months") {
		t.Errorf("card missing sub info:\n%s", view)
	}
	if !strings.Contains(view, "2018") {
		t.Errorf("card missing account-created year:\n%s", view)
	}
}

func TestCardShowsNotSubscribed(t *testing.T) {
	f := &fakeInfo{info: UserInfo{CreatedAt: time.Now(), SubTier: ""}}
	m := openCardWithInfo(t, f)
	if !strings.Contains(m.View(), "not subscribed") {
		t.Errorf("card should say not subscribed:\n%s", m.View())
	}
}

func TestCardHandlesHiddenSub(t *testing.T) {
	f := &fakeInfo{info: UserInfo{CreatedAt: time.Now(), SubHidden: true}}
	m := openCardWithInfo(t, f)
	if !strings.Contains(m.View(), "hidden") {
		t.Errorf("card should mark sub hidden:\n%s", m.View())
	}
}

func TestCardShowsInfoError(t *testing.T) {
	f := &fakeInfo{err: errors.New("ivr down")}
	m := openCardWithInfo(t, f)
	if !strings.Contains(m.View(), "unavailable") {
		t.Errorf("card should mark info unavailable on error:\n%s", m.View())
	}
}

// A stale response for a card that was closed or reopened for someone else must
// be ignored.
func TestStaleCardInfoIgnored(t *testing.T) {
	m := newTestModel(t, 100, 20,
		chat.Message{AuthorID: "1", Author: "alice", AuthorLogin: "alice", Text: "hi"})
	m.info = &fakeInfo{}
	click(m, 7, 0)
	if m.card == nil {
		t.Fatal("no card")
	}
	// A response for a different user than the open card must not apply.
	m.Update(cardInfoLoaded{userID: "999", info: UserInfo{SubTier: "1"}})
	if m.card.info != nil {
		t.Error("applied info meant for a different user")
	}
}

// Without an info provider (e.g. anonymous), the card opens with no fetch and
// says so instead of hanging on "loading".
func TestCardWithoutProvider(t *testing.T) {
	m := newTestModel(t, 100, 20,
		chat.Message{AuthorID: "1", Author: "alice", AuthorLogin: "alice", Text: "hi"})
	_, cmd := m.Update(tea.MouseMsg{X: 7, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.card == nil {
		t.Fatal("no card")
	}
	if cmd != nil {
		t.Error("no info provider, but a fetch was issued")
	}
	if !strings.Contains(m.View(), "log in to load") {
		t.Errorf("card should prompt to log in:\n%s", m.View())
	}
}

func TestEscClosesCard(t *testing.T) {
	m := newTestModel(t, 80, 10, chat.Message{AuthorID: "1", Author: "alice", Text: "hi"})
	click(m, 7, 0)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.card != nil {
		t.Error("esc did not close the card")
	}
}

// --- sending ---------------------------------------------------------------

// newSendModel builds a logged-in model whose Send records what it was given.
func newSendModel(t *testing.T, w, h int) (*Model, *[]string) {
	t.Helper()
	var sent []string
	m := NewModel(Options{
		Channel:  "buh",
		Incoming: make(chan chat.Message),
		Send:     func(s string) { sent = append(sent, s) },
	})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.View()
	return m, &sent
}

func typeRunes(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func TestTypingAndEnterSends(t *testing.T) {
	m, sent := newSendModel(t, 80, 12)
	typeRunes(m, "hello chat")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(*sent) != 1 || (*sent)[0] != "hello chat" {
		t.Fatalf("sent = %v, want [hello chat]", *sent)
	}
	// The input clears after sending so the next message starts fresh.
	if m.input.Value() != "" {
		t.Errorf("input still holds %q after send", m.input.Value())
	}
}

// An empty or whitespace-only line must not be sent: Twitch rejects it and it
// is almost always an accidental Enter.
func TestEnterOnEmptyDoesNotSend(t *testing.T) {
	m, sent := newSendModel(t, 80, 12)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	typeRunes(m, "   ")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(*sent) != 0 {
		t.Errorf("sent %v, want nothing", *sent)
	}
}

// Sending snaps the view back to live so the user sees their own message.
func TestSendSnapsToLive(t *testing.T) {
	var msgs []chat.Message
	for i := 0; i < 40; i++ {
		msgs = append(msgs, chat.Message{AuthorID: "1", Author: "a", Text: "line"})
	}
	m, _ := newSendModel(t, 80, 12)
	for _, msg := range msgs {
		m.append(msg)
	}
	m.scrollBy(5)
	if m.scroll == 0 {
		t.Fatal("precondition: expected to be scrolled back")
	}
	typeRunes(m, "hi")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.scroll != 0 {
		t.Errorf("scroll = %d after send, want snapped to 0", m.scroll)
	}
}

// A logged-in session shows the input line, which costs one row of chat.
func TestInputLineConsumesARow(t *testing.T) {
	withSend, _ := newSendModel(t, 80, 12)
	readonly := newTestModel(t, 80, 12)
	if withSend.viewportHeight() != readonly.viewportHeight()-1 {
		t.Errorf("logged-in viewport %d, read-only %d; want one row less for the input",
			withSend.viewportHeight(), readonly.viewportHeight())
	}
}

// 'q' is a character to type when logged in, not a quit key.
func TestQTypesWhenLoggedIn(t *testing.T) {
	m, _ := newSendModel(t, 80, 12)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		// tea.Quit is a command; a nil command means we did not quit.
		if msg := cmd(); isQuit(msg) {
			t.Fatal("'q' quit the program while logged in; it should type")
		}
	}
	if m.input.Value() != "q" {
		t.Errorf("input = %q, want the typed 'q'", m.input.Value())
	}
}

func isQuit(msg tea.Msg) bool {
	_, ok := msg.(tea.QuitMsg)
	return ok
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
	click(m, 7, 0)
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

// A message with no id (the user's own locally-echoed messages) can't be
// deleted; pressing d must explain that rather than do nothing.
func TestDeleteWithoutIDExplains(t *testing.T) {
	f := &fakeMod{}
	m := NewModel(Options{Channel: "buh", Incoming: make(chan chat.Message), Mod: f})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	// No ID, as an echoed own-message has.
	m.append(chat.Message{ID: "", AuthorID: "42", Author: "nyx", AuthorLogin: "nyx", Text: "buh"})
	m.View()
	click(m, 7, 0)
	if m.card == nil {
		t.Fatal("no card")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd == nil {
		t.Fatal("delete with no id produced no feedback command")
	}
	m.Update(cmd())
	if f.deleted != "" {
		t.Error("attempted to delete a message with no id")
	}
	if !m.card.statusErr || !strings.Contains(m.card.status, "no message id") {
		t.Errorf("status = %q, want an explanation about the missing id", m.card.status)
	}
}

// A CLEARMSG marks one message deleted; a CLEARCHAT for a user marks all of
// theirs; a full clear marks everything. Messages are kept, not removed.
func TestApplyModEvents(t *testing.T) {
	fresh := func() *Model {
		return newTestModel(t, 80, 20,
			chat.Message{ID: "m1", AuthorID: "1", Author: "alice", Text: "one"},
			chat.Message{ID: "m2", AuthorID: "2", Author: "bob", Text: "two"},
			chat.Message{ID: "m3", AuthorID: "1", Author: "alice", Text: "three"},
		)
	}

	// Delete a single message by id.
	m := fresh()
	m.applyModEvent(chat.ModEvent{Kind: chat.DeleteMessage, MessageID: "m2"})
	if !m.msgs[1].Deleted || m.msgs[0].Deleted || m.msgs[2].Deleted {
		t.Errorf("delete by id hit the wrong messages: %v", deletedFlags(m))
	}

	// Clear a user: every message from that user id.
	m = fresh()
	m.applyModEvent(chat.ModEvent{Kind: chat.ClearUser, UserID: "1"})
	if !m.msgs[0].Deleted || m.msgs[1].Deleted || !m.msgs[2].Deleted {
		t.Errorf("clear user hit the wrong messages: %v", deletedFlags(m))
	}

	// Clear all.
	m = fresh()
	m.applyModEvent(chat.ModEvent{Kind: chat.ClearAll})
	for i, msg := range m.msgs {
		if !msg.Deleted {
			t.Errorf("message %d not marked deleted on clear-all", i)
		}
	}

	// Messages are kept (struck through), not removed.
	if len(m.msgs) != 3 {
		t.Errorf("clear-all removed messages (%d left); they should be kept", len(m.msgs))
	}
}

// A deleted message renders struck through with a marker.
func TestDeletedMessageRendered(t *testing.T) {
	m := newTestModel(t, 80, 20, chat.Message{ID: "m1", AuthorID: "1", Author: "alice", Text: "oops"})
	m.applyModEvent(chat.ModEvent{Kind: chat.DeleteMessage, MessageID: "m1"})
	view := m.View()
	if !strings.Contains(view, "deleted") {
		t.Errorf("deleted message missing its marker:\n%s", view)
	}
}

func deletedFlags(m *Model) []bool {
	out := make([]bool, len(m.msgs))
	for i := range m.msgs {
		out[i] = m.msgs[i].Deleted
	}
	return out
}

// The card header shows the subject's badges — as the text tag when graphics
// are off (the image path is covered by the kitty package tests).
func TestCardHeaderShowsBadges(t *testing.T) {
	m := newTestModel(t, 100, 20,
		chat.Message{ID: "1", AuthorID: "1", Author: "alice", AuthorLogin: "alice", Text: "hi", Broadcaster: true})
	// The "[B] " text tag shifts the name right by 4 columns (timestamp 6 + tag 4).
	click(m, 11, 0)
	if m.card == nil {
		t.Fatal("no card")
	}
	// renderCard is reached through View; the broadcaster tag must appear in it.
	if !strings.Contains(m.View(), "[B]") {
		t.Errorf("card header missing the badge/role marker:\n%s", m.View())
	}
}

// Without a login there is nothing to moderate with; keys must not silently do
// nothing, they must say why.
func TestActionsWithoutLoginExplain(t *testing.T) {
	m := NewModel(Options{Channel: "buh", Incoming: make(chan chat.Message)})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m.append(chat.Message{ID: "m", AuthorID: "42", Author: "alice", Text: "hi"})
	m.View()
	click(m, 7, 0)

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
