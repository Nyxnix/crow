// Package overlay serves the browser-source chat overlay: a static page plus a
// Server-Sent Events stream of messages.
//
// SSE rather than WebSockets because the traffic is one-way and EventSource
// reconnects on its own, which matters when OBS reloads a browser source.
package overlay

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Nyxnix/typetype/internal/chat"
)

//go:embed overlay.html
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

// Publish sends a message to every connected browser source. It never blocks:
// a client that has fallen behind loses the message.
func (s *Server) Publish(m chat.Message) {
	w := wireMessage{
		ID:     m.ID,
		Author: m.Author,
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

	b, err := json.Marshal(w)
	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- b:
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
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The page is a browser source, not a cacheable asset; OBS should always
		// get the current build after an upgrade.
		w.Header().Set("Cache-Control", "no-store")
		b, _ := assets.ReadFile("overlay.html")
		w.Write(b)
	})
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
	rc.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			rc.Flush()
		}
	}
}
