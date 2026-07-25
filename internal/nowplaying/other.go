//go:build !linux

package nowplaying

// Now reports nothing off Linux: the now-playing source needs a local player
// protocol, and only MPRIS is implemented so far.
func Now() Track { return Track{} }
