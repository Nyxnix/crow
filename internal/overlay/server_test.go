package overlay

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyxnix/typetype/internal/chat"
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
