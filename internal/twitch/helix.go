package twitch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// helixBase is a var, not a const, so tests can point it at a stand-in server;
// nothing else reassigns it.
var helixBase = "https://api.twitch.tv/helix"

// Helix performs moderation actions through the Twitch API. It implements the
// TUI's Moderator interface.
//
// Every mod endpoint needs a broadcaster_id (whose channel) and a moderator_id
// (who is acting). The moderator is always the logged-in user, so it is fixed
// at construction; the broadcaster is the channel being watched.
type Helix struct {
	ClientID      string
	Token         string // user access token, no "oauth:" prefix
	BroadcasterID string
	ModeratorID   string

	HTTP *http.Client
}

func (h *Helix) client() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Whoami identifies the token's owner. It doubles as a token validity check at
// startup: a bad or expired token fails here rather than on the first ban.
func Whoami(ctx context.Context, clientID, token string, hc *http.Client) (id, login string, err error) {
	return userLookup(ctx, clientID, token, "", hc)
}

// UserID resolves a channel login to its numeric Twitch ID, which the mod
// endpoints require as broadcaster_id. Resolving it up front means the mod is
// fully constructed before the TUI starts, with no field mutated later.
func UserID(ctx context.Context, clientID, token, login string, hc *http.Client) (string, error) {
	id, _, err := userLookup(ctx, clientID, token, login, hc)
	return id, err
}

// userLookup calls GET /users. With an empty login it returns the token's own
// user; with a login it returns that channel.
func userLookup(ctx context.Context, clientID, token, login string, hc *http.Client) (id, name string, err error) {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	u := helixBase + "/users"
	if login != "" {
		u += "?login=" + url.QueryEscape(login)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Client-Id", clientID)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", apiError(resp)
	}

	var body struct {
		Data []struct {
			ID    string `json:"id"`
			Login string `json:"login"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}
	if len(body.Data) == 0 {
		return "", "", fmt.Errorf("no such user")
	}
	return body.Data[0].ID, body.Data[0].Login, nil
}

// Timeout times a user out for the given number of seconds. A ban is the same
// endpoint with no duration, which is why Ban delegates here with 0.
func (h *Helix) Timeout(ctx context.Context, userID string, seconds int, reason string) error {
	type banData struct {
		UserID   string `json:"user_id"`
		Duration int    `json:"duration,omitempty"`
		Reason   string `json:"reason,omitempty"`
	}
	payload := struct {
		Data banData `json:"data"`
	}{banData{UserID: userID, Duration: seconds, Reason: reason}}

	q := url.Values{
		"broadcaster_id": {h.BroadcasterID},
		"moderator_id":   {h.ModeratorID},
	}
	return h.do(ctx, http.MethodPost, "/moderation/bans?"+q.Encode(), payload)
}

// Ban permanently bans a user: the bans endpoint with no duration.
func (h *Helix) Ban(ctx context.Context, userID, reason string) error {
	return h.Timeout(ctx, userID, 0, reason)
}

func (h *Helix) Unban(ctx context.Context, userID string) error {
	q := url.Values{
		"broadcaster_id": {h.BroadcasterID},
		"moderator_id":   {h.ModeratorID},
		"user_id":        {userID},
	}
	return h.do(ctx, http.MethodDelete, "/moderation/bans?"+q.Encode(), nil)
}

func (h *Helix) DeleteMessage(ctx context.Context, messageID string) error {
	q := url.Values{
		"broadcaster_id": {h.BroadcasterID},
		"moderator_id":   {h.ModeratorID},
		"message_id":     {messageID},
	}
	return h.do(ctx, http.MethodDelete, "/moderation/chat?"+q.Encode(), nil)
}

// do issues one Helix call. body is JSON-encoded when non-nil.
func (h *Helix) do(ctx context.Context, method, path string, body any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, helixBase+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Client-Id", h.ClientID)
	req.Header.Set("Authorization", "Bearer "+h.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Helix returns 200/204 on success across these endpoints.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return apiError(resp)
}

// apiError turns a Helix error body into a message worth showing in the card.
// Helix puts the useful part in "message"; the status alone ("400 Bad Request")
// tells the user nothing about why their action failed.
func apiError(resp *http.Response) error {
	var e struct {
		Error   string `json:"error"`
		Status  int    `json:"status"`
		Message string `json:"message"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	json.Unmarshal(data, &e)

	status := e.Status
	if status == 0 {
		status = resp.StatusCode
	}
	switch {
	case e.Message != "":
		return fmt.Errorf("%d: %s", status, e.Message)
	case e.Error != "":
		return fmt.Errorf("%d: %s", status, e.Error)
	default:
		return fmt.Errorf("twitch API: %s", strconv.Itoa(status))
	}
}
