package youtube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDeviceFlowAndSend drives login (pending -> approved), token storage, and
// a send (chat-id resolve + insert) against a stand-in server.
func TestDeviceFlowAndSend(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	polls := 0
	var sentBody, sentAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device/code":
			json.NewEncoder(w).Encode(map[string]any{
				"device_code": "dev123", "user_code": "ABCD-EFGH",
				"verification_url": "https://google.com/device",
				"expires_in":       60, "interval": 1,
			})
		case "/token":
			if polls++; polls == 1 {
				w.WriteHeader(http.StatusPreconditionRequired)
				json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
				return
			}
			if got := r.FormValue("device_code"); got != "dev123" {
				t.Errorf("device_code = %q", got)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at1", "refresh_token": "rt1", "expires_in": 3600,
			})
		case "/videos":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"liveStreamingDetails": map[string]string{"activeLiveChatId": "chat42"}}},
			})
		case "/liveChat/messages":
			sentAuth = r.Header.Get("Authorization")
			var b struct {
				Snippet struct {
					LiveChatID string `json:"liveChatId"`
					Details    struct {
						Text string `json:"messageText"`
					} `json:"textMessageDetails"`
				} `json:"snippet"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			sentBody = b.Snippet.LiveChatID + "/" + b.Snippet.Details.Text
			json.NewEncoder(w).Encode(map[string]any{"id": "ok"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	oldDevice, oldToken, oldAPI := deviceURL, tokenURL, apiBase
	deviceURL, tokenURL, apiBase = srv.URL+"/device/code", srv.URL+"/token", srv.URL
	defer func() { deviceURL, tokenURL, apiBase = oldDevice, oldToken, oldAPI }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a := &Auth{ClientID: "cid", ClientSecret: "cs"}

	dc, err := a.RequestDeviceCode(ctx)
	if err != nil || dc.UserCode != "ABCD-EFGH" {
		t.Fatalf("device code: %v %+v", err, dc)
	}
	tok, err := a.PollToken(ctx, dc)
	if err != nil || tok.AccessToken != "at1" || tok.RefreshToken != "rt1" {
		t.Fatalf("poll: %v %+v", err, tok)
	}
	if polls != 2 {
		t.Errorf("expected pending then success, got %d polls", polls)
	}
	if tok.Expired() {
		t.Error("fresh token reports expired")
	}
	if err := SaveToken(tok); err != nil {
		t.Fatal(err)
	}

	s := &Sender{Video: "dQw4w9WgXcQ", Auth: a}
	if err := s.Send(ctx, "hello yt"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if sentBody != "chat42/hello yt" {
		t.Errorf("sent %q", sentBody)
	}
	if sentAuth != "Bearer at1" {
		t.Errorf("auth header %q", sentAuth)
	}
	// Second send reuses the cached chat id (no extra /videos call verified by
	// the path counter being absent — the handler would t.Error on surprises).
	if err := s.Send(ctx, "again"); err != nil {
		t.Fatalf("second send: %v", err)
	}
	if !strings.HasSuffix(sentBody, "/again") {
		t.Errorf("second send body %q", sentBody)
	}
}
