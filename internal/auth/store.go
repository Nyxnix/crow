package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StoredToken is what we persist: the token plus who it belongs to, so the app
// can show and use the login without a round-trip on every start.
type StoredToken struct {
	Token
	UserID string `json:"user_id"`
	Login  string `json:"login"`
}

// tokenPath returns the on-disk location, honoring the platform config dir so
// this behaves on Linux, macOS and Windows without special-casing.
func tokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "crow", "token.json"), nil
}

// Save writes the token with owner-only permissions. It holds a live
// credential, so it must never be group- or world-readable.
func Save(st *StoredToken) error {
	path, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file and rename so a crash mid-write can't leave a
	// truncated token that fails to parse on next start.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads the stored token. It returns (nil, nil) when no token exists yet,
// so "not logged in" is an ordinary state rather than an error to handle.
func Load() (*StoredToken, error) {
	path, err := tokenPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st StoredToken
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("stored token is corrupt (%w); run login again", err)
	}
	return &st, nil
}

// Clear removes the stored token, for logout. Missing is success.
func Clear() error {
	path, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Path reports where the token is stored, for the login command to tell the
// user where their credential now lives.
func Path() string {
	p, _ := tokenPath()
	return p
}

// Ensure returns a usable access token, refreshing and re-saving it if it has
// expired. It returns (nil, nil) when there is no stored token, so callers can
// treat unauthenticated as a normal state.
//
// This is the one entry point the app uses at startup: it hides whether the
// token on disk was still good or had to be refreshed.
func (c *Client) Ensure(ctx context.Context) (*StoredToken, error) {
	st, err := Load()
	if err != nil || st == nil {
		return nil, err
	}
	if !st.Expired() {
		return st, nil
	}

	tok, err := c.Refresh(ctx, st.RefreshToken)
	if err != nil {
		// The refresh token expires after 30 days idle, or the user may have
		// revoked access. Either way the stored token is now useless; drop it
		// so the app comes up unauthenticated rather than wedged.
		Clear()
		return nil, fmt.Errorf("session expired, run login again: %w", err)
	}
	st.Token = *tok
	if err := Save(st); err != nil {
		return nil, err
	}
	return st, nil
}
