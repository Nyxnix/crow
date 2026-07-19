package twitch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// The first poll primes the baseline silently; later polls fire OnFollow once
// per follower not seen before; a failed poll changes nothing.
func TestFollowPollerDiffsNewFollowers(t *testing.T) {
	var mu sync.Mutex
	body := `{"data":[{"user_id":"1","user_name":"Old"}]}`
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	old := helixBase
	helixBase = srv.URL
	defer func() { helixBase = old }()

	var got []string
	p := &FollowPoller{
		ClientID: "cid", Token: "tok", BroadcasterID: "42", HTTP: srv.Client(),
		OnFollow: func(id, name string) { got = append(got, name) },
	}

	// Priming poll: the existing follower is not "new".
	p.poll(context.Background())
	if len(got) != 0 {
		t.Fatalf("priming poll fired OnFollow: %v", got)
	}

	// A failed poll must not clear the baseline (or the next success would
	// re-announce every follower).
	mu.Lock()
	fail = true
	mu.Unlock()
	p.poll(context.Background())

	// Two new followers arrive, newest first as Helix returns them.
	mu.Lock()
	fail = false
	body = `{"data":[{"user_id":"3","user_name":"Newest"},{"user_id":"2","user_name":"Newer"},{"user_id":"1","user_name":"Old"}]}`
	mu.Unlock()
	p.poll(context.Background())
	if len(got) != 2 || got[0] != "Newer" || got[1] != "Newest" {
		t.Fatalf("new followers = %v, want [Newer Newest] in follow order", got)
	}

	// Steady state: nothing new, nothing fired.
	p.poll(context.Background())
	if len(got) != 2 {
		t.Errorf("steady-state poll re-announced: %v", got)
	}
}
