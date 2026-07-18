// Package kitty renders small remote images (badges, emotes) inline in the
// terminal using the kitty graphics protocol with Unicode placeholders.
//
// It works in terminals that speak the protocol (ghostty, kitty, WezTerm); on
// everything else Supported reports false and callers fall back to text. Images
// are fetched and decoded once, transmitted to the terminal on their first
// appearance, and thereafter displayed with cheap placeholder cells that
// reference the transmitted image by id.
package kitty

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	_ "image/jpeg" // decode Twitch/YouTube profile pictures
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "golang.org/x/image/webp" // decode static 7TV/BTTV webp emotes
)

// placeholder is U+10EEEE, the code point a cell holds to display part of an
// image identified by the cell's foreground color.
const placeholder = "\U0010EEEE"

// Supported reports whether the current terminal speaks the kitty graphics
// protocol. CROW_GRAPHICS=0/1 forces it off/on for terminals we don't
// recognise or for debugging.
func Supported() bool {
	switch os.Getenv("CROW_GRAPHICS") {
	case "0", "false", "off":
		return false
	case "1", "true", "on":
		return true
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return true
	}
	if strings.Contains(os.Getenv("TERM"), "kitty") {
		return true
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "ghostty", "WezTerm":
		return true
	}
	return false
}

// TextSizing reports whether the terminal supports the OSC 66 text sizing
// protocol (kitty ≥ 0.40). Other graphics-capable terminals (ghostty, WezTerm)
// do not speak it, and an unsupported terminal swallows the wrapped text
// entirely, so this must be a strict kitty check.
func TextSizing() bool {
	return os.Getenv("KITTY_WINDOW_ID") != "" || strings.Contains(os.Getenv("TERM"), "kitty")
}

// ScaleText wraps the plain-text runs of an already-styled line in OSC 66 so
// kitty draws them at scale (each glyph scale× wide, scale rows tall, anchored
// at the emitted row). Escape sequences pass through untouched — SGR state
// still applies to the scaled text — and image placeholder runs (U+10EEEE plus
// its combining diacritics) also pass through, because kitty does not render
// placeholders inside a scaled run; callers draw those as multi-row placements
// instead.
func ScaleText(s string, scale int) string {
	if scale <= 1 || s == "" {
		return s
	}
	var b strings.Builder
	var run []byte
	flush := func() {
		// The protocol caps one sequence's text at 4096 bytes; chat lines are far
		// shorter, but split defensively rather than corrupt.
		for len(run) > 0 {
			n := len(run)
			if n > 4000 {
				n = 4000
				for n > 0 && run[n]&0xC0 == 0x80 { // don't split mid-rune
					n--
				}
			}
			fmt.Fprintf(&b, "\x1b]66;s=%d;%s\x1b\\", scale, run[:n])
			run = run[n:]
		}
	}
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			flush()
			j := escapeEnd(s, i)
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == placeholderRune || isDiacritic(r) {
			flush()
			b.WriteString(s[i : i+size])
			i += size
			continue
		}
		run = append(run, s[i:i+size]...)
		i += size
	}
	flush()
	return b.String()
}

// escapeEnd returns the index just past the escape sequence starting at i.
func escapeEnd(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[': // CSI: params, then a final byte in 0x40..0x7E
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7E) {
			j++
		}
		if j < len(s) {
			j++
		}
		return j
	case ']', '_', 'P': // OSC / APC / DCS: until BEL or ESC \
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	default:
		return i + 2
	}
}

const placeholderRune = '\U0010EEEE'

var diacriticSet = func() map[rune]bool {
	m := make(map[rune]bool, len(diacritics))
	for _, r := range diacritics {
		m[r] = true
	}
	return m
}()

func isDiacritic(r rune) bool { return diacriticSet[r] }

// entry is one image's state in the cache. An animated emote has more than one
// frame; a static one has exactly one.
type entry struct {
	id          uint32
	rows        int      // placement height in cells; 0 means inline (one row)
	cols        int      // placement width in cells
	frames      [][]byte // PNG bytes per frame
	delays      []int    // per-frame display time in ms
	ready       bool      // fetched and decoded
	failed      bool      // fetch/decode failed; do not retry
	readyAt     time.Time // when ready flipped true, for the re-emit window
	transmitted bool      // upload settled: emitted long enough to be flushed
}

// placeRows is the placement height, treating the inline default (0) as 1.
func (e *entry) placeRows() int {
	if e.rows < 1 {
		return 1
	}
	return e.rows
}

// Cache fetches, decodes and remembers images by URL. It is safe for concurrent
// use: layout reads from the render goroutine while fetches complete on their
// own goroutines.
type Cache struct {
	mu     sync.Mutex
	byURL  map[string]*entry
	nextID uint32

	HTTP *http.Client

	// onReady is called when an image finishes loading, so the UI can request a
	// redraw and let the image pop in. Optional.
	onReady func()
}

func New(onReady func()) *Cache {
	return &Cache{byURL: map[string]*entry{}, nextID: 1, onReady: onReady}
}

func (c *Cache) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Render returns the placeholder cells that display the image at url and its
// width in cells. ok is false until the image has loaded; the first call for a
// not-yet-seen url starts an async fetch.
//
// The result contains only placeholder cells, never the image upload itself:
// uploads are collected separately by FlushUploads and emitted together at the
// top of a frame, so an upload sequence never lands between the placeholder
// cells of two adjacent images (which some terminals mishandle). Render is
// therefore side-effect free and safe to call from throwaway layout passes.
func (c *Cache) Render(url string) (s string, cols int, ok bool) {
	c.mu.Lock()
	e := c.byURL[url]
	if e == nil {
		// First sighting: reserve an id and kick off the fetch.
		e = &entry{id: c.nextID}
		c.nextID++
		c.byURL[url] = e
		c.mu.Unlock()
		go c.load(url, e)
		return "", 0, false
	}
	if !e.ready {
		c.mu.Unlock()
		return "", 0, false
	}
	id, cols := e.id, e.cols
	c.mu.Unlock()

	var b strings.Builder
	writePlaceholders(&b, id, 0, cols)
	return b.String(), cols, true
}

// RenderLarge is Render at rows cells tall: it returns one placeholder string
// per row (to place on successive screen lines) and the width in cols. It keeps
// a separate cache entry per (url, rows) so the inline copy is untouched, at the
// cost of a second upload — acceptable since it only happens when a card opens.
func (c *Cache) RenderLarge(url string, rows int) (lines []string, cols int, ok bool) {
	if rows < 1 {
		rows = 1
	}
	key := fmt.Sprintf("%s#%d", url, rows)

	c.mu.Lock()
	e := c.byURL[key]
	if e == nil {
		e = &entry{id: c.nextID, rows: rows}
		c.nextID++
		c.byURL[key] = e
		c.mu.Unlock()
		go c.load(url, e)
		return nil, 0, false
	}
	if !e.ready {
		c.mu.Unlock()
		return nil, 0, false
	}
	id, cols := e.id, e.cols
	c.mu.Unlock()

	lines = make([]string, rows)
	for row := 0; row < rows; row++ {
		var b strings.Builder
		writePlaceholders(&b, id, row, cols)
		lines[row] = b.String()
	}
	return lines, cols, true
}

// uploadWindow is how long an upload keeps being re-emitted after its image
// loads. Bubbletea renders on a ~16ms ticker and discards all but the last
// View() built within a tick, so an upload emitted in a discarded frame would
// be lost if we marked it sent immediately. Re-emitting for a window many ticks
// wide means every candidate frame carries it, so whichever the ticker flushes
// has it — then we stop, so a heavy animation uploads only a handful of times.
const uploadWindow = 200 * time.Millisecond

// maxLargeCols caps a large placement's width so a wide emote fits the emote
// card and stays within the diacritic table used to encode cell columns.
const maxLargeCols = 40

// FlushUploads returns the upload sequences for every image that has loaded but
// not yet settled. Emit the result once at the start of a frame, before any
// placeholder cells. It is empty when nothing is pending.
func (c *Cache) FlushUploads() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, e := range c.byURL {
		if !e.ready || e.transmitted {
			continue
		}
		writeUpload(&b, e)
		// An animated upload must be emitted exactly once: re-sending its a=T root
		// wipes the composed frames and re-sending a=f frames appends duplicates,
		// so re-emitting corrupts the animation. Only a single-frame (static) image
		// is safe to re-emit across the settle window that protects it from a
		// discarded frame during a message burst.
		if len(e.frames) > 1 || time.Since(e.readyAt) > uploadWindow {
			e.transmitted = true
		}
	}
	return b.String()
}

// load fetches and decodes one image off the render path, retrying a few times
// so a transient failure (a CDN rate-limit when several badges load at once, a
// blip) doesn't drop the image for the whole session.
func (c *Cache) load(url string, e *entry) {
	var (
		d   decoded
		err error
	)
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
		}
		if d, err = c.fetch(url); err == nil {
			break
		}
	}

	c.mu.Lock()
	if err != nil {
		e.failed = true
	} else {
		cols := d.cols // one-row width
		if e.rows > 1 {
			cols = colsForRows(d.w, d.h, e.rows)
			if cols > maxLargeCols {
				cols = maxLargeCols // fit the card; the placement scales to match
			}
		}
		e.frames, e.delays, e.cols, e.ready, e.readyAt = d.frames, d.delays, cols, true, time.Now()
	}
	c.mu.Unlock()

	if err == nil && c.onReady != nil {
		c.onReady()
	}
}

// decoded is the result of fetching an image: one PNG frame for a static image,
// several for an animated one, with per-frame delays and the natural pixel size
// (from which a display width in cells is derived per placement).
type decoded struct {
	frames [][]byte
	delays []int
	cols   int // display width at one row tall
	w, h   int // natural pixel dimensions
}

// fetch downloads and decodes an image. GIFs decode to all their frames;
// animated WebP (which the Go decoder cannot read) is retried as the GIF the
// provider serves alongside it. Every frame is re-encoded as PNG for upload.
func (c *Cache) fetch(url string) (decoded, error) {
	resp, err := c.client().Get(url)
	if err != nil {
		return decoded{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decoded{}, fmt.Errorf("%s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return decoded{}, err
	}

	// A GIF may be animated; decode every frame.
	if bytes.HasPrefix(raw, []byte("GIF8")) {
		return decodeGIF(raw)
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		// x/image/webp can't read animated WebP; providers (7TV, BTTV) serve a
		// GIF at the same path, so try that once.
		if alt := gifAlternative(url); alt != "" {
			return c.fetch(alt)
		}
		return decoded{}, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return decoded{}, err
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	return decoded{frames: [][]byte{buf.Bytes()}, delays: []int{0}, cols: cellsWide(w, h), w: w, h: h}, nil
}

// AnimatedURL maps a WebP emote URL to the small GIF the TUI animates. Callers
// that already know an emote is animated pass this to Render so the cache never
// downloads the undecodable WebP first. Returns url unchanged if it isn't a WebP.
func AnimatedURL(url string) string {
	if alt := gifAlternative(url); alt != "" {
		return alt
	}
	return url
}

// gifAlternative maps a WebP emote URL to its GIF sibling, or "" if not a WebP.
// It also steps the size down to 1x: an animated emote displays at about two
// cells (~34px), so 1x (32px) is already native resolution — the 4x source the
// overlay uses would upload roughly sixteen times the data per frame, decode
// far slower, and look no crisper. This is what keeps a 158-frame emote from
// taking a second or more to appear.
func gifAlternative(url string) string {
	if !strings.HasSuffix(url, ".webp") {
		return ""
	}
	u := strings.TrimSuffix(url, ".webp") + ".gif"
	for _, sz := range []string{"/4x.gif", "/3x.gif", "/2x.gif"} {
		u = strings.Replace(u, sz, "/1x.gif", 1)
	}
	return u
}

// decodeGIF composites each GIF frame into a full image, honoring the per-frame
// disposal method, and PNG-encodes each. Without disposal handling, a frame's
// transparent areas would let earlier frames show through, smearing an emote
// with a transparent background into an unrecognizable blur.
func decodeGIF(raw []byte) (decoded, error) {
	g, err := gif.DecodeAll(bytes.NewReader(raw))
	if err != nil || len(g.Image) == 0 {
		return decoded{}, fmt.Errorf("gif: %v", err)
	}
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	if bounds.Empty() {
		bounds = g.Image[0].Bounds()
	}
	canvas := image.NewRGBA(bounds)

	out := decoded{cols: cellsWide(bounds.Dx(), bounds.Dy()), w: bounds.Dx(), h: bounds.Dy()}
	for i, frame := range g.Image {
		var saved *image.RGBA
		if g.Disposal[i] == gif.DisposalPrevious {
			saved = cloneRGBA(canvas)
		}

		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)

		var buf bytes.Buffer
		if err := png.Encode(&buf, cloneRGBA(canvas)); err != nil {
			return decoded{}, err
		}
		out.frames = append(out.frames, buf.Bytes())
		ms := g.Delay[i] * 10 // GIF delays are hundredths of a second
		if ms <= 0 {
			ms = 100
		}
		out.delays = append(out.delays, ms)

		// Prepare the canvas for the next frame according to disposal.
		switch g.Disposal[i] {
		case gif.DisposalBackground:
			// Clear this frame's region back to transparent.
			draw.Draw(canvas, frame.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			if saved != nil {
				copy(canvas.Pix, saved.Pix)
			}
		}
		// DisposalNone (and unspecified 0): leave the frame in place.
	}
	return subsample(out, maxFrames), nil
}

// maxFrames caps how many frames an animation uploads. Set high enough that a
// normal emote (catJAM is 158 frames) keeps its full framerate; the cap only
// trims a pathologically long animation so it can't upload tens of megabytes.
const maxFrames = 300

// subsample thins an animation to at most max frames, spread evenly across the
// loop, folding the delays of dropped frames into the kept frame before them so
// the loop keeps its original total duration. Even spacing (rather than a fixed
// step) avoids halving a 100-frame emote just because it is one over the cap.
func subsample(d decoded, max int) decoded {
	n := len(d.frames)
	if n <= max {
		return d
	}
	out := decoded{cols: d.cols, w: d.w, h: d.h, frames: make([][]byte, 0, max), delays: make([]int, 0, max)}
	prev := 0
	for k := 0; k < max; k++ {
		idx := k * n / max
		next := (k + 1) * n / max
		sum := 0
		for j := prev; j < next; j++ {
			sum += d.delays[j]
		}
		out.frames = append(out.frames, d.frames[idx])
		out.delays = append(out.delays, sum)
		prev = next
	}
	return out
}

// cloneRGBA returns an independent copy of an RGBA image.
func cloneRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

// cellsWide picks a cell width that preserves the image's aspect ratio at a
// height of one row, clamped so one inline image never dominates a line.
func cellsWide(w, h int) int {
	cols := colsForRows(w, h, 1)
	if cols > 6 {
		cols = 6 // inline clamp; large placements (the emote card) are unclamped
	}
	return cols
}

// colsForRows is the aspect-correct cell width for an image displayed rows cells
// tall. A terminal cell is about twice as tall as it is wide, so a square image
// at one row is ~2 cells; at R rows it is ~2R cells.
func colsForRows(w, h, rows int) int {
	if h <= 0 {
		return 2 * rows
	}
	cols := int(float64(w)/float64(h)*2*float64(rows) + 0.5)
	if cols < 1 {
		cols = 1
	}
	return cols
}

// writeUpload emits the protocol to upload an image and, if it has more than
// one frame, its animation. The first frame is a virtual placement (U=1) sized
// cols x 1; extra frames are composed with a=f and the animation is set running
// with a=a. A terminal that ignores the animation controls simply shows the
// first frame, so this degrades to a static image.
func writeUpload(b *strings.Builder, e *entry) {
	// Root frame with the placement (continuations of an upload need only m=).
	writeChunked(b, fmt.Sprintf("a=T,U=1,i=%d,f=100,c=%d,r=%d,q=2", e.id, e.cols, e.placeRows()), "", e.frames[0])

	if len(e.frames) <= 1 {
		return
	}
	// Additional frames. X=1 is replace composition: without it, kitty
	// alpha-blends each frame onto the previous, so transparent emote frames
	// accumulate into a smear. z is this frame's display time. Continuation
	// chunks of a frame must repeat a=f — the one detail that, when missing,
	// left multi-chunk frames silently ignored and the emote static.
	for i := 1; i < len(e.frames); i++ {
		first := fmt.Sprintf("a=f,i=%d,f=100,X=1,z=%d,q=2", e.id, e.delays[i])
		cont := fmt.Sprintf("a=f,i=%d,q=2", e.id)
		writeChunked(b, first, cont, e.frames[i])
	}
	// Give the root frame its gap, then run with an infinite loop (v=1; v=0 is
	// "ignored", which plays once and stops on the last frame).
	fmt.Fprintf(b, "\x1b_Ga=a,i=%d,r=1,z=%d,q=2\x1b\\", e.id, e.delays[0])
	fmt.Fprintf(b, "\x1b_Ga=a,i=%d,s=3,v=1,q=2\x1b\\", e.id)
}

// writeChunked emits one graphics command, its base64 payload split into the
// protocol's 4096-byte chunks. The first chunk carries firstControl; each
// continuation carries contControl (empty means just the m= key, which is what
// a=t/a=T uploads use — but a=f frames must repeat their action on every chunk).
func writeChunked(b *strings.Builder, firstControl, contControl string, payload []byte) {
	data := base64.StdEncoding.EncodeToString(payload)
	const chunk = 4096
	first := true
	for len(data) > 0 {
		n := min(chunk, len(data))
		part := data[:n]
		data = data[n:]
		more := 0
		if len(data) > 0 {
			more = 1
		}
		switch {
		case first:
			fmt.Fprintf(b, "\x1b_G%s,m=%d;%s\x1b\\", firstControl, more, part)
			first = false
		case contControl != "":
			fmt.Fprintf(b, "\x1b_G%s,m=%d;%s\x1b\\", contControl, more, part)
		default:
			fmt.Fprintf(b, "\x1b_Gm=%d;%s\x1b\\", more, part)
		}
	}
}

// writePlaceholders emits one row of placeholder cells that display image id.
// The id is carried in the foreground color; each cell names its row and column
// via combining diacritics so the terminal reconstructs the image. A one-row
// image passes row 0; a tall placement emits this once per row.
func writePlaceholders(b *strings.Builder, id uint32, row, cols int) {
	// Foreground color carries the 24-bit image id.
	fmt.Fprintf(b, "\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)
	for col := 0; col < cols; col++ {
		b.WriteString(placeholder)
		b.WriteString(diacritic(row))
		b.WriteString(diacritic(col))
	}
	b.WriteString("\x1b[39m")
}

// diacritic returns the combining character kitty uses to encode a row or
// column index. The table is defined by the protocol; these first entries cover
// the small grids badges and emotes need.
func diacritic(n int) string {
	if n < 0 || n >= len(diacritics) {
		return ""
	}
	return string(diacritics[n])
}

// diacritics is the head of kitty's row/column encoding table (protocol order).
// Enough entries to cover a full-width large placement; inline images use only
// the first few.
var diacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
	0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484,
	0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1,
	0x05A8, 0x05A9, 0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611,
	0x0612, 0x0613, 0x0614, 0x0615, 0x0616, 0x0617, 0x0657, 0x0658,
}
