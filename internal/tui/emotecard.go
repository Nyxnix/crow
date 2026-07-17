package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Nyxnix/typetype/internal/chat"
)

// emoteCardRows is how many cells tall the previewed emote is drawn. Big enough
// to actually see the art, short enough to leave room for the label below.
const emoteCardRows = 6

// emoteCard is the popup shown when an emote is clicked: a large preview, the
// name, and where it's from — the terminal's take on jChat's emote tooltip.
type emoteCard struct {
	emote chat.Emote
}

// providerName turns a provider slug into its display name.
func providerName(p string) string {
	switch p {
	case "7tv":
		return "7TV"
	case "bttv":
		return "BetterTTV"
	case "ffz":
		return "FrankerFaceZ"
	case "twitch":
		return "Twitch"
	}
	return p
}

// largeEmoteURL is the best source for the big preview: an animated emote's
// WebP can't be decoded here, so use its GIF sibling at full size (unlike the
// tiny inline copy, which drops to 1x for speed).
func largeEmoteURL(e chat.Emote) string {
	if e.Animated && strings.HasSuffix(e.URL, ".webp") {
		return strings.TrimSuffix(e.URL, ".webp") + ".gif"
	}
	return e.URL
}

func (m *Model) renderEmoteCard(body string) string {
	s := m.styles
	e := m.emoteCard.emote

	var b strings.Builder

	// Preview: a tall inline image, centered, or a placeholder while it loads /
	// on terminals without graphics.
	if m.gfx != nil {
		if lines, cols, ok := m.gfx.RenderLarge(largeEmoteURL(e), emoteCardRows); ok {
			pad := (cardWidth - cols) / 2
			if pad < 0 {
				pad = 0
			}
			for _, ln := range lines {
				b.WriteString(strings.Repeat(" ", pad) + ln + "\n")
			}
		} else {
			for i := 0; i < emoteCardRows; i++ {
				b.WriteString("\n")
			}
			b.WriteString(center(s.dim.Render("loading…")) + "\n")
		}
	}
	b.WriteString("\n")

	// Label: name, then provider and owner, all centered under the image.
	b.WriteString(center(s.cardTitle.Render(e.Name)) + "\n")
	prov := providerName(e.Provider)
	if prov != "" {
		b.WriteString(center(s.cardKey.Render(prov)+s.cardLabel.Render(" emote")) + "\n")
	}
	if e.Owner != "" {
		b.WriteString(center(s.cardLabel.Render("by "+e.Owner)) + "\n")
	}
	b.WriteString("\n" + center(s.cardKey.Render("esc")+s.dim.Render(" close")))

	panel := s.cardBorder.Width(cardWidth).Render(b.String())
	panel = lipgloss.NewStyle().Height(m.viewportHeight()).Render(panel)

	left := lipgloss.NewStyle().
		Width(m.chatWidth()).
		Height(m.viewportHeight()).
		Render(body)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", cardGutter), panel)
}

// center pads a rendered string to the card's content width. Width is measured
// with lipgloss so ANSI styling doesn't throw the centering off.
func center(s string) string {
	return lipgloss.PlaceHorizontal(cardWidth, lipgloss.Center, s)
}
