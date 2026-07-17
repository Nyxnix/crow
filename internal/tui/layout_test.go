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
	lines := layout(msgs, 40, newStyles(), nil)

	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	h := lines[0].hit
	if h == nil {
		t.Fatal("no hit recorded; the name would not be clickable")
	}
	// The name sits after a 6-column "HH:MM " timestamp, so its box starts at 6.
	if h.row != 0 || h.x0 != 6 || h.x1 != 9 || h.msg != 0 {
		t.Errorf("hit = %+v, want row 0 cols [6,9) msg 0", *h)
	}
}

// The hit box must cover the name in display columns, or clicks land off-target
// for anyone with a CJK display name.
func TestLayoutHitBoxIsDisplayWidth(t *testing.T) {
	msgs := []chat.Message{{Author: "日本語", Text: "hi"}}
	lines := layout(msgs, 40, newStyles(), nil)

	h := lines[0].hit
	if h == nil {
		t.Fatal("no hit recorded")
	}
	// Three CJK runes occupy six columns, after the 6-column timestamp: [6,12).
	if h.x0 != 6 || h.x1 != 12 {
		t.Errorf("hit = [%d,%d), want [6,12); rune-counting would give width 3", h.x0, h.x1)
	}
}

// A role marker is printed before the name, so the hit box must shift right by
// its width. Without this, clicking a broadcaster or mod's name lands short.
func TestLayoutHitBoxShiftsPastRoleTag(t *testing.T) {
	plain := layout([]chat.Message{{Author: "nyx", Text: "hi"}}, 40, newStyles(), nil)
	tagged := layout([]chat.Message{{Author: "nyx", Text: "hi", Broadcaster: true}}, 40, newStyles(), nil)

	ph, th := plain[0].hit, tagged[0].hit
	if ph == nil || th == nil {
		t.Fatal("missing hit")
	}
	if th.x0 == ph.x0 {
		t.Errorf("tagged hit starts at %d, same as untagged; the [B] marker was not accounted for", th.x0)
	}
	// Untagged name sits at column 6 (after the timestamp); the "[B] " marker is
	// four more columns, so a tagged name starts at 10.
	if th.x0 != 10 || th.x1 != 13 {
		t.Errorf("tagged hit = [%d,%d), want [10,13)", th.x0, th.x1)
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
	lines := layout(msgs, 30, newStyles(), nil)

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

// Without graphics, badges fall back to a single text role tag, and the name's
// hit box shifts past it.
func TestRenderBadgesTextFallback(t *testing.T) {
	s := newStyles()
	// A broadcaster with no graphics cache: the [B] tag stands in.
	str, w := renderBadges(chat.Message{Broadcaster: true}, s, nil)
	if !strings.Contains(str, "[B]") {
		t.Errorf("fallback = %q, want the [B] tag", str)
	}
	if w != lipglossWidth(s.roleTag(chat.Message{Broadcaster: true}))+1 {
		t.Errorf("width %d does not match the tag plus its trailing space", w)
	}

	// A plain user contributes no badge segment.
	if str, w := renderBadges(chat.Message{}, s, nil); str != "" || w != 0 {
		t.Errorf("plain user badges = (%q, %d), want empty", str, w)
	}
}

func TestTimestampFormat(t *testing.T) {
	at := time.Date(2026, 7, 17, 9, 5, 0, 0, time.UTC)
	if got := timestamp(chat.Message{At: at}); got != "09:05 " {
		t.Errorf("timestamp = %q, want %q", got, "09:05 ")
	}
	// A zero time still takes the same width so columns line up.
	if got := timestamp(chat.Message{}); got != "      " {
		t.Errorf("zero-time timestamp = %q, want 6 spaces", got)
	}
}

// The timestamp is rendered before the name, and the name stays clickable.
func TestLayoutRendersTimestamp(t *testing.T) {
	at := time.Date(2026, 7, 17, 14, 30, 0, 0, time.UTC)
	lines := layout([]chat.Message{{Author: "nyx", Text: "hi", At: at}}, 40, newStyles(), nil)
	if !strings.Contains(lines[0].text, "14:30") {
		t.Errorf("line %q missing the timestamp", lines[0].text)
	}
	if lines[0].hit == nil {
		t.Error("timestamp broke the name hit")
	}
}

func TestLayoutNarrowWidthDoesNotPanic(t *testing.T) {
	msgs := []chat.Message{{Author: "averylongusernameindeed", Text: "some text here"}}
	for _, w := range []int{1, 5, 10, 21} {
		lines := layout(msgs, w, newStyles(), nil)
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
