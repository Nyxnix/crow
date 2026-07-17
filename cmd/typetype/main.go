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

	// Messages are fanned out here: the overlay needs every message, and so does
	// the TUI, but they consume at different rates.
	fromIRC := make(chan chat.Message, 256)
	echoCh := make(chan chat.Message, 32) // the user's own sent messages
	toTUI := make(chan chat.Message, 256)

	// ROOMSTATE arrives after JOIN and again on every reconnect; load once.
	var loadOnce sync.Once
	tw := &twitch.Client{
		Channel: channel,
		Out:     fromIRC,
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
	// Connect authenticated when logged in, so the user can send and their own
	// badges resolve; otherwise the client falls back to an anonymous read.
	if session != nil {
		tw.Nick = session.Login
		tw.Token = session.AccessToken
	}
	go tw.Run(ctx)

	// Merge live chat and the user's own echoes into one stream, apply emotes,
	// and fan out. Twitch does not echo a client's own PRIVMSGs back, so a sent
	// message only appears here because we inject it.
	go func() {
		defer close(toTUI)
		for fromIRC != nil {
			var m chat.Message
			select {
			case msg, ok := <-fromIRC:
				if !ok {
					fromIRC = nil // IRC stopped; drain no more from it
					continue
				}
				m = msg
			case m = <-echoCh:
			}
			emotes.Apply(&m)
			badges.Resolve(&m)
			ov.Publish(m)
			select {
			case toTUI <- m:
			default: // ponytail: drop rather than stall the overlay if the UI lags
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
	// TUI show its input line.
	var sendFn func(string)
	if session != nil {
		sendFn = func(text string) {
			tw.Send(text)
			// Echo carries the user's real badges/color/display name from
			// USERSTATE, so a sent message looks the same locally as it does to
			// everyone else. Badges get their image URLs in the fan-out below.
			select {
			case echoCh <- tw.Echo(text, session.UserID, session.Login):
			default:
			}
		}
	}

	model := tui.NewModel(tui.Options{
		Channel:  channel,
		Incoming: toTUI,
		Emotes:   emotes,
		Clients:  ov.Clients,
		Mod:      mod,
		Send:     sendFn,
	})

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	_, err = p.Run()
	return err
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
