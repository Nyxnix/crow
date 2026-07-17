package tui

import (
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/Nyxnix/typetype/internal/chat"
	"github.com/Nyxnix/typetype/internal/kitty"
)

// continuationIndent is the hanging indent on wrapped lines, which keeps the
// author column readable when messages are long.
const continuationIndent = 2

// renderBadges builds the badge segment shown before the author's name, and
// reports its display width in cells so the caller can place the name's hit box.
//
// With a graphics cache and resolved badge URLs, badges render as inline images
// separated by nothing and followed by one space. Until an image has loaded, or
// on terminals without graphics, it falls back to a single text role tag so the
// column is never empty for a privileged user.
func renderBadges(m chat.Message, style *styles, gfx *kitty.Cache) (s string, width int) {
	if gfx != nil {
		var b strings.Builder
		n := 0
		for _, bd := range m.Badges {
			if bd.URL == "" {
				continue // unresolved badge has no image to show
			}
			img, cols, ok := gfx.Render(bd.URL)
			if !ok {
				continue // still loading; a redraw will bring it in
			}
			b.WriteString(img)
			width += cols
			n++
		}
		if n > 0 {
			b.WriteString(" ")
			return b.String(), width + 1
		}
		// Fall through to the text tag while images load.
	}

	tag := style.roleTag(m)
	if tag == "" {
		return "", 0
	}
	return tag + " ", lipglossWidth(tag) + 1
}

// timestamp formats a message's time as the "HH:MM " prefix shown before the
// author. A message with no time (shouldn't happen for real ones) still gets a
// fixed-width blank so columns stay aligned.
func timestamp(m chat.Message) string {
	if m.At.IsZero() {
		return "      "
	}
	return m.At.Format("15:04") + " "
}

// hit records where a clickable username landed on screen. The renderer is the
// only thing that knows this, so it has to hand it back rather than have the
// click handler guess.
type hit struct {
	row    int // 0-based row within the chat viewport
	x0, x1 int // columns [x0, x1)
	msg    int // index into the model's message slice
}

// line is one laid-out screen line.
type line struct {
	text string
	hit  *hit // non-nil when this line contains a username
}

// layout renders messages into screen lines and records where each username
// landed. Widths are measured with runewidth because CJK and emoji occupy two
// terminal cells: measuring in runes would misplace every hit box after them.
//
// gfx, when non-nil, renders badges as inline images; otherwise a text role tag
// stands in.
func layout(msgs []chat.Message, width int, style *styles, gfx *kitty.Cache) []line {
	if width < 20 {
		width = 20 // below this, wrapping degenerates; clamp rather than loop forever
	}

	var out []line
	for i, m := range msgs {
		name := m.Author

		// A "HH:MM " timestamp leads every message, so it shifts everything after
		// it — including the name's hit box — right by its fixed 6 columns.
		ts := timestamp(m)
		tsW := runewidth.StringWidth(ts)

		// Badges sit before the name and shift its hit box right. With graphics
		// they render as images; without, a single text role tag stands in.
		badgeStr, badgeW := renderBadges(m, style, gfx)

		nameStart := tsW + badgeW
		prefixW := nameStart + runewidth.StringWidth(name) + 2 // name + ": "

		// A name wider than the line has nothing sensible to wrap against; give
		// the text its own lines instead of a negative budget.
		firstW := width - prefixW
		if firstW < 8 {
			firstW = width
		}

		chunks := wrap(m.Text, firstW, width-continuationIndent)

		first := ""
		if len(chunks) > 0 {
			first = chunks[0]
		}

		h := &hit{
			row: len(out),
			x0:  nameStart,
			x1:  nameStart + runewidth.StringWidth(name),
			msg: i,
		}

		var b strings.Builder
		b.WriteString(style.dim.Render(ts))
		b.WriteString(badgeStr)
		b.WriteString(style.name(m).Render(name))
		b.WriteString(style.punct.Render(": "))
		b.WriteString(style.text.Render(first))
		out = append(out, line{text: b.String(), hit: h})

		for _, c := range chunks[1:] {
			out = append(out, line{
				text: strings.Repeat(" ", continuationIndent) + style.text.Render(c),
			})
		}
	}
	return out
}

// wrap breaks text into chunks that fit firstW display columns on the first
// line and restW on the rest.
//
// It breaks on spaces, and hard-splits any single word too long to ever fit
// (pasted URLs, emote spam) rather than letting it overflow the viewport.
func wrap(text string, firstW, restW int) []string {
	if firstW < 1 {
		firstW = 1
	}
	if restW < 1 {
		restW = 1
	}

	var out []string
	cur, curW := "", 0

	// Only the first emitted line gets the narrower first-line budget.
	avail := func() int {
		if len(out) == 0 {
			return firstW
		}
		return restW
	}
	push := func() {
		out = append(out, cur)
		cur, curW = "", 0
	}

	for _, word := range strings.Fields(text) {
		for {
			wW := runewidth.StringWidth(word)
			sep := 0
			if curW > 0 {
				sep = 1
			}

			if curW+sep+wW <= avail() {
				if sep == 1 {
					cur += " "
					curW++
				}
				cur += word
				curW += wW
				break
			}

			// Doesn't fit here; a fresh line may be enough.
			if curW > 0 {
				push()
				continue
			}

			// Alone on a line and still too wide: split it by display column.
			piece, rest := takeWidth(word, avail())
			if piece == "" {
				// avail() is at least 1 and takeWidth returned nothing, meaning a
				// single glyph is wider than the line. Emit it anyway; refusing
				// to make progress here would loop forever.
				piece, rest = word, ""
			}
			cur, curW = piece, runewidth.StringWidth(piece)
			if rest == "" {
				break
			}
			push()
			word = rest
		}
	}

	if curW > 0 || len(out) == 0 {
		out = append(out, cur)
	}
	return out
}

// takeWidth splits s into the longest prefix fitting width display columns and
// the remainder. It measures per rune because CJK and emoji are two cells wide.
func takeWidth(s string, width int) (prefix, rest string) {
	w := 0
	for i, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > width {
			return s[:i], s[i:]
		}
		w += rw
	}
	return s, ""
}
