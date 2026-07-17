package badge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyxnix/crow/internal/chat"
)

// fakeHelix serves the two badge endpoints from canned JSON.
func fakeHelix(t *testing.T) *httptest.Server {
	t.Helper()
	global := `{"data":[
		{"set_id":"broadcaster","versions":[{"id":"1","image_url_4x":"https://cdn/broadcaster"}]},
		{"set_id":"subscriber","versions":[
			{"id":"0","image_url_4x":"https://cdn/sub-global-0"},
			{"id":"1","image_url_4x":"https://cdn/sub-global-1"}]}
	]}`
	channel := `{"data":[
		{"set_id":"subscriber","versions":[{"id":"0","image_url_4x":"https://cdn/sub-CHANNEL-0"}]},
		{"set_id":"cdawg-badge","versions":[{"id":"1","image_url_4x":"https://cdn/cdawg"}]}
	]}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "global") {
			w.Write([]byte(global))
			return
		}
		w.Write([]byte(channel))
	}))
}

// loadAgainst points the registry's fetches at the fake by rewriting the base;
// the registry builds full URLs, so the test overrides them via the client's
// transport instead.
type rewriteTransport struct{ base string }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Send every Helix call to the fake, preserving path and query.
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(rt.base, "http://")
	return http.DefaultTransport.RoundTrip(req)
}

func newTestRegistry(t *testing.T, srv *httptest.Server) *Registry {
	r := New("cid", "tok")
	r.HTTP = &http.Client{Transport: rewriteTransport{base: srv.URL}}
	return r
}

func TestLoadMergesChannelOverGlobal(t *testing.T) {
	srv := fakeHelix(t)
	defer srv.Close()
	r := newTestRegistry(t, srv)

	if err := r.Load(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}

	// broadcaster comes only from global.
	if got := r.URL("broadcaster", "1"); got != "https://cdn/broadcaster" {
		t.Errorf("broadcaster = %q", got)
	}
	// subscriber/0 exists in both; the channel version must win.
	if got := r.URL("subscriber", "0"); got != "https://cdn/sub-CHANNEL-0" {
		t.Errorf("subscriber/0 = %q, want the channel override", got)
	}
	// subscriber/1 only in global, so it survives the merge.
	if got := r.URL("subscriber", "1"); got != "https://cdn/sub-global-1" {
		t.Errorf("subscriber/1 = %q, want the global one", got)
	}
	// channel-only custom badge.
	if got := r.URL("cdawg-badge", "1"); got != "https://cdn/cdawg" {
		t.Errorf("cdawg = %q", got)
	}
}

func TestResolveFillsKnownBadges(t *testing.T) {
	srv := fakeHelix(t)
	defer srv.Close()
	r := newTestRegistry(t, srv)
	r.Load(context.Background(), "123")

	m := chat.Message{Badges: []chat.Badge{
		{Name: "broadcaster", Version: "1"},
		{Name: "cdawg-badge", Version: "1"},
		{Name: "unknown", Version: "9"}, // not in the registry
	}}
	r.Resolve(&m)

	if m.Badges[0].URL != "https://cdn/broadcaster" {
		t.Errorf("broadcaster URL = %q", m.Badges[0].URL)
	}
	if m.Badges[1].URL != "https://cdn/cdawg" {
		t.Errorf("cdawg URL = %q", m.Badges[1].URL)
	}
	// An unknown badge keeps its empty URL, which the overlay reads as "skip".
	if m.Badges[2].URL != "" {
		t.Errorf("unknown badge got a URL %q, want empty", m.Badges[2].URL)
	}
}

// With no token the registry cannot call Helix and must stay a safe no-op, so
// anonymous sessions still render chat, just without badge images.
func TestAnonymousLoadIsNoop(t *testing.T) {
	r := New("cid", "") // no token
	if err := r.Load(context.Background(), "123"); err != nil {
		t.Errorf("anonymous Load = %v, want nil", err)
	}
	if r.Len() != 0 {
		t.Errorf("Len = %d, want 0", r.Len())
	}
	m := chat.Message{Badges: []chat.Badge{{Name: "broadcaster", Version: "1"}}}
	r.Resolve(&m)
	if m.Badges[0].URL != "" {
		t.Error("anonymous registry filled a URL")
	}
}

// A global-badges failure fails the load; a channel-badges failure must not,
// since a channel may legitimately have no custom badges.
func TestChannelFailureKeepsGlobal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "global") {
			w.Write([]byte(`{"data":[{"set_id":"vip","versions":[{"id":"1","image_url_4x":"https://cdn/vip"}]}]}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // channel badges fail
	}))
	defer srv.Close()
	r := newTestRegistry(t, srv)

	if err := r.Load(context.Background(), "123"); err != nil {
		t.Fatalf("load failed on a channel error that should be tolerated: %v", err)
	}
	if got := r.URL("vip", "1"); got != "https://cdn/vip" {
		t.Errorf("global vip badge lost when channel badges failed: %q", got)
	}
}
