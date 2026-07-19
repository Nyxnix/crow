package tui

// Tests for scaled (kitty text-sizing) layout. The load-bearing invariant is
// that no rendered row exceeds the terminal width in physical cells: one
// overflowing row autowraps, scrolls the screen, and desyncs every
// absolutely-positioned cell after it.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/Nyxnix/crow/internal/chat"
	"github.com/Nyxnix/crow/internal/kitty"
)

func servePNG(t *testing.T, w, h int, left, right color.RGBA) *httptest.Server {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := left
			if x >= w/2 {
				c = right
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)
	return srv
}

func loadedCache(t *testing.T, msgs []chat.Message, width, scale int, urls ...string) *kitty.Cache {
	t.Helper()
	gfx := kitty.New(nil)
	for i := 0; i < 200; i++ {
		layout(msgs, width, newStyles(), gfx, scale)
		all := true
		for _, u := range urls {
			if _, _, ok := gfx.RenderLarge(u, scale); !ok {
				all = false
			}
		}
		if all {
			return gfx
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("images never loaded")
	return nil
}

// physicalWidth measures the terminal cells a rendered row occupies: OSC 66
// payloads count scale cells per column, placeholder cells one each, escape
// sequences zero, and a CHA jump repositions the column.
func physicalWidth(t *testing.T, row string) int {
	t.Helper()
	col, max := 0, 0
	for i := 0; i < len(row); {
		if row[i] == 0x1b {
			rest := row[i:]
			switch {
			case strings.HasPrefix(rest, "\x1b]66;"):
				end := strings.Index(rest, "\x1b\\")
				if end < 0 {
					t.Fatalf("unterminated OSC 66 in %q", row)
				}
				payload := rest[len("\x1b]66;"):end]
				meta, text, _ := strings.Cut(payload, ";")
				// Layout may downgrade the scale on narrow widths; trust the
				// emitted s= over the requested scale.
				s := 1
				if i := strings.Index(meta, "s="); i >= 0 {
					s = int(meta[i+2] - '0')
				}
				col += runewidth.StringWidth(text) * s
			case strings.HasPrefix(rest, "\x1b["):
				j := 2
				for j < len(rest) && (rest[j] < 0x40 || rest[j] > 0x7e) {
					j++
				}
				seq := rest[:j+1]
				if seq[len(seq)-1] == 'G' { // CHA: absolute column, 1-based
					n := 0
					for _, c := range seq[2 : len(seq)-1] {
						n = n*10 + int(c-'0')
					}
					col = n - 1
				}
				i += len(seq)
				if col > max {
					max = col
				}
				continue
			default:
				i += 2
				continue
			}
			i += strings.Index(row[i:], "\x1b\\") + 2
		} else {
			r, size := utf8.DecodeRuneInString(row[i:])
			if r == '\U0010EEEE' {
				col++
			} else if !isCombining(r) {
				col += runewidth.RuneWidth(r)
			}
			i += size
		}
		if col > max {
			max = col
		}
	}
	return max
}

// isCombining reports the placeholder row/col diacritics, which occupy no cells.
func isCombining(r rune) bool { return runewidth.RuneWidth(r) == 0 }

func TestScaledRowsNeverExceedWidth(t *testing.T) {
	srv := servePNG(t, 64, 64, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255})
	longName := "CopegeBigfootIsFakeXXXXX" // 24 chars: prefix nearly fills a narrow line
	msgs := []chat.Message{
		{Author: "user", Text: "https://example.com/a/very/long/url/that/never/breaks/2077931234567890", At: time.Now()},
		{Author: longName, Text: "hello there this should wrap onto its own lines", At: time.Now()},
		{Author: "emoteonly", Text: "GAGAGA", At: time.Now(),
			Emotes: []chat.Emote{{Name: "GAGAGA", URL: srv.URL, Start: 0, End: 6}}},
		{Author: "mixed", Text: "word GAGAGA word GAGAGA end", At: time.Now(),
			Emotes: []chat.Emote{{Name: "GAGAGA", URL: srv.URL, Start: 5, End: 11}, {Name: "GAGAGA", URL: srv.URL, Start: 17, End: 23}}},
		{Author: "cjk", Text: "日本語のテキストは二セル幅で折り返しが難しいですね、はい", At: time.Now()},
	}
	for _, scale := range []int{1, 2, 3} {
		for _, width := range []int{40, 64, 100} {
			gfx := loadedCache(t, msgs, width, scale, srv.URL)
			for li, l := range layout(msgs, width, newStyles(), gfx, scale) {
				if w := physicalWidth(t, l.text); w > width {
					t.Errorf("scale=%d width=%d line %d: %d cells wide: %q", scale, width, li, w, l.text)
				}
				for fi, f := range l.fillers {
					if w := physicalWidth(t, f); w > width {
						t.Errorf("scale=%d width=%d line %d filler %d: %d cells wide", scale, width, li, fi, w)
					}
				}
			}
		}
	}
}

func TestEmoteOnlyMessageKeepsPrefixAtScale(t *testing.T) {
	srv := servePNG(t, 64, 64, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255})
	msgs := []chat.Message{{Author: "skullguy", Text: "GAGAGA", At: time.Now(),
		Emotes: []chat.Emote{{Name: "GAGAGA", URL: srv.URL, Start: 0, End: 6}}}}
	gfx := loadedCache(t, msgs, 100, 2, srv.URL)
	lines := layout(msgs, 100, newStyles(), gfx, 2)
	if len(lines) == 0 || !strings.Contains(lines[0].text, "skullguy") {
		t.Fatalf("first line missing author: %q", lines[0].text)
	}
	if !strings.Contains(lines[0].text, "\U0010EEEE") {
		t.Errorf("first line missing emote placeholders: %q", lines[0].text)
	}
	if len(lines[0].fillers) != 1 || !strings.Contains(lines[0].fillers[0], "\U0010EEEE") {
		t.Errorf("filler missing emote bottom row: %+v", lines[0].fillers)
	}
}
