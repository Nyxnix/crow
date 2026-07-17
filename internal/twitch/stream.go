package twitch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// StreamInfo is a channel's current live status, from Helix Get Streams.
type StreamInfo struct {
	Live      bool
	Viewers   int
	StartedAt time.Time // stream start, for uptime; zero when offline
	Game      string
}

// GetStream fetches a channel's live status. An offline channel returns a
// zero-value StreamInfo (Live false) and a nil error, since offline is a normal
// state rather than a failure.
func GetStream(ctx context.Context, clientID, token, login string, hc *http.Client) (StreamInfo, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	u := helixBase + "/streams?user_login=" + url.QueryEscape(login)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return StreamInfo{}, err
	}
	req.Header.Set("Client-Id", clientID)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := hc.Do(req)
	if err != nil {
		return StreamInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StreamInfo{}, apiError(resp)
	}

	var body struct {
		Data []struct {
			ViewerCount int    `json:"viewer_count"`
			StartedAt   string `json:"started_at"`
			Type        string `json:"type"`
			GameName    string `json:"game_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return StreamInfo{}, err
	}
	if len(body.Data) == 0 {
		return StreamInfo{}, nil // offline
	}

	s := body.Data[0]
	info := StreamInfo{
		Live:    s.Type == "live",
		Viewers: s.ViewerCount,
		Game:    s.GameName,
	}
	if t, err := time.Parse(time.RFC3339, s.StartedAt); err == nil {
		info.StartedAt = t
	}
	return info, nil
}

// StreamStats is a snapshot of a channel's live status for display.
type StreamStats struct {
	Live       bool
	Viewers    int
	AvgViewers int           // running average over this session's samples
	Uptime     time.Duration // since the stream started; 0 when offline
	Game       string
}

// StreamPoller periodically fetches a channel's live status and keeps a running
// average of the viewer count over the samples it has taken this session
// (Twitch exposes no historical average). It is safe for concurrent use.
type StreamPoller struct {
	ClientID string
	Token    string
	Login    string
	Interval time.Duration
	HTTP     *http.Client

	// OnUpdate, if set, is called after each successful poll so the UI can
	// redraw with the new numbers.
	OnUpdate func()

	mu          sync.Mutex
	info        StreamInfo
	viewerSum   int64
	viewerCount int64
}

// Run polls until ctx is cancelled: once immediately, then every Interval.
func (p *StreamPoller) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 60 * time.Second
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

func (p *StreamPoller) poll(ctx context.Context) {
	info, err := GetStream(ctx, p.ClientID, p.Token, p.Login, p.HTTP)
	if err != nil {
		return // keep the last snapshot; a transient failure shouldn't blank the stats
	}
	p.mu.Lock()
	p.info = info
	if info.Live {
		p.viewerSum += int64(info.Viewers)
		p.viewerCount++
	}
	p.mu.Unlock()

	if p.OnUpdate != nil {
		p.OnUpdate()
	}
}

// Snapshot returns the current stats, computing uptime and the running average.
func (p *StreamPoller) Snapshot() StreamStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	s := StreamStats{
		Live:    p.info.Live,
		Viewers: p.info.Viewers,
		Game:    p.info.Game,
	}
	if p.info.Live && !p.info.StartedAt.IsZero() {
		s.Uptime = time.Since(p.info.StartedAt)
	}
	if p.viewerCount > 0 {
		s.AvgViewers = int(p.viewerSum / p.viewerCount)
	}
	return s
}
