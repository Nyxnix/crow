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

	"github.com/Nyxnix/crow/internal/chat"
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

// modQuery is the broadcaster+moderator pair every "acting as a mod in this
// channel" endpoint wants.
func (h *Helix) modQuery() string {
	q := url.Values{
		"broadcaster_id": {h.BroadcasterID},
		"moderator_id":   {h.ModeratorID},
	}
	return q.Encode()
}

// ClearChat removes all chat messages: the delete-message endpoint with no
// message_id.
func (h *Helix) ClearChat(ctx context.Context) error {
	return h.do(ctx, http.MethodDelete, "/moderation/chat?"+h.modQuery(), nil)
}

// UpdateChatSettings patches the channel's chat modes (slow, follower-only,
// emote-only, unique). The patch is passed through as-is; Helix validates
// values and reports usable errors, so they are not re-validated here.
func (h *Helix) UpdateChatSettings(ctx context.Context, patch map[string]any) error {
	return h.do(ctx, http.MethodPatch, "/chat/settings?"+h.modQuery(), patch)
}

// Announce posts a highlighted announcement to chat.
func (h *Helix) Announce(ctx context.Context, text string) error {
	return h.do(ctx, http.MethodPost, "/chat/announcements?"+h.modQuery(),
		map[string]string{"message": text})
}

// CreatePoll starts a poll, with channel-points voting when pointsPerVote > 0.
// Broadcaster-only and Affiliate+; Helix enforces both and its error says so.
func (h *Helix) CreatePoll(ctx context.Context, title string, choices []string, durationSecs, pointsPerVote int) error {
	type choice struct {
		Title string `json:"title"`
	}
	cs := make([]choice, len(choices))
	for i, c := range choices {
		cs[i] = choice{c}
	}
	body := map[string]any{
		"broadcaster_id": h.BroadcasterID,
		"title":          title,
		"choices":        cs,
		"duration":       durationSecs,
	}
	if pointsPerVote > 0 {
		body["channel_points_voting_enabled"] = true
		body["channel_points_per_vote"] = pointsPerVote
	}
	return h.do(ctx, http.MethodPost, "/polls", body)
}

// CreatePrediction starts a prediction with the given outcomes.
func (h *Helix) CreatePrediction(ctx context.Context, title string, outcomes []string, windowSecs int) error {
	type outcome struct {
		Title string `json:"title"`
	}
	outs := make([]outcome, len(outcomes))
	for i, o := range outcomes {
		outs[i] = outcome{o}
	}
	return h.do(ctx, http.MethodPost, "/predictions", map[string]any{
		"broadcaster_id":    h.BroadcasterID,
		"title":             title,
		"outcomes":          outs,
		"prediction_window": windowSecs,
	})
}

// Raid starts a raid from the watched channel. Helix 401s unless the token
// owns that channel — exactly Twitch's own /raid rule, so it is surfaced, not
// pre-checked.
func (h *Helix) Raid(ctx context.Context, toBroadcasterID string) error {
	q := url.Values{
		"from_broadcaster_id": {h.BroadcasterID},
		"to_broadcaster_id":   {toBroadcasterID},
	}
	return h.do(ctx, http.MethodPost, "/raids?"+q.Encode(), nil)
}

// SetVIP grants or removes a user's VIP status.
func (h *Helix) SetVIP(ctx context.Context, userID string, on bool) error {
	q := url.Values{
		"broadcaster_id": {h.BroadcasterID},
		"user_id":        {userID},
	}
	method := http.MethodPost
	if !on {
		method = http.MethodDelete
	}
	return h.do(ctx, method, "/channels/vips?"+q.Encode(), nil)
}

// SetMod grants or removes a user's moderator status.
func (h *Helix) SetMod(ctx context.Context, userID string, on bool) error {
	q := url.Values{
		"broadcaster_id": {h.BroadcasterID},
		"user_id":        {userID},
	}
	method := http.MethodPost
	if !on {
		method = http.MethodDelete
	}
	return h.do(ctx, method, "/moderation/moderators?"+q.Encode(), nil)
}

// PinMessage pins a chat message to the top of the channel's chat until the
// stream ends (no duration_seconds). Twitch keeps one mod-pin per channel and
// replaces any existing one.
func (h *Helix) PinMessage(ctx context.Context, messageID string) error {
	q := url.Values{
		"broadcaster_id": {h.BroadcasterID},
		"moderator_id":   {h.ModeratorID},
		"message_id":     {messageID},
	}
	return h.do(ctx, http.MethodPut, "/chat/pins?"+q.Encode(), nil)
}

// UnpinMessage removes a pinned chat message.
func (h *Helix) UnpinMessage(ctx context.Context, messageID string) error {
	q := url.Values{
		"broadcaster_id": {h.BroadcasterID},
		"moderator_id":   {h.ModeratorID},
		"message_id":     {messageID},
	}
	return h.do(ctx, http.MethodDelete, "/chat/pins?"+q.Encode(), nil)
}

// ResolveUser turns a login into its numeric ID, for slash commands naming
// someone who hasn't spoken recently.
func (h *Helix) ResolveUser(ctx context.Context, login string) (string, error) {
	return UserID(ctx, h.ClientID, h.Token, login, h.HTTP)
}

// PollStatus returns the channel's most recent poll with live vote counts;
// Status is empty when the channel never ran one.
func (h *Helix) PollStatus(ctx context.Context) (chat.Poll, error) {
	var body struct {
		Data []struct {
			Title     string    `json:"title"`
			Status    string    `json:"status"`
			Duration  int       `json:"duration"`
			StartedAt time.Time `json:"started_at"`
			Choices   []struct {
				Title string `json:"title"`
				Votes int    `json:"votes"`
			} `json:"choices"`
		} `json:"data"`
	}
	err := h.get(ctx, "/polls?first=1&broadcaster_id="+url.QueryEscape(h.BroadcasterID), &body)
	if err != nil || len(body.Data) == 0 {
		return chat.Poll{}, err
	}
	d := body.Data[0]
	p := chat.Poll{
		Kind:   "poll",
		Title:  d.Title,
		Status: d.Status,
		EndsAt: d.StartedAt.Add(time.Duration(d.Duration) * time.Second),
	}
	for _, c := range d.Choices {
		p.Choices = append(p.Choices, chat.PollChoice{Title: c.Title, Votes: c.Votes})
	}
	return p, nil
}

// PredictionStatus mirrors PollStatus for predictions; Votes carries the
// channel points staked on each outcome, and EndsAt is when entries lock.
func (h *Helix) PredictionStatus(ctx context.Context) (chat.Poll, error) {
	var body struct {
		Data []struct {
			Title            string    `json:"title"`
			Status           string    `json:"status"`
			PredictionWindow int       `json:"prediction_window"`
			CreatedAt        time.Time `json:"created_at"`
			Outcomes         []struct {
				Title         string `json:"title"`
				ChannelPoints int    `json:"channel_points"`
			} `json:"outcomes"`
		} `json:"data"`
	}
	err := h.get(ctx, "/predictions?first=1&broadcaster_id="+url.QueryEscape(h.BroadcasterID), &body)
	if err != nil || len(body.Data) == 0 {
		return chat.Poll{}, err
	}
	d := body.Data[0]
	p := chat.Poll{
		Kind:   "prediction",
		Title:  d.Title,
		Status: d.Status,
		EndsAt: d.CreatedAt.Add(time.Duration(d.PredictionWindow) * time.Second),
	}
	for _, o := range d.Outcomes {
		p.Choices = append(p.Choices, chat.PollChoice{Title: o.Title, Votes: o.ChannelPoints})
	}
	return p, nil
}

// get issues one Helix GET and decodes the response into out.
func (h *Helix) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, helixBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Client-Id", h.ClientID)
	req.Header.Set("Authorization", "Bearer "+h.Token)
	resp, err := h.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
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
