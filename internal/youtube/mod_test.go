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

// TestModAndChannelInfo drives timeout/ban/unban/delete and the card info
// fetch against a stand-in Data API.
func TestModAndChannelInfo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := SaveToken(&Token{AccessToken: "at", RefreshToken: "rt", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	var banBody string
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/videos":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"liveStreamingDetails": map[string]string{"activeLiveChatId": "chat42"}}},
			})
		case r.URL.Path == "/liveChat/bans" && r.Method == http.MethodPost:
			b, _ := json.Marshal(json.RawMessage(mustRead(t, r)))
			banBody = string(b)
			json.NewEncoder(w).Encode(map[string]string{"id": "ban1"})
		case r.URL.Path == "/liveChat/bans" && r.Method == http.MethodDelete:
			deleted = append(deleted, "ban:"+r.URL.Query().Get("id"))
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/liveChat/messages" && r.Method == http.MethodDelete:
			deleted = append(deleted, "msg:"+r.URL.Query().Get("id"))
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/channels":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{
					"snippet": map[string]any{
						"title": "Some Channel", "publishedAt": "2019-05-01T00:00:00Z",
						"thumbnails": map[string]any{"medium": map[string]string{"url": "https://yt.example/pfp.png"}},
					},
					"statistics": map[string]any{"subscriberCount": "12345", "hiddenSubscriberCount": false},
				}},
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	oldAPI := apiBase
	apiBase = srv.URL
	defer func() { apiBase = oldAPI }()

	ctx := context.Background()
	a := &Auth{ClientID: "cid", ClientSecret: "cs"}
	m := &Mod{Sender: &Sender{Video: "dQw4w9WgXcQ", Auth: a}}

	if err := m.Timeout(ctx, "UCbad", 600, ""); err != nil {
		t.Fatalf("timeout: %v", err)
	}
	if !strings.Contains(banBody, `"temporary"`) || !strings.Contains(banBody, `"banDurationSeconds":600`) {
		t.Errorf("timeout body = %s", banBody)
	}
	if err := m.Ban(ctx, "UCbad", ""); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if !strings.Contains(banBody, `"permanent"`) {
		t.Errorf("ban body = %s", banBody)
	}
	if err := m.Unban(ctx, "UCbad"); err != nil {
		t.Fatalf("unban: %v", err)
	}
	if err := m.Unban(ctx, "UCnever"); err == nil {
		t.Error("unban of a ban not placed this session should error")
	}
	if err := m.DeleteMessage(ctx, "msg9"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := []string{"ban:ban1", "msg:msg9"}
	if len(deleted) != 2 || deleted[0] != want[0] || deleted[1] != want[1] {
		t.Errorf("deleted = %v", deleted)
	}

	ci, err := a.ChannelInfo(ctx, "UCabc")
	if err != nil {
		t.Fatalf("channel info: %v", err)
	}
	if ci.Title != "Some Channel" || ci.Subs != 12345 ||
		ci.AvatarURL != "https://yt.example/pfp.png" || ci.Created.Year() != 2019 {
		t.Errorf("info = %+v", ci)
	}
}

func mustRead(t *testing.T, r *http.Request) []byte {
	t.Helper()
	b := make([]byte, r.ContentLength)
	r.Body.Read(b)
	return b
}
