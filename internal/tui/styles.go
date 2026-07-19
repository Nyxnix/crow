package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Nyxnix/crow/internal/chat"
)

// styles holds every lipgloss style the TUI renders with, so colors are decided
// in one place rather than scattered through the render code.
type styles struct {
	text   lipgloss.Style
	punct  lipgloss.Style
	dim    lipgloss.Style
	status lipgloss.Style

	cardBorder lipgloss.Style
	cardTitle  lipgloss.Style
	cardLabel  lipgloss.Style
	cardKey    lipgloss.Style

	badgeBroadcaster lipgloss.Style
	badgeMod         lipgloss.Style
	badgeVIP         lipgloss.Style
	badgeSub         lipgloss.Style

	danger    lipgloss.Style
	deleted   lipgloss.Style
	highlight lipgloss.Style // red background: alert lines and mentions of the user
	pin       lipgloss.Style // the pinned-message row above the input

	// login is the logged-in user's lowercase name, for mention detection.
	// Empty when not logged in, which disables the mention highlight.
	login string

	tabBar      lipgloss.Style
	tabActive   lipgloss.Style
	tabInactive lipgloss.Style

	splashTitle lipgloss.Style
	key         lipgloss.Style
}

func newStyles() *styles {
	return &styles{
		text:   lipgloss.NewStyle(),
		punct:  lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		dim:    lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		status: lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("236")),

		cardBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("99")).
			Padding(0, 1),
		cardTitle: lipgloss.NewStyle().Bold(true),
		cardLabel: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		cardKey:   lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true),

		badgeBroadcaster: lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true),
		badgeMod:         lipgloss.NewStyle().Foreground(lipgloss.Color("46")).Bold(true),
		badgeVIP:         lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true),
		badgeSub:         lipgloss.NewStyle().Foreground(lipgloss.Color("111")),

		danger:    lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true),
		deleted:   lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Strikethrough(true),
		highlight: lipgloss.NewStyle().Background(lipgloss.Color("52")).Foreground(lipgloss.Color("231")),
		pin:       lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("220")),

		tabBar:      lipgloss.NewStyle().Background(lipgloss.Color("236")),
		tabActive:   lipgloss.NewStyle().Background(lipgloss.Color("99")).Foreground(lipgloss.Color("231")).Bold(true),
		tabInactive: lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("250")),

		splashTitle: lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true),
		key:         lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true),
	}
}

// name styles an author's name in the color they chose on Twitch, falling back
// to a hash of the name so a chatter who never picked one is still consistently
// colored rather than blending into everyone else.
func (s *styles) name(m chat.Message) lipgloss.Style {
	c := m.Color
	if c == "" {
		c = fallbackColor(m.Author)
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true)
}

// fallbackPalette mirrors Twitch's own default colors, so a user with no color
// set looks the same here as in the overlay.
var fallbackPalette = []string{
	"#FF0000", "#0000FF", "#00FF00", "#B22222", "#FF7F50", "#9ACD32",
	"#FF4500", "#2E8B57", "#DAA520", "#D2691E", "#5F9EA0", "#1E90FF",
	"#FF69B4", "#8A2BE2", "#00FF7F",
}

func fallbackColor(name string) string {
	var h uint32
	for _, r := range name {
		h = h*31 + uint32(r)
	}
	return fallbackPalette[int(h%uint32(len(fallbackPalette)))]
}

// isMention reports whether text mentions the logged-in user: "@login" or the
// bare login, case-insensitive, on word boundaries (so "nyx" doesn't hit
// inside "nyxlike"). ponytail: scanned per render over the visible backlog;
// cache a flag on the message if a profiler ever cares.
func (s *styles) isMention(text string) bool {
	if s.login == "" {
		return false
	}
	t := strings.ToLower(text)
	for i := 0; ; {
		j := strings.Index(t[i:], s.login)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(s.login)
		// '@' is not a word byte, so a boundary check alone covers "@login".
		if (j == 0 || !isWordByte(t[j-1])) && (end == len(t) || !isWordByte(t[end])) {
			return true
		}
		i = end
	}
}

// isWordByte matches the character set of Twitch logins ([a-z0-9_]); anything
// else ends a word for mention purposes.
func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_'
}

// roleTag returns a short colored marker for an author's role, standing in for
// the badge images the overlay can show but a terminal cannot.
func (s *styles) roleTag(m chat.Message) string {
	switch {
	case m.Broadcaster:
		return s.badgeBroadcaster.Render("[B]")
	case m.Moderator:
		return s.badgeMod.Render("[M]")
	case m.VIP:
		return s.badgeVIP.Render("[V]")
	case m.Subscriber:
		return s.badgeSub.Render("[S]")
	}
	return ""
}
