package tui

import (
	"context"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Nyxnix/crow/internal/chat"
)

// fakeChan records the ChannelManager calls the slash commands make.
type fakeChan struct {
	settings map[string]any
	announce string
	pollT    string
	pollC    []string
	pollDur  int
	raided      string
	vip         map[string]bool
	moded       map[string]bool
	cleared     bool
	pinnedMsg   string
	unpinnedMsg string
	err         error
}

func (f *fakeChan) UpdateChatSettings(_ context.Context, p map[string]any) error {
	f.settings = p
	return f.err
}
func (f *fakeChan) Announce(_ context.Context, t string) error { f.announce = t; return f.err }
func (f *fakeChan) CreatePoll(_ context.Context, title string, choices []string, secs int) error {
	f.pollT, f.pollC, f.pollDur = title, choices, secs
	return f.err
}
func (f *fakeChan) CreatePrediction(_ context.Context, title string, outcomes []string, secs int) error {
	f.pollT, f.pollC, f.pollDur = title, outcomes, secs
	return f.err
}
func (f *fakeChan) Raid(_ context.Context, to string) error { f.raided = to; return f.err }
func (f *fakeChan) SetVIP(_ context.Context, id string, on bool) error {
	if f.vip == nil {
		f.vip = map[string]bool{}
	}
	f.vip[id] = on
	return f.err
}
func (f *fakeChan) SetMod(_ context.Context, id string, on bool) error {
	if f.moded == nil {
		f.moded = map[string]bool{}
	}
	f.moded[id] = on
	return f.err
}
func (f *fakeChan) ClearChat(_ context.Context) error { f.cleared = true; return f.err }
func (f *fakeChan) PinMessage(_ context.Context, id string) error {
	f.pinnedMsg = id
	return f.err
}
func (f *fakeChan) UnpinMessage(_ context.Context, id string) error {
	f.unpinnedMsg = id
	return f.err
}
func (f *fakeChan) ResolveUser(_ context.Context, login string) (string, error) {
	return "id-" + login, f.err
}

// commandModel is a logged-in single-Twitch-tab model: mod + channel manager.
func commandModel(t *testing.T, mod Moderator, ch ChannelManager) (*Model, *[]string) {
	t.Helper()
	var sent []string
	m := NewModel(Options{
		Channel: "buh",
		Mod:     mod,
		Chan:    ch,
		Send:    func(s string) { sent = append(sent, s) },
	})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m.Append(chat.Message{ID: "m1", Author: "Alice", AuthorLogin: "alice", AuthorID: "42", Text: "hi"})
	return m, &sent
}

// run types a line, presses enter, and returns the resulting actionResult.
func run(t *testing.T, m *Model, line string) actionResult {
	t.Helper()
	typeRunes(m, line)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("%q produced no command", line)
	}
	res, ok := cmd().(actionResult)
	if !ok {
		t.Fatalf("%q did not yield an actionResult", line)
	}
	return res
}

func TestSplitQuoted(t *testing.T) {
	cases := map[string][]string{
		"a b c":            {"a", "b", "c"},
		`/poll "a b" c`:    {"/poll", "a b", "c"},
		`"" x`:             {"", "x"},
		`"unterminated y`:  {"unterminated y"},
		"  spaced   out  ": {"spaced", "out"},
		"":                 nil,
	}
	for in, want := range cases {
		if got := splitQuoted(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitQuoted(%q) = %#v, want %#v", in, got, want)
		}
	}
}

func TestParseDur(t *testing.T) {
	for _, c := range []struct {
		in   string
		def  int
		want int
		bad  bool
	}{
		{"", 30, 30, false},
		{"90", 0, 90, false},
		{"10m", 0, 600, false},
		{"garbage", 0, 0, true},
	} {
		got, err := parseDur(c.in, c.def)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("parseDur(%q) = %d, err=%v; want %d, bad=%v", c.in, got, err, c.want, c.bad)
		}
	}
}

func TestSlashTimeoutResolvesFromBuffer(t *testing.T) {
	f := &fakeMod{}
	m, _ := commandModel(t, f, &fakeChan{})
	res := run(t, m, "/timeout @Alice 10m spam")
	if res.err {
		t.Fatalf("unexpected error: %s", res.text)
	}
	if f.timeoutUser != "42" || f.timeoutSecs != 600 {
		t.Errorf("timeout = %s/%ds, want 42/600s", f.timeoutUser, f.timeoutSecs)
	}
}

func TestSlashSlowPatchesSettings(t *testing.T) {
	ch := &fakeChan{}
	m, _ := commandModel(t, &fakeMod{}, ch)
	run(t, m, "/slow 60")
	want := map[string]any{"slow_mode": true, "slow_mode_wait_time": 60}
	if !reflect.DeepEqual(ch.settings, want) {
		t.Errorf("settings = %#v, want %#v", ch.settings, want)
	}
}

func TestSlashPollParsesQuotes(t *testing.T) {
	ch := &fakeChan{}
	m, _ := commandModel(t, &fakeMod{}, ch)
	run(t, m, `/poll "Best letter?" "a" "b or c" 2m`)
	if ch.pollT != "Best letter?" || !reflect.DeepEqual(ch.pollC, []string{"a", "b or c"}) || ch.pollDur != 120 {
		t.Errorf("poll = %q %#v %ds", ch.pollT, ch.pollC, ch.pollDur)
	}

	// Too few choices is caught before any call.
	res := run(t, m, `/poll "title" "only one"`)
	if !res.err {
		t.Error("a one-choice poll was accepted")
	}
}

func TestTwitchOnlyOnYouTubeTab(t *testing.T) {
	m, _ := commandModel(t, &fakeMod{}, nil) // nil ChannelManager = YouTube/combined
	res := run(t, m, "/slow")
	if !res.err || res.text != "twitch only (needs a single logged-in twitch tab)" {
		t.Errorf("got %q err=%v", res.text, res.err)
	}
}

func TestUnknownCommandNotices(t *testing.T) {
	m, sent := commandModel(t, &fakeMod{}, &fakeChan{})
	res := run(t, m, "/bogus")
	if !res.err || res.text != "unknown command /bogus (try /help)" {
		t.Errorf("got %q err=%v", res.text, res.err)
	}
	if len(*sent) != 0 {
		t.Errorf("an unknown command was sent to chat: %v", *sent)
	}
}

func TestSlashDeleteTargetsLastMessageWithID(t *testing.T) {
	f := &fakeMod{}
	m, _ := commandModel(t, f, &fakeChan{})
	m.Append(chat.Message{ID: "m2", Author: "Alice", AuthorLogin: "alice", AuthorID: "42", Text: "newer"})
	m.Append(chat.Message{Author: "Alice", AuthorLogin: "alice", AuthorID: "42", Text: "own echo, no id"})
	run(t, m, "/delete alice")
	if f.deleted != "m2" {
		t.Errorf("deleted %q, want m2 (latest with an id)", f.deleted)
	}
}

func TestMePassesThrough(t *testing.T) {
	m, sent := commandModel(t, &fakeMod{}, &fakeChan{})
	typeRunes(m, "/me waves")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(*sent) != 1 || (*sent)[0] != "/me waves" {
		t.Errorf("sent = %v, want [/me waves]", *sent)
	}
}

func TestScopeHintOn401(t *testing.T) {
	ch := &fakeChan{err: context.DeadlineExceeded}
	m, _ := commandModel(t, &fakeMod{}, ch)
	res := run(t, m, "/clear")
	if !res.err || res.text != context.DeadlineExceeded.Error() {
		t.Fatalf("non-auth error mangled: %q", res.text)
	}

	ch.err = errStatus("401: missing scope")
	res = run(t, m, "/clear")
	if res.text != "401: missing scope — re-login may be needed: crow logout && crow login" {
		t.Errorf("auth error got no hint: %q", res.text)
	}
}

type errStatus string

func (e errStatus) Error() string { return string(e) }
