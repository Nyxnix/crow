// Command crow reads a live chat in the terminal and serves an overlay for
// OBS to point a browser source at.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Nyxnix/crow/internal/auth"
	"github.com/Nyxnix/crow/internal/badge"
	"github.com/Nyxnix/crow/internal/chat"
	"github.com/Nyxnix/crow/internal/config"
	"github.com/Nyxnix/crow/internal/emote"
	"github.com/Nyxnix/crow/internal/ivr"
	"github.com/Nyxnix/crow/internal/overlay"
	"github.com/Nyxnix/crow/internal/tui"
	"github.com/Nyxnix/crow/internal/twitch"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Subcommands come before flags: `crow login`, `crow logout`,
	// `crow whoami`. Anything else is the default chat/overlay run.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			exit(login(ctx))
			return
		case "logout":
			exit(logout())
			return
		case "whoami":
			exit(whoami())
			return
		case "-h", "--help", "help":
			usage()
			return
		}
	}

	channel := flag.String("channel", "", "Twitch channel(s) to open, comma-separated; omit to start on the splash")
	addr := flag.String("addr", "", "address for the overlay server (overrides the saved config)")
	headless := flag.Bool("headless", false, "serve the overlay without the terminal UI (needs -channel)")
	flag.Parse()

	var channels []string
	for _, c := range strings.Split(*channel, ",") {
		if c = strings.TrimSpace(c); c != "" {
			channels = append(channels, strings.ToLower(strings.TrimPrefix(c, "#")))
		}
	}

	if *headless && len(channels) == 0 {
		fmt.Fprintln(os.Stderr, "crow: -headless needs -channel")
		os.Exit(2)
	}

	exit(run(ctx, channels, *addr, *headless))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  crow [-channel a,b,c] [-addr host:port] [-headless]")
	fmt.Fprintln(os.Stderr, "  crow login | logout | whoami")
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "crow:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, channels []string, addrFlag string, headless bool) error {
	cfg := config.Load()
	if addrFlag != "" {
		cfg.OverlayAddr = addrFlag
	}

	// One overlay server for the whole app, pinned to a single channel so
	// switching tabs never disrupts the stream. Headless mode is overlay-only, so
	// it serves regardless of the toggle.
	ov := overlay.New()
	if cfg.OverlayEnabled || headless {
		ln, err := net.Listen("tcp", cfg.OverlayAddr)
		if err != nil {
			return fmt.Errorf("overlay: %w", err)
		}
		srv := &http.Server{Handler: ov.Handler()}
		go srv.Serve(ln)
		defer srv.Close()
	}

	ovState := &overlayState{
		configured: strings.ToLower(cfg.OverlayChannel),
		enabled:    cfg.OverlayEnabled || headless,
	}

	if headless {
		return runHeadless(ctx, channels[0], cfg.OverlayAddr, ov, ovState)
	}

	// The factory opens a channel's connections and returns its chat model. It
	// captures the App (assigned below) so each model's redraw wakes the App.
	var app *tui.App
	factory := func(channel string) (*tui.Model, func()) {
		return openChannel(ctx, channel, ov, ovState, app.RequestRedraw)
	}

	session := loadSession(ctx)
	login := ""
	if session != nil {
		login = session.Login
	}
	initial := channels
	if len(initial) == 0 {
		initial = cfg.Channels // reopen last session's channels
	}
	if len(initial) == 0 && login != "" {
		initial = []string{login} // logged in with nothing else: open your own chat
	}

	app = tui.NewApp(tui.AppOptions{
		Factory:     factory,
		Login:       login,
		Config:      cfg,
		Save:        func(c config.Config) { config.Save(c) },
		Channels:    initial,
		RequestCode: requestDeviceCode,
		PollLogin:   pollLogin,
		Logout:      func() { auth.Clear() },
	})

	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

// overlayState tracks which channel currently feeds the overlay. Only one does,
// so deleted/banned content is scoped and tab switching never disrupts it.
type overlayState struct {
	mu         sync.Mutex
	enabled    bool   // overlay off in config: no channel ever publishes
	configured string // channel the config pins the overlay to; "" = first opened
	owner      string
}

// claim reports whether channel should publish to the overlay, taking ownership
// if it is free and this channel is the configured one (or none is configured).
func (o *overlayState) claim(channel string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.enabled {
		return false
	}
	if o.owner == channel {
		return true
	}
	if o.owner == "" && (o.configured == "" || o.configured == channel) {
		o.owner = channel
		return true
	}
	return false
}

func (o *overlayState) release(channel string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.owner == channel {
		o.owner = ""
	}
}

// openChannel wires one channel's reader, sender, registries, stats and chat
// model under a sub-context that its returned close func cancels.
func openChannel(parent context.Context, channel string, ov *overlay.Server, ovState *overlayState, redraw func()) (*tui.Model, func()) {
	ctx, cancel := context.WithCancel(parent)

	// Reload the session per open, so a login completed on the splash applies to
	// channels opened afterwards.
	session := loadSession(ctx)
	toOverlay := ovState.claim(channel)

	emotes := emote.New()
	var badgeToken string
	if session != nil {
		badgeToken = session.AccessToken
	}
	badges := badge.New(clientID(), badgeToken)

	fromIRC := make(chan chat.Message, 256)
	modIRC := make(chan chat.ModEvent, 64)

	var loadOnce sync.Once
	reader := &twitch.Client{
		Channel: channel,
		Out:     fromIRC,
		Events:  modIRC,
		OnRoomID: func(id string) {
			loadOnce.Do(func() {
				go func() {
					if err := emotes.Load(ctx, id); err != nil {
						log.Printf("emotes: %v", err)
					}
				}()
				go func() {
					if err := badges.Load(ctx, id); err != nil {
						log.Printf("badges: %v", err)
					}
				}()
			})
		},
	}
	go reader.Run(ctx)

	var sendFn func(string)
	if session != nil {
		sender := &twitch.Client{Channel: channel, Nick: session.Login, Token: session.AccessToken, Out: make(chan chat.Message, 16)}
		go sender.Run(ctx)
		go func() {
			for range sender.Out {
			}
		}()
		sendFn = sender.Send
	}

	var poller *twitch.StreamPoller
	var statsFn func() tui.StreamStats
	if session != nil {
		poller = &twitch.StreamPoller{ClientID: clientID(), Token: session.AccessToken, Login: channel, Interval: time.Minute, OnUpdate: redraw}
		statsFn = func() tui.StreamStats {
			s := poller.Snapshot()
			return tui.StreamStats{Live: s.Live, Viewers: s.Viewers, AvgViewers: s.AvgViewers, Uptime: s.Uptime}
		}
		go poller.Run(ctx)
	}

	var clientsFn func() int
	if toOverlay {
		clientsFn = ov.Clients
	}

	model := tui.NewModel(tui.Options{
		Channel:  channel,
		Emotes:   emotes,
		Mod:      buildModerator(ctx, channel, session),
		Info:     infoProvider{&ivr.Client{}},
		Clients:  clientsFn,
		Stats:    statsFn,
		Send:     sendFn,
		OnRedraw: redraw,
	})

	// Ingest chat: apply emotes/badges, publish to the overlay if this channel
	// owns it, and append to the model.
	go func() {
		for m := range fromIRC {
			emotes.Apply(&m)
			badges.Resolve(&m)
			if toOverlay {
				ov.Publish(m)
			}
			model.Append(m)
		}
	}()
	// Ingest moderation: remove from the overlay, strike through in the model.
	go func() {
		for ev := range modIRC {
			if toOverlay {
				ov.Remove(ev)
			}
			model.ApplyModEvent(ev)
		}
	}()

	return model, func() {
		cancel()
		ovState.release(channel)
	}
}

// runHeadless serves the overlay for one channel with no terminal UI.
func runHeadless(ctx context.Context, channel, addr string, ov *overlay.Server, ovState *overlayState) error {
	ovState.claim(channel)
	emotes := emote.New()
	session := loadSession(ctx)
	var badgeToken string
	if session != nil {
		badgeToken = session.AccessToken
	}
	badges := badge.New(clientID(), badgeToken)

	fromIRC := make(chan chat.Message, 256)
	modIRC := make(chan chat.ModEvent, 64)
	var loadOnce sync.Once
	reader := &twitch.Client{
		Channel: channel, Out: fromIRC, Events: modIRC,
		OnRoomID: func(id string) {
			loadOnce.Do(func() {
				go emotes.Load(ctx, id)
				go badges.Load(ctx, id)
			})
		},
	}
	go reader.Run(ctx)
	go func() {
		for ev := range modIRC {
			ov.Remove(ev)
		}
	}()

	log.Printf("overlay: http://%s", addr)
	for m := range fromIRC {
		emotes.Apply(&m)
		badges.Resolve(&m)
		ov.Publish(m)
	}
	return nil
}

// requestDeviceCode / pollLogin drive the splash's inline login through the
// device flow. The handle is the device code carried between the two steps.
func requestDeviceCode() (code, url string, handle any, err error) {
	ac := &auth.Client{ClientID: clientID()}
	dc, err := ac.RequestDeviceCode(context.Background())
	if err != nil {
		return "", "", nil, err
	}
	return dc.UserCode, dc.VerificationURI, dc, nil
}

func pollLogin(handle any) (login string, err error) {
	dc, ok := handle.(*auth.DeviceCode)
	if !ok {
		return "", fmt.Errorf("bad login handle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	ac := &auth.Client{ClientID: clientID()}
	tok, err := ac.PollToken(ctx, dc)
	if err != nil {
		return "", err
	}
	id, name, err := twitch.Whoami(ctx, clientID(), tok.AccessToken, nil)
	if err != nil {
		return "", err
	}
	if err := auth.Save(&auth.StoredToken{Token: *tok, UserID: id, Login: name}); err != nil {
		return "", err
	}
	return name, nil
}

// infoProvider adapts the IVR client to the TUI's InfoProvider interface,
// translating ivr.CardInfo into the card's UserInfo. This keeps the tui package
// from importing ivr.
type infoProvider struct{ c *ivr.Client }

func (p infoProvider) CardInfo(ctx context.Context, userLogin, channel string) (tui.UserInfo, error) {
	i, err := p.c.CardInfo(ctx, userLogin, channel)
	return tui.UserInfo{
		CreatedAt:  i.CreatedAt,
		FollowedAt: i.FollowedAt,
		SubTier:    i.SubTier,
		SubMonths:  i.SubMonths,
		SubHidden:  i.SubHidden,
	}, err
}

// loadSession returns the stored login, refreshing it if needed, or nil when
// the user is not logged in. An auth error is logged and treated as "not logged
// in" rather than blocking chat, which needs no auth at all.
func loadSession(ctx context.Context) *auth.StoredToken {
	st, err := (&auth.Client{ClientID: clientID()}).Ensure(ctx)
	if err != nil {
		log.Printf("auth: %v", err)
		return nil
	}
	return st
}

// buildModerator returns a Moderator for the session, or nil.
//
// The channel's broadcaster ID is resolved synchronously here so the returned
// Helix is fully constructed — no field is mutated later from another
// goroutine, which is what a ROOMSTATE-driven callback would have required.
func buildModerator(ctx context.Context, channel string, st *auth.StoredToken) tui.Moderator {
	if st == nil {
		return nil
	}
	broadcasterID, err := twitch.UserID(ctx, clientID(), st.AccessToken, channel, nil)
	if err != nil {
		log.Printf("auth: resolving channel %q: %v", channel, err)
		return nil
	}
	return &twitch.Helix{
		ClientID:      clientID(),
		Token:         st.AccessToken,
		ModeratorID:   st.UserID,
		BroadcasterID: broadcasterID,
	}
}
