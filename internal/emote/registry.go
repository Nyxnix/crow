// Package emote resolves third-party emotes (7TV, BetterTTV, FrankerFaceZ) by
// name and annotates messages with them.
//
// Twitch tags its own emotes with exact positions, but third-party emotes are
// just words in the text: the only way to find them is to look every word up in
// a table the providers publish per channel.
package emote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nyxnix/typetype/internal/chat"
)

// Provider names the service an emote came from, so the TUI and overlay can say
// where a given emote is defined.
type Provider string

const (
	SevenTV Provider = "7tv"
	BTTV    Provider = "bttv"
	FFZ     Provider = "ffz"
)

// Emote is one third-party emote as published by a provider.
type Emote struct {
	Name      string
	ID        string
	URL       string
	Provider  Provider
	ZeroWidth bool
}

// Registry maps emote names to images for one channel.
//
// The zero value is usable and empty: Apply is a no-op until Load succeeds, so
// chat renders (without third-party emotes) from the first message rather than
// waiting on six HTTP calls.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]Emote

	// HTTP is the client used for provider calls. Nil means a default with a
	// timeout, so a hung provider can't stall a load forever.
	HTTP *http.Client
}

func New() *Registry {
	return &Registry{byName: map[string]Emote{}}
}

func (r *Registry) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Lookup finds an emote by exact name. Names are case-sensitive: Twitch, 7TV
// and BTTV all treat "Kappa" and "kappa" as different emotes.
func (r *Registry) Lookup(name string) (Emote, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byName[name]
	return e, ok
}

// Len reports how many emotes are loaded, for the TUI's status line.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byName)
}

// source is one provider fetch, kept as data so Load can run them concurrently
// and merge in a fixed order regardless of which finishes first.
type source struct {
	name  string
	fetch func(context.Context) ([]Emote, error)
}

// Load fetches global and channel emotes from every provider and swaps them in.
//
// channelID is the numeric Twitch user ID (IRC's ROOMSTATE room-id tag). It may
// be empty, in which case only global emotes load.
//
// A provider that fails is skipped rather than failing the whole load: a 7TV
// outage should cost you 7TV emotes, not the other two providers and not chat.
// The returned error is non-nil only if every source failed.
func (r *Registry) Load(ctx context.Context, channelID string) error {
	sources := []source{
		{"ffz global", r.ffzGlobal},
		{"bttv global", r.bttvGlobal},
		{"7tv global", r.sevenTVGlobal},
	}
	if channelID != "" {
		sources = append(sources,
			source{"ffz channel", func(c context.Context) ([]Emote, error) { return r.ffzChannel(c, channelID) }},
			source{"bttv channel", func(c context.Context) ([]Emote, error) { return r.bttvChannel(c, channelID) }},
			source{"7tv channel", func(c context.Context) ([]Emote, error) { return r.sevenTVChannel(c, channelID) }},
		)
	}

	results := make([][]Emote, len(sources))
	errs := make([]error, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			es, err := s.fetch(ctx)
			results[i], errs[i] = es, err
		}()
	}
	wg.Wait()

	// Merge in source order so precedence is deterministic: later sources
	// overwrite earlier ones, which puts channel emotes above global and lets a
	// channel's own "Kappa" win over a provider's.
	merged := make(map[string]Emote)
	var failed []string
	for i := range sources {
		if errs[i] != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", sources[i].name, errs[i]))
			continue
		}
		for _, e := range results[i] {
			if e.Name == "" || e.URL == "" {
				continue
			}
			merged[e.Name] = e
		}
	}
	if len(failed) == len(sources) {
		return fmt.Errorf("all emote providers failed: %s", strings.Join(failed, "; "))
	}

	r.mu.Lock()
	r.byName = merged
	r.mu.Unlock()
	return nil
}

// Apply annotates a message with any third-party emotes in its text.
//
// Twitch's own emotes are already positioned by the IRC tag, so their ranges are
// left alone and any word overlapping one is skipped: a channel that defines a
// 7TV emote named after a Twitch emote must not produce two images on one word.
func (r *Registry) Apply(m *chat.Message) {
	r.mu.RLock()
	empty := len(r.byName) == 0
	r.mu.RUnlock()
	if empty {
		return
	}

	runes := []rune(m.Text)
	// Twitch splits words on a single space, so tokenizing on spaces matches how
	// the providers themselves decide what counts as an emote word.
	var found []chat.Emote
	for i := 0; i < len(runes); {
		for i < len(runes) && runes[i] == ' ' {
			i++
		}
		j := i
		for j < len(runes) && runes[j] != ' ' {
			j++
		}
		if j > i && !overlaps(m.Emotes, i, j) {
			if e, ok := r.Lookup(string(runes[i:j])); ok {
				found = append(found, chat.Emote{
					Name:      e.Name,
					ID:        e.ID,
					URL:       e.URL,
					Start:     i,
					End:       j,
					ZeroWidth: e.ZeroWidth,
				})
			}
		}
		i = j
	}
	if len(found) == 0 {
		return
	}
	m.Emotes = append(m.Emotes, found...)
	// Renderers walk emotes in one pass and assume ascending position.
	sort.Slice(m.Emotes, func(a, b int) bool { return m.Emotes[a].Start < m.Emotes[b].Start })
}

// overlaps reports whether [start,end) intersects any existing emote range.
func overlaps(es []chat.Emote, start, end int) bool {
	for _, e := range es {
		if start < e.End && end > e.Start {
			return true
		}
	}
	return false
}

// getJSON fetches and decodes one provider endpoint.
func (r *Registry) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A channel with no emotes on a provider 404s; that is a normal, empty
		// answer rather than a failure, but the caller can't tell them apart, so
		// report it and let Load treat it as one skipped source.
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- 7TV -------------------------------------------------------------------

// sevenTVZeroWidth is the ZeroWidth bit in 7TV's emote flags (1 << 8).
const sevenTVZeroWidth = 256

type sevenTVFile struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Width  int    `json:"width"`
}

type sevenTVHost struct {
	URL   string        `json:"url"`
	Files []sevenTVFile `json:"files"`
}

type sevenTVData struct {
	Flags int         `json:"flags"`
	Host  sevenTVHost `json:"host"`
}

type sevenTVEmote struct {
	ID   string      `json:"id"`
	Name string      `json:"name"`
	Data sevenTVData `json:"data"`
}

type sevenTVSet struct {
	Emotes []sevenTVEmote `json:"emotes"`
}

func (s sevenTVSet) toEmotes() []Emote {
	out := make([]Emote, 0, len(s.Emotes))
	for _, e := range s.Emotes {
		file := bestSevenTVFile(e.Data.Host.Files)
		if file == "" || e.Data.Host.URL == "" {
			continue
		}
		// host.url is protocol-relative ("//cdn.7tv.app/emote/<id>").
		out = append(out, Emote{
			Name:      e.Name,
			ID:        e.ID,
			URL:       "https:" + e.Data.Host.URL + "/" + file,
			Provider:  SevenTV,
			ZeroWidth: e.Data.Flags&sevenTVZeroWidth != 0,
		})
	}
	return out
}

// bestSevenTVFile picks the widest WebP. WebP is the only format 7TV publishes
// for every emote, and it animates, which AVIF does not reliably do in OBS's
// browser engine.
func bestSevenTVFile(files []sevenTVFile) string {
	best, bestW := "", 0
	for _, f := range files {
		if f.Format != "WEBP" {
			continue
		}
		if f.Width > bestW {
			best, bestW = f.Name, f.Width
		}
	}
	return best
}

func (r *Registry) sevenTVGlobal(ctx context.Context) ([]Emote, error) {
	var set sevenTVSet
	if err := r.getJSON(ctx, "https://7tv.io/v3/emote-sets/global", &set); err != nil {
		return nil, err
	}
	return set.toEmotes(), nil
}

func (r *Registry) sevenTVChannel(ctx context.Context, channelID string) ([]Emote, error) {
	var user struct {
		EmoteSet sevenTVSet `json:"emote_set"`
	}
	url := "https://7tv.io/v3/users/twitch/" + channelID
	if err := r.getJSON(ctx, url, &user); err != nil {
		return nil, err
	}
	return user.EmoteSet.toEmotes(), nil
}

// --- BetterTTV -------------------------------------------------------------

type bttvEmote struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	ImageType string `json:"imageType"`
}

func (e bttvEmote) toEmote() Emote {
	return Emote{
		Name:     e.Code,
		ID:       e.ID,
		URL:      "https://cdn.betterttv.net/emote/" + e.ID + "/3x." + e.ImageType,
		Provider: BTTV,
	}
}

func (r *Registry) bttvGlobal(ctx context.Context) ([]Emote, error) {
	var raw []bttvEmote
	if err := r.getJSON(ctx, "https://api.betterttv.net/3/cached/emotes/global", &raw); err != nil {
		return nil, err
	}
	out := make([]Emote, 0, len(raw))
	for _, e := range raw {
		out = append(out, e.toEmote())
	}
	return out, nil
}

func (r *Registry) bttvChannel(ctx context.Context, channelID string) ([]Emote, error) {
	var user struct {
		ChannelEmotes []bttvEmote `json:"channelEmotes"`
		SharedEmotes  []bttvEmote `json:"sharedEmotes"`
	}
	url := "https://api.betterttv.net/3/cached/users/twitch/" + channelID
	if err := r.getJSON(ctx, url, &user); err != nil {
		return nil, err
	}
	out := make([]Emote, 0, len(user.ChannelEmotes)+len(user.SharedEmotes))
	for _, e := range append(user.ChannelEmotes, user.SharedEmotes...) {
		out = append(out, e.toEmote())
	}
	return out, nil
}

// --- FrankerFaceZ ----------------------------------------------------------

type ffzResponse struct {
	DefaultSets []int `json:"default_sets"`
	Sets        map[string]struct {
		Emoticons []struct {
			ID   int               `json:"id"`
			Name string            `json:"name"`
			URLs map[string]string `json:"urls"`
		} `json:"emoticons"`
	} `json:"sets"`
}

// toEmotes flattens the sets. only, when non-empty, restricts to those set IDs:
// the global endpoint returns extra sets that aren't actually active by default.
func (f ffzResponse) toEmotes(only []int) []Emote {
	active := make(map[string]bool, len(only))
	for _, id := range only {
		active[fmt.Sprint(id)] = true
	}

	var out []Emote
	for id, set := range f.Sets {
		if len(only) > 0 && !active[id] {
			continue
		}
		for _, e := range set.Emoticons {
			url := bestFFZURL(e.URLs)
			if url == "" {
				continue
			}
			out = append(out, Emote{
				Name:     e.Name,
				ID:       fmt.Sprint(e.ID),
				URL:      url,
				Provider: FFZ,
			})
		}
	}
	return out
}

// bestFFZURL picks the highest scale FFZ published. Not every emote has a 4x,
// so fall back down rather than skipping the emote.
func bestFFZURL(urls map[string]string) string {
	for _, scale := range []string{"4", "2", "1"} {
		if u := urls[scale]; u != "" {
			if strings.HasPrefix(u, "//") {
				return "https:" + u
			}
			return u
		}
	}
	return ""
}

func (r *Registry) ffzGlobal(ctx context.Context) ([]Emote, error) {
	var resp ffzResponse
	if err := r.getJSON(ctx, "https://api.frankerfacez.com/v1/set/global", &resp); err != nil {
		return nil, err
	}
	return resp.toEmotes(resp.DefaultSets), nil
}

func (r *Registry) ffzChannel(ctx context.Context, channelID string) ([]Emote, error) {
	var resp ffzResponse
	url := "https://api.frankerfacez.com/v1/room/id/" + channelID
	if err := r.getJSON(ctx, url, &resp); err != nil {
		return nil, err
	}
	// A room's sets are all active; there is no default_sets filter to apply.
	return resp.toEmotes(nil), nil
}
