// Package config persists app settings (overlay options, the last channels
// opened) to a JSON file under the OS config dir, next to the auth token.
package config

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	// Anonymous records that the user chose to browse without logging in, so the
	// splash does not nag them every launch.
	Anonymous bool `json:"anonymous"`
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

// Default is the config used when none is saved yet.
func Default() Config {
	return Config{
		OverlayEnabled: true,
		OverlayAddr:    "127.0.0.1:7788",
		Overlay: OverlayOptions{
			Align:   "bottom",
			Size:    20,
			Stroke:  2,
			Max:     50,
			Animate: true,
			Badges:  true,
		},
	}
}

// OverlayURL is the browser-source URL for OBS, encoding only the options that
// differ from the overlay's own defaults so a stock setup stays a clean "/".
func (c Config) OverlayURL() string {
	o := c.Overlay
	q := url.Values{}
	if !o.Animate {
		q.Set("animate", "0")
	}
	if !o.Badges {
		q.Set("badges", "0")
	}
	if o.HideCommands {
		q.Set("hide_commands", "1")
	}
	if o.Align == "top" {
		q.Set("align", "top")
	}
	if o.Font != 0 {
		q.Set("font", strconv.Itoa(o.Font))
	}
	if o.Size != 20 {
		q.Set("size", strconv.Itoa(o.Size))
	}
	if o.Stroke != 2 {
		q.Set("stroke", strconv.Itoa(o.Stroke))
	}
	if o.Fade != 0 {
		q.Set("fade", strconv.Itoa(o.Fade))
	}
	if o.Max != 50 {
		q.Set("max", strconv.Itoa(o.Max))
	}
	if o.Bots != "" {
		q.Set("bots", o.Bots)
	}
	u := "http://" + c.OverlayAddr + "/"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
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
