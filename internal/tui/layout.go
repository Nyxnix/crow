package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/Nyxnix/crow/internal/chat"
	"github.com/Nyxnix/crow/internal/kitty"
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
// It also returns the badges' below-first-row placeholder cells as fills (at
// columns relative to the badge segment's start) when scale > 1, since badges
// then render as scale-row placements just like emotes.
func renderBadges(m chat.Message, style *styles, gfx *kitty.Cache, scale int) (s string, width int, fills []fill) {
	if gfx != nil {
		var b strings.Builder
		n := 0
		for _, bd := range m.Badges {
			if bd.URL == "" {
				continue // unresolved badge has no image to show
			}
			if scale > 1 {
				lines, cols, ok := gfx.RenderLarge(bd.URL, scale)
				if !ok {
					continue
				}
				b.WriteString(lines[0])
				fills = append(fills, fill{x0: width, x1: width + cols, rows: lines[1:]})
				width += cols
				n++
				continue
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
			return b.String(), width + scale, fills
		}
		// Fall through to the text tag while images load.
	}

	tag := style.roleTag(m)
	if tag == "" {
		return "", 0, nil
	}
	return tag + " ", (lipglossWidth(tag) + 1) * scale, nil
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

// ehit records where a clickable emote landed. Unlike a username hit there can
// be several on one line, so they are carried as a slice with absolute rows.
type ehit struct {
	row    int
	x0, x1 int
	emote  chat.Emote
}

// line is one laid-out screen line. At scale > 1 a line occupies scale screen
// rows: text carries the top row (scaled text plus the emotes' first placeholder
// row) and fillers carry the scale-1 rows below, holding the emotes' remaining
// placeholder rows so a multi-row emote placement completes.
type line struct {
	text    string
	fillers []string
	hit     *hit   // non-nil when this line contains a username
	emotes  []ehit // clickable emotes on this line, if any
}

// layout renders messages into screen lines and records where each username
// landed. Widths are measured with runewidth because CJK and emoji occupy two
// terminal cells: measuring in runes would misplace every hit box after them.
//
// gfx, when non-nil, renders badges as inline images; otherwise a text role tag
// stands in.
//
// scale > 1 draws message text at that multiple via kitty's OSC 66 (each glyph
// scale cells wide, the line scale rows tall) and emotes as scale-row image
// placements. All widths here are physical terminal cells: a text glyph counts
// scale cells, an image placement counts its own cells.
func layout(msgs []chat.Message, width int, style *styles, gfx *kitty.Cache, scale int) []line {
	if scale < 1 {
		scale = 1
	}
	// A narrow window downgrades the scale rather than the layout lying about
	// the width: rows wider than the real terminal autowrap and desync the
	// whole frame, which is strictly worse than smaller text.
	for scale > 1 && width/scale < 20 {
		scale--
	}
	if width < 20 {
		width = 20 // below this, wrapping degenerates; clamp rather than loop forever
	}

	var out []line
	for i, m := range msgs {
		// Stream alerts and messages mentioning the logged-in user render as
		// full-width red banner rows instead of the normal prefix+body shape.
		if m.Alert != "" || (!m.Deleted && style.isMention(m.Text)) {
			out = append(out, bannerLines(m, width, style, gfx, scale, i)...)
			continue
		}

		name := m.Author

		// A "HH:MM " timestamp leads every message, so it shifts everything after
		// it — including the name's hit box — right by its fixed 6 columns.
		ts := timestamp(m)
		tsW := runewidth.StringWidth(ts) * scale

		// Badges sit before the name and shift its hit box right. With graphics
		// they render as images; without, a single text role tag stands in.
		badgeStr, badgeW, badgeFills := renderBadges(m, style, gfx, scale)
		for f := range badgeFills {
			badgeFills[f].x0 += tsW // badge fills are relative to the segment start
			badgeFills[f].x1 += tsW
		}

		nameStart := tsW + badgeW

		// The prefix row must itself fit the line: truncate a name that would
		// push "name: " past the width (leaving no room is how rows overflow).
		if maxName := (width-nameStart)/scale - 2; maxName >= 2 &&
			runewidth.StringWidth(name) > maxName {
			head, _ := takeWidth(name, maxName-1)
			name = head + "…"
		}

		prefixW := nameStart + (runewidth.StringWidth(name)+2)*scale // name + ": "

		// A prefix that nearly fills the line leaves nothing to wrap against.
		// The body then goes entirely on continuation lines — never on the
		// prefix row with a full-width budget, which would overflow the
		// terminal, autowrap, and desync every row after it.
		firstW := width - prefixW
		if m.Deleted {
			firstW -= 11 * scale // room for the " ✗ deleted" suffix on the first row
		}
		bodyOwnLines := firstW < 8*scale
		if bodyOwnLines {
			firstW = width - continuationIndent*scale
		}

		// A deleted message keeps its place but is struck through, so a moderator
		// can see what was removed — the same as Twitch's own moderator view.
		textStyle := style.text
		if m.Deleted {
			textStyle = style.deleted
		}

		// Render the body into already-styled lines. With graphics and a live
		// message, emotes become inline images; a deleted message and terminals
		// without graphics get plain (struck) text with emote names.
		var bodyLines []string
		var spans [][]emoteSpan
		restW := width - continuationIndent*scale
		if gfx != nil && !m.Deleted {
			bodyLines, spans = layoutBodyEmotes(m, firstW, restW, textStyle, gfx, scale)
		} else {
			for _, c := range wrap(m.Text, firstW/scale, restW/scale) {
				bodyLines = append(bodyLines, textStyle.Render(c))
			}
		}
		first := ""
		if bodyOwnLines {
			// Keep the prefix row body-free; every body line becomes a
			// continuation below it.
			bodyLines = append([]string{""}, bodyLines...)
			spans = append([][]emoteSpan{nil}, spans...)
		} else if len(bodyLines) > 0 {
			first = bodyLines[0]
		}

		nameRow := len(out)
		h := &hit{
			row: nameRow,
			x0:  nameStart,
			x1:  nameStart + runewidth.StringWidth(name)*scale,
			msg: i,
		}

		// Emotes on body line 0 sit after the prefix; continuation lines are only
		// indented. spanHits maps a packed line's spans to absolute click boxes.
		spanHits := func(row, colOffset int, ss []emoteSpan) []ehit {
			var hs []ehit
			for _, s := range ss {
				hs = append(hs, ehit{row: row, x0: colOffset + s.x0, x1: colOffset + s.x1, emote: *s.emote})
			}
			return hs
		}

		var b strings.Builder
		b.WriteString(style.dim.Render(ts))
		b.WriteString(badgeStr)
		b.WriteString(style.name(m).Render(name))
		b.WriteString(style.punct.Render(": "))
		b.WriteString(first)
		if m.Deleted {
			b.WriteString(style.dim.Render(" ✗ deleted"))
		}
		// spanFills places a packed line's emote fills at absolute columns.
		spanFills := func(colOffset int, ss []emoteSpan) []fill {
			var fs []fill
			for _, s := range ss {
				if len(s.rows) > 0 {
					fs = append(fs, fill{x0: colOffset + s.x0, x1: colOffset + s.x1, rows: s.rows})
				}
			}
			return fs
		}

		firstLine := line{text: kitty.ScaleText(b.String(), scale), hit: h}
		firstFills := badgeFills
		if len(spans) > 0 {
			firstLine.emotes = spanHits(nameRow, prefixW, spans[0])
			firstFills = append(firstFills, spanFills(prefixW, spans[0])...)
		}
		firstLine.fillers = fillerRows(firstFills, scale, width)
		out = append(out, firstLine)

		for k, c := range bodyLines[1:] {
			row := nameRow + 1 + k
			indent := continuationIndent * scale
			l := line{text: kitty.ScaleText(strings.Repeat(" ", continuationIndent)+c, scale)}
			if k+1 < len(spans) {
				l.emotes = spanHits(row, indent, spans[k+1])
			}
			var fs []fill
			if k+1 < len(spans) {
				fs = spanFills(indent, spans[k+1])
			}
			l.fillers = fillerRows(fs, scale, width)
			out = append(out, l)
		}
	}
	return out
}

// bannerLines renders a message as a full-width red banner: a stream alert
// ("★ Nyx subscribed at Tier 1." plus any attached message) or a mention of
// the logged-in user, which keeps its normal prefix — badges, colored
// clickable name — on the red band. Text renders at chat scale like any
// other line, but the scaled run stays text-length — a fully-scaled padded
// row is the one shape kitty's multicell-glyph erase rules mangle on
// rewrites (glyphs vanish, background stays) — and the band is completed to
// the terminal edge with plain red spaces: beside the text on each row, and
// as red filler rows under it, where space-writes skip the scaled glyph
// bottoms and paint only the cells around them. Body text is plain words
// (no inline emote images).
func bannerLines(m chat.Message, width int, style *styles, gfx *kitty.Cache, scale int, msgIdx int) []line {
	ts := timestamp(m)
	cols := width / scale
	tsW := runewidth.StringWidth(ts)

	// One banner row: the scaled text run, then plain red to the edge.
	row := func(scaled string, usedCells int) string {
		pad := width - usedCells
		if pad < 0 {
			pad = 0
		}
		return kitty.ScaleText(scaled, scale) + style.highlight.Render(strings.Repeat(" ", pad))
	}
	// A red filler row, with any badge-image bottom cells placed over it. Red
	// runs are interleaved around the cells rather than painted full-width
	// first: bubbletea truncates lines whose visible width exceeds the
	// terminal's, and a doubled-back row measures over and loses its tail —
	// which is exactly the image cells.
	fillerRow := func(fills []fill, dy int) string {
		var b strings.Builder
		col := 0
		for _, f := range fills {
			if dy >= len(f.rows) {
				continue
			}
			if f.x0 > col {
				b.WriteString(style.highlight.Render(strings.Repeat(" ", f.x0-col)))
			}
			fmt.Fprintf(&b, "\x1b[%dG", f.x0+1) // CHA is 1-based
			b.WriteString(f.rows[dy])
			col = f.x1
		}
		if col < width {
			b.WriteString(style.highlight.Render(strings.Repeat(" ", width-col)))
		}
		return b.String()
	}
	plainFillers := func() []string {
		var fs []string
		for dy := 1; dy < scale; dy++ {
			fs = append(fs, fillerRow(nil, dy-1))
		}
		return fs
	}

	var out []line
	addBody := func(body string) {
		for _, c := range wrap(body, cols-continuationIndent, cols-continuationIndent) {
			s := style.highlight.Render(strings.Repeat(" ", continuationIndent) + c)
			out = append(out, line{
				text:    row(s, (continuationIndent+runewidth.StringWidth(c))*scale),
				fillers: plainFillers(),
			})
		}
	}

	if m.Alert != "" {
		for i, c := range wrap("★ "+m.AlertText, cols-tsW, cols-continuationIndent) {
			prefix, cells := style.highlight.Render(strings.Repeat(" ", continuationIndent)), continuationIndent
			if i == 0 {
				prefix, cells = style.dim.Render(ts), tsW
			}
			s := prefix + style.highlight.Render(c)
			out = append(out, line{
				text:    row(s, (cells+runewidth.StringWidth(c))*scale),
				fillers: plainFillers(),
			})
		}
		if m.Text != "" { // wrap("") yields one empty line; an alert may have no body
			addBody(m.Text)
		}
		return out
	}

	// A mention: the normal prefix shape (badges, colored name, hit box) on
	// the band, body as plain highlighted words.
	bg := style.highlight.GetBackground()
	badgeStr, badgeW, badgeFills := renderBadges(m, style, gfx, scale)
	for f := range badgeFills {
		badgeFills[f].x0 += tsW * scale
		badgeFills[f].x1 += tsW * scale
	}
	nameStart := tsW*scale + badgeW
	name := m.Author
	prefixCells := nameStart + (runewidth.StringWidth(name)+2)*scale

	firstW := (width - prefixCells) / scale
	chunks := wrap(m.Text, firstW, cols-continuationIndent)
	if firstW < 8 {
		// The prefix nearly fills the line; body goes entirely on continuation
		// rows rather than overflowing the first (the autowrap desync hazard).
		chunks = append([]string{""}, wrap(m.Text, cols-continuationIndent, cols-continuationIndent)...)
	}

	var fillers []string
	for dy := 1; dy < scale; dy++ {
		fillers = append(fillers, fillerRow(badgeFills, dy-1))
	}
	first := style.dim.Render(ts) + badgeStr +
		style.name(m).Background(bg).Render(name) +
		style.punct.Background(bg).Render(": ") +
		style.highlight.Render(chunks[0])
	out = append(out, line{
		text:    row(first, prefixCells+runewidth.StringWidth(chunks[0])*scale),
		fillers: fillers,
		hit: &hit{
			x0:  nameStart,
			x1:  nameStart + runewidth.StringWidth(name)*scale,
			msg: msgIdx,
		},
	})
	for _, c := range chunks[1:] {
		s := style.highlight.Render(strings.Repeat(" ", continuationIndent) + c)
		out = append(out, line{
			text:    row(s, (continuationIndent+runewidth.StringWidth(c))*scale),
			fillers: plainFillers(),
		})
	}
	return out
}

// fill is a multi-row image placement's below-first-row placeholder cells,
// positioned at absolute columns within the line.
type fill struct {
	x0, x1 int
	rows   []string
}

// fillerRows builds the scale-1 screen rows below a scaled line. These rows
// hold the bottom cells of the scaled glyphs above, and kitty deletes a whole
// multicell glyph if an erase (EL/ED) touches any of its cells — so a filler
// row must NEVER be erased. Stale content is cleared by sweeping spaces
// instead: a write over a glyph's bottom cell is skipped (the glyph survives)
// while a write over a stale normal cell replaces it. Because skips displace
// the cursor unpredictably, every image fill is positioned with an absolute
// cursor jump (CSI n G) rather than counted spaces. The sweep also keeps the
// row's measured width at the terminal width, which stops bubbletea from
// appending its own erase-to-end-of-line.
func fillerRows(fills []fill, scale, width int) []string {
	if scale <= 1 {
		return nil
	}
	out := make([]string, scale-1)
	for d := range out {
		var b strings.Builder
		b.WriteString("\x1b[1G")
		col := 0
		for _, f := range fills {
			if d >= len(f.rows) || f.rows[d] == "" {
				continue
			}
			if f.x0 > col {
				b.WriteString(strings.Repeat(" ", f.x0-col))
			}
			fmt.Fprintf(&b, "\x1b[%dG", f.x0+1) // CHA is 1-based
			b.WriteString(f.rows[d])
			col = f.x1
		}
		if col < width {
			b.WriteString(strings.Repeat(" ", width-col))
		}
		out[d] = b.String()
	}
	return out
}

// bodyToken is one unit of a message body: a plain word or an emote.
type bodyToken struct {
	text  string      // the word, or the emote's name
	emote *chat.Emote // non-nil when this word is an emote
}

// renderedToken is a body token after styling: its on-screen string and its
// true display width in cells (which for an emote image is not derivable from
// the string, so it is carried explicitly). emote is set when the token is an
// emote, so packing can report where it landed for click handling. rows holds
// a multi-row emote's placeholder rows below the first, for the line's fillers.
type renderedToken struct {
	str   string
	rows  []string
	w     int
	emote *chat.Emote
}

// layoutBodyEmotes renders a message body into styled lines with emotes shown
// as inline images. An emote whose image has not loaded (or failed) falls back
// to its name, so the line is never empty where an emote should be. textStyle
// styles the plain-word tokens (normal, or highlight for a mention).
func layoutBodyEmotes(m chat.Message, firstW, restW int, textStyle lipgloss.Style, gfx *kitty.Cache, scale int) ([]string, [][]emoteSpan) {
	if restW < scale {
		restW = scale
	}
	// A token must fit whichever line it lands on, and an empty first line
	// accepts any token — so split against the smaller of the two budgets.
	// Splitting against restW alone let a prefix-wide first line overflow the
	// terminal, and one overflowed row autowraps, scrolls the screen, and
	// desyncs every absolutely-positioned cell after it.
	minW := restW
	if firstW < minW {
		minW = firstW
	}
	if minW < scale {
		minW = scale
	}

	// Split long plain words up front (a pasted URL) so no token is wider than a
	// line; emotes are small and never need this. After this, packing never has
	// to split a token. Text is measured in physical cells: scale per glyph.
	var tokens []bodyToken
	for _, t := range tokenizeBody(m) {
		if t.emote == nil && runewidth.StringWidth(t.text)*scale > minW {
			for _, piece := range splitWord(t.text, minW/scale) {
				tokens = append(tokens, bodyToken{text: piece})
			}
			continue
		}
		tokens = append(tokens, t)
	}

	rendered := make([]renderedToken, 0, len(tokens))
	for _, t := range tokens {
		if t.emote != nil && t.emote.URL != "" {
			// For an animated emote, fetch the GIF straight away; the WebP the
			// registry supplies (for the overlay) is not decodable here.
			url := t.emote.URL
			if t.emote.Animated {
				url = kitty.AnimatedURL(url)
			}
			// At scale, the emote becomes a scale-row placement (placeholders can't
			// ride inside a scaled text run — kitty renders them blank there).
			// An emote wider than a line falls back to its name: overflowing the
			// terminal is the desync catastrophe described above.
			if scale > 1 {
				if lines, cols, ok := gfx.RenderLarge(url, scale); ok && cols <= minW {
					rendered = append(rendered, renderedToken{str: lines[0], rows: lines[1:], w: cols, emote: t.emote})
					continue
				}
			} else if img, cols, ok := gfx.Render(url); ok && cols <= minW {
				rendered = append(rendered, renderedToken{str: img, w: cols, emote: t.emote})
				continue
			}
		}
		rendered = append(rendered, renderedToken{str: textStyle.Render(t.text), w: runewidth.StringWidth(t.text) * scale, emote: t.emote})
	}

	return packTokens(rendered, firstW, restW, scale)
}

// emoteSpan marks where an emote token landed within one packed line. rows
// carries a multi-row emote's placeholder rows below the first, for the fillers.
type emoteSpan struct {
	emote  *chat.Emote
	rows   []string
	x0, x1 int // columns within the line
}

// tokenizeBody splits a message into words, marking those that are emotes.
// Emote positions index runes, matching how the parser records them.
func tokenizeBody(m chat.Message) []bodyToken {
	runes := []rune(m.Text)
	emoteAt := make(map[int]*chat.Emote, len(m.Emotes))
	for i := range m.Emotes {
		emoteAt[m.Emotes[i].Start] = &m.Emotes[i]
	}

	var tokens []bodyToken
	for i := 0; i < len(runes); {
		for i < len(runes) && runes[i] == ' ' {
			i++
		}
		if i >= len(runes) {
			break
		}
		if e, ok := emoteAt[i]; ok && e.End <= len(runes) {
			tokens = append(tokens, bodyToken{text: string(runes[e.Start:e.End]), emote: e})
			i = e.End
			continue
		}
		j := i
		for j < len(runes) && runes[j] != ' ' {
			j++
		}
		tokens = append(tokens, bodyToken{text: string(runes[i:j])})
		i = j
	}
	return tokens
}

// packTokens greedily packs rendered tokens into lines, one space between
// tokens, honoring the narrower first-line budget. Every token is assumed to
// fit a line on its own (splitWord guarantees it for text; emotes are small).
func packTokens(tokens []renderedToken, firstW, restW, scale int) ([]string, [][]emoteSpan) {
	var lines []string
	var spans [][]emoteSpan
	var cur strings.Builder
	var curSpans []emoteSpan
	curW := 0
	avail := func() int {
		if len(lines) == 0 {
			return firstW
		}
		return restW
	}
	flush := func() {
		lines = append(lines, cur.String())
		spans = append(spans, curSpans)
		cur.Reset()
		curSpans = nil
		curW = 0
	}

	for _, t := range tokens {
		sep := 0
		if curW > 0 {
			sep = scale // the separating space is text, so it scales too
		}
		if curW > 0 && curW+sep+t.w > avail() {
			flush()
			sep = 0
		}
		if sep > 0 {
			cur.WriteByte(' ')
			curW += sep
		}
		if t.emote != nil {
			curSpans = append(curSpans, emoteSpan{emote: t.emote, rows: t.rows, x0: curW, x1: curW + t.w})
		}
		cur.WriteString(t.str)
		curW += t.w
	}
	if curW > 0 || len(lines) == 0 {
		flush()
	}
	return lines, spans
}

// splitWord chops an unbreakable word into width-sized pieces, measuring by
// display column so CJK does not overflow.
func splitWord(word string, width int) []string {
	var out []string
	for word != "" {
		piece, rest := takeWidth(word, width)
		if piece == "" { // a single glyph wider than the line; emit it anyway
			piece, rest = word, ""
		}
		out = append(out, piece)
		word = rest
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
