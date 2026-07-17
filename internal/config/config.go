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
	// OverlayAddr is where the overlay server listens.
	OverlayAddr string `json:"overlay_addr"`
	// OverlayChannel is the channel whose chat the overlay shows. Empty means
	// the first channel opened this session.
	OverlayChannel string `json:"overlay_channel"`
	// Channels are the channels to reopen on the next launch.
	Channels []string `json:"channels"`
	// Anonymous records that the user chose to browse without logging in, so the
	// splash does not nag them every launch.
	Anonymous bool `json:"anonymous"`
}

// Default is the config used when none is saved yet.
func Default() Config {
	return Config{OverlayAddr: "127.0.0.1:7788"}
}

func path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "typetype", "config.json"), nil
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
