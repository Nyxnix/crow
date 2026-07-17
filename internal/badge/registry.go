// Package badge resolves Twitch chat badges (broadcaster, moderator, subscriber
// tiers, channel-specific badges) to their image URLs.
//
// Badge images come from Helix, which needs a user token, so resolution only
// works when logged in. Chat still renders without it; badges just stay
// image-less, which the overlay treats as "don't show this badge". This mirrors
// how Chatterino sources badges from Twitch's badge sets.
package badge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Nyxnix/typetype/internal/chat"
)

// Registry maps a badge's set and version to an image URL, for one channel plus
// the global set.
//
// The zero value is not usable; use New. An empty registry (no token, or load
// failed) is safe: Resolve is a no-op, so messages render without badge images.
type Registry struct {
	clientID string
	token    string

	mu    sync.RWMutex
	byKey map[string]string // "set_id/version" -> image URL

	HTTP *http.Client
}

func New(clientID, token string) *Registry {
	return &Registry{clientID: clientID, token: token, byKey: map[string]string{}}
}

func (r *Registry) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Len reports how many badge versions are loaded, for a status line.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byKey)
}

func key(setID, version string) string { return setID + "/" + version }

// Load fetches the global badge set and the channel's own badges and swaps them
// in. Channel badges are merged over global so a channel's custom subscriber
// badge wins over the generic one.
//
// With no token it does nothing and returns nil: anonymous sessions simply have
// no badge images, which is not an error.
func (r *Registry) Load(ctx context.Context, channelID string) error {
	if r.token == "" {
		return nil
	}

	global, err := r.fetch(ctx, "https://api.twitch.tv/helix/chat/badges/global")
	if err != nil {
		return fmt.Errorf("global badges: %w", err)
	}

	merged := make(map[string]string, len(global))
	for k, v := range global {
		merged[k] = v
	}

	// Channel badges are best-effort: a channel with none 200s with an empty
	// list, and a lookup failure should not cost the global badges.
	if channelID != "" {
		channel, err := r.fetch(ctx, "https://api.twitch.tv/helix/chat/badges?broadcaster_id="+url.QueryEscape(channelID))
		if err == nil {
			for k, v := range channel {
				merged[k] = v
			}
		}
	}

	r.mu.Lock()
	r.byKey = merged
	r.mu.Unlock()
	return nil
}

// Resolve fills in the URL of every badge on a message that the registry knows.
// Unknown badges keep their empty URL, which the overlay reads as "skip".
func (r *Registry) Resolve(m *chat.Message) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.byKey) == 0 {
		return
	}
	for i := range m.Badges {
		if u := r.byKey[key(m.Badges[i].Name, m.Badges[i].Version)]; u != "" {
			m.Badges[i].URL = u
		}
	}
}

// URL returns the image for one badge, for callers that hold a badge outside a
// message (the user card). Empty if unknown.
func (r *Registry) URL(setID, version string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byKey[key(setID, version)]
}

// helixBadges is the shape both badge endpoints return.
type helixBadges struct {
	Data []struct {
		SetID    string `json:"set_id"`
		Versions []struct {
			ID       string `json:"id"`
			ImageURL string `json:"image_url_4x"`
		} `json:"versions"`
	} `json:"data"`
}

func (r *Registry) fetch(ctx context.Context, endpoint string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Client-Id", r.clientID)
	req.Header.Set("Authorization", "Bearer "+r.token)

	resp, err := r.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}

	var body helixBadges
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, set := range body.Data {
		for _, v := range set.Versions {
			if v.ImageURL != "" {
				out[key(set.SetID, v.ID)] = v.ImageURL
			}
		}
	}
	return out, nil
}
