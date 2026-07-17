package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/Nyxnix/typetype/internal/chat"
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

	danger  lipgloss.Style
	deleted lipgloss.Style

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

		danger:  lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true),
		deleted: lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Strikethrough(true),

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
