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
	// Resolve the login, if any, before starting the UI. A logged-in user gets
	// moderation; everyone else reads chat and the card explains the gap.
	mod := setupModerator(ctx, channel)

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

	// Messages are fanned out here: the overlay needs every message, and so does
	// the TUI, but they consume at different rates.
	fromIRC := make(chan chat.Message, 256)
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
			})
		},
	}
	go tw.Run(ctx)

	go func() {
		defer close(toTUI)
		for m := range fromIRC {
			emotes.Apply(&m)
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

	model := tui.NewModel(tui.Options{
		Channel:  channel,
		Incoming: toTUI,
		Emotes:   emotes,
		Clients:  ov.Clients,
		Mod:      mod,
	})

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	_, err = p.Run()
	return err
}

// setupModerator returns a Moderator if the user is logged in, or nil.
//
// A nil interface is what the TUI reads as "not logged in", which the card
// explains. Any auth or lookup problem is logged and returns nil rather than
// blocking chat, which needs no auth at all.
//
// The channel's broadcaster ID is resolved synchronously here so the returned
// Helix is fully constructed — no field is mutated later from another
// goroutine, which is what a ROOMSTATE-driven callback would have required.
func setupModerator(ctx context.Context, channel string) tui.Moderator {
	ac := &auth.Client{ClientID: clientID()}
	st, err := ac.Ensure(ctx)
	if err != nil {
		log.Printf("auth: %v", err)
		return nil
	}
	if st == nil {
		return nil // not logged in
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
