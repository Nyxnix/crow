// Package chat defines the platform-agnostic message model that every source
// (Twitch today, YouTube later) converts into, and that the TUI and overlay
// both render from.
package chat

import "time"

// Platform identifies which service a message came from.
type Platform string

const (
	Twitch  Platform = "twitch"
	YouTube Platform = "youtube"
)

// Emote is one image occupying a range of the message text. Start and End are
// indexes into Text's runes, inclusive of Start and exclusive of End, matching
// Go's usual slice convention. Twitch reports its own emotes as inclusive
// ranges; the parser converts them on the way in.
type Emote struct {
	Name  string
	ID    string
	URL   string // highest quality the provider offers
	Start int
	End   int

	// ZeroWidth marks a 7TV overlay emote, which is drawn on top of the emote
	// before it rather than taking its own space. Rendered inline it would look
	// like an unrelated image sitting next to the one it belongs on.
	ZeroWidth bool
}

// Badge is a small image shown before the author's name (broadcaster, mod,
// subscriber, ...). URL is resolved from the badge registry, so it may be empty
// if the registry has not loaded yet.
type Badge struct {
	Name    string
	Version string
	URL     string
}

// Message is one chat line, normalized across platforms.
type Message struct {
	ID       string
	Platform Platform
	Channel  string

	AuthorID    string
	Author      string // display name as the author set it
	AuthorLogin string // lowercase canonical name; the one to use for API calls
	Color       string // "#RRGGBB", empty if the author never set one

	Text   string
	Emotes []Emote
	Badges []Badge

	// Role flags, used for both display and for deciding which mod actions are
	// offered against this author.
	Broadcaster bool
	Moderator   bool
	Subscriber  bool
	VIP         bool

	At time.Time
}

// IsPrivileged reports whether the author outranks a regular viewer. Mod
// actions against these users are hidden in the TUI, since Twitch rejects them
// anyway and offering a button that always fails is worse than no button.
func (m Message) IsPrivileged() bool {
	return m.Broadcaster || m.Moderator
}
