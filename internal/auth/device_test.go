package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTwitch stands in for id.twitch.tv so the polling logic can be driven
// without waiting on the real service or a browser approval.
type fakeTwitch struct {
	srv   *httptest.Server
	polls int32

	// pendingUntil is how many token polls return authorization_pending before
	// success, so a test can prove PollToken actually waits.
	pendingUntil int32
}

func newFakeTwitch() *fakeTwitch {
	f := &fakeTwitch{}
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(DeviceCode{
			DeviceCode:      "dev-123",
			UserCode:        "WXYZ",
			VerificationURI: "https://twitch.tv/activate",
			ExpiresIn:       1800,
			Interval:        0, // exercises the interval floor
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&f.polls, 1)
		if n <= f.pendingUntil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"status": 400, "message": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(Token{
			AccessToken:  "access-abc",
			RefreshToken: "refresh-def",
			Scope:        strings.Fields(Scopes),
			ExpiresIn:    3600,
		})
	})
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeTwitch) client() *Client {
	return &Client{ClientID: "cid", HTTP: f.srv.Client()}
}

func (f *fakeTwitch) close() { f.srv.Close() }

// withEndpoints points the package endpoints at the fake for one test.
func withEndpoints(t *testing.T, base string) {
	t.Helper()
	od, ot := deviceURLVar, tokenURLVar
	deviceURLVar = base + "/device"
	tokenURLVar = base + "/token"
	t.Cleanup(func() { deviceURLVar, tokenURLVar = od, ot })
}

func TestRequestDeviceCodeFloorsInterval(t *testing.T) {
	f := newFakeTwitch()
	defer f.close()
	withEndpoints(t, f.srv.URL)

	dc, err := f.client().RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "WXYZ" {
		t.Errorf("user code = %q", dc.UserCode)
	}
	// A zero interval from the server would busy-loop PollToken; it must floor.
	if dc.Interval < 1 {
		t.Errorf("interval = %d, want floored to >= 1", dc.Interval)
	}
}

func TestPollTokenWaitsThenSucceeds(t *testing.T) {
	f := newFakeTwitch()
	f.pendingUntil = 2 // two pending replies before the token
	defer f.close()
	withEndpoints(t, f.srv.URL)

	// A tiny interval keeps the test fast while still exercising the wait loop.
	dc := &DeviceCode{DeviceCode: "dev-123", ExpiresIn: 60, Interval: 0}
	c := f.client()
	// Interval 0 would sleep 0; set it just above so the loop actually cycles.
	dc.Interval = 1

	start := time.Now()
	tok, err := c.PollToken(context.Background(), dc)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access-abc" || tok.RefreshToken != "refresh-def" {
		t.Errorf("token = %+v", tok)
	}
	// Three polls: two pending, one success.
	if got := atomic.LoadInt32(&f.polls); got != 3 {
		t.Errorf("polled %d times, want 3", got)
	}
	// Expiry must be set from ExpiresIn, not left zero.
	if tok.Expiry.IsZero() || tok.Expiry.Before(start) {
		t.Errorf("expiry = %v, want a future time", tok.Expiry)
	}
}

func TestPollTokenRespectsExpiry(t *testing.T) {
	f := newFakeTwitch()
	f.pendingUntil = 1000 // never approves
	defer f.close()
	withEndpoints(t, f.srv.URL)

	// Already effectively expired: one poll interval passes the deadline.
	dc := &DeviceCode{DeviceCode: "dev-123", ExpiresIn: 1, Interval: 1}
	_, err := f.client().PollToken(context.Background(), dc)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("err = %v, want an expiry error", err)
	}
}

func TestPollTokenCancels(t *testing.T) {
	f := newFakeTwitch()
	f.pendingUntil = 1000
	defer f.close()
	withEndpoints(t, f.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dc := &DeviceCode{DeviceCode: "dev-123", ExpiresIn: 60, Interval: 1}
	if _, err := f.client().PollToken(ctx, dc); err == nil {
		t.Error("want a context error on a cancelled poll")
	}
}

func TestRefreshCarriesRefreshTokenForward(t *testing.T) {
	// Twitch may omit the refresh token when it doesn't rotate; the old one must
	// survive so the next refresh still works.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Token{AccessToken: "new-access", ExpiresIn: 3600})
	}))
	defer srv.Close()
	withEndpoints(t, srv.URL)

	c := &Client{ClientID: "cid", HTTP: srv.Client()}
	tok, err := c.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "new-access" {
		t.Errorf("access = %q", tok.AccessToken)
	}
	if tok.RefreshToken != "old-refresh" {
		t.Errorf("refresh = %q, want the old one carried forward", tok.RefreshToken)
	}
	if tok.Expiry.IsZero() {
		t.Error("expiry not set after refresh")
	}
}

func TestRefreshRejectsEmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{}) // no access_token
	}))
	defer srv.Close()
	withEndpoints(t, srv.URL)

	c := &Client{ClientID: "cid", HTTP: srv.Client()}
	if _, err := c.Refresh(context.Background(), "old"); err == nil {
		t.Error("want an error when refresh yields no access token")
	}
}
