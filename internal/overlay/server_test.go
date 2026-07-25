package overlay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyxnix/crow/internal/chat"
	"github.com/Nyxnix/crow/internal/nowplaying"
)

func TestPublishReachesSubscriber(t *testing.T) {
	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	br := bufio.NewReader(resp.Body)
	// The handler greets immediately so EventSource fires onopen; reading it
	// also proves the client is registered before we publish.
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("no greeting: %v", err)
	}

	// Wait for the subscription to land rather than racing Publish against it.
	deadline := time.Now().Add(2 * time.Second)
	for s.Clients() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Clients() != 1 {
		t.Fatalf("Clients() = %d, want 1", s.Clients())
	}

	s.Publish(chat.Message{
		Author: "nyx",
		Text:   "hello Kappa",
		Color:  "#1E90FF",
		Emotes: []chat.Emote{{Name: "Kappa", ID: "25", URL: "https://cdn/25", Start: 6, End: 11}},
		Badges: []chat.Badge{
			{Name: "broadcaster", Version: "1", URL: "https://cdn/bc"},
			{Name: "unresolved", Version: "1"}, // no URL: must be dropped, not sent empty
		},
	})

	line := readData(t, br)
	var got wireMessage
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("bad JSON %q: %v", line, err)
	}
	if got.Author != "nyx" || got.Text != "hello Kappa" {
		t.Errorf("got %+v", got)
	}
	if len(got.Emotes) != 1 || got.Emotes[0].Start != 6 || got.Emotes[0].End != 11 {
		t.Errorf("emotes = %+v, want one at [6,11)", got.Emotes)
	}
	if len(got.Badges) != 1 || got.Badges[0].Name != "broadcaster" {
		t.Errorf("badges = %+v, want only the resolved one", got.Badges)
	}
}

// A browser source that stops reading must not wedge Publish, because Publish
// runs on the path that feeds the TUI too.
func TestPublishDoesNotBlockOnSlowClient(t *testing.T) {
	s := New()
	ch := s.subscribe()
	defer s.unsubscribe(ch)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < clientBuffer*3; i++ {
			s.Publish(chat.Message{Author: "nyx", Text: "flood"})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a client that never reads")
	}

	if len(ch) != clientBuffer {
		t.Errorf("buffered %d, want it to fill to %d and drop the rest", len(ch), clientBuffer)
	}
}

func TestOverlayPageServes(t *testing.T) {
	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	// The page is useless if the embed lost the script or the SSE wiring.
	for _, want := range []string{"EventSource", "/events", `id="chat"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("overlay page missing %q", want)
		}
	}
}

// readData returns the payload of the next SSE "data:" line, skipping comments.
func readData(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	for i := 0; i < 20; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if after, ok := strings.CutPrefix(line, "data: "); ok {
			return strings.TrimSpace(after)
		}
	}
	t.Fatal("no data frame")
	return ""
}

func TestSettingsSentOnConnectAndChange(t *testing.T) {
	s := New()
	s.SetOptions(map[string]int{"size": 20})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	readEvent := func() string {
		var ev strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended: %v", err)
			}
			if line == "\n" && ev.Len() > 0 {
				return ev.String()
			}
			if line != "\n" && !strings.HasPrefix(line, ":") {
				ev.WriteString(line)
			}
		}
	}

	// The current settings arrive before anything else.
	if ev := readEvent(); !strings.Contains(ev, "event: settings") || !strings.Contains(ev, `"size":20`) {
		t.Fatalf("first event = %q, want settings with size 20", ev)
	}

	// Unchanged options are not re-broadcast (the next frame read is the changed
	// value, not a duplicate of the old one).
	s.SetOptions(map[string]int{"size": 20})
	s.SetOptions(map[string]int{"size": 28})
	if ev := readEvent(); !strings.Contains(ev, `"size":28`) {
		t.Fatalf("after change got %q, want size 28", ev)
	}
}

// An alert publishes as its own SSE event with the alert wire fields; a
// non-alert message publishes no alert frame at all.
func TestAlertReachesSubscriber(t *testing.T) {
	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("no greeting: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.Clients() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	s.PublishAlert(chat.Message{Author: "nyx", Text: "plain chat"}) // not an alert: dropped
	s.PublishAlert(chat.Message{
		Author: "nyx", Color: "#1E90FF", Text: "great stream",
		Alert: chat.AlertResub, AlertText: "nyx subscribed for 6 months!",
	})

	// The first frame to arrive must be the real alert, proving the non-alert
	// was dropped rather than sent.
	line := readData(t, br)
	var got wireAlert
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("bad JSON %q: %v", line, err)
	}
	if got.Kind != "resub" || got.AlertText != "nyx subscribed for 6 months!" ||
		got.Author != "nyx" || got.Text != "great stream" {
		t.Errorf("alert = %+v", got)
	}
}

// The alerts page serves and wires up its own event listeners.
func TestAlertsPageServes(t *testing.T) {
	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/alerts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"EventSource", "/events", `"alert"`, "alert_settings"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("alerts page missing %q", want)
		}
	}
}

// Alert settings ride their own event name, arrive on connect after the chat
// settings, and re-broadcast only on change.
func TestAlertSettingsSentOnConnectAndChange(t *testing.T) {
	s := New()
	s.SetOptions(map[string]int{"size": 20})
	s.SetAlertOptions(map[string]int{"duration": 6})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	readEvent := func() string {
		var ev strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended: %v", err)
			}
			if line == "\n" && ev.Len() > 0 {
				return ev.String()
			}
			if line != "\n" && !strings.HasPrefix(line, ":") {
				ev.WriteString(line)
			}
		}
	}

	if ev := readEvent(); !strings.Contains(ev, "event: settings") {
		t.Fatalf("first event = %q, want chat settings", ev)
	}
	if ev := readEvent(); !strings.Contains(ev, "event: alert_settings") || !strings.Contains(ev, `"duration":6`) {
		t.Fatalf("second event = %q, want alert settings with duration 6", ev)
	}

	s.SetAlertOptions(map[string]int{"duration": 6}) // unchanged: no re-broadcast
	s.SetAlertOptions(map[string]int{"duration": 9})
	if ev := readEvent(); !strings.Contains(ev, "event: alert_settings") || !strings.Contains(ev, `"duration":9`) {
		t.Fatalf("after change got %q, want duration 9", ev)
	}
}

// The now-playing path has the two pieces that can quietly break on stream:
// local artwork has to become a URL the browser source can actually fetch, and
// an unchanged track must not re-broadcast every poll tick.
func TestNowPlayingArtAndDedupe(t *testing.T) {
	dir := t.TempDir()
	cover := filepath.Join(dir, "cover.png")
	if err := os.WriteFile(cover, []byte("\x89PNG\r\n\x1a\nfake"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	track := nowplaying.Track{
		Title: "Song", Artist: "Artist", Album: "Album",
		Art:      "file://" + cover,
		Position: 30 * time.Second, Duration: 3*time.Minute + 20*time.Second,
		Playing: true,
	}
	s.SetNowPlaying(track)

	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	// The current track is replayed on connect, so a reloaded browser source
	// isn't blank until the song changes.
	var got wireTrack
	if err := json.Unmarshal([]byte(readData(t, br)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "Song" || got.Artist != "Artist" || got.Album != "Album" {
		t.Errorf("got %+v", got)
	}
	if got.Position != 30 || got.Duration != 200 || !got.Playing {
		t.Errorf("times = %v/%v playing=%v, want 30/200 playing", got.Position, got.Duration, got.Playing)
	}
	if !strings.HasPrefix(got.Art, "/nowplaying/art?") {
		t.Fatalf("art = %q, want the local file served back over http", got.Art)
	}

	art, err := http.Get(srv.URL + got.Art)
	if err != nil {
		t.Fatal(err)
	}
	defer art.Body.Close()
	body, _ := io.ReadAll(art.Body)
	if art.StatusCode != 200 || !bytes.HasPrefix(body, []byte("\x89PNG")) {
		t.Errorf("art status %d, body %q", art.StatusCode, body)
	}

	// Same track again: nothing new on the wire.
	s.SetNowPlaying(track)
	s.SetNowPlaying(nowplaying.Track{Title: "Next"})
	var next wireTrack
	if err := json.Unmarshal([]byte(readData(t, br)), &next); err != nil {
		t.Fatal(err)
	}
	if next.Title != "Next" {
		t.Errorf("next frame = %q, want the changed track (unchanged ones must not re-broadcast)", next.Title)
	}
	if next.Art != "" {
		t.Errorf("art = %q, want none for a track without artwork", next.Art)
	}
}

// http(s) artwork (Spotify and friends) goes straight to the page.
func TestNowPlayingRemoteArt(t *testing.T) {
	s := New()
	s.SetNowPlaying(nowplaying.Track{Title: "Song", Art: "https://i.scdn.co/image/abc"})
	var got wireTrack
	if err := json.Unmarshal(s.track, &got); err != nil {
		t.Fatal(err)
	}
	if got.Art != "https://i.scdn.co/image/abc" {
		t.Errorf("art = %q, want the remote URL untouched", got.Art)
	}
	if s.artPath != "" {
		t.Errorf("artPath = %q, want nothing served locally", s.artPath)
	}
}
