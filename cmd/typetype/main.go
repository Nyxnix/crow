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

	"github.com/Nyxnix/typetype/internal/chat"
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

	tw := &twitch.Client{Channel: *channel, Out: make(chan chat.Message, 256)}
	go tw.Run(ctx)

	for m := range tw.Out {
		ov.Publish(m)
		log.Printf("%s: %s", m.Author, m.Text)
	}
}
