package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Nyxnix/crow/internal/chat"
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

// cardAvatarRows is how many cells tall the profile picture is drawn.
const cardAvatarRows = 4

// UserInfo is the account detail the card shows beyond the local message
// buffer, matching the top of Twitch's own mod card. YouTube fills the shared
// fields (CreatedAt, AvatarURL) plus Subscribers; the sub/follow fields are
// Twitch-only.
type UserInfo struct {
	CreatedAt   time.Time
	AvatarURL   string     // profile picture, drawn when the terminal has graphics
	FollowedAt  *time.Time // nil if not following or hidden
	SubTier     string     // "1"/"2"/"3", empty if not subscribed
	SubMonths   int
	SubHidden   bool
	Subscribers int // YouTube channel subscriber count; 0 = unknown/hidden
}

// InfoProvider fetches UserInfo for the card. It is nil when unavailable (for
// example not logged in), in which case the card just omits that section.
type InfoProvider interface {
	CardInfo(ctx context.Context, userLogin, channel string) (UserInfo, error)
}

// cardInfoLoaded delivers an async CardInfo result back into the update loop.
type cardInfoLoaded struct {
	userID string
	info   UserInfo
	err    bool
}

// card is the open user card: who it's about, the async-loaded account detail,
// and the result of the last action taken from it.
type card struct {
	userID   string
	login    string
	author   string
	platform chat.Platform

	// msgID is the message that was clicked, which is the one 'd' deletes.
	msgID string

	// msg is the full clicked message, which is what 'p' pins.
	msg chat.Message

	// info is the account/sub detail, nil until the fetch returns. infoErr marks
	// a fetch that failed so the card can say so rather than spin forever.
	info    *UserInfo
	infoErr bool

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

// openCard opens the card for a message's author and returns a command that
// fetches their account detail, or nil if there is nothing to open or no info
// provider is configured.
func (m *Model) openCard(msgIdx int) tea.Cmd {
	if msgIdx < 0 || msgIdx >= len(m.lastRender) {
		return nil
	}
	src := m.lastRender[msgIdx]
	m.card = &card{
		userID:   src.AuthorID,
		login:    src.AuthorLogin,
		author:   src.Author,
		platform: src.Platform,
		msgID:    src.ID,
		msg:      src,
	}

	if m.info == nil || src.AuthorLogin == "" {
		return nil
	}
	login, channel, userID := src.AuthorLogin, m.channel, src.AuthorID
	provider := m.info
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		info, err := provider.CardInfo(ctx, login, channel)
		return cardInfoLoaded{userID: userID, info: info, err: err != nil}
	}
}

// userMessages returns the card subject's own messages, most recent last.
// Filtering the shared history on demand keeps one source of truth; at
// historyLimit this scan is far too fast to matter.
func (m *Model) userMessages(userID string) []chat.Message {
	var out []chat.Message
	for _, msg := range m.snapshot() {
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
	case "p":
		// Pinning is local display, no login needed. ID+Text compared because
		// own-echo messages carry no id.
		if m.pinned != nil && m.pinned.ID == c.msg.ID && m.pinned.Text == c.msg.Text {
			m.pinned = nil
			c.status, c.statusErr = "unpinned", false
		} else {
			pin := c.msg
			m.pinned = &pin
			c.status, c.statusErr = "pinned", false
		}
		return m, nil
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
			// Messages the user sent themselves are shown from a local echo and
			// carry no Twitch message id (Twitch does not echo our own PRIVMSGs
			// back with one), so there is nothing to delete by id. Say so rather
			// than appear to do nothing.
			return func() tea.Msg {
				return actionResult{text: "no message id (your own sent messages can't be deleted here)", err: true}
			}
		}
		return runAction(func(ctx context.Context) error {
			return mod.DeleteMessage(ctx, c.msgID)
		}, "message deleted")
	}
	return nil
}

// renderCardInfo renders the account/subscription block. It reflects the async
// load's state: no provider, still loading, failed, or loaded.
func (m *Model) renderCardInfo() string {
	s := m.styles
	c := m.card

	if m.info == nil {
		return s.dim.Render("account detail: log in to load")
	}
	if c.info == nil && !c.infoErr {
		return s.dim.Render("account detail: loading…")
	}
	if c.infoErr {
		return s.dim.Render("account detail: unavailable")
	}

	var lines []string
	if !c.info.CreatedAt.IsZero() {
		age := yearsSince(c.info.CreatedAt)
		lines = append(lines, s.cardLabel.Render("created ")+
			c.info.CreatedAt.Format("Jan 2, 2006")+s.dim.Render(age))
	}
	if c.platform == chat.YouTube {
		// YouTube has no follow/sub-tier equivalent; the channel's own
		// subscriber count is the stat worth showing.
		if c.info.Subscribers > 0 {
			lines = append(lines, s.cardLabel.Render("subs ")+humanCount(c.info.Subscribers))
		}
		return strings.Join(lines, "\n")
	}
	if c.info.FollowedAt != nil {
		lines = append(lines, s.cardLabel.Render("followed ")+
			c.info.FollowedAt.Format("Jan 2, 2006"))
	}
	switch {
	case c.info.SubHidden:
		lines = append(lines, s.cardLabel.Render("sub ")+s.dim.Render("hidden"))
	case c.info.SubTier != "":
		lines = append(lines, s.cardLabel.Render("sub ")+
			fmt.Sprintf("Tier %s · %d months", tierLabel(c.info.SubTier), c.info.SubMonths))
	default:
		lines = append(lines, s.cardLabel.Render("sub ")+s.dim.Render("not subscribed"))
	}
	return strings.Join(lines, "\n")
}

// tierLabel turns Twitch's "1000"/"2000"/"3000" or "1"/"2"/"3" tier codes into
// a plain tier number. IVR returns the short form, but guard the long one too.
func tierLabel(tier string) string {
	switch tier {
	case "1000":
		return "1"
	case "2000":
		return "2"
	case "3000":
		return "3"
	default:
		return tier
	}
}

// yearsSince renders a compact " (Ny)" age suffix, empty for under a year.
func yearsSince(t time.Time) string {
	years := int(time.Since(t).Hours() / 24 / 365)
	if years < 1 {
		return ""
	}
	return fmt.Sprintf(" (%dy)", years)
}

// renderCard draws the card panel and joins it beside the chat body.
func (m *Model) renderCard(body string) string {
	s := m.styles
	c := m.card
	history := m.userMessages(c.userID)

	var b strings.Builder

	// Profile picture, when loaded and the terminal has graphics. Drawn as a
	// small block above the name, the same mechanism as the emote preview.
	if m.gfx != nil && c.info != nil && c.info.AvatarURL != "" {
		if lines, _, ok := m.gfx.RenderLarge(c.info.AvatarURL, cardAvatarRows); ok {
			for _, ln := range lines {
				b.WriteString(ln + "\n")
			}
		}
	}

	// Header: who this is, and what they are in this channel. Badges render as
	// the same inline images as in chat (or the text tag as a fallback), taken
	// from the most recent message we hold for them.
	title := s.name(chat.Message{Author: c.author, Color: cardColor(history)}).Render(c.author)
	if len(history) > 0 {
		if seg, _, _ := renderBadges(history[len(history)-1], s, m.gfx, 1); seg != "" {
			title = seg + title
		}
	}
	b.WriteString(title + "\n")
	if c.login != "" && !strings.EqualFold(c.login, c.author) && c.platform != chat.YouTube {
		// Display names can differ from the login (case, or a localized name);
		// the login is what actually identifies them. YouTube's "login" is the
		// channel ID, which the id row below already shows.
		b.WriteString(s.cardLabel.Render("@"+c.login) + "\n")
	}
	b.WriteString(s.cardLabel.Render("id "+c.userID) + "\n")

	// Account / subscription detail, the top of Twitch's own mod card. Loads
	// asynchronously, so show its state.
	b.WriteString(m.renderCardInfo() + "\n")

	// Their recent messages (from the local buffer; see renderCardInfo for why
	// there is no all-time history).
	b.WriteString(s.cardLabel.Render(fmt.Sprintf("recent (%d held)", len(history))) + "\n")
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
	b.WriteString("  " + s.cardKey.Render("p") + " pin/unpin msg   " + s.cardKey.Render("esc") + " close\n")

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

// lipglossWidth measures rendered width, ignoring ANSI escapes.
func lipglossWidth(s string) int { return lipgloss.Width(s) }
