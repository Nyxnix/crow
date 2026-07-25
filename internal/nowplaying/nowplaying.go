// Package nowplaying reports what a local media player is playing, for the
// now-playing browser source.
//
// It reads the player's own state rather than any streaming service's API, so
// anything on the machine — Spotify, mpv, VLC, a browser tab, Rhythmbox — works
// with no per-player setup and no login. On Linux that is MPRIS over D-Bus,
// which effectively every player speaks; other platforms report nothing yet.
package nowplaying

import "time"

// Track is what a player is currently on. The zero Track means nothing is
// playing (or no player is running).
type Track struct {
	Title    string
	Artist   string
	Album    string
	Art      string // artwork as the player gives it: a file:// or http(s) URL
	Position time.Duration
	Duration time.Duration
	Playing  bool // false while paused
}

// Empty reports whether there is nothing to show.
func (t Track) Empty() bool { return t.Title == "" && t.Artist == "" }
