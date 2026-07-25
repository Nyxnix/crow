//go:build linux

package nowplaying

import (
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	namePrefix = "org.mpris.MediaPlayer2." // one bus name per running player
	objectPath = "/org/mpris/MediaPlayer2"
	playerIf   = "org.mpris.MediaPlayer2.Player"
	propsIf    = "org.freedesktop.DBus.Properties"
)

// The session bus connection is shared and never closed — dbus.SessionBus
// hands out a reference-counted singleton. A failed connect (no D-Bus in the
// session) is cached too, so polling a machine without one costs nothing.
var (
	busOnce sync.Once
	busConn *dbus.Conn
)

func bus() *dbus.Conn {
	busOnce.Do(func() {
		if c, err := dbus.SessionBus(); err == nil {
			busConn = c
		}
	})
	return busConn
}

// Now returns the current track. A playing player wins; if every player is
// paused the first one found is reported, so a paused track still shows.
func Now() Track {
	conn := bus()
	if conn == nil {
		return Track{}
	}
	var names []string
	if err := conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		return Track{}
	}
	var paused Track
	for _, n := range names {
		if !strings.HasPrefix(n, namePrefix) {
			continue
		}
		t := read(conn, n)
		if t.Empty() {
			continue
		}
		if t.Playing {
			return t
		}
		if paused.Empty() {
			paused = t
		}
	}
	return paused
}

func read(conn *dbus.Conn, name string) Track {
	obj := conn.Object(name, objectPath)
	var props map[string]dbus.Variant
	if err := obj.Call(propsIf+".GetAll", 0, playerIf).Store(&props); err != nil {
		return Track{}
	}
	meta, _ := props["Metadata"].Value().(map[string]dbus.Variant)
	t := Track{
		Title:    str(meta["xesam:title"]),
		Artist:   str(meta["xesam:artist"]),
		Album:    str(meta["xesam:album"]),
		Art:      str(meta["mpris:artUrl"]),
		Duration: micros(meta["mpris:length"]),
		Playing:  str(props["PlaybackStatus"]) == "Playing",
	}
	// Position comes from its own Get: several players (Spotify among them)
	// return a stale zero for it in GetAll.
	var pos dbus.Variant
	if err := obj.Call(propsIf+".Get", 0, playerIf, "Position").Store(&pos); err == nil {
		t.Position = micros(pos)
	}
	return t
}

// str reads a metadata string. Artist (and genre, and others) are string lists
// in the spec even when there is only one.
func str(v dbus.Variant) string {
	switch x := v.Value().(type) {
	case string:
		return x
	case []string:
		return strings.Join(x, ", ")
	}
	return ""
}

// micros converts an MPRIS time (microseconds) to a Duration. The spec says
// int64, but players are loose about signedness.
func micros(v dbus.Variant) time.Duration {
	switch x := v.Value().(type) {
	case int64:
		return time.Duration(x) * time.Microsecond
	case uint64:
		return time.Duration(x) * time.Microsecond
	case int32:
		return time.Duration(x) * time.Microsecond
	}
	return 0
}
