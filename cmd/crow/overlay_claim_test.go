package main

import (
	"testing"

	"github.com/Nyxnix/crow/internal/chat"
)

func TestOverlayClaimSubset(t *testing.T) {
	tw := chat.Source{Platform: chat.Twitch, Channel: "nyxnx_"}
	yt := chat.Source{Platform: chat.YouTube, Channel: "870AYEFTc7Y"}
	combined := []chat.Source{tw, yt}

	// A pin naming just the Twitch channel selects a combined tab that adds a
	// YouTube source — the workflow for an unlisted stream whose video ID churns.
	o := &overlayState{enabled: true, pin: []chat.Source{tw}}
	if !o.claim("nyxnx_+yt:870AYEFTc7Y", combined) {
		t.Fatal("subset pin should claim a superset tab")
	}
	// Owned now: a different tab can't take it.
	if o.claim("someoneelse", []chat.Source{{Platform: chat.Twitch, Channel: "someoneelse"}}) {
		t.Error("a non-owner tab claimed an owned overlay")
	}
	// The owner re-claims idempotently (reconnect).
	if !o.claim("nyxnx_+yt:870AYEFTc7Y", combined) {
		t.Error("owner should re-claim")
	}

	// Empty pin: first tab wins.
	if o2 := (&overlayState{enabled: true}); !o2.claim("anyone", []chat.Source{tw}) {
		t.Error("empty pin should let the first tab claim")
	}

	// A pin source absent from the tab does not match.
	o3 := &overlayState{enabled: true, pin: []chat.Source{yt}}
	if o3.claim("nyxnx_", []chat.Source{tw}) {
		t.Error("pin source missing from tab should not claim")
	}

	// Disabled overlay never claims.
	if (&overlayState{enabled: false}).claim("nyxnx_", []chat.Source{tw}) {
		t.Error("disabled overlay claimed")
	}
}

func TestOverlayUnclaimed(t *testing.T) {
	tw := chat.Source{Platform: chat.Twitch, Channel: "nyxnx_"}
	yt := chat.Source{Platform: chat.YouTube, Channel: "870AYEFTc7Y"}

	// Pinned but no matching tab open: warn.
	o := &overlayState{enabled: true, pin: []chat.Source{yt}}
	o.claim("nyxnx_", []chat.Source{tw})
	if !o.unclaimed() {
		t.Error("pinned overlay with no matching tab should report unclaimed")
	}
	// A matching tab claims: warning clears.
	o.claim("yt:870AYEFTc7Y", []chat.Source{yt})
	if o.unclaimed() {
		t.Error("claimed overlay should not report unclaimed")
	}
	// That tab closes: warning returns.
	o.release("yt:870AYEFTc7Y")
	if !o.unclaimed() {
		t.Error("released overlay should report unclaimed again")
	}

	// Empty pin never warns — the first tab always qualifies.
	if (&overlayState{enabled: true}).unclaimed() {
		t.Error("empty pin should not warn")
	}
	// Disabled overlay never warns.
	if (&overlayState{enabled: false, pin: []chat.Source{yt}}).unclaimed() {
		t.Error("disabled overlay should not warn")
	}
}
