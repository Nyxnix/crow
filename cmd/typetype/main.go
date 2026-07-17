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

	"github.com/Nyxnix/typetype/internal/chat"
	"github.com/Nyxnix/typetype/internal/emote"
	"github.com/Nyxnix/typetype/internal/overlay"
	"github.com/Nyxnix/typetype/internal/tui"
	"github.com/Nyxnix/typetype/internal/twitch"
)

func main() {
	channel := flag.String("channel", "", "Twitch channel to read (required)")
	addr := flag.String("addr", "127.0.0.1:7788", "address for the overlay server")
	headless := flag.Bool("headless", false, "serve the overlay without the terminal UI")
	flag.Parse()

	if *channel == "" {
		fmt.Fprintln(os.Stderr, "usage: typetype -channel <name>")
		flag.PrintDefaults()
		os.Exit(2)
	}

	if err := run(*channel, *addr, *headless); err != nil {
		fmt.Fprintln(os.Stderr, "typetype:", err)
		os.Exit(1)
	}
}

func run(channel, addr string, headless bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

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
	})

	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	_, err = p.Run()
	return err
}
