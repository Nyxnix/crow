package twitch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPollStatus covers the GET-with-response shape; PredictionStatus is the
// same get() pattern with different field names.
func TestPollStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/polls" || r.URL.Query().Get("broadcaster_id") != "b1" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		w.Write([]byte(`{"data":[{"title":"Best?","status":"ACTIVE","duration":120,
			"started_at":"2026-07-19T12:00:00Z",
			"choices":[{"title":"yes","votes":7},{"title":"no","votes":3}]}]}`))
	}))
	t.Cleanup(srv.Close)
	old := helixBase
	helixBase = srv.URL
	t.Cleanup(func() { helixBase = old })

	h := &Helix{ClientID: "cid", Token: "tok", BroadcasterID: "b1"}
	p, err := h.PollStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "poll" || p.Title != "Best?" || p.Status != "ACTIVE" ||
		len(p.Choices) != 2 || p.Choices[0].Votes != 7 ||
		p.EndsAt.Format("15:04") != "12:02" {
		t.Errorf("poll = %+v", p)
	}
}

// helixRecorder stands in for Helix and captures the one request a method
// makes. The remaining new methods are the same do() one-liners as the
// already-tested Timeout shape, so these two cover the non-trivial bodies.
func helixRecorder(t *testing.T) (*Helix, *http.Request, *[]byte) {
	t.Helper()
	var req http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req = *r
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		body = buf
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	old := helixBase
	helixBase = srv.URL
	t.Cleanup(func() { helixBase = old })
	return &Helix{ClientID: "cid", Token: "tok", BroadcasterID: "b1", ModeratorID: "m1"}, &req, &body
}

func TestUpdateChatSettings(t *testing.T) {
	h, req, body := helixRecorder(t)
	err := h.UpdateChatSettings(t.Context(), map[string]any{"slow_mode": true, "slow_mode_wait_time": 60})
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPatch || req.URL.Path != "/chat/settings" {
		t.Errorf("%s %s, want PATCH /chat/settings", req.Method, req.URL.Path)
	}
	q := req.URL.Query()
	if q.Get("broadcaster_id") != "b1" || q.Get("moderator_id") != "m1" {
		t.Errorf("query = %v", q)
	}
	var got map[string]any
	json.Unmarshal(*body, &got)
	if got["slow_mode"] != true || got["slow_mode_wait_time"] != float64(60) {
		t.Errorf("body = %v", got)
	}
}

func TestCreatePoll(t *testing.T) {
	h, req, body := helixRecorder(t)
	if err := h.CreatePoll(t.Context(), "Best?", []string{"yes", "no"}, 120, 500); err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost || req.URL.Path != "/polls" {
		t.Errorf("%s %s, want POST /polls", req.Method, req.URL.Path)
	}
	var got struct {
		BroadcasterID string `json:"broadcaster_id"`
		Title         string `json:"title"`
		Choices       []struct {
			Title string `json:"title"`
		} `json:"choices"`
		Duration  int  `json:"duration"`
		CPEnabled bool `json:"channel_points_voting_enabled"`
		CPPer     int  `json:"channel_points_per_vote"`
	}
	json.Unmarshal(*body, &got)
	if got.BroadcasterID != "b1" || got.Title != "Best?" || got.Duration != 120 ||
		len(got.Choices) != 2 || got.Choices[0].Title != "yes" || got.Choices[1].Title != "no" ||
		!got.CPEnabled || got.CPPer != 500 {
		t.Errorf("body = %+v", got)
	}
}
