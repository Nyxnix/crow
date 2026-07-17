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
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp" // decode static 7TV/BTTV webp emotes
)

// placeholder is U+10EEEE, the code point a cell holds to display part of an
// image identified by the cell's foreground color.
const placeholder = "\U0010EEEE"

// Supported reports whether the current terminal speaks the kitty graphics
// protocol. TYPETYPE_GRAPHICS=0/1 forces it off/on for terminals we don't
// recognise or for debugging.
func Supported() bool {
	switch os.Getenv("TYPETYPE_GRAPHICS") {
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

// entry is one image's state in the cache. An animated emote has more than one
// frame; a static one has exactly one.
type entry struct {
	id          uint32
	cols        int      // display width in cells (height is one row)
	frames      [][]byte // PNG bytes per frame
	delays      []int    // per-frame display time in ms
	ready       bool     // fetched and decoded
	failed      bool     // fetch/decode failed; do not retry
	transmitted bool     // upload escape has been emitted to the terminal
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
	writePlaceholders(&b, id, cols)
	return b.String(), cols, true
}

// FlushUploads returns the upload sequences for every image that has become
// ready since the last call, marking them uploaded. Emit the result once at the
// start of a frame, before any placeholder cells. It is empty when nothing new
// has loaded.
func (c *Cache) FlushUploads() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, e := range c.byURL {
		if e.ready && !e.transmitted {
			writeUpload(&b, e)
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
		e.frames, e.delays, e.cols, e.ready = d.frames, d.delays, d.cols, true
	}
	c.mu.Unlock()

	if err == nil && c.onReady != nil {
		c.onReady()
	}
}

// decoded is the result of fetching an image: one PNG frame for a static image,
// several for an animated one, with per-frame delays and the display width.
type decoded struct {
	frames [][]byte
	delays []int
	cols   int
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
	return decoded{frames: [][]byte{buf.Bytes()}, delays: []int{0}, cols: cellsWide(img.Bounds().Dx(), img.Bounds().Dy())}, nil
}

// gifAlternative maps a WebP emote URL to its GIF sibling, or "" if not a WebP.
// It also steps the size down: an animated emote shows at about two cells, so
// the 4x source used for the overlay would upload many megabytes of frames for
// no visible gain — 2x is plenty.
func gifAlternative(url string) string {
	if !strings.HasSuffix(url, ".webp") {
		return ""
	}
	u := strings.TrimSuffix(url, ".webp") + ".gif"
	u = strings.Replace(u, "/4x.gif", "/2x.gif", 1)
	u = strings.Replace(u, "/3x.gif", "/2x.gif", 1)
	return u
}

// decodeGIF composites each GIF frame onto a full canvas (frames may be partial
// regions) and PNG-encodes it, returning per-frame delays in milliseconds.
func decodeGIF(raw []byte) (decoded, error) {
	g, err := gif.DecodeAll(bytes.NewReader(raw))
	if err != nil || len(g.Image) == 0 {
		return decoded{}, fmt.Errorf("gif: %v", err)
	}
	b := g.Image[0].Bounds()
	if len(g.Image) > 1 {
		// Later frames may extend the first; size to the logical screen.
		b = image.Rect(0, 0, g.Config.Width, g.Config.Height)
	}
	canvas := image.NewRGBA(b)

	out := decoded{cols: cellsWide(b.Dx(), b.Dy())}
	for i, frame := range g.Image {
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		var buf bytes.Buffer
		snap := image.NewRGBA(canvas.Bounds())
		copy(snap.Pix, canvas.Pix)
		if err := png.Encode(&buf, snap); err != nil {
			return decoded{}, err
		}
		out.frames = append(out.frames, buf.Bytes())
		ms := g.Delay[i] * 10 // GIF delays are hundredths of a second
		if ms <= 0 {
			ms = 100
		}
		out.delays = append(out.delays, ms)
	}
	return out, nil
}

// cellsWide picks a cell width that preserves the image's aspect ratio at a
// height of one row. A terminal cell is about twice as tall as it is wide, so a
// square image (badge) needs ~2 cells to look square; the result is clamped so
// one image never dominates a line.
func cellsWide(w, h int) int {
	if h <= 0 {
		return 2
	}
	cols := int(float64(w)/float64(h)*2 + 0.5)
	if cols < 1 {
		cols = 1
	}
	if cols > 6 {
		cols = 6
	}
	return cols
}

// writeUpload emits the protocol to upload an image and, if it has more than
// one frame, its animation. The first frame is a virtual placement (U=1) sized
// cols x 1; extra frames are composed with a=f and the animation is set running
// with a=a. A terminal that ignores the animation controls simply shows the
// first frame, so this degrades to a static image.
func writeUpload(b *strings.Builder, e *entry) {
	// Root frame with the placement.
	writeChunked(b, fmt.Sprintf("a=T,U=1,i=%d,f=100,c=%d,r=1,q=2", e.id, e.cols), e.frames[0])

	if len(e.frames) <= 1 {
		return
	}
	// Additional frames: z is this frame's display time in ms.
	for i := 1; i < len(e.frames); i++ {
		writeChunked(b, fmt.Sprintf("a=f,i=%d,f=100,z=%d,q=2", e.id, e.delays[i]), e.frames[i])
	}
	// Set the first frame's gap and start looping playback.
	fmt.Fprintf(b, "\x1b_Ga=a,i=%d,r=1,z=%d,q=2\x1b\\", e.id, e.delays[0])
	fmt.Fprintf(b, "\x1b_Ga=a,i=%d,s=3,v=0,q=2\x1b\\", e.id)
}

// writeChunked emits one graphics command with the given control keys, its
// base64 payload split into the protocol's 4096-byte chunks.
func writeChunked(b *strings.Builder, control string, payload []byte) {
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
		if first {
			fmt.Fprintf(b, "\x1b_G%s,m=%d;%s\x1b\\", control, more, part)
			first = false
		} else {
			fmt.Fprintf(b, "\x1b_Gm=%d;%s\x1b\\", more, part)
		}
	}
}

// writePlaceholders emits the row of placeholder cells that display image id.
// The id is carried in the foreground color; each cell names its column via a
// combining diacritic so the terminal reconstructs the image left to right.
func writePlaceholders(b *strings.Builder, id uint32, cols int) {
	// Foreground color carries the 24-bit image id.
	fmt.Fprintf(b, "\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)
	for col := 0; col < cols; col++ {
		b.WriteString(placeholder)
		b.WriteString(diacritic(0)) // row 0
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

var diacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
}
