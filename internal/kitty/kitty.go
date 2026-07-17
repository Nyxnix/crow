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
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	_ "image/gif"
	"image/png"

	_ "golang.org/x/image/webp" // decode 7TV/BTTV webp emotes
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

// entry is one image's state in the cache.
type entry struct {
	id          uint32
	cols        int    // display width in cells (height is one row)
	png         []byte // normalized PNG bytes, ready to transmit
	ready       bool   // fetched and decoded
	failed      bool   // fetch/decode failed; do not retry
	transmitted bool   // transmit escape has been emitted to the terminal
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
			writeTransmit(&b, e.id, e.cols, e.png)
			e.transmitted = true
		}
	}
	return b.String()
}

// load fetches and decodes one image off the render path.
func (c *Cache) load(url string, e *entry) {
	png, cols, err := c.fetch(url)

	c.mu.Lock()
	if err != nil {
		e.failed = true
	} else {
		e.png, e.cols, e.ready = png, cols, true
	}
	c.mu.Unlock()

	if err == nil && c.onReady != nil {
		c.onReady()
	}
}

// fetch downloads the image, decodes it (webp/png/gif), and re-encodes it as
// PNG for transmission, returning the cell width to display it at.
func (c *Cache) fetch(url string) (pngBytes []byte, cols int, err error) {
	resp, err := c.client().Get(url)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("%s", resp.Status)
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), cellsWide(img.Bounds().Dx(), img.Bounds().Dy()), nil
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

// writeTransmit emits the protocol sequence that uploads the PNG as a virtual
// placement (U=1) sized cols x 1, chunked at the protocol's 4096-byte limit.
func writeTransmit(b *strings.Builder, id uint32, cols int, png []byte) {
	data := base64.StdEncoding.EncodeToString(png)
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
			fmt.Fprintf(b, "\x1b_Ga=T,U=1,i=%d,f=100,c=%d,r=1,q=2,m=%d;%s\x1b\\", id, cols, more, part)
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
