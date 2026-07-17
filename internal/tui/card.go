package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Nyxnix/typetype/internal/chat"
)

// cardWidth is the card's content width. Wide enough for a timestamp plus a
// readable slice of a message, narrow enough to leave chat usable beside it.
const cardWidth = 44

// cardOuterWidth is what the panel actually occupies: content, plus one column
// of padding and one of border on each side.
const cardOuterWidth = cardWidth + 4

// cardGutter separates chat from the card's border so text doesn't touch it.
const cardGutter = 2

// cardHistory is how many of the user's own messages the card shows.
const cardHistory = 12

// card is the open user card: who it's about, and the result of the last action
// taken from it.
type card struct {
	userID string
	login  string
	author string

	// msgID is the message that was clicked, which is the one 'd' deletes.
	msgID string

	status    string
	statusErr bool
	confirm   string // pending destructive action awaiting y/n
}

// timeoutPresets are the durations offered by number key. They match the
// buckets Twitch's own mod tools use, so muscle memory carries over.
var timeoutPresets = []struct {
	key     string
	seconds int
	label   string
}{
	{"1", 10, "10s"},
	{"2", 60, "1m"},
	{"3", 600, "10m"},
	{"4", 3600, "1h"},
	{"5", 86400, "24h"},
}

func (m *Model) openCard(msgIdx int) {
	if msgIdx < 0 || msgIdx >= len(m.msgs) {
		return
	}
	src := m.msgs[msgIdx]
	m.card = &card{
		userID: src.AuthorID,
		login:  src.AuthorLogin,
		author: src.Author,
		msgID:  src.ID,
	}
}

// userMessages returns the card subject's own messages, most recent last.
// Filtering the shared history on demand keeps one source of truth; at
// historyLimit this scan is far too fast to matter.
func (m *Model) userMessages(userID string) []chat.Message {
	var out []chat.Message
	for _, msg := range m.msgs {
		if msg.AuthorID == userID {
			out = append(out, msg)
		}
	}
	if len(out) > cardHistory {
		out = out[len(out)-cardHistory:]
	}
	return out
}

// actionResult reports a moderation call's outcome back into the update loop.
type actionResult struct {
	text string
	err  bool
}

// runAction performs a moderation call off the update loop, so a slow Helix
// request can't freeze chat rendering.
func runAction(fn func(context.Context) error, okText string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := fn(ctx); err != nil {
			return actionResult{text: err.Error(), err: true}
		}
		return actionResult{text: okText}
	}
}

func (m *Model) cardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.card
	key := msg.String()

	// A pending destructive action swallows the next keypress.
	if c.confirm != "" {
		switch key {
		case "y", "Y":
			action := c.confirm
			c.confirm = ""
			return m, m.perform(action)
		default:
			c.confirm = ""
			c.status = "cancelled"
			c.statusErr = false
			return m, nil
		}
	}

	switch key {
	case "esc", "q":
		m.card = nil
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}

	if m.mod == nil {
		c.status = "log in to moderate"
		c.statusErr = true
		return m, nil
	}

	switch key {
	case "b":
		// Bans are not practically reversible from the viewer's side; make the
		// user say so out loud.
		c.confirm = "ban"
		return m, nil
	case "u":
		return m, m.perform("unban")
	case "d":
		return m, m.perform("delete")
	}

	for _, p := range timeoutPresets {
		if key == p.key {
			return m, m.perform("timeout:" + p.key)
		}
	}
	return m, nil
}

// perform dispatches a named action against the card's subject.
func (m *Model) perform(action string) tea.Cmd {
	c := m.card
	mod := m.mod

	if strings.HasPrefix(action, "timeout:") {
		want := strings.TrimPrefix(action, "timeout:")
		for _, p := range timeoutPresets {
			if p.key == want {
				return runAction(func(ctx context.Context) error {
					return mod.Timeout(ctx, c.userID, p.seconds, "")
				}, "timed out "+p.label)
			}
		}
		return nil
	}

	switch action {
	case "ban":
		return runAction(func(ctx context.Context) error {
			return mod.Ban(ctx, c.userID, "")
		}, "banned")
	case "unban":
		return runAction(func(ctx context.Context) error {
			return mod.Unban(ctx, c.userID)
		}, "unbanned")
	case "delete":
		if c.msgID == "" {
			return nil
		}
		return runAction(func(ctx context.Context) error {
			return mod.DeleteMessage(ctx, c.msgID)
		}, "message deleted")
	}
	return nil
}

// renderCard draws the card panel and joins it beside the chat body.
func (m *Model) renderCard(body string) string {
	s := m.styles
	c := m.card
	history := m.userMessages(c.userID)

	var b strings.Builder

	// Header: who this is, and what they are in this channel.
	title := s.name(chat.Message{Author: c.author, Color: cardColor(history)}).Render(c.author)
	if tag := roleOf(history, s); tag != "" {
		title = tag + " " + title
	}
	b.WriteString(title + "\n")
	if c.login != "" && !strings.EqualFold(c.login, c.author) {
		// Display names can differ from the login (case, or a localized name);
		// the login is what actually identifies them.
		b.WriteString(s.cardLabel.Render("@"+c.login) + "\n")
	}
	b.WriteString(s.cardLabel.Render(fmt.Sprintf("id %s · %d msgs held", c.userID, len(history))) + "\n\n")

	// Their recent messages.
	b.WriteString(s.cardLabel.Render("recent") + "\n")
	if len(history) == 0 {
		b.WriteString(s.dim.Render("  (nothing in buffer)") + "\n")
	}
	for _, h := range history {
		line := fmt.Sprintf("%s %s", h.At.Format("15:04"), h.Text)
		for _, w := range wrap(line, cardWidth-4, cardWidth-4) {
			b.WriteString("  " + s.text.Render(w) + "\n")
		}
	}
	b.WriteString("\n")

	// Actions.
	b.WriteString(s.cardLabel.Render("actions") + "\n")
	if m.mod == nil {
		b.WriteString(s.dim.Render("  log in to moderate") + "\n")
	} else {
		var keys []string
		for _, p := range timeoutPresets {
			keys = append(keys, s.cardKey.Render(p.key)+" "+p.label)
		}
		b.WriteString("  " + s.cardLabel.Render("timeout ") + strings.Join(keys, "  ") + "\n")
		b.WriteString("  " + s.cardKey.Render("b") + " " + s.danger.Render("ban") +
			"   " + s.cardKey.Render("u") + " unban" +
			"   " + s.cardKey.Render("d") + " delete msg\n")
	}
	b.WriteString("  " + s.cardKey.Render("esc") + " close\n")

	if c.confirm != "" {
		b.WriteString("\n" + s.danger.Render(fmt.Sprintf("%s @%s? (y/n)", c.confirm, c.login)) + "\n")
	}
	if c.status != "" {
		st := s.dim
		if c.statusErr {
			st = s.danger
		}
		b.WriteString("\n" + st.Render(c.status) + "\n")
	}

	panel := s.cardBorder.Width(cardWidth).Render(strings.TrimRight(b.String(), "\n"))
	panel = lipgloss.NewStyle().Height(m.viewportHeight()).Render(panel)

	// Pin the chat column to an explicit width. JoinHorizontal otherwise sizes
	// it to its longest line, so a quiet chat of short messages would let the
	// card drift left and jitter as message lengths change. The gutter keeps
	// text off the border.
	left := lipgloss.NewStyle().
		Width(m.chatWidth()).
		Height(m.viewportHeight()).
		Render(body)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", cardGutter), panel)
}

// cardColor recovers the subject's chosen color from their messages, since the
// card only stores identity.
func cardColor(history []chat.Message) string {
	for _, h := range history {
		if h.Color != "" {
			return h.Color
		}
	}
	return ""
}

// roleOf finds the subject's highest role from their messages.
func roleOf(history []chat.Message, s *styles) string {
	for _, h := range history {
		if tag := s.roleTag(h); tag != "" {
			return tag
		}
	}
	return ""
}

// lipglossWidth measures rendered width, ignoring ANSI escapes.
func lipglossWidth(s string) int { return lipgloss.Width(s) }
