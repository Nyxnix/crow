package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Google's OAuth device-flow endpoints ("TV and Limited Input devices" client
// type). Vars, not consts, so tests can point them at a stand-in server.
var (
	deviceURL = "https://oauth2.googleapis.com/device/code"
	tokenURL  = "https://oauth2.googleapis.com/token"
)

// Scope requested at login: the broad youtube scope, which covers sending live
// chat messages and is on Google's short list of scopes the device flow allows
// (youtube.force-ssl is not).
const Scope = "https://www.googleapis.com/auth/youtube"

// DeviceCode is what the user acts on: open VerificationURL, enter UserCode.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// Token is a Google user access token with its refresh token.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	Expiry       time.Time `json:"expiry"`
}

// Expired reports whether the access token is at or near expiry, with a minute
// of slack so a token never dies mid-request.
func (t Token) Expired() bool {
	return !t.Expiry.IsZero() && time.Now().After(t.Expiry.Add(-time.Minute))
}

// Auth runs the device flow for the user's own Google OAuth client. Unlike
// Twitch, Google issues no shared public client for third-party apps: the user
// creates a "TVs and Limited Input devices" OAuth client once and crow stores
// its ID and secret in the config. For that client type the "secret" is not
// confidential — Google's own docs say so — it just has to be present.
type Auth struct {
	ClientID     string
	ClientSecret string
	HTTP         *http.Client
}

func (a *Auth) http() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// RequestDeviceCode starts the flow. The returned code is shown to the user
// and then passed to PollToken.
func (a *Auth) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	form := url.Values{"client_id": {a.ClientID}, "scope": {Scope}}
	var dc DeviceCode
	if err := a.postForm(ctx, deviceURL, form, &dc); err != nil {
		return nil, err
	}
	if dc.Interval < 1 {
		dc.Interval = 5
	}
	return &dc, nil
}

// PollToken polls until the user approves, the code expires, or ctx is
// cancelled, honoring the server interval and slow_down.
func (a *Auth) PollToken(ctx context.Context, dc *DeviceCode) (*Token, error) {
	interval := time.Duration(dc.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("device code expired before authorization; run login again")
		}

		form := url.Values{
			"client_id":     {a.ClientID},
			"client_secret": {a.ClientSecret},
			"device_code":   {dc.DeviceCode},
			"grant_type":    {"urn:ietf:params:oauth:grant-type:device_code"},
		}
		var tok Token
		err := a.postForm(ctx, tokenURL, form, &tok)
		switch {
		case err == nil && tok.AccessToken != "":
			tok.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
			return &tok, nil
		case err != nil && strings.Contains(err.Error(), "authorization_pending"):
			// keep waiting
		case err != nil && strings.Contains(err.Error(), "slow_down"):
			interval += time.Second
		case err != nil:
			return nil, err
		}
	}
}

// Refresh exchanges the refresh token for a fresh access token. Google keeps
// the same refresh token, so it is carried forward.
func (a *Auth) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	form := url.Values{
		"client_id":     {a.ClientID},
		"client_secret": {a.ClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	var tok Token
	if err := a.postForm(ctx, tokenURL, form, &tok); err != nil {
		return nil, err
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("refresh returned no access token; the grant may have been revoked")
	}
	tok.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	tok.RefreshToken = refreshToken
	return &tok, nil
}

// Ensure returns a usable access token, refreshing and re-saving as needed. It
// returns (nil, nil) when the user never logged in, so unauthenticated is an
// ordinary state — the same contract as the Twitch auth package.
func (a *Auth) Ensure(ctx context.Context) (*Token, error) {
	tok, err := LoadToken()
	if err != nil || tok == nil {
		return nil, err
	}
	if !tok.Expired() {
		return tok, nil
	}
	fresh, err := a.Refresh(ctx, tok.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("youtube session expired, run `crow login youtube` again: %w", err)
	}
	if err := SaveToken(fresh); err != nil {
		return nil, err
	}
	return fresh, nil
}

// postForm posts a URL-encoded form and decodes the JSON response, turning
// Google's {"error": "..."} bodies into errors regardless of HTTP status
// (pending/slow_down come back as 4xx with an error field).
func (a *Auth) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var raw struct {
		Error     string `json:"error"`
		ErrorDesc string `json:"error_description"`
	}
	body, err := readBody(resp)
	if err != nil {
		return err
	}
	json.Unmarshal(body, &raw)
	if raw.Error != "" {
		return fmt.Errorf("%s (%s)", raw.Error, raw.ErrorDesc)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", endpoint, resp.Status)
	}
	return json.Unmarshal(body, out)
}

// --- token storage, next to the Twitch token in the crow config dir ---

func tokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "crow", "youtube_token.json"), nil
}

// TokenPath reports where the token is stored, for the login command.
func TokenPath() string {
	p, _ := tokenPath()
	return p
}

// SaveToken writes the token with owner-only permissions.
func SaveToken(t *Token) error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// LoadToken reads the stored token; (nil, nil) when none exists.
func LoadToken() (*Token, error) {
	p, err := tokenPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("stored youtube token is corrupt (%w); run login again", err)
	}
	return &t, nil
}

// ClearToken removes the stored token; missing is success.
func ClearToken() error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
