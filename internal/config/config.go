// Package config persists app settings (overlay options, the last channels
// opened) to a JSON file under the OS config dir, next to the auth token.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is the persisted app configuration.
type Config struct {
	// OverlayEnabled starts the overlay server. When false, no browser source is
	// served and the status bar hides the overlay count.
	OverlayEnabled bool `json:"overlay_enabled"`
	// OverlayAddr is where the overlay server listens.
	OverlayAddr string `json:"overlay_addr"`
	// OverlayChannel is the channel whose chat the overlay shows. Empty means
	// the first channel opened this session.
	OverlayChannel string `json:"overlay_channel"`
	// Overlay holds the visual options the browser source reads from its URL.
	Overlay OverlayOptions `json:"overlay"`
	// Channels are the channels to reopen on the next launch.
	Channels []string `json:"channels"`
	// ChatScale draws TUI chat lines at this multiple (1 = normal, 2 = double)
	// on terminals that support kitty's text sizing protocol.
	ChatScale int `json:"chat_scale"`
	// Anonymous records that the user chose to browse without logging in, so the
	// splash does not nag them every launch.
	Anonymous bool `json:"anonymous"`

	// Alerts holds the stream-alert options the alerts browser source reads.
	Alerts AlertOptions `json:"alerts"`

	// YouTubeCookies is the user's youtube.com Cookie header, the primary way
	// crow acts as their account on YouTube (send, moderate, card info) via the
	// same innertube endpoints the web player uses — no Google Cloud client or
	// API quota. Pasted once on the settings YouTube page. A credential; the
	// config file is written 0600.
	YouTubeCookies string `json:"youtube_cookies,omitempty"`

	// YouTubeClientID/Secret are the user's own Google OAuth client ("TVs and
	// Limited Input devices" type), the Data-API fallback used by `crow login
	// youtube` when no cookies are set. For this client type the secret is not
	// confidential, but the config file is 0600 anyway.
	YouTubeClientID     string `json:"youtube_client_id,omitempty"`
	YouTubeClientSecret string `json:"youtube_client_secret,omitempty"`
}

// OverlayOptions are the jChat-style overlay parameters. They map one-to-one to
// the query params overlay.html reads; OverlayURL turns them into that URL.
type OverlayOptions struct {
	Align        string `json:"align"`         // "bottom" or "top"
	Font         int    `json:"font"`          // index into the overlay's font list, 0-4
	Size         int    `json:"size"`          // font size, px
	Stroke       int    `json:"stroke"`        // text outline width, px
	Fade         int    `json:"fade"`          // seconds before a message fades; 0 = never
	Max          int    `json:"max"`           // max messages kept on screen
	Animate      bool   `json:"animate"`       // slide-in animation
	Badges       bool   `json:"badges"`        // show badges
	HideCommands bool   `json:"hide_commands"` // hide messages starting with "!"
	Bots         string `json:"bots"`          // comma-separated logins to hide
}

// AlertOptions are the stream-alert parameters, pushed to the alerts overlay
// page over SSE and used by the TUI to decide which alerts to surface.
type AlertOptions struct {
	Enabled     bool `json:"enabled"`
	Follows     bool `json:"follows"`
	Subs        bool `json:"subs"` // subs + resubs
	GiftSubs    bool `json:"gift_subs"`
	Bits        bool `json:"bits"`
	Members     bool `json:"members"` // YouTube new member + milestone
	GiftMembers bool `json:"gift_members"`
	Superchats  bool `json:"superchats"`
	Duration    int  `json:"duration"` // seconds each alert stays on screen
}

// Default is the config used when none is saved yet.
func Default() Config {
	return Config{
		OverlayEnabled: true,
		OverlayAddr:    "127.0.0.1:7788",
		ChatScale:      1,
		Overlay: OverlayOptions{
			Align:   "bottom",
			Size:    20,
			Stroke:  2,
			Max:     50,
			Animate: true,
			Badges:  true,
		},
		Alerts: AlertOptions{
			Enabled:     true,
			Follows:     true,
			Subs:        true,
			GiftSubs:    true,
			Bits:        true,
			Members:     true,
			GiftMembers: true,
			Superchats:  true,
			Duration:    6,
		},
	}
}

// OverlayURL is the browser-source URL for OBS. Always a bare "/" — the server
// pushes the display options to the page over SSE, so nothing rides the URL.
func (c Config) OverlayURL() string {
	return "http://" + c.OverlayAddr + "/"
}

// AlertsURL is the alerts browser-source URL for OBS, a separate page so alert
// popups can be positioned independently of the chat overlay.
func (c Config) AlertsURL() string {
	return "http://" + c.OverlayAddr + "/alerts"
}

// NowPlayingURL is the now-playing browser-source URL for OBS, showing the
// track a local media player is on.
func (c Config) NowPlayingURL() string {
	return "http://" + c.OverlayAddr + "/nowplaying"
}

func path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "crow", "config.json"), nil
}

// Load reads the saved config, falling back to defaults for a missing file or
// any field left unset.
func Load() Config {
	c := Default()
	p, err := path()
	if err != nil {
		return c
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return c
	}
	// Decode over the defaults so absent fields keep their default.
	json.Unmarshal(data, &c)
	if c.OverlayAddr == "" {
		c.OverlayAddr = Default().OverlayAddr
	}
	if c.ChatScale < 1 {
		c.ChatScale = 1 // configs saved before this field existed decode as 0
	}
	if c.Alerts.Duration < 1 {
		c.Alerts.Duration = Default().Alerts.Duration
	}
	return c
}

// Save writes the config, creating the directory as needed.
func Save(c Config) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Path reports where the config is stored, for display in settings.
func Path() string {
	p, _ := path()
	return p
}
