package chat

import "strings"

// Source is one platform channel a tab reads from.
type Source struct {
	Platform Platform
	Channel  string
}

// ParseSpec parses a tab spec into its sources. A spec is one or more parts
// joined with "+", each either a Twitch channel name ("#" optional,
// case-insensitive) or a YouTube target prefixed "yt:"/"youtube:" (an @handle,
// video ID, or URL — case preserved, since video IDs are case-sensitive). A
// bare youtube.com/youtu.be URL is recognized without the prefix.
//
// It returns the canonical spec string, which is the tab's identity (dedup,
// persistence, overlay pinning), and the parsed sources. An empty or
// all-garbage spec returns ("", nil).
func ParseSpec(spec string) (string, []Source) {
	var srcs []Source
	var parts []string
	for _, p := range strings.Split(spec, "+") {
		p = strings.TrimSpace(p)
		lower := strings.ToLower(p)
		switch {
		case strings.HasPrefix(lower, "yt:"), strings.HasPrefix(lower, "youtube:"):
			_, rest, _ := strings.Cut(p, ":")
			if rest = strings.TrimSpace(rest); rest != "" {
				srcs = append(srcs, Source{YouTube, rest})
				parts = append(parts, "yt:"+rest)
			}
		case strings.Contains(lower, "youtube.com/"), strings.Contains(lower, "youtu.be/"):
			srcs = append(srcs, Source{YouTube, p})
			parts = append(parts, "yt:"+p)
		default:
			if ch := strings.ToLower(strings.TrimPrefix(p, "#")); ch != "" {
				srcs = append(srcs, Source{Twitch, ch})
				parts = append(parts, ch)
			}
		}
	}
	return strings.Join(parts, "+"), srcs
}
