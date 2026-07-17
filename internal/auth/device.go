// Package auth implements Twitch's OAuth device code flow and token storage.
//
// Device code flow is used because TypeType is a public client: it ships to
// anyone, so it cannot hold a client secret. The user authorizes on Twitch's
// own page and TypeType only ever sees the resulting user token.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Twitch's OAuth endpoints. Vars, not consts, so tests can point them at a
// stand-in server; nothing else reassigns them.
var (
	deviceURLVar = "https://id.twitch.tv/oauth2/device"
	tokenURLVar  = "https://id.twitch.tv/oauth2/token"
)

// Scopes requested at login. Fixed by what the app does: read chat (chat:read)
// and send messages (chat:edit) over IRC, and moderate over Helix.
const Scopes = "chat:read chat:edit moderator:manage:banned_users moderator:manage:chat_messages user:read:email"

// DeviceCode is what the user acts on: they open VerificationURI and enter
// UserCode.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// Token is a Twitch user access token with its refresh token.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Scope        []string  `json:"scope"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	Expiry       time.Time `json:"expiry"`
}

// Expired reports whether the access token is at or near expiry. A minute of
// slack avoids using a token that dies mid-request.
func (t Token) Expired() bool {
	return !t.Expiry.IsZero() && time.Now().After(t.Expiry.Add(-time.Minute))
}

// Client runs the device flow for one Twitch application.
type Client struct {
	ClientID string
	HTTP     *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// RequestDeviceCode starts the flow. The returned code is shown to the user and
// then passed to PollToken.
func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	form := url.Values{
		"client_id": {c.ClientID},
		"scopes":    {Scopes},
	}
	var dc DeviceCode
	if err := c.postForm(ctx, deviceURLVar, form, &dc); err != nil {
		return nil, err
	}
	if dc.Interval < 1 {
		dc.Interval = 5 // Twitch's documented default; guard against a 0 that would busy-loop
	}
	return &dc, nil
}

// tokenPending is the sentinel error while the user hasn't approved yet, so
// PollToken can tell "keep waiting" apart from a real failure.
var tokenPending = fmt.Errorf("authorization_pending")

// PollToken polls until the user approves, the code expires, or ctx is
// cancelled. It respects the server-provided interval and the slow_down signal.
func (c *Client) PollToken(ctx context.Context, dc *DeviceCode) (*Token, error) {
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

		tok, err := c.pollOnce(ctx, dc.DeviceCode)
		switch {
		case err == nil:
			return tok, nil
		case err == tokenPending:
			// keep waiting
		case strings.Contains(err.Error(), "slow_down"):
			// Twitch asks us to back off; honour it or it keeps returning this.
			interval += time.Second
		default:
			return nil, err
		}
	}
}

func (c *Client) pollOnce(ctx context.Context, deviceCode string) (*Token, error) {
	form := url.Values{
		"client_id":   {c.ClientID},
		"scopes":      {Scopes},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	// The pending and slow_down states come back as HTTP 400 with a message, so
	// decode the body before judging by status.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURLVar, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		Token
		Status  int    `json:"status"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}

	if resp.StatusCode == http.StatusOK && body.AccessToken != "" {
		tok := body.Token
		tok.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
		return &tok, nil
	}
	if strings.Contains(body.Message, "authorization_pending") {
		return nil, tokenPending
	}
	if body.Message != "" {
		return nil, fmt.Errorf("%s", body.Message)
	}
	return nil, fmt.Errorf("token request failed: %s", resp.Status)
}

// Refresh exchanges a refresh token for a fresh access token. Twitch may rotate
// the refresh token, so callers must persist whatever comes back.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	form := url.Values{
		"client_id":     {c.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	var tok Token
	if err := c.postForm(ctx, tokenURLVar, form, &tok); err != nil {
		return nil, err
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("refresh returned no access token; the grant may have been revoked")
	}
	tok.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	// Twitch keeps the same refresh token when it doesn't rotate; carry it
	// forward so the caller never loses the ability to refresh again.
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return &tok, nil
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Message != "" {
			return fmt.Errorf("%s: %s", endpoint, e.Message)
		}
		return fmt.Errorf("%s: %s", endpoint, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
