package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/Nyxnix/crow/internal/chat"
	"github.com/Nyxnix/crow/internal/kitty"
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

func TestTokenizeBody(t *testing.T) {
	m := chat.Message{
		Text: "hello Kappa world",
		Emotes: []chat.Emote{
			{Name: "Kappa", URL: "https://cdn/kappa", Start: 6, End: 11},
		},
	}
	tokens := tokenizeBody(m)
	if len(tokens) != 3 {
		t.Fatalf("got %d tokens, want 3: %+v", len(tokens), tokens)
	}
	if tokens[0].text != "hello" || tokens[0].emote != nil {
		t.Errorf("token 0 = %+v, want plain 'hello'", tokens[0])
	}
	if tokens[1].text != "Kappa" || tokens[1].emote == nil {
		t.Errorf("token 1 = %+v, want the Kappa emote", tokens[1])
	}
	if tokens[2].text != "world" || tokens[2].emote != nil {
		t.Errorf("token 2 = %+v, want plain 'world'", tokens[2])
	}
}

// Emote positions index runes, so a multi-byte prefix must not misplace them.
func TestTokenizeBodyRuneIndexed(t *testing.T) {
	m := chat.Message{
		Text:   "日本語 PogU",
		Emotes: []chat.Emote{{Name: "PogU", URL: "https://cdn/pogu", Start: 4, End: 8}},
	}
	tokens := tokenizeBody(m)
	if len(tokens) != 2 || tokens[1].emote == nil || tokens[1].text != "PogU" {
		t.Errorf("got %+v, want the PogU emote as the second token", tokens)
	}
}

func TestPackTokens(t *testing.T) {
	toks := []renderedToken{
		{str: "aaa", w: 3}, {str: "bbb", w: 3}, {str: "ccc", w: 3},
	}
	// Width 7 fits "aaa bbb" (7) then "ccc".
	lines, _ := packTokens(toks, 7, 7)
	if len(lines) != 2 || lines[0] != "aaa bbb" || lines[1] != "ccc" {
		t.Errorf("got %q, want [aaa bbb, ccc]", lines)
	}
}

// packTokens reports where each emote token landed so clicks can hit it. The
// span columns must use the declared cell width, not the escape's rune length.
func TestPackTokensReportsEmoteSpans(t *testing.T) {
	e := &chat.Emote{Name: "PogU"}
	toks := []renderedToken{
		{str: "word", w: 4},
		{str: "\x1bIMG\x1b", w: 2, emote: e},
	}
	lines, spans := packTokens(toks, 40, 40)
	if len(lines) != 1 || len(spans) != 1 || len(spans[0]) != 1 {
		t.Fatalf("got %d lines / spans %v, want one line with one span", len(lines), spans)
	}
	// "word" (4) + space (1) puts the emote at columns [5, 7).
	if got := spans[0][0]; got.x0 != 5 || got.x1 != 7 || got.emote != e {
		t.Errorf("span = %+v, want x0=5 x1=7 for the emote", got)
	}
}

// An emote token keeps its declared cell width even though its string (an image
// escape) has a different rune length, so packing places it correctly.
func TestPackTokensUsesDeclaredWidth(t *testing.T) {
	toks := []renderedToken{
		{str: "word", w: 4},
		{str: "\x1bIMAGE\x1b", w: 2}, // pretend image escape, 2 cells
		{str: "next", w: 4},
	}
	// Budget 6: "word" (4) + emote (2) = 6 fits with a space? 4+1+2=7 > 6, so
	// the emote wraps. Then emote(2)+space+next(4)=7 > 6, next wraps too.
	lines, _ := packTokens(toks, 6, 6)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (declared widths respected): %q", len(lines), lines)
	}
}

// With graphics but an unloaded emote, the body falls back to the emote name so
// nothing is missing while the image loads.
func TestLayoutBodyEmotesFallsBackToName(t *testing.T) {
	gfx := kitty.New(nil) // nothing loaded; Render returns not-ready
	m := chat.Message{
		Text:   "gg Kappa",
		Emotes: []chat.Emote{{Name: "Kappa", URL: "https://cdn/never-loads", Start: 3, End: 8}},
	}
	lines, _ := layoutBodyEmotes(m, 40, 40, newStyles(), gfx)
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "Kappa") {
		t.Errorf("unloaded emote not shown as its name: %q", lines)
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
