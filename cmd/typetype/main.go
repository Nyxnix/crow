// Command typetype reads a live chat in the terminal and serves an overlay for
// OBS to point a browser source at.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"

	"github.com/Nyxnix/typetype/internal/chat"
	"github.com/Nyxnix/typetype/internal/emote"
	"github.com/Nyxnix/typetype/internal/overlay"
	"github.com/Nyxnix/typetype/internal/twitch"
)

func main() {
	channel := flag.String("channel", "", "Twitch channel to read (required)")
	addr := flag.String("addr", "127.0.0.1:7788", "address for the overlay server")
	flag.Parse()

	if *channel == "" {
		fmt.Fprintln(os.Stderr, "usage: typetype -channel <name>")
		flag.PrintDefaults()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ov := overlay.New()
	srv := &http.Server{Addr: *addr, Handler: ov.Handler()}
	go func() {
		log.Printf("overlay: http://%s", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("overlay: %v", err)
		}
	}()
	defer srv.Close()

	emotes := emote.New()

	// ROOMSTATE arrives right after JOIN and fires again on every reconnect;
	// load once and let later reconnects reuse what we already have.
	var loadOnce sync.Once
	tw := &twitch.Client{
		Channel: *channel,
		Out:     make(chan chat.Message, 256),
		OnRoomID: func(id string) {
			loadOnce.Do(func() {
				go func() {
					if err := emotes.Load(ctx, id); err != nil {
						log.Printf("emotes: %v", err)
						return
					}
					log.Printf("emotes: %d loaded for room %s", emotes.Len(), id)
				}()
			})
		},
	}
	go tw.Run(ctx)

	for m := range tw.Out {
		emotes.Apply(&m)
		ov.Publish(m)
		log.Printf("%s: %s", m.Author, m.Text)
	}
}
