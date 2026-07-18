package chat

import (
	"reflect"
	"testing"
)

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in    string
		canon string
		srcs  []Source
	}{
		{"Caedrel", "caedrel", []Source{{Twitch, "caedrel"}}},
		{"#Caedrel", "caedrel", []Source{{Twitch, "caedrel"}}},
		{"yt:@LofiGirl", "yt:@LofiGirl", []Source{{YouTube, "@LofiGirl"}}},
		{"YouTube:dQw4w9WgXcQ", "yt:dQw4w9WgXcQ", []Source{{YouTube, "dQw4w9WgXcQ"}}},
		{"https://youtu.be/dQw4w9WgXcQ", "yt:https://youtu.be/dQw4w9WgXcQ", []Source{{YouTube, "https://youtu.be/dQw4w9WgXcQ"}}},
		{"a + yt:@B", "a+yt:@B", []Source{{Twitch, "a"}, {YouTube, "@B"}}},
		{" ", "", nil},
		{"+#+yt: ", "", nil},
	}
	for _, c := range cases {
		canon, srcs := ParseSpec(c.in)
		if canon != c.canon || !reflect.DeepEqual(srcs, c.srcs) {
			t.Errorf("ParseSpec(%q) = %q, %v; want %q, %v", c.in, canon, srcs, c.canon, c.srcs)
		}
	}
}
