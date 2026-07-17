package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/Nyxnix/typetype/internal/chat"
)

func TestWrapBreaksOnSpaces(t *testing.T) {
	got := wrap("the quick brown fox", 10, 10)
	want := []string{"the quick", "brown fox"}
	if !eq(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The first line is narrower because the author's name sits on it.
func TestWrapUsesNarrowerFirstLine(t *testing.T) {
	got := wrap("aaa bbb ccc ddd", 7, 11)
	want := []string{"aaa bbb", "ccc ddd"}
	if !eq(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A pasted URL has no spaces to break on and must not overflow the viewport.
func TestWrapHardSplitsLongWords(t *testing.T) {
	long := strings.Repeat("x", 25)
	got := wrap(long, 10, 10)
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(got), got)
	}
	for _, l := range got {
		if runewidth.StringWidth(l) > 10 {
			t.Errorf("line %q exceeds width 10", l)
		}
	}
	if strings.Join(got, "") != long {
		t.Errorf("hard split lost characters: %q", got)
	}
}

// Widths are display columns, not runes: CJK is two cells wide.
func TestWrapMeasuresDisplayWidth(t *testing.T) {
	// Six runes, twelve columns. At width 10 it cannot be one line.
	got := wrap("日本語日本語", 10, 10)
	for _, l := range got {
		if w := runewidth.StringWidth(l); w > 10 {
			t.Errorf("line %q is %d columns, want <= 10", l, w)
		}
	}
	if len(got) < 2 {
		t.Errorf("got %q, want it split; rune-counting would wrongly fit this", got)
	}
}

func TestWrapEmptyText(t *testing.T) {
	if got := wrap("", 10, 10); len(got) != 1 || got[0] != "" {
		t.Errorf("got %q, want one empty line", got)
	}
}

// A width of zero or less must not spin.
func TestWrapDegenerateWidth(t *testing.T) {
	done := make(chan []string, 1)
	go func() { done <- wrap("hello world", 0, 0) }()
	select {
	case got := <-done:
		if len(got) == 0 {
			t.Error("want some output")
		}
	case <-timeout():
		t.Fatal("wrap did not terminate at width 0")
	}
}

func TestLayoutRecordsNameHit(t *testing.T) {
	msgs := []chat.Message{{Author: "nyx", Text: "hello"}}
	lines := layout(msgs, 40, newStyles())

	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	h := lines[0].hit
	if h == nil {
		t.Fatal("no hit recorded; the name would not be clickable")
	}
	if h.row != 0 || h.x0 != 0 || h.x1 != 3 || h.msg != 0 {
		t.Errorf("hit = %+v, want row 0 cols [0,3) msg 0", *h)
	}
}

// The hit box must cover the name in display columns, or clicks land off-target
// for anyone with a CJK display name.
func TestLayoutHitBoxIsDisplayWidth(t *testing.T) {
	msgs := []chat.Message{{Author: "日本語", Text: "hi"}}
	lines := layout(msgs, 40, newStyles())

	h := lines[0].hit
	if h == nil {
		t.Fatal("no hit recorded")
	}
	// Three CJK runes occupy six columns.
	if h.x1 != 6 {
		t.Errorf("hit x1 = %d, want 6; rune-counting would give 3", h.x1)
	}
}

// A role marker is printed before the name, so the hit box must shift right by
// its width. Without this, clicking a broadcaster or mod's name lands short.
func TestLayoutHitBoxShiftsPastRoleTag(t *testing.T) {
	plain := layout([]chat.Message{{Author: "nyx", Text: "hi"}}, 40, newStyles())
	tagged := layout([]chat.Message{{Author: "nyx", Text: "hi", Broadcaster: true}}, 40, newStyles())

	ph, th := plain[0].hit, tagged[0].hit
	if ph == nil || th == nil {
		t.Fatal("missing hit")
	}
	if th.x0 == ph.x0 {
		t.Errorf("tagged hit starts at %d, same as untagged; the [B] marker was not accounted for", th.x0)
	}
	// "[B] " is four columns.
	if th.x0 != 4 || th.x1 != 7 {
		t.Errorf("tagged hit = [%d,%d), want [4,7)", th.x0, th.x1)
	}
	if width := th.x1 - th.x0; width != ph.x1-ph.x0 {
		t.Errorf("tagged name box is %d wide, untagged %d; they are the same name", width, ph.x1-ph.x0)
	}
}

// Only the line the name is on is clickable, and each message's hit points at
// its own index.
func TestLayoutHitsAcrossWrappedMessages(t *testing.T) {
	msgs := []chat.Message{
		{Author: "a", Text: strings.Repeat("word ", 20)},
		{Author: "b", Text: "short"},
	}
	lines := layout(msgs, 30, newStyles())

	var hits []hit
	for _, l := range lines {
		if l.hit != nil {
			hits = append(hits, *l.hit)
		}
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want one per message", len(hits))
	}
	if hits[0].msg != 0 || hits[1].msg != 1 {
		t.Errorf("hits point at %d,%d, want 0,1", hits[0].msg, hits[1].msg)
	}
	if hits[0].row != 0 {
		t.Errorf("first hit row = %d, want 0", hits[0].row)
	}
	// The second message starts after the first one's wrapped lines.
	if hits[1].row <= hits[0].row {
		t.Errorf("second hit row %d not after first %d", hits[1].row, hits[0].row)
	}
}

func TestLayoutNarrowWidthDoesNotPanic(t *testing.T) {
	msgs := []chat.Message{{Author: "averylongusernameindeed", Text: "some text here"}}
	for _, w := range []int{1, 5, 10, 21} {
		lines := layout(msgs, w, newStyles())
		if len(lines) == 0 {
			t.Errorf("width %d produced nothing", w)
		}
	}
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// timeout returns a channel that fires after a short deadline, used to catch
// non-terminating layout code rather than hanging the whole test run.
func timeout() <-chan time.Time { return time.After(2 * time.Second) }
