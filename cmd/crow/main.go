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
	"slices"
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
	"github.com/Nyxnix/crow/internal/youtube"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Subcommands come before flags: `crow login`, `crow logout`,
	// `crow whoami`. Anything else is the default chat/overlay run.
	if len(os.Args) > 1 {
		yt := len(os.Args) > 2 && (os.Args[2] == "youtube" || os.Args[2] == "yt")
		switch os.Args[1] {
		case "login":
			if yt {
				exit(loginYouTube(ctx))
				return
			}
			exit(login(ctx))
			return
		case "logout":
			if yt {
				exit(youtube.ClearToken())
				return
			}
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

	channel := flag.String("channel", "", "chats to open, comma-separated: a Twitch channel, yt:<handle|video|url> for YouTube, or a+yt:b to combine sources in one tab")
	addr := flag.String("addr", "", "address for the overlay server (overrides the saved config)")
	headless := flag.Bool("headless", false, "serve the overlay without the terminal UI (needs -channel)")
	flag.Parse()

	var channels []string
	for _, c := range strings.Split(*channel, ",") {
		if canon, _ := chat.ParseSpec(c); canon != "" {
			channels = append(channels, canon)
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
	fmt.Fprintln(os.Stderr, "  crow login | logout | whoami       (Twitch)")
	fmt.Fprintln(os.Stderr, "  crow login youtube | logout youtube")
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
	ov.SetOptions(cfg.Overlay)
	ov.SetAlertOptions(cfg.Alerts)
	if cfg.OverlayEnabled || headless {
		ln, err := net.Listen("tcp", cfg.OverlayAddr)
		if err != nil {
			return fmt.Errorf("overlay: %w", err)
		}
		srv := &http.Server{Handler: ov.Handler()}
		go srv.Serve(ln)
		defer srv.Close()
		// Push config-file edits to connected browser sources as they happen, so
		// the overlay restyles without touching OBS. Polling mtime keeps it
		// dependency-free; the TUI's own saves push directly and this is a no-op.
		go watchConfig(ctx, ov)
	}

	// The pin is matched by source, not exact spec, so pinning a stable channel
	// (a Twitch name) still selects a combined tab that adds other sources —
	// which survives an unlisted YouTube video ID changing between streams.
	_, pinSources := chat.ParseSpec(cfg.OverlayChannel)
	ovState := &overlayState{
		pin:     pinSources,
		enabled: cfg.OverlayEnabled || headless,
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

	// Test alerts cycle through every kind with realistic sentences, so the
	// OBS popup can be positioned and styled without waiting for real events.
	// Only the kinds that really carry a user message (subs, resubs, bits,
	// superchats, member milestones) get an attached one.
	testAlerts := []struct {
		kind chat.AlertKind
		text string
		msg  string
	}{
		{chat.AlertFollow, "TestUser followed!", ""},
		{chat.AlertSub, "TestUser subscribed at Tier 1.", "an attached test message"},
		{chat.AlertResub, "TestUser subscribed for 12 months!", "an attached test message"},
		{chat.AlertGift, "TestUser is gifting 5 subs!", ""},
		{chat.AlertBits, "TestUser cheered 500 bits", "an attached test message"},
		{chat.AlertMember, "TestUser became a member", "an attached test message"},
		{chat.AlertGiftMember, "TestUser gifted 5 memberships", ""},
		{chat.AlertSuperchat, "TestUser sent $5.00", "an attached test message"},
	}
	var testAlertN int

	app = tui.NewApp(tui.AppOptions{
		Factory:            factory,
		Login:              login,
		FollowScopeMissing: session != nil && !slices.Contains(session.Scope, "moderator:read:followers"),
		TestAlert: func() chat.Message {
			ta := testAlerts[testAlertN%len(testAlerts)]
			testAlertN++
			m := chat.Message{
				Platform: chat.Twitch, Channel: "test",
				AuthorID: "0", Author: "TestUser", AuthorLogin: "testuser", Color: "#FF69B4",
				Alert: ta.kind, AlertText: ta.text,
				Text: ta.msg, At: time.Now(),
			}
			ov.PublishAlert(m)
			return m
		},
		Config: cfg,
		Save: func(c config.Config) {
			config.Save(c)
			ov.SetOptions(c.Overlay) // restyle connected browser sources live
			ov.SetAlertOptions(c.Alerts)
		},
		Channels:        initial,
		RequestCode:     requestDeviceCode,
		PollLogin:       pollLogin,
		Logout:          func() { auth.Clear() },
		YTVerify:        ytVerifyCookies,
		YTCookiesAuthed: func() bool { return config.Load().YouTubeCookies != "" },
		YTCookiesLogout: ytClearCookies,
		YTOAuthStart:    ytRequestDeviceCode,
		YTOAuthPoll:     ytPollLogin,
		YTOAuthAuthed:   func() bool { t, _ := youtube.LoadToken(); return t != nil },
		YTOAuthLogout:   func() { youtube.ClearToken() },
	})

	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	_, err := p.Run()
	fmt.Print("\x1b[?7h") // re-enable autowrap; the TUI disables it while drawing
	return err
}

// watchConfig polls the config file's mtime and pushes the overlay options to
// connected browser sources when it changes, so hand-editing the file restyles
// the overlay live. ponytail: 2s mtime polling over fsnotify — no new
// dependency, and a human editing a file can't feel two seconds.
func watchConfig(ctx context.Context, ov *overlay.Server) {
	var last time.Time
	if st, err := os.Stat(config.Path()); err == nil {
		last = st.ModTime()
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st, err := os.Stat(config.Path())
			if err != nil || st.ModTime().Equal(last) {
				continue
			}
			last = st.ModTime()
			c := config.Load()
			ov.SetOptions(c.Overlay)
			ov.SetAlertOptions(c.Alerts)
		}
	}
}

// overlayState tracks which tab currently feeds the overlay. Only one does, so
// deleted/banned content is scoped and tab switching never disrupts it.
type overlayState struct {
	mu      sync.Mutex
	enabled bool          // overlay off in config: no tab ever publishes
	pin     []chat.Source // sources the config pins to; empty = first opened tab
	owner   string        // spec of the tab that owns the overlay
}

// claim reports whether the tab with this spec (carrying these sources) should
// publish to the overlay, taking ownership if it is free. A tab qualifies when
// the pin is empty (first opened tab wins) or every pinned source is present in
// the tab — so a pin can name a subset (e.g. just the Twitch channel) and still
// select a combined tab.
func (o *overlayState) claim(spec string, sources []chat.Source) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.enabled {
		return false
	}
	if o.owner == spec {
		return true
	}
	if o.owner == "" && sourcesContain(sources, o.pin) {
		o.owner = spec
		return true
	}
	return false
}

// sourcesContain reports whether every source in want appears in have.
func sourcesContain(have, want []chat.Source) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// unclaimed reports whether the overlay is enabled and pinned but no open tab
// matched the pin — the overlay is blank and the user should know why.
func (o *overlayState) unclaimed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.enabled && len(o.pin) > 0 && o.owner == ""
}

func (o *overlayState) release(channel string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.owner == channel {
		o.owner = ""
	}
}

// sourceFeed is one running source's output streams, plus the registries its
// messages need applied (Twitch third-party emotes/badges; nil for YouTube,
// whose messages carry their emotes inline).
type sourceFeed struct {
	msgs   chan chat.Message
	events chan chat.ModEvent
	emotes *emote.Registry
	badges *badge.Registry
}

// startSource starts one platform reader under ctx and returns its feed. The
// caller pumps the channels; they close when ctx ends.
func startSource(ctx context.Context, src chat.Source, session *auth.StoredToken) sourceFeed {
	out := make(chan chat.Message, 256)
	events := make(chan chat.ModEvent, 64)

	if src.Platform == chat.YouTube {
		yc := &youtube.Client{Channel: src.Channel, Out: out, Events: events}
		go yc.Run(ctx)
		return sourceFeed{msgs: out, events: events}
	}

	emotes := emote.New()
	var badgeToken string
	if session != nil {
		badgeToken = session.AccessToken
	}
	badges := badge.New(clientID(), badgeToken)
	var loadOnce sync.Once
	reader := &twitch.Client{
		Channel: src.Channel,
		Out:     out,
		Events:  events,
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
	return sourceFeed{msgs: out, events: events, emotes: emotes, badges: badges}
}

// applyRegistries decorates a Twitch message with third-party emotes and badge
// URLs; a YouTube feed has no registries and passes through untouched.
func (f sourceFeed) apply(m *chat.Message) {
	if f.emotes != nil {
		f.emotes.Apply(m)
	}
	if f.badges != nil {
		f.badges.Resolve(m)
	}
}

// openChannel wires a tab's sources (one Twitch channel, a YouTube stream, or
// several of either combined with "+") into one chat model, under a
// sub-context that its returned close func cancels.
func openChannel(parent context.Context, spec string, ov *overlay.Server, ovState *overlayState, redraw func()) (*tui.Model, func()) {
	ctx, cancel := context.WithCancel(parent)

	// Reload the session per open, so a login completed on the splash applies to
	// channels opened afterwards.
	session := loadSession(ctx)
	spec, sources := chat.ParseSpec(spec)
	toOverlay := ovState.claim(spec, sources)

	feeds := make([]sourceFeed, len(sources))
	for i, s := range sources {
		feeds[i] = startSource(ctx, s, session)
	}

	// Sending, moderation, user info and stream stats are Twitch APIs tied to a
	// single channel, so only a plain one-Twitch-channel tab gets them; combined
	// and YouTube tabs are read-only.
	single := len(sources) == 1 && sources[0].Platform == chat.Twitch
	var sendFn func(string)
	var mod tui.Moderator
	var statsFn func() tui.StreamStats
	var info tui.InfoProvider
	var emotes *emote.Registry
	var helix *twitch.Helix // kept concrete: the follow poller needs BroadcasterID
	if single {
		channel := sources[0].Channel
		emotes = feeds[0].emotes
		info = infoProvider{&ivr.Client{}}
		if helix = buildModerator(ctx, channel, session); helix != nil {
			mod = helix // assign only when non-nil: a typed nil would fake a Moderator
		}
		if session != nil {
			sender := &twitch.Client{Channel: channel, Nick: session.Login, Token: session.AccessToken, Out: make(chan chat.Message, 16)}
			go sender.Run(ctx)
			go func() {
				for range sender.Out {
				}
			}()
			sendFn = sender.Send

			poller := &twitch.StreamPoller{ClientID: clientID(), Token: session.AccessToken, Login: channel, Interval: time.Minute, OnUpdate: redraw}
			statsFn = func() tui.StreamStats {
				s := poller.Snapshot()
				return tui.StreamStats{Live: s.Live, Viewers: s.Viewers, AvgViewers: s.AvgViewers, Uptime: s.Uptime}
			}
			go poller.Run(ctx)
		}
	}
	// A single YouTube tab gets a send box, user-card info and mod actions once
	// the user has authed in settings (cookies) or `crow login youtube`
	// (OAuth). YouTube rejects mod calls from non-moderators, which the card
	// surfaces just like Helix rejections.
	sendLimit := 0
	var observe func(chat.Message) // CookieMod needs to see messages for their tokens
	if len(sources) == 1 && sources[0].Platform == chat.YouTube {
		if s := youtubeSession(sources[0].Channel); s.send != nil {
			sendLimit = 200 // YouTube's chat message cap
			sendFn = func(text string) {
				go func() {
					sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
					defer cancel()
					if err := s.send(sctx, text); err != nil {
						log.Printf("youtube send: %v", err)
					}
				}()
			}
			mod = s.mod
			info = s.info
			observe = s.observe
		}
	}
	if sendFn == nil && os.Getenv("CROW_FAKE_INPUT") == "1" {
		sendFn = func(string) {} // debug: show the input line without a login
	}

	var clientsFn func() int
	if toOverlay {
		clientsFn = ov.Clients
	}

	login := ""
	if session != nil {
		login = session.Login
	}
	model := tui.NewModel(tui.Options{
		Channel:          spec,
		Emotes:           emotes,
		Mod:              mod,
		Info:             info,
		Clients:          clientsFn,
		OverlayUnclaimed: ovState.unclaimed,
		Stats:            statsFn,
		Send:             sendFn,
		SendLimit:        sendLimit,
		OnRedraw:         redraw,
		Login:            login,
	})

	// Follow alerts have no chat feed: a Helix poller synthesizes them straight
	// into the model and alerts overlay. Only on a single Twitch tab, logged in
	// with the follower scope, and with follow alerts switched on.
	if cfg := config.Load(); helix != nil && cfg.Alerts.Enabled && cfg.Alerts.Follows &&
		slices.Contains(session.Scope, "moderator:read:followers") {
		channel := sources[0].Channel
		fp := &twitch.FollowPoller{
			ClientID:      clientID(),
			Token:         session.AccessToken,
			BroadcasterID: helix.BroadcasterID,
			OnFollow: func(id, name string) {
				m := chat.Message{
					Platform:    chat.Twitch,
					Channel:     channel,
					AuthorID:    id,
					Author:      name,
					AuthorLogin: strings.ToLower(name),
					Alert:       chat.AlertFollow,
					AlertText:   name + " followed!",
					At:          time.Now(),
				}
				if toOverlay {
					ov.PublishAlert(m)
				}
				model.Append(m)
			},
		}
		go fp.Run(ctx)
	}

	// Debug: inject one synthetic alert of each kind shortly after open, so the
	// chat line, alerts page and settings toggles can be exercised offline.
	if os.Getenv("CROW_FAKE_ALERTS") == "1" {
		go func() {
			time.Sleep(2 * time.Second)
			kinds := []chat.AlertKind{
				chat.AlertFollow, chat.AlertSub, chat.AlertResub, chat.AlertGift,
				chat.AlertBits, chat.AlertMember, chat.AlertGiftMember, chat.AlertSuperchat,
			}
			for _, k := range kinds {
				text := ""
				switch k { // only these kinds carry a user message in reality
				case chat.AlertSub, chat.AlertResub, chat.AlertBits, chat.AlertSuperchat, chat.AlertMember:
					text = "an attached message"
				}
				m := chat.Message{
					Platform: chat.Twitch, Channel: spec,
					AuthorID: "0", Author: "FakeUser", AuthorLogin: "fakeuser", Color: "#FF69B4",
					Alert: k, AlertText: "FakeUser fired a " + string(k) + " alert",
					Text: text, At: time.Now(),
				}
				if toOverlay {
					ov.PublishAlert(m)
				}
				model.Append(m)
			}
		}()
	}

	// Ingest every source into the one model: apply registries, publish to the
	// overlay if this tab owns it, and append. Moderation likewise.
	for _, f := range feeds {
		f := f
		go func() {
			for m := range f.msgs {
				f.apply(&m)
				if observe != nil {
					observe(m) // record YouTube moderation tokens
				}
				if toOverlay {
					// An alert with no attached message is not a chat line; it goes
					// only to the alerts page. PublishAlert ignores non-alerts.
					if m.Alert == "" || m.Text != "" {
						ov.Publish(m)
					}
					ov.PublishAlert(m)
				}
				model.Append(m)
			}
		}()
		go func() {
			for ev := range f.events {
				if toOverlay {
					ov.Remove(ev)
				}
				model.ApplyModEvent(ev)
			}
		}()
	}

	return model, func() {
		cancel()
		ovState.release(spec)
	}
}

// ytSession bundles a YouTube tab's authenticated capabilities. send/mod/info
// are nil when the user hasn't authed; observe is set only for cookie mode,
// where the mod needs to see messages for their per-message tokens.
type ytSession struct {
	send    func(context.Context, string) error
	mod     tui.Moderator
	info    tui.InfoProvider
	observe func(chat.Message)
}

// youtubeSession picks the best available YouTube auth for a stream: cookie
// auth (the primary, quota-free path) if the user pasted cookies, else the
// OAuth/Data-API path if they ran `crow login youtube`, else an empty session.
func youtubeSession(target string) ytSession {
	cfg := config.Load()

	if ca := (&youtube.CookieAuth{Cookies: cfg.YouTubeCookies}); ca.Valid() {
		sender := &youtube.CookieSender{Video: target, Auth: ca}
		cmod := &youtube.CookieMod{Sender: sender}
		return ytSession{
			send:    sender.Send,
			mod:     cmod,
			info:    ytCookieInfo{ca},
			observe: cmod.Observe,
		}
	}

	if cfg.YouTubeClientID != "" && cfg.YouTubeClientSecret != "" {
		if tok, err := youtube.LoadToken(); err == nil && tok != nil {
			ya := &youtube.Auth{ClientID: cfg.YouTubeClientID, ClientSecret: cfg.YouTubeClientSecret}
			sender := &youtube.Sender{Video: target, Auth: ya}
			return ytSession{send: sender.Send, mod: &youtube.Mod{Sender: sender}, info: ytInfoProvider{auth: ya}}
		}
	}
	return ytSession{}
}

// ytCookieInfo serves the user card for cookie-auth YouTube tabs.
type ytCookieInfo struct{ auth *youtube.CookieAuth }

func (p ytCookieInfo) CardInfo(ctx context.Context, userLogin, _ string) (tui.UserInfo, error) {
	ci, err := p.auth.ChannelInfo(ctx, userLogin)
	return tui.UserInfo{CreatedAt: ci.Created, AvatarURL: ci.AvatarURL, Subscribers: ci.Subs}, err
}

// runHeadless serves the overlay for one tab spec with no terminal UI.
func runHeadless(ctx context.Context, spec, addr string, ov *overlay.Server, ovState *overlayState) error {
	spec, sources := chat.ParseSpec(spec)
	ovState.claim(spec, sources)
	session := loadSession(ctx)
	for _, src := range sources {
		f := startSource(ctx, src, session)
		go func() {
			for ev := range f.events {
				ov.Remove(ev)
			}
		}()
		go func() {
			for m := range f.msgs {
				f.apply(&m)
				if m.Alert == "" || m.Text != "" {
					ov.Publish(m)
				}
				ov.PublishAlert(m)
			}
		}()
	}
	log.Printf("overlay: http://%s", addr)
	<-ctx.Done()
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

// ytVerifyCookies checks a pasted youtube.com cookie header by fetching the
// account name. The settings page has already saved the cookies to the config
// (so a tab opened right after picks them up); this just proves they work.
func ytVerifyCookies(cookies string) (name string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return (&youtube.CookieAuth{Cookies: cookies}).WhoAmI(ctx)
}

// ytClearCookies logs out of YouTube by dropping the stored cookies.
func ytClearCookies() {
	cfg := config.Load()
	cfg.YouTubeCookies = ""
	config.Save(cfg)
}

// ytHandle carries the OAuth client and device code between the settings
// page's two device-flow steps.
type ytHandle struct {
	auth *youtube.Auth
	dc   *youtube.DeviceCode
}

// ytRequestDeviceCode / ytPollLogin drive the settings page's Google OAuth
// login, the same two-step shape as the Twitch device flow. The client
// id/secret are already committed to the config by the settings save; the
// verified token is written to disk by ytPollLogin.
func ytRequestDeviceCode(clientID, clientSecret string) (code, url string, handle any, err error) {
	ya := &youtube.Auth{ClientID: clientID, ClientSecret: clientSecret}
	dc, err := ya.RequestDeviceCode(context.Background())
	if err != nil {
		return "", "", nil, err
	}
	return dc.UserCode, dc.VerificationURL, &ytHandle{auth: ya, dc: dc}, nil
}

func ytPollLogin(handle any) (name string, err error) {
	h, ok := handle.(*ytHandle)
	if !ok {
		return "", fmt.Errorf("bad login handle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	tok, err := h.auth.PollToken(ctx, h.dc)
	if err != nil {
		return "", err
	}
	// Identify the account; this also proves the token works before saving.
	name, err = h.auth.WhoAmI(ctx, tok.AccessToken)
	if err != nil {
		return "", err
	}
	if err := youtube.SaveToken(tok); err != nil {
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
		AvatarURL:  i.AvatarURL,
		FollowedAt: i.FollowedAt,
		SubTier:    i.SubTier,
		SubMonths:  i.SubMonths,
		SubHidden:  i.SubHidden,
	}, err
}

// ytInfoProvider serves the user card for YouTube tabs from the Data API.
// The channel argument is unused: a YouTube author is identified globally by
// their channel ID, which arrives as the card's "login".
type ytInfoProvider struct{ auth *youtube.Auth }

func (p ytInfoProvider) CardInfo(ctx context.Context, userLogin, _ string) (tui.UserInfo, error) {
	ci, err := p.auth.ChannelInfo(ctx, userLogin)
	return tui.UserInfo{
		CreatedAt:   ci.Created,
		AvatarURL:   ci.AvatarURL,
		Subscribers: ci.Subs,
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
func buildModerator(ctx context.Context, channel string, st *auth.StoredToken) *twitch.Helix {
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
