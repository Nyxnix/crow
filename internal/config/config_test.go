package config

import "testing"

func TestOverlayURL(t *testing.T) {
	// Stock defaults encode nothing: a clean browser-source URL.
	if got := Default().OverlayURL(); got != "http://127.0.0.1:7788/" {
		t.Errorf("default url = %q, want a bare /", got)
	}

	// Only non-default options appear, and toggles-off become explicit 0s.
	c := Default()
	c.Overlay.Align = "top"
	c.Overlay.Font = 3
	c.Overlay.Size = 28
	c.Overlay.Animate = false
	c.Overlay.Badges = false
	c.Overlay.HideCommands = true
	c.Overlay.Bots = "nightbot,streamelements"
	got := c.OverlayURL()
	want := "http://127.0.0.1:7788/?align=top&animate=0&badges=0&bots=nightbot%2Cstreamelements&font=3&hide_commands=1&size=28"
	if got != want {
		t.Errorf("url =\n %q\nwant\n %q", got, want)
	}
}
