package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// ChannelInfo is the channel detail shown on the user card.
type ChannelInfo struct {
	Title     string
	AvatarURL string
	Created   time.Time
	Subs      int // 0 when the channel hides its subscriber count
}

// ChannelInfo fetches a channel's public detail via the Data API.
func (a *Auth) ChannelInfo(ctx context.Context, channelID string) (ChannelInfo, error) {
	tok, err := a.Ensure(ctx)
	if err != nil {
		return ChannelInfo{}, err
	}
	if tok == nil {
		return ChannelInfo{}, fmt.Errorf("not logged in to youtube")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		apiBase+"/channels?part=snippet,statistics&id="+url.QueryEscape(channelID), nil)
	if err != nil {
		return ChannelInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := a.http().Do(req)
	if err != nil {
		return ChannelInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ChannelInfo{}, apiError("channel info", resp)
	}
	var out struct {
		Items []struct {
			Snippet struct {
				Title       string `json:"title"`
				PublishedAt string `json:"publishedAt"`
				Thumbnails  struct {
					Medium struct {
						URL string `json:"url"`
					} `json:"medium"`
				} `json:"thumbnails"`
			} `json:"snippet"`
			Statistics struct {
				SubscriberCount       string `json:"subscriberCount"`
				HiddenSubscriberCount bool   `json:"hiddenSubscriberCount"`
			} `json:"statistics"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ChannelInfo{}, err
	}
	if len(out.Items) == 0 {
		return ChannelInfo{}, fmt.Errorf("no such channel %s", channelID)
	}
	it := out.Items[0]
	ci := ChannelInfo{Title: it.Snippet.Title, AvatarURL: it.Snippet.Thumbnails.Medium.URL}
	if t, err := time.Parse(time.RFC3339, it.Snippet.PublishedAt); err == nil {
		ci.Created = t
	}
	if !it.Statistics.HiddenSubscriberCount {
		ci.Subs, _ = strconv.Atoi(it.Statistics.SubscriberCount)
	}
	return ci, nil
}

// Mod moderates a live chat via the Data API. It satisfies tui.Moderator, so
// the same user card that drives Twitch's Helix drives this. The API rejects
// calls from accounts that aren't moderators of the chat, which surfaces on
// the card the same way Helix rejections do.
type Mod struct {
	// Sender supplies the stream target, auth, and the resolved live chat ID.
	Sender *Sender

	// bans remembers the ban IDs this session placed, keyed by the banned
	// channel. The API can only lift a ban by its ID, which is returned once at
	// insert, so unban works for bans placed here and errors honestly otherwise.
	mu   sync.Mutex
	bans map[string]string
}

// Timeout bans userID for seconds. YouTube has no reasons on chat bans; the
// reason argument exists to satisfy the interface.
func (m *Mod) Timeout(ctx context.Context, userID string, seconds int, _ string) error {
	return m.ban(ctx, userID, seconds)
}

// Ban permanently bans userID from the chat.
func (m *Mod) Ban(ctx context.Context, userID, _ string) error {
	return m.ban(ctx, userID, 0)
}

// ban inserts a liveChatBan: temporary when seconds > 0, else permanent.
func (m *Mod) ban(ctx context.Context, userID string, seconds int) error {
	tok, chatID, err := m.session(ctx)
	if err != nil {
		return err
	}
	snippet := map[string]any{
		"liveChatId":        chatID,
		"type":              "permanent",
		"bannedUserDetails": map[string]string{"channelId": userID},
	}
	if seconds > 0 {
		snippet["type"] = "temporary"
		snippet["banDurationSeconds"] = seconds
	}
	body, _ := json.Marshal(map[string]any{"snippet": snippet})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+"/liveChat/bans?part=snippet", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.Sender.Auth.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError("ban", resp)
	}
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.ID != "" {
		m.mu.Lock()
		if m.bans == nil {
			m.bans = make(map[string]string)
		}
		m.bans[userID] = out.ID
		m.mu.Unlock()
	}
	return nil
}

// Unban lifts a ban placed this session.
func (m *Mod) Unban(ctx context.Context, userID string) error {
	m.mu.Lock()
	banID := m.bans[userID]
	m.mu.Unlock()
	if banID == "" {
		return fmt.Errorf("youtube can only lift bans placed this session (no ban id held)")
	}
	if err := m.del(ctx, "/liveChat/bans?id="+url.QueryEscape(banID), "unban"); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.bans, userID)
	m.mu.Unlock()
	return nil
}

// DeleteMessage removes one message; the innertube reader's message IDs are
// the same IDs the Data API uses.
func (m *Mod) DeleteMessage(ctx context.Context, messageID string) error {
	return m.del(ctx, "/liveChat/messages?id="+url.QueryEscape(messageID), "delete")
}

func (m *Mod) del(ctx context.Context, path, op string) error {
	tok, _, err := m.session(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := m.Sender.Auth.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return apiError(op, resp)
	}
	return nil
}

// session returns a live access token and the resolved chat ID.
func (m *Mod) session(ctx context.Context) (accessToken, chatID string, err error) {
	tok, err := m.Sender.Auth.Ensure(ctx)
	if err != nil {
		return "", "", err
	}
	if tok == nil {
		return "", "", fmt.Errorf("not logged in to youtube")
	}
	id, err := m.Sender.chatID(ctx, tok.AccessToken)
	if err != nil {
		return "", "", err
	}
	return tok.AccessToken, id, nil
}
