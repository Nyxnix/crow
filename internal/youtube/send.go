package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// apiBase is the YouTube Data API root; a var so tests can stand it in.
var apiBase = "https://www.googleapis.com/youtube/v3"

// Sender posts chat messages to a live stream through the YouTube Data API.
// Reading stays on the anonymous innertube path — the API's default quota
// (10k units/day at ~50 per insert) is fine for typing but would not survive
// polling a busy chat.
type Sender struct {
	// Video is the stream target, same forms as Client.Channel.
	Video string
	Auth  *Auth
	HTTP  *http.Client // nil = default with timeout

	// liveChatID is resolved on first send and kept; a stream's chat ID does
	// not change while it is live.
	mu         sync.Mutex
	liveChatID string
}

// Send posts one message. YouTube echoes it back through the live chat feed,
// so the reader shows it a few seconds later without a local echo.
func (s *Sender) Send(ctx context.Context, text string) error {
	tok, err := s.Auth.Ensure(ctx)
	if err != nil {
		return err
	}
	if tok == nil {
		return fmt.Errorf("not logged in to youtube; run `crow login youtube`")
	}

	id, err := s.chatID(ctx, tok.AccessToken)
	if err != nil {
		return err
	}

	body, _ := json.Marshal(map[string]any{
		"snippet": map[string]any{
			"liveChatId":         id,
			"type":               "textMessageEvent",
			"textMessageDetails": map[string]string{"messageText": text},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+"/liveChat/messages?part=snippet", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Auth.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError("send", resp)
	}
	return nil
}

// chatID resolves and caches the stream's activeLiveChatId.
func (s *Sender) chatID(ctx context.Context, accessToken string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.liveChatID != "" {
		return s.liveChatID, nil
	}

	vid, err := (&Client{Channel: s.Video, HTTP: s.HTTP}).videoID(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		apiBase+"/videos?part=liveStreamingDetails&id="+vid, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := s.Auth.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", apiError("resolve chat", resp)
	}
	var out struct {
		Items []struct {
			LiveStreamingDetails struct {
				ActiveLiveChatID string `json:"activeLiveChatId"`
			} `json:"liveStreamingDetails"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Items) == 0 || out.Items[0].LiveStreamingDetails.ActiveLiveChatID == "" {
		return "", fmt.Errorf("no active live chat on %s", s.Video)
	}
	s.liveChatID = out.Items[0].LiveStreamingDetails.ActiveLiveChatID
	return s.liveChatID, nil
}

// WhoAmI returns the logged-in account's channel title, verifying the token
// works. Used by the login command.
func (a *Auth) WhoAmI(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		apiBase+"/channels?part=snippet&mine=true", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := a.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", apiError("whoami", resp)
	}
	var out struct {
		Items []struct {
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Items) == 0 {
		return "", fmt.Errorf("token works but no channel found on the account")
	}
	return out.Items[0].Snippet.Title, nil
}

// readBody reads a bounded response body.
func readBody(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// apiError surfaces the Data API's error message, which names the actual
// problem (quota exceeded, chat disabled) far better than the status line.
func apiError(op string, resp *http.Response) error {
	body, _ := readBody(resp)
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	if e.Error.Message != "" {
		return fmt.Errorf("youtube %s: %s", op, e.Error.Message)
	}
	return fmt.Errorf("youtube %s: %s", op, resp.Status)
}
