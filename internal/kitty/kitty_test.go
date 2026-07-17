package kitty

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pngServer serves a solid image of the given pixel size.
func pngServer(t *testing.T, w, h int) *httptest.Server {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(body)
	}))
}

// waitReady polls Render until the image has loaded or the deadline passes.
func waitReady(t *testing.T, c *Cache, url string) (string, int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s, cols, ok := c.Render(url); ok {
			return s, cols
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("image never became ready")
	return "", 0
}

func TestRenderLoadsAndEmitsGraphics(t *testing.T) {
	srv := pngServer(t, 72, 72) // square badge
	defer srv.Close()

	var readyCalls atomic.Int32
	c := New(func() { readyCalls.Add(1) })
	c.HTTP = srv.Client()

	// First call kicks off the async load and reports not-ready.
	if s, _, ok := c.Render(srv.URL); ok || s != "" {
		t.Fatalf("first Render = (%q, ok=%v), want empty/not-ready", s, ok)
	}

	s, cols := waitReady(t, c, srv.URL)
	if readyCalls.Load() == 0 {
		t.Error("onReady was never called")
	}
	// A square image is ~2 cells wide at one row tall.
	if cols != 2 {
		t.Errorf("cols = %d, want 2 for a square image", cols)
	}
	// Render emits only placeholder cells, never the upload.
	if strings.Contains(s, "\x1b_Ga=T") {
		t.Error("Render leaked the upload sequence; it belongs in FlushUploads")
	}
	if !strings.Contains(s, placeholder) {
		t.Error("render missing the placeholder character")
	}
	// The foreground color must carry the image id (1) in its blue channel.
	if !strings.Contains(s, "\x1b[38;2;0;0;1m") {
		t.Errorf("render missing the id-carrying color:\n%q", s)
	}
}

// Uploads are flushed once: the first flush after an image loads carries the
// data, later flushes do not, so a frame never re-uploads a badge.
func TestFlushUploadsOnce(t *testing.T) {
	srv := pngServer(t, 72, 72)
	defer srv.Close()
	c := New(nil)
	c.HTTP = srv.Client()

	// Before load, nothing to upload.
	if got := c.FlushUploads(); got != "" {
		t.Errorf("pre-load flush = %q, want empty", got)
	}

	waitReady(t, c, srv.URL)

	first := c.FlushUploads()
	if !strings.Contains(first, "\x1b_Ga=T") {
		t.Error("first flush after load should carry the upload")
	}
	if second := c.FlushUploads(); second != "" {
		t.Errorf("second flush = %q, want empty (already uploaded)", second)
	}
}

// A wide image gets more cells than a square one, preserving aspect.
func TestCellsWideByAspect(t *testing.T) {
	if got := cellsWide(72, 72); got != 2 {
		t.Errorf("square -> %d cells, want 2", got)
	}
	if got := cellsWide(144, 72); got != 4 {
		t.Errorf("2:1 -> %d cells, want 4", got)
	}
	// Clamped so one image can't dominate a line.
	if got := cellsWide(1000, 72); got != 6 {
		t.Errorf("very wide -> %d cells, want clamp 6", got)
	}
	if got := cellsWide(10, 0); got != 2 {
		t.Errorf("zero height -> %d, want the safe default 2", got)
	}
}

// The bug that cost the most: a frame split across chunks must repeat its a=f
// action on every continuation chunk, or the terminal silently drops the frame
// and the emote never animates.
func TestWriteChunkedRepeatsContinuationControl(t *testing.T) {
	var b strings.Builder
	big := make([]byte, 7000) // > 4096 base64-chunk, so it splits
	writeChunked(&b, "a=f,i=5,z=40", "a=f,i=5", big)
	out := b.String()

	// Two chunks: first with the full control, the continuation repeating a=f.
	if n := strings.Count(out, "\x1b_Ga=f,i=5,z=40,m=1;"); n != 1 {
		t.Errorf("first chunk = %d, want one with the full control", n)
	}
	if !strings.Contains(out, "\x1b_Ga=f,i=5,m=0;") {
		t.Errorf("continuation chunk did not repeat a=f:\n%q", out)
	}
	if strings.Contains(out, "\x1b_Gm=") {
		t.Error("a=f continuation fell back to a bare m= chunk, which the terminal drops")
	}
}

// An upload continuation (a=T/a=t) needs only m=, not the action repeated.
func TestWriteChunkedUploadContinuation(t *testing.T) {
	var b strings.Builder
	writeChunked(&b, "a=T,i=1", "", make([]byte, 7000))
	if !strings.Contains(b.String(), "\x1b_Gm=0;") {
		t.Error("upload continuation should be a bare m= chunk")
	}
}

func TestSubsampleKeepsDurationAndCap(t *testing.T) {
	d := decoded{cols: 2}
	for i := 0; i < 100; i++ {
		d.frames = append(d.frames, []byte{byte(i)})
		d.delays = append(d.delays, 40)
	}
	out := subsample(d, 24)
	if len(out.frames) != 24 {
		t.Errorf("got %d frames, want 24", len(out.frames))
	}
	total := 0
	for _, ms := range out.delays {
		total += ms
	}
	if total != 100*40 {
		t.Errorf("total duration = %dms, want the original %dms preserved", total, 100*40)
	}
	// Under the cap, frames are untouched.
	small := decoded{frames: make([][]byte, 10), delays: make([]int, 10)}
	if got := subsample(small, 24); len(got.frames) != 10 {
		t.Errorf("under-cap animation changed from 10 to %d", len(got.frames))
	}
}

func TestAnimatedURL(t *testing.T) {
	got := AnimatedURL("https://cdn.7tv.app/emote/ABC/4x.webp")
	if got != "https://cdn.7tv.app/emote/ABC/1x.gif" {
		t.Errorf("got %q, want the 1x gif", got)
	}
	// A non-webp URL is returned unchanged.
	if got := AnimatedURL("https://cdn/badge.png"); got != "https://cdn/badge.png" {
		t.Errorf("non-webp url changed: %q", got)
	}
}

func TestFailedFetchStaysNotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(nil)
	c.HTTP = srv.Client()

	c.Render(srv.URL) // kick off the load
	time.Sleep(300 * time.Millisecond)
	if _, _, ok := c.Render(srv.URL); ok {
		t.Error("a failed fetch reported ready")
	}
}
