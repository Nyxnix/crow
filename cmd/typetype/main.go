// Command typetype reads a live chat in the terminal and serves an overlay for
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
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Nyxnix/typetype/internal/auth"
	"github.com/Nyxnix/typetype/internal/badge"
	"github.com/Nyxnix/typetype/internal/chat"
	"github.com/Nyxnix/typetype/internal/emote"
	"github.com/Nyxnix/typetype/internal/ivr"
	"github.com/Nyxnix/typetype/internal/overlay"
	"github.com/Nyxnix/typetype/internal/tui"
	"github.com/Nyxnix/typetype/internal/twitch"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Subcommands come before flags: `typetype login`, `typetype logout`,
	// `typetype whoami`. Anything else is the default chat/overlay run.
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

	channel := flag.String("channel", "", "Twitch channel to read (required)")
	addr := flag.String("addr", "127.0.0.1:7788", "address for the overlay server")
	headless := flag.Bool("headless", false, "serve the overlay without the terminal UI")
	flag.Parse()

	if *channel == "" {
		usage()
		os.Exit(2)
	}

	exit(run(ctx, *channel, *addr, *headless))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  typetype -channel <name> [-addr host:port] [-headless]")
	fmt.Fprintln(os.Stderr, "  typetype login | logout | whoami")
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "typetype:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, channel, addr string, headless bool) error {
	// Resolve the login once, before starting the UI. A logged-in user reads as
	// themselves, can send, and can moderate; everyone else reads anonymously and
	// the UI explains the gaps.
	session := loadSession(ctx)
	mod := buildModerator(ctx, channel, session)

	ov := overlay.New()
	srv := &http.Server{Addr: addr, Handler: ov.Handler()}
	// Bind before starting the UI so a port clash is a plain error message
	// rather than something that scrolls past behind a full-screen TUI.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	emotes := emote.New()

	// Badge images come from Helix, so they need the login; anonymous sessions
	// get an empty registry that resolves nothing.
	var badgeToken string
	if session != nil {
		badgeToken = session.AccessToken
	}
	badges := badge.New(clientID(), badgeToken)

	fromIRC := make(chan chat.Message, 256)
	toTUI := make(chan chat.Message, 256)
	modIRC := make(chan chat.ModEvent, 64) // deletions/timeouts/bans from the reader
	toTUIMod := make(chan chat.ModEvent, 64)

	// The reader is anonymous even when logged in. An authenticated connection
	// never receives its own PRIVMSGs, so it would never see the user's own
	// messages; an anonymous reader sees the whole channel, including the user's
	// own messages with their real message id and badges — which is what makes
	// the user's own messages deletable. ROOMSTATE arrives after JOIN and again
	// on reconnect; load emotes and badges once.
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

	// The sender is a separate authenticated connection used only to send; its
	// own reads are ignored, since the reader above is the single source of
	// truth for what appears in chat.
	var sender *twitch.Client
	if session != nil {
		sender = &twitch.Client{
			Channel: channel,
			Nick:    session.Login,
			Token:   session.AccessToken,
			Out:     make(chan chat.Message, 16),
		}
		go sender.Run(ctx)
		go func() {
			for range sender.Out { // drain and discard
			}
		}()
	}

	// Apply emotes and badges, then fan out to the overlay and the TUI.
	go func() {
		defer close(toTUI)
		for m := range fromIRC {
			emotes.Apply(&m)
			badges.Resolve(&m)
			ov.Publish(m)
			select {
			case toTUI <- m:
			default: // ponytail: drop rather than stall the overlay if the UI lags
			}
		}
	}()

	// Fan moderation events to the overlay (which removes the messages, so
	// deleted or banned content does not linger on stream) and the TUI (which
	// strikes them through for the moderator).
	go func() {
		defer close(toTUIMod)
		for ev := range modIRC {
			ov.Remove(ev)
			select {
			case toTUIMod <- ev:
			default:
			}
		}
	}()

	if headless {
		log.Printf("overlay: http://%s", addr)
		for range toTUI {
		}
		return nil
	}

	// The send path is present only when logged in, which is also what makes the
	// TUI show its input line. A sent message appears in chat when the anonymous
	// reader receives it back from Twitch, the same as any other message, so
	// there is no local echo to reconcile.
	var sendFn func(string)
	if sender != nil {
		sendFn = sender.Send
	}

	model := tui.NewModel(tui.Options{
		Channel:   channel,
		Incoming:  toTUI,
		ModEvents: toTUIMod,
		Emotes:    emotes,
		Clients:   ov.Clients,
		Mod:       mod,
		Info:      infoProvider{&ivr.Client{}},
		Send:      sendFn,
	})

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	_, err = p.Run()
	return err
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
