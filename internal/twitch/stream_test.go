package twitch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

func TestGetStreamLiveAndOffline(t *testing.T) {
	var live bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if live {
			w.Write([]byte(`{"data":[{"viewer_count":1234,"started_at":"2026-07-17T09:00:00Z","type":"live","game_name":"Chess"}]}`))
		} else {
			w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer srv.Close()

	old := helixBase
	helixBase = srv.URL
	defer func() { helixBase = old }()

	live = true
	info, err := GetStream(context.Background(), "cid", "tok", "x", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Live || info.Viewers != 1234 || info.Game != "Chess" {
		t.Errorf("live info = %+v", info)
	}
	if info.StartedAt.IsZero() {
		t.Error("started_at not parsed")
	}

	// Offline is a normal state: no error, Live false.
	live = false
	info, err = GetStream(context.Background(), "cid", "tok", "x", srv.Client())
	if err != nil {
		t.Fatalf("offline returned an error: %v", err)
	}
	if info.Live {
		t.Error("offline reported as live")
	}
}

// The poller keeps a running average across samples, not just the latest.
func TestPollerRunningAverage(t *testing.T) {
	counts := []int{100, 200, 300}
	var i int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := counts[i]
		if i < len(counts)-1 {
			i++
		}
		mu.Unlock()
		w.Write([]byte(`{"data":[{"viewer_count":` + strconv.Itoa(n) + `,"started_at":"2026-07-17T09:00:00Z","type":"live"}]}`))
	}))
	defer srv.Close()

	old := helixBase
	helixBase = srv.URL
	defer func() { helixBase = old }()

	p := &StreamPoller{ClientID: "cid", Token: "tok", Login: "x", HTTP: srv.Client()}
	p.poll(context.Background())
	p.poll(context.Background())
	p.poll(context.Background())

	s := p.Snapshot()
	if s.Viewers != 300 {
		t.Errorf("current viewers = %d, want the latest 300", s.Viewers)
	}
	if s.AvgViewers != 200 { // (100+200+300)/3
		t.Errorf("avg = %d, want 200", s.AvgViewers)
	}
	if s.Uptime <= 0 {
		t.Error("uptime should be positive for a live stream")
	}
}

// A failed poll keeps the previous snapshot rather than blanking it.
func TestPollerKeepsSnapshotOnFailure(t *testing.T) {
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"data":[{"viewer_count":50,"started_at":"2026-07-17T09:00:00Z","type":"live"}]}`))
	}))
	defer srv.Close()
	old := helixBase
	helixBase = srv.URL
	defer func() { helixBase = old }()

	p := &StreamPoller{ClientID: "cid", Token: "tok", Login: "x", HTTP: srv.Client()}
	p.poll(context.Background())
	fail = true
	p.poll(context.Background())

	if s := p.Snapshot(); s.Viewers != 50 || !s.Live {
		t.Errorf("failed poll blanked the snapshot: %+v", s)
	}
}
