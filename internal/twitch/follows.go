package twitch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// FollowPoller watches a channel's followers via Helix and reports new ones.
// Twitch offers no follow event over IRC; polling Get Channel Followers (which
// needs the moderator:read:followers scope) is the dependency-free alternative
// to an EventSub websocket.
type FollowPoller struct {
	ClientID      string
	Token         string
	BroadcasterID string
	Interval      time.Duration // default 10s
	HTTP          *http.Client

	// OnFollow is called once per new follower, after the first poll primes the
	// baseline (existing followers at startup are not "new").
	OnFollow func(userID, userName string)

	seen   map[string]bool // first-page follower IDs from the previous poll
	primed bool
}

// Run polls until ctx is cancelled: once immediately, then every Interval.
func (p *FollowPoller) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	p.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.poll(ctx)
		}
	}
}

func (p *FollowPoller) poll(ctx context.Context) {
	hc := p.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	u := helixBase + "/channels/followers?first=100&broadcaster_id=" + url.QueryEscape(p.BroadcasterID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return
	}
	req.Header.Set("Client-Id", p.ClientID)
	req.Header.Set("Authorization", "Bearer "+p.Token)

	resp, err := hc.Do(req)
	if err != nil {
		return // transient failure must not fake or drop the baseline
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var body struct {
		Data []struct {
			UserID   string `json:"user_id"`
			UserName string `json:"user_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return
	}

	next := make(map[string]bool, len(body.Data))
	for _, f := range body.Data {
		next[f.UserID] = true
	}
	if p.primed && p.OnFollow != nil {
		// Newest first: walk backwards so multiple new follows fire in order.
		// ponytail: one page; >100 follows inside one interval loses the excess,
		// paginate if that ever becomes a real channel's problem.
		for i := len(body.Data) - 1; i >= 0; i-- {
			if f := body.Data[i]; !p.seen[f.UserID] {
				p.OnFollow(f.UserID, f.UserName)
			}
		}
	}
	p.seen, p.primed = next, true
}
