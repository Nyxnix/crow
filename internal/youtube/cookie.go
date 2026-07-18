package youtube

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nyxnix/crow/internal/chat"
)

// Cookie auth drives the same innertube endpoints youtube.com's own player
// uses, authorized the way the site's JS authorizes itself: the session
// cookies plus a SAPISIDHASH Authorization header. No Google Cloud project,
// no Data API, no quota — the closest thing YouTube has to Twitch's "just
// approve the app" login. The trade-off is that innertube is unversioned:
// YouTube can rearrange it, and when it does these calls degrade with an
// error rather than crow breaking.

// CookieAuth authenticates innertube calls with the user's youtube.com
// session cookies (the Cookie header of any logged-in youtube.com request).
type CookieAuth struct {
	Cookies string
	HTTP    *http.Client
}

func (a *CookieAuth) http() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// sapisid pulls the SAPISID (or its __Secure-3PAPISID twin) out of the cookie
// string; it is the ingredient of the auth hash.
func (a *CookieAuth) sapisid() string {
	for _, part := range strings.Split(a.Cookies, ";") {
		name, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && (name == "SAPISID" || name == "__Secure-3PAPISID") {
			return val
		}
	}
	return ""
}

// Valid reports whether the cookie string can authorize at all.
func (a *CookieAuth) Valid() bool { return a.sapisid() != "" }

// decorate attaches the cookies and the SAPISIDHASH header, computed exactly
// as youtube.com's JS does: sha1("<unix> <SAPISID> <origin>").
func (a *CookieAuth) decorate(req *http.Request) {
	ts := time.Now().Unix()
	sum := sha1.Sum(fmt.Appendf(nil, "%d %s %s", ts, a.sapisid(), base))
	req.Header.Set("Cookie", a.Cookies)
	req.Header.Set("Authorization", fmt.Sprintf("SAPISIDHASH %d_%x", ts, sum))
	req.Header.Set("Origin", base)
	req.Header.Set("X-Origin", base)
	req.Header.Set("User-Agent", userAgent)
}

// innertube POSTs one innertube API call and returns the raw response body.
func (a *CookieAuth) innertube(ctx context.Context, path string, extra map[string]any) ([]byte, error) {
	if !a.Valid() {
		return nil, fmt.Errorf("cookies missing SAPISID; copy the full Cookie header from a logged-in youtube.com request")
	}
	payload := map[string]any{
		"context": map[string]any{
			"client": map[string]any{"clientName": "WEB", "clientVersion": clientVersion, "hl": "en", "gl": "US"},
		},
	}
	for k, v := range extra {
		payload[k] = v
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	a.decorate(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("youtube %s: %s", path, resp.Status)
	}
	return data, nil
}

// WhoAmI returns the logged-in account's name, proving the cookies work.
func (a *CookieAuth) WhoAmI(ctx context.Context) (string, error) {
	data, err := a.innertube(ctx, "/youtubei/v1/account/account_menu?prettyPrint=false", nil)
	if err != nil {
		return "", err
	}
	var v any
	json.Unmarshal(data, &v)
	if h, ok := findKey(v, "activeAccountHeaderRenderer"); ok {
		if n, ok := findKey(h, "accountName"); ok {
			if s, ok := findKey(n, "simpleText"); ok {
				if name, ok := s.(string); ok && name != "" {
					return name, nil
				}
			}
		}
	}
	return "", fmt.Errorf("cookies rejected (no account in response); re-copy them from a logged-in youtube.com tab")
}

// ChannelInfo fetches a channel's title, avatar and approximate subscriber
// count from the channel page's innertube data. Created date is not exposed
// there, so it stays zero.
func (a *CookieAuth) ChannelInfo(ctx context.Context, channelID string) (ChannelInfo, error) {
	data, err := a.innertube(ctx, "/youtubei/v1/browse?prettyPrint=false", map[string]any{"browseId": channelID})
	if err != nil {
		return ChannelInfo{}, err
	}
	var out struct {
		Metadata struct {
			ChannelMetadataRenderer struct {
				Title  string `json:"title"`
				Avatar struct {
					Thumbnails []struct {
						URL string `json:"url"`
					} `json:"thumbnails"`
				} `json:"avatar"`
			} `json:"channelMetadataRenderer"`
		} `json:"metadata"`
	}
	json.Unmarshal(data, &out)
	md := out.Metadata.ChannelMetadataRenderer
	if md.Title == "" {
		return ChannelInfo{}, fmt.Errorf("no channel metadata for %s", channelID)
	}
	ci := ChannelInfo{Title: md.Title}
	if n := len(md.Avatar.Thumbnails); n > 0 {
		ci.AvatarURL = md.Avatar.Thumbnails[n-1].URL
	}
	if m := reSubscribers.FindSubmatch(data); m != nil {
		ci.Subs = approxCount(string(m[1]))
	}
	return ci, nil
}

var reSubscribers = regexp.MustCompile(`"([\d.,]+[KMB]?) subscribers"`)

// approxCount parses YouTube's rounded counts: "1.23M" -> 1230000.
func approxCount(s string) int {
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1e3, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1e6, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "B"):
		mult, s = 1e9, strings.TrimSuffix(s, "B")
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	if err != nil {
		return 0
	}
	return int(f * mult)
}

// CookieSender sends chat messages through innertube's send_message, the same
// call the web player makes when you type in chat.
type CookieSender struct {
	// Video is the stream target, same forms as Client.Channel.
	Video string
	Auth  *CookieAuth

	// key and sendParams come from the live chat page fetched with cookies;
	// params identify "this viewer typing into this chat".
	mu         sync.Mutex
	key        string
	sendParams string
}

var reSendParams = regexp.MustCompile(`"sendLiveChatMessageEndpoint":\{"params":"([^"]+)"`)

// prepare fetches the live chat page as the logged-in user and extracts the
// API key and send params. Called once, and again after a send failure in
// case the stream restarted.
func (s *CookieSender) prepare(ctx context.Context) (key, params string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendParams != "" {
		return s.key, s.sendParams, nil
	}

	vid, err := (&Client{Channel: s.Video, HTTP: s.Auth.HTTP}).videoID(ctx)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/live_chat?is_popout=1&v="+url.QueryEscape(vid), nil)
	if err != nil {
		return "", "", err
	}
	s.Auth.decorate(req)
	resp, err := s.Auth.http().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	page, err := readBody(resp)
	if err != nil {
		return "", "", err
	}

	s.key = fallbackKey
	if m := reAPIKey.FindSubmatch(page); m != nil {
		s.key = string(m[1])
	}
	m := reSendParams.FindSubmatch(page)
	if m == nil {
		return "", "", fmt.Errorf("chat won't accept messages from this account (cookies stale, not signed in, or chat restricted)")
	}
	s.sendParams = string(m[1])
	return s.key, s.sendParams, nil
}

// Send posts one message. YouTube echoes it back through the live chat feed,
// so the reader shows it a few seconds later without a local echo.
func (s *CookieSender) Send(ctx context.Context, text string) error {
	key, params, err := s.prepare(ctx)
	if err != nil {
		return err
	}
	_, err = s.Auth.innertube(ctx,
		"/youtubei/v1/live_chat/send_message?key="+url.QueryEscape(key)+"&prettyPrint=false",
		map[string]any{
			"params":      params,
			"richMessage": map[string]any{"textSegments": []map[string]string{{"text": text}}},
		})
	if err != nil {
		// Stale params (stream restarted, cookies rotated): drop them so the
		// next send re-prepares. A silent refusal (slow mode, members-only)
		// comes back 200 and is indistinguishable here; the feed simply won't
		// echo the message.
		s.mu.Lock()
		s.sendParams = ""
		s.mu.Unlock()
	}
	return err
}

// CookieMod moderates through innertube the way the web UI does: each
// message's context-menu token is exchanged for that menu's actions (remove,
// timeout, hide/unhide user), and the matching action is invoked. It needs to
// see messages to hold their tokens, so the host feeds it via Observe.
type CookieMod struct {
	Sender *CookieSender

	mu     sync.Mutex
	byMsg  map[string]string // message ID -> context-menu params
	byUser map[string]string // author ID -> latest message's params
}

// Observe records a message's moderation token. Bounded like the chat buffer:
// when the maps outgrow it, they reset (older messages have scrolled away).
func (m *CookieMod) Observe(msg chat.Message) {
	if msg.ModParams == "" {
		return
	}
	m.mu.Lock()
	if len(m.byMsg) > 4096 {
		m.byMsg, m.byUser = nil, nil
	}
	if m.byMsg == nil {
		m.byMsg = make(map[string]string)
		m.byUser = make(map[string]string)
	}
	m.byMsg[msg.ID] = msg.ModParams
	m.byUser[msg.AuthorID] = msg.ModParams
	m.mu.Unlock()
}

func (m *CookieMod) lookup(key string, byUser bool) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if byUser {
		return m.byUser[key]
	}
	return m.byMsg[key]
}

// Timeout puts the user in timeout. YouTube's menu offers exactly one
// duration (5 minutes), so the requested seconds are ignored.
// ponytail: fixed 5m is YouTube's own ceiling in cookie mode; per-duration
// timeouts need the Data API path.
func (m *CookieMod) Timeout(ctx context.Context, userID string, _ int, _ string) error {
	return m.menuAction(ctx, m.lookup(userID, true), "timeout")
}

// Ban hides the user on the channel permanently.
func (m *CookieMod) Ban(ctx context.Context, userID, _ string) error {
	return m.menuAction(ctx, m.lookup(userID, true), "hide")
}

// Unban unhides the user, when their message's menu offers it.
func (m *CookieMod) Unban(ctx context.Context, userID string) error {
	return m.menuAction(ctx, m.lookup(userID, true), "unhide")
}

// DeleteMessage removes one message.
func (m *CookieMod) DeleteMessage(ctx context.Context, messageID string) error {
	return m.menuAction(ctx, m.lookup(messageID, false), "remove")
}

// menuAction fetches the context menu behind token and invokes the item
// matching want ("remove"/"timeout"/"hide"/"unhide").
func (m *CookieMod) menuAction(ctx context.Context, token, want string) error {
	if token == "" {
		return fmt.Errorf("no moderation token held for them (message scrolled out of the buffer)")
	}
	key, _, err := m.Sender.prepare(ctx)
	if err != nil {
		return err
	}
	data, err := m.Sender.Auth.innertube(ctx,
		"/youtubei/v1/live_chat/get_item_context_menu?params="+url.QueryEscape(token)+
			"&key="+url.QueryEscape(key)+"&prettyPrint=false", nil)
	if err != nil {
		return err
	}

	var v any
	json.Unmarshal(data, &v)
	var labels []string
	for _, item := range findAll(v, "menuServiceItemRenderer") {
		label := ""
		if t, ok := findKey(item, "text"); ok {
			if s, ok := findKey(t, "text"); ok {
				label, _ = s.(string)
			}
		}
		labels = append(labels, label)
		if !matchMenuLabel(label, want) {
			continue
		}
		ep, ok := findKey(item, "serviceEndpoint")
		if !ok {
			continue
		}
		params, ok := findKey(ep, "params")
		p, isStr := params.(string)
		if !ok || !isStr {
			continue
		}
		_, err := m.Sender.Auth.innertube(ctx,
			"/youtubei/v1/live_chat/moderate?key="+url.QueryEscape(key)+"&prettyPrint=false",
			map[string]any{"params": p})
		return err
	}
	return fmt.Errorf("youtube offered no %q action here (not a moderator?); menu had: %s",
		want, strings.Join(labels, ", "))
}

// matchMenuLabel maps our action names onto YouTube's menu wording without
// depending on its exact phrasing.
func matchMenuLabel(label, want string) bool {
	l := strings.ToLower(label)
	switch want {
	case "remove":
		return strings.Contains(l, "remove") || strings.Contains(l, "delete")
	case "timeout":
		return strings.Contains(l, "timeout")
	case "hide":
		return strings.Contains(l, "hide") && !strings.Contains(l, "unhide")
	case "unhide":
		return strings.Contains(l, "unhide")
	}
	return false
}

// findKey walks arbitrary decoded JSON depth-first for the first value under
// key. Innertube nests renderers unpredictably; hunting by key survives its
// rearrangements far better than fixed struct paths.
func findKey(v any, key string) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		if val, ok := t[key]; ok {
			return val, true
		}
		for _, val := range t {
			if r, ok := findKey(val, key); ok {
				return r, true
			}
		}
	case []any:
		for _, val := range t {
			if r, ok := findKey(val, key); ok {
				return r, true
			}
		}
	}
	return nil, false
}

// findAll collects every value under key, depth-first.
func findAll(v any, key string) []any {
	var out []any
	switch t := v.(type) {
	case map[string]any:
		if val, ok := t[key]; ok {
			out = append(out, val)
		}
		for _, val := range t {
			out = append(out, findAll(val, key)...)
		}
	case []any:
		for _, val := range t {
			out = append(out, findAll(val, key)...)
		}
	}
	return out
}
