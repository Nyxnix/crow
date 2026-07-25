//go:build linux

package nowplaying

import (
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakePlayer answers the two property calls Now makes, the way a real MPRIS
// player does: artist is a string list, times are microseconds.
type fakePlayer struct {
	meta   map[string]dbus.Variant
	status string
	pos    int64
}

func (f *fakePlayer) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	return map[string]dbus.Variant{
		"Metadata":       dbus.MakeVariant(f.meta),
		"PlaybackStatus": dbus.MakeVariant(f.status),
		// Deliberately stale, like Spotify's: Now must not trust it.
		"Position": dbus.MakeVariant(int64(0)),
	}, nil
}

func (f *fakePlayer) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	if prop == "Position" {
		return dbus.MakeVariant(f.pos), nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(dbus.ErrMsgUnknownMethod)
}

// export publishes a fake player under its own bus name, on its own connection
// (one MPRIS object per name).
func export(t *testing.T, name string, p *fakePlayer) {
	t.Helper()
	conn, err := dbus.SessionBusPrivate()
	if err != nil {
		t.Skipf("no session bus: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.Auth(nil); err != nil {
		t.Skipf("no session bus: %v", err)
	}
	if err := conn.Hello(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Export(p, objectPath, propsIf); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName(namePrefix+name, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
}

func TestNowPrefersThePlayingPlayer(t *testing.T) {
	export(t, "paused", &fakePlayer{
		status: "Paused",
		meta:   map[string]dbus.Variant{"xesam:title": dbus.MakeVariant("Other")},
	})
	export(t, "playing", &fakePlayer{
		status: "Playing",
		pos:    int64(90 * time.Second / time.Microsecond),
		meta: map[string]dbus.Variant{
			"xesam:title":  dbus.MakeVariant("Song"),
			"xesam:artist": dbus.MakeVariant([]string{"A", "B"}),
			"xesam:album":  dbus.MakeVariant("Album"),
			"mpris:artUrl": dbus.MakeVariant("file:///tmp/cover.png"),
			"mpris:length": dbus.MakeVariant(int64(200 * time.Second / time.Microsecond)),
		},
	})

	got := Now()
	want := Track{
		Title: "Song", Artist: "A, B", Album: "Album", Art: "file:///tmp/cover.png",
		Position: 90 * time.Second, Duration: 200 * time.Second, Playing: true,
	}
	if got != want {
		t.Errorf("Now() = %+v\nwant %+v", got, want)
	}
}
