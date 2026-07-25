// Package overlay serves the browser-source chat overlay: a static page plus a
// Server-Sent Events stream of messages.
//
// SSE rather than WebSockets because the traffic is one-way and EventSource
// reconnects on its own, which matters when OBS reloads a browser source.
package overlay

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Nyxnix/crow/internal/chat"
	"github.com/Nyxnix/crow/internal/nowplaying"
)

//go:embed overlay.html alerts.html nowplaying.html
var assets embed.FS

// zeroTime clears a write deadline rather than setting one.
var zeroTime time.Time

// clientBuffer is how many messages may queue for one browser before we start
// dropping. A browser source that can't keep up is better off missing old
// messages than stalling the whole app.
const clientBuffer = 64

// Server fans messages out to every connected browser source.
type Server struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
	// settings is the current display options as JSON, sent to each browser
	// source on connect and re-broadcast when they change, so the page needs
	// nothing in its URL and picks up edits live.
	settings []byte
	// alertSettings is the same for the alerts page, as its own SSE event name
	// so the two pages' settings can't collide on the shared /events stream.
	alertSettings []byte
	// track is the last now-playing frame, replayed to browser sources on
	// connect so a reloaded source shows the current song immediately.
	track []byte
	// artPath is the local file the current artwork lives at, served from
	// /nowplaying/art because a browser source cannot load file:// URLs.
	artPath string
}

func New() *Server {
	return &Server{clients: make(map[chan []byte]struct{})}
}

// wireMessage is the shape the overlay page consumes. It is deliberately
// narrower than chat.Message: the page has no use for role flags beyond badges,
// and keeping it small keeps the SSE frames small.
type wireMessage struct {
	ID     string      `json:"id"`
	Author string      `json:"author"`
	Login  string      `json:"login"` // for removing a user's messages on a timeout/ban
	Color  string      `json:"color"`
	Text   string      `json:"text"`
	Emotes []wireEmote `json:"emotes"`
	Badges []wireBadge `json:"badges"`
}

type wireEmote struct {
	URL   string `json:"url"`
	Name  string `json:"name"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Zero  bool   `json:"zero,omitempty"` // 7TV overlay emote; drawn on the one before it
}

type wireBadge struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

// SetOptions publishes the overlay's display options (any JSON-marshalable
// value; in practice config.OverlayOptions, kept as `any` so this package
// doesn't depend on config's shape). Unchanged options are not re-broadcast,
// so callers can push on every save and file-watch tick without spamming.
func (s *Server) SetOptions(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.mu.Lock()
	changed := !bytes.Equal(s.settings, b)
	s.settings = b
	s.mu.Unlock()
	if changed {
		s.broadcast(frame("settings", b))
	}
}

// SetAlertOptions publishes the alert page's options (config.AlertOptions,
// kept as `any` like SetOptions). Unchanged options are not re-broadcast.
func (s *Server) SetAlertOptions(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.mu.Lock()
	changed := !bytes.Equal(s.alertSettings, b)
	s.alertSettings = b
	s.mu.Unlock()
	if changed {
		s.broadcast(frame("alert_settings", b))
	}
}

// wireTrack is the shape the now-playing page consumes. Times are seconds
// (floats) because that is what the page does arithmetic in.
type wireTrack struct {
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Album    string  `json:"album"`
	Art      string  `json:"art,omitempty"`
	Position float64 `json:"position"`
	Duration float64 `json:"duration"`
	Playing  bool    `json:"playing"`
}

// SetNowPlaying publishes the track a local player is on, or a zero Track when
// nothing is playing (the page hides itself). Unchanged state is not
// re-broadcast, so a paused or idle player costs no traffic at all.
func (s *Server) SetNowPlaying(t nowplaying.Track) {
	w := wireTrack{
		Title:    t.Title,
		Artist:   t.Artist,
		Album:    t.Album,
		Position: t.Position.Seconds(),
		Duration: t.Duration.Seconds(),
		Playing:  t.Playing,
	}
	// Artwork is usually a local file the browser source cannot open, so those
	// are served back out of /nowplaying/art. http(s) and data: URLs go straight
	// to the page.
	artPath := ""
	switch {
	case strings.HasPrefix(t.Art, "file://"):
		if u, err := url.Parse(t.Art); err == nil {
			artPath = u.Path
			w.Art = fmt.Sprintf("/nowplaying/art?v=%08x", fnv32(t.Art))
		}
	case strings.HasPrefix(t.Art, "http://"), strings.HasPrefix(t.Art, "https://"), strings.HasPrefix(t.Art, "data:"):
		w.Art = t.Art
	}

	b, err := json.Marshal(w)
	if err != nil {
		return
	}
	s.mu.Lock()
	changed := !bytes.Equal(s.track, b)
	s.track, s.artPath = b, artPath
	s.mu.Unlock()
	if changed {
		s.broadcast(frame("now_playing", b))
	}
}

func fnv32(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// handleArt serves the current track's local artwork file. The path never comes
// from the request — only the player's metadata — so there is nothing to
// traverse; the request just asks for "whatever is playing".
func (s *Server) handleArt(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	p := s.artPath
	s.mu.Unlock()
	if p == "" {
		http.NotFound(w, r)
		return
	}
	// Players reuse the same temp path for successive covers, so never cache.
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, p)
}

// wireAlert is the shape the alerts page consumes: a complete event sentence
// plus the user's optional attached message. Emotes and badges are deliberately
// omitted — an alert popup is text.
type wireAlert struct {
	Kind      string `json:"kind"`
	Author    string `json:"author"`
	Color     string `json:"color"`
	AlertText string `json:"alert_text"`
	Text      string `json:"text,omitempty"`
}

// PublishAlert sends an alert to every connected browser source; only the
// alerts page listens for the event. Non-alert messages are ignored.
func (s *Server) PublishAlert(m chat.Message) {
	if m.Alert == "" {
		return
	}
	w := wireAlert{
		Kind:      string(m.Alert),
		Author:    m.Author,
		Color:     m.Color,
		AlertText: m.AlertText,
		Text:      m.Text,
	}
	if b, err := json.Marshal(w); err == nil {
		s.broadcast(frame("alert", b))
	}
}

// Publish sends a message to every connected browser source. It never blocks:
// a client that has fallen behind loses the message.
func (s *Server) Publish(m chat.Message) {
	w := wireMessage{
		ID:     m.ID,
		Author: m.Author,
		Login:  m.AuthorLogin,
		Color:  m.Color,
		Text:   m.Text,
		Emotes: make([]wireEmote, 0, len(m.Emotes)),
		Badges: make([]wireBadge, 0, len(m.Badges)),
	}
	for _, e := range m.Emotes {
		w.Emotes = append(w.Emotes, wireEmote{URL: e.URL, Name: e.Name, Start: e.Start, End: e.End, Zero: e.ZeroWidth})
	}
	for _, b := range m.Badges {
		if b.URL == "" {
			continue // registry hasn't resolved this badge; don't render a broken img
		}
		w.Badges = append(w.Badges, wireBadge{URL: b.URL, Name: b.Name})
	}

	if b, err := json.Marshal(w); err == nil {
		s.broadcast(frame("message", b))
	}
}

// Remove tells every browser source to remove the messages a moderation action
// affects, so deleted or banned content does not stay on stream.
func (s *Server) Remove(ev chat.ModEvent) {
	payload := struct {
		Kind  string `json:"kind"`
		ID    string `json:"id,omitempty"`
		Login string `json:"login,omitempty"`
	}{ID: ev.MessageID, Login: ev.Login}
	switch ev.Kind {
	case chat.DeleteMessage:
		payload.Kind = "message"
	case chat.ClearUser:
		payload.Kind = "user"
	case chat.ClearAll:
		payload.Kind = "all"
	}
	if b, err := json.Marshal(payload); err == nil {
		s.broadcast(frame("remove", b))
	}
}

// frame formats one SSE event with the given name and JSON data.
func frame(event string, data []byte) []byte {
	return []byte("event: " + event + "\ndata: " + string(data) + "\n\n")
}

// broadcast delivers a pre-formatted SSE frame to every client, dropping it for
// any that have fallen behind.
func (s *Server) broadcast(f []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- f:
		default: // ponytail: drop for slow clients; add per-client backpressure if OBS ever needs replay
		}
	}
}

func (s *Server) subscribe() chan []byte {
	ch := make(chan []byte, clientBuffer)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan []byte) {
	s.mu.Lock()
	delete(s.clients, ch)
	s.mu.Unlock()
}

// Clients reports how many browser sources are connected, so the TUI can show
// whether OBS actually picked the overlay up.
func (s *Server) Clients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// Handler returns the overlay routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", s.handleEvents)
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// The page is a browser source, not a cacheable asset; OBS should
			// always get the current build after an upgrade.
			w.Header().Set("Cache-Control", "no-store")
			b, _ := assets.ReadFile(name)
			w.Write(b)
		}
	}
	mux.HandleFunc("GET /{$}", page("overlay.html"))
	mux.HandleFunc("GET /alerts", page("alerts.html"))
	mux.HandleFunc("GET /nowplaying", page("nowplaying.html"))
	mux.HandleFunc("GET /nowplaying/art", s.handleArt)
	return mux
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")

	rc := http.NewResponseController(w)
	// SSE streams for as long as the browser source lives, so clear the write
	// deadline the server would otherwise impose.
	if err := rc.SetWriteDeadline(zeroTime); err != nil {
		log.Printf("overlay: no write deadline control: %v", err)
	}

	ch := s.subscribe()
	defer s.unsubscribe(ch)

	// Flush headers immediately so EventSource fires onopen rather than waiting
	// for the first message, which may be minutes away in a quiet chat.
	fmt.Fprint(w, ": connected\n\n")
	// Current settings first, so the page styles itself before any message.
	// Both blobs go to every client; each page listens only to its own event.
	s.mu.Lock()
	settings, alertSettings, track := s.settings, s.alertSettings, s.track
	s.mu.Unlock()
	if settings != nil {
		w.Write(frame("settings", settings))
	}
	if alertSettings != nil {
		w.Write(frame("alert_settings", alertSettings))
	}
	if track != nil {
		w.Write(frame("now_playing", track))
	}
	rc.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case f := <-ch:
			// f is a complete SSE frame ("event: ...\ndata: ...\n\n").
			if _, err := w.Write(f); err != nil {
				return
			}
			rc.Flush()
		}
	}
}
