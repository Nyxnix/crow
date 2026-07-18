// Package youtube reads a YouTube live stream's chat through the same
// innertube endpoints the web player uses, and converts what it reads into
// chat.Message values. Anonymous and read-only: no API key or login needed.
package youtube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Nyxnix/crow/internal/chat"
)

// base is the YouTube web root; a var so tests can stand it in.
var base = "https://www.youtube.com"

const (
	// The public web-client key baked into every YouTube page, used only if the
	// chat page unexpectedly stops embedding one.
	fallbackKey   = "AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8"
	clientVersion = "2.20240701.00.00"
	userAgent     = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

var (
	reAPIKey    = regexp.MustCompile(`"INNERTUBE_API_KEY":"([^"]+)"`)
	reCont      = regexp.MustCompile(`"continuation":"([^"]+)"`)
	reCanonical = regexp.MustCompile(`<link rel="canonical" href="https://www\.youtube\.com/watch\?v=([A-Za-z0-9_-]{11})"`)
	reVideoID   = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
)

// Client reads one live stream's chat. It mirrors twitch.Client's shape so the
// host wires both the same way.
type Client struct {
	// Channel identifies the stream: an @handle, a watch/live/channel URL, or a
	// bare 11-character video ID.
	Channel string

	// Out receives every parsed message. Run closes it on return.
	Out chan chat.Message

	// Events, when set, receives moderation actions (message deletions, per-user
	// clears). Sends are non-blocking, so a stalled consumer drops events.
	Events chan chat.ModEvent

	// HTTP overrides the client used; nil means a default with a timeout.
	HTTP *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// Run resolves the live stream and pumps its chat into Out until ctx is
// cancelled, retrying with backoff on any failure (stream not live yet, chat
// ended, network trouble) — the same contract as twitch.Client.Run.
func (c *Client) Run(ctx context.Context) error {
	defer close(c.Out)

	backoff := 5 * time.Second
	for {
		err := c.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 2*time.Minute {
			backoff *= 2
		}
		_ = err
	}
}

// session resolves the video, opens its chat, and polls until it fails.
func (c *Client) session(ctx context.Context) error {
	vid, err := c.videoID(ctx)
	if err != nil {
		return err
	}
	page, err := c.get(ctx, base+"/live_chat?is_popout=1&v="+url.QueryEscape(vid))
	if err != nil {
		return err
	}

	key := fallbackKey
	if m := reAPIKey.FindSubmatch(page); m != nil {
		key = string(m[1])
	}
	// The page embeds one continuation per chat view; the first is "Top chat"
	// (filtered), the second "Live chat" (everything). Take everything.
	conts := reCont.FindAllSubmatch(page, 2)
	if len(conts) == 0 {
		return errors.New("no live chat found (stream offline or chat disabled)")
	}
	cont := string(conts[len(conts)-1][1])

	for {
		next, wait, err := c.poll(ctx, key, cont)
		if err != nil {
			return err
		}
		if next == "" {
			return errors.New("live chat ended")
		}
		cont = next
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// videoID resolves the configured target to the live video's ID.
func (c *Client) videoID(ctx context.Context) (string, error) {
	in := strings.TrimSpace(c.Channel)
	if strings.Contains(in, "youtu") {
		if !strings.Contains(in, "://") {
			in = "https://" + in
		}
		u, err := url.Parse(in)
		if err != nil {
			return "", fmt.Errorf("bad youtube url %q: %w", c.Channel, err)
		}
		if v := u.Query().Get("v"); v != "" {
			return v, nil
		}
		if strings.Contains(u.Host, "youtu.be") {
			return strings.Trim(u.Path, "/"), nil
		}
		// A channel or /live URL: its page's canonical link names the live video.
		return c.liveVideo(ctx, in)
	}
	if reVideoID.MatchString(in) {
		return in, nil
	}
	return c.liveVideo(ctx, base+"/@"+strings.TrimPrefix(in, "@")+"/live")
}

// liveVideo fetches a channel-ish page and pulls the live video ID from its
// canonical watch link, which only exists while the channel is live.
func (c *Client) liveVideo(ctx context.Context, pageURL string) (string, error) {
	page, err := c.get(ctx, pageURL)
	if err != nil {
		return "", err
	}
	if m := reCanonical.FindSubmatch(page); m != nil {
		return string(m[1]), nil
	}
	return "", fmt.Errorf("%s is not live", c.Channel)
}

func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Pre-agreed consent keeps EU requests from bouncing to a consent page.
	req.Header.Set("Cookie", "CONSENT=YES+cb; SOCS=CAI")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// poll fetches one batch of chat actions, emits them, and returns the next
// continuation plus how long YouTube asks us to wait before the next poll.
func (c *Client) poll(ctx context.Context, key, cont string) (string, time.Duration, error) {
	body := fmt.Sprintf(
		`{"context":{"client":{"clientName":"WEB","clientVersion":%q,"hl":"en","gl":"US"}},"continuation":%q}`,
		clientVersion, cont)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/youtubei/v1/live_chat/get_live_chat?key="+url.QueryEscape(key)+"&prettyPrint=false",
		strings.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http().Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("get_live_chat: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", 0, err
	}

	msgs, events, next, wait := parseChat(data, c.Channel)
	for _, ev := range events {
		if c.Events == nil {
			continue
		}
		select {
		case c.Events <- ev:
		default: // ponytail: drop if the consumer is behind; deletions are best-effort UI
		}
	}
	for _, m := range msgs {
		select {
		case c.Out <- m:
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
	}
	return next, wait, nil
}
