package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// isolateConfig points os.UserConfigDir at a temp dir for one test, so the
// suite never reads or writes the developer's real token.
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	// UserConfigDir reads these in order depending on platform; set the ones
	// that matter on macOS and Linux.
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

func TestSaveLoadClearRoundTrip(t *testing.T) {
	isolateConfig(t)

	if got, err := Load(); err != nil || got != nil {
		t.Fatalf("Load with no token = %v, %v; want nil, nil", got, err)
	}

	st := &StoredToken{
		Token:  Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)},
		UserID: "42",
		Login:  "nyx",
	}
	if err := Save(st); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "a" || got.UserID != "42" || got.Login != "nyx" {
		t.Errorf("loaded %+v", got)
	}

	if err := Clear(); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(); got != nil {
		t.Error("token survived Clear")
	}
	// Clearing an already-absent token is not an error.
	if err := Clear(); err != nil {
		t.Errorf("second Clear = %v, want nil", err)
	}
}

// The token file holds a live credential and must never be readable by other
// users on the machine.
func TestSaveIsOwnerOnly(t *testing.T) {
	isolateConfig(t)
	if err := Save(&StoredToken{Token: Token{AccessToken: "secret"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %o, want 600", perm)
	}
}

func TestLoadRejectsCorruptToken(t *testing.T) {
	isolateConfig(t)
	if err := os.MkdirAll(dirOf(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("want an error on a corrupt token file")
	}
}

func TestExpired(t *testing.T) {
	if (Token{}).Expired() {
		t.Error("a token with no expiry must be treated as non-expiring, not expired")
	}
	if !(Token{Expiry: time.Now().Add(-time.Hour)}).Expired() {
		t.Error("a past-expiry token must be expired")
	}
	// Within the one-minute slack, a token is treated as already expired so it
	// gets refreshed before it dies mid-request.
	if !(Token{Expiry: time.Now().Add(30 * time.Second)}).Expired() {
		t.Error("a token expiring within the slack window must be refreshed")
	}
	if (Token{Expiry: time.Now().Add(time.Hour)}).Expired() {
		t.Error("a token an hour out must not be expired")
	}
}

// Ensure must refresh an expired stored token and persist the new one.
func TestEnsureRefreshesExpiredToken(t *testing.T) {
	isolateConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Token{AccessToken: "refreshed", RefreshToken: "r2", ExpiresIn: 3600})
	}))
	defer srv.Close()
	withEndpoints(t, srv.URL)

	Save(&StoredToken{
		Token:  Token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Hour)},
		UserID: "42", Login: "nyx",
	})

	c := &Client{ClientID: "cid", HTTP: srv.Client()}
	st, err := c.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.AccessToken != "refreshed" {
		t.Errorf("access = %q, want the refreshed token", st.AccessToken)
	}
	// Identity must be preserved across a refresh.
	if st.UserID != "42" || st.Login != "nyx" {
		t.Errorf("identity lost on refresh: %+v", st)
	}
	// And the refreshed token must be on disk for next time.
	reloaded, _ := Load()
	if reloaded.AccessToken != "refreshed" {
		t.Errorf("disk has %q, want the refreshed token persisted", reloaded.AccessToken)
	}
}

// A dead refresh token must clear the stored token so the app comes up
// unauthenticated rather than wedged on a token it can't use.
func TestEnsureClearsOnFailedRefresh(t *testing.T) {
	isolateConfig(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"message": "Invalid refresh token"})
	}))
	defer srv.Close()
	withEndpoints(t, srv.URL)

	Save(&StoredToken{
		Token:  Token{AccessToken: "old", RefreshToken: "dead", Expiry: time.Now().Add(-time.Hour)},
		UserID: "42",
	})

	c := &Client{ClientID: "cid", HTTP: srv.Client()}
	if _, err := c.Ensure(context.Background()); err == nil {
		t.Error("want an error when the refresh token is dead")
	}
	if got, _ := Load(); got != nil {
		t.Error("a dead token must be cleared from disk")
	}
}

func TestEnsureNoTokenReturnsNil(t *testing.T) {
	isolateConfig(t)
	c := &Client{ClientID: "cid"}
	st, err := c.Ensure(context.Background())
	if err != nil || st != nil {
		t.Errorf("Ensure with no token = %v, %v; want nil, nil", st, err)
	}
}

// dirOf is filepath.Dir, pulled out so the test reads clearly.
func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
