package config

import "testing"

func TestOverlayURL(t *testing.T) {
	// Always a bare "/": the server pushes options to the page, nothing rides
	// the URL.
	if got := Default().OverlayURL(); got != "http://127.0.0.1:7788/" {
		t.Errorf("default url = %q, want a bare /", got)
	}
	c := Default()
	c.Overlay.Align = "top"
	c.Overlay.Size = 28
	if got := c.OverlayURL(); got != "http://127.0.0.1:7788/" {
		t.Errorf("url = %q, want a bare / regardless of options", got)
	}
}
