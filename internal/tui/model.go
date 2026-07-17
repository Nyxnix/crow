// Package tui renders chat in the terminal, with clickable usernames that open
// a moderation card for that user.
package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Nyxnix/typetype/internal/chat"
	"github.com/Nyxnix/typetype/internal/emote"
	"github.com/Nyxnix/typetype/internal/kitty"
)

// historyLimit caps messages held in memory. The user card filters this slice
// on click rather than maintaining a per-user index: at this size a linear scan
// is instant, and an index would be a second thing to keep correct.
const historyLimit = 2000

// Moderator performs moderation actions. It is nil until the user logs in, in
// which case the card shows what it would offer and why it can't.
//
// Defined here rather than in the twitch package because this is what the card
// needs, not what Twitch happens to expose.
type Moderator interface {
	Timeout(ctx context.Context, userID string, seconds int, reason string) error
	Ban(ctx context.Context, userID, reason string) error
	Unban(ctx context.Context, userID string) error
	DeleteMessage(ctx context.Context, messageID string) error
}

// Model is the bubbletea model for the whole app.
type Model struct {
	channel string
	msgs    []chat.Message
	styles  *styles

	width, height int

	// scroll is how many lines up from the bottom the viewport is pinned. Zero
	// means following live chat.
	scroll int

	hits []hit
	card *card

	// lastRender is the message snapshot the current hit boxes index into, so a
	// click maps to the right message regardless of buffer changes since render.
	lastRender []chat.Message

	emotes  *emote.Registry
	mod     Moderator
	info    InfoProvider
	stats   func() StreamStats // live viewer/uptime stats, nil to hide
	clients func() int         // connected overlay browser sources

	// send delivers a typed message to Twitch. Nil when not logged in, which is
	// also what decides whether the input line is shown and focused.
	send  func(string)
	input textinput.Model

	// gfx renders badge images inline on terminals that support the kitty
	// graphics protocol; nil elsewhere, where badges fall back to text.
	gfx *kitty.Cache

	// onRedraw asks the host (the App) to re-render, so state changed off the UI
	// goroutine — a new message, a loaded image, fresh stats — becomes visible.
	onRedraw func()

	// mu guards msgs, which the host's reader goroutines append to while the UI
	// goroutine reads it to render.
	mu  sync.Mutex
	err error
}

type Options struct {
	Channel string
	Emotes  *emote.Registry
	Mod     Moderator
	Info    InfoProvider
	Clients func() int

	// Stats returns the channel's live viewer count, uptime and session average
	// for the status bar. Leave nil to omit them.
	Stats func() StreamStats

	// Send delivers a typed message. Leave nil for a read-only (not logged in)
	// session; the input line then shows a hint instead of a prompt.
	Send func(string)

	// OnRedraw asks the host to re-render after Append/ApplyModEvent or an image
	// load. Required for a hosted model; defaults to a no-op.
	OnRedraw func()
}

func NewModel(o Options) *Model {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "Send a message…"
	// Twitch's own hard cap on a chat line; stop typing rather than have the
	// message silently truncated on send.
	ti.CharLimit = 500
	if o.Send != nil {
		ti.Focus()
	}

	onRedraw := o.OnRedraw
	if onRedraw == nil {
		onRedraw = func() {}
	}
	m := &Model{
		channel:  o.Channel,
		styles:   newStyles(),
		emotes:   o.Emotes,
		mod:      o.Mod,
		info:     o.Info,
		stats:    o.Stats,
		clients:  o.Clients,
		send:     o.Send,
		input:    ti,
		onRedraw: onRedraw,
	}
	if kitty.Supported() {
		m.gfx = kitty.New(onRedraw)
	}
	return m
}

// Append adds a received message. It is safe to call from a reader goroutine
// while the UI goroutine renders, and asks the host to redraw.
func (m *Model) Append(msg chat.Message) {
	m.mu.Lock()
	m.appendLocked(msg)
	m.mu.Unlock()
	m.onRedraw()
}

// Redraw asks the host to re-render, for callers (the stats poller) that change
// what the status bar shows without touching the message buffer.
func (m *Model) Redraw() { m.onRedraw() }

// Init returns the model's startup command. Ingestion is driven by the host via
// Append/ApplyModEvent, so the only command here is the input cursor's blink.
func (m *Model) Init() tea.Cmd {
	if m.send != nil {
		return textinput.Blink
	}
	return nil
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.onKey(msg)

	case tea.MouseMsg:
		return m.onMouse(msg)

	case actionResult:
		if m.card != nil {
			m.card.status = msg.text
			m.card.statusErr = msg.err
		}
		return m, nil

	case cardInfoLoaded:
		// Ignore a response for a card that was closed or reopened for someone
		// else while the fetch was in flight.
		if m.card != nil && m.card.userID == msg.userID {
			m.card.info = &msg.info
			m.card.infoErr = msg.err
		}
		return m, nil
	}

	// Non-key, non-mouse messages (the cursor's blink ticks) belong to the
	// input when it exists.
	if m.send != nil {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// appendLocked adds a message; the caller holds m.mu.
func (m *Model) appendLocked(msg chat.Message) {
	m.msgs = append(m.msgs, msg)
	if len(m.msgs) > historyLimit {
		// Drop from the front in one slice op; copying keeps the backing array
		// from growing without bound as the stream runs for hours.
		keep := m.msgs[len(m.msgs)-historyLimit:]
		m.msgs = append(m.msgs[:0], keep...)
	}
	// While scrolled back, hold position rather than yanking the view to the
	// bottom every time someone talks.
	if m.scroll > 0 {
		m.scroll++
	}
}

// ApplyModEvent strikes through the messages a moderation action affects.
// Messages are kept, not removed, so a moderator can see what was deleted — the
// same as Twitch's own moderator view. Safe to call from a reader goroutine.
func (m *Model) ApplyModEvent(ev chat.ModEvent) {
	m.mu.Lock()
	for i := range m.msgs {
		switch ev.Kind {
		case chat.DeleteMessage:
			if m.msgs[i].ID == ev.MessageID {
				m.msgs[i].Deleted = true
			}
		case chat.ClearUser:
			if m.msgs[i].AuthorID == ev.UserID {
				m.msgs[i].Deleted = true
			}
		case chat.ClearAll:
			m.msgs[i].Deleted = true
		}
	}
	m.mu.Unlock()
	m.onRedraw()
}

// snapshot returns a copy of the message buffer taken under the lock, so the
// renderer works from a stable slice while reader goroutines append.
func (m *Model) snapshot() []chat.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]chat.Message, len(m.msgs))
	copy(out, m.msgs)
	return out
}

func (m *Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The card owns the keyboard while it's open.
	if m.card != nil {
		return m.cardKey(msg)
	}

	// Keys that mean the same thing in either mode.
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "pgup":
		m.scrollBy(m.viewportHeight() / 2)
		return m, nil
	case "pgdown":
		m.scrollBy(-m.viewportHeight() / 2)
		return m, nil
	}

	// Logged in: the input is focused, so letters type. Scrolling is by wheel,
	// PgUp/PgDn, and the arrows (which a single-line input ignores). Quit is
	// ctrl+c only, since 'q' is a character someone may want to type.
	if m.send != nil {
		switch msg.String() {
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text != "" {
				m.send(text)
				m.input.Reset()
				m.scroll = 0 // snap to live so the user sees their own message land
			}
			return m, nil
		case "up":
			m.scrollBy(1)
			return m, nil
		case "down":
			m.scrollBy(-1)
			return m, nil
		case "esc":
			m.scroll = 0
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	// Read-only: no input to focus, so vim-style navigation and 'q' to quit.
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "g":
		m.scroll = m.maxScroll()
	case "G", "end":
		m.scroll = 0
	case "up", "k":
		m.scrollBy(1)
	case "down", "j":
		m.scrollBy(-1)
	}
	return m, nil
}

func (m *Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.scrollBy(3)
		return m, nil
	case tea.MouseButtonWheelDown:
		m.scrollBy(-3)
		return m, nil
	}

	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}
	// A click anywhere while the card is open dismisses it, matching how the
	// same overlay behaves on Twitch itself.
	if m.card != nil {
		m.card = nil
		return m, nil
	}
	if h := m.hitAt(msg.X, msg.Y); h != nil {
		return m, m.openCard(h.msg)
	}
	return m, nil
}

// hitAt finds the username, if any, under a click.
func (m *Model) hitAt(x, y int) *hit {
	for i := range m.hits {
		h := m.hits[i]
		if h.row == y && x >= h.x0 && x < h.x1 {
			return &m.hits[i]
		}
	}
	return nil
}

func (m *Model) scrollBy(n int) {
	m.scroll += n
	if m.scroll < 0 {
		m.scroll = 0
	}
	if max := m.maxScroll(); m.scroll > max {
		m.scroll = max
	}
}

func (m *Model) viewportHeight() int {
	h := m.height - 1 // status bar
	if m.send != nil {
		h-- // input line
	}
	if h < 1 {
		return 1
	}
	return h
}

// chatWidth is the width available to message text, which shrinks when the card
// takes the right-hand column.
func (m *Model) chatWidth() int {
	if m.card == nil {
		return m.width
	}
	w := m.width - cardOuterWidth - cardGutter
	if w < 20 {
		// Too narrow to show both. The card is the thing the user just asked
		// for, so let it win and squeeze chat to the floor.
		w = 20
	}
	return w
}

// maxScroll is how far back the current layout allows. It re-lays out to find
// out, which is cheap at this history size and avoids caching a number that
// goes stale on every resize or card toggle.
func (m *Model) maxScroll() int {
	lines := layout(m.snapshot(), m.chatWidth(), m.styles, m.gfx)
	if n := len(lines) - m.viewportHeight(); n > 0 {
		return n
	}
	return 0
}

func (m *Model) View() string {
	if m.width == 0 {
		return "" // no size yet; bubbletea sends one immediately
	}

	// Render from a snapshot and keep it: hit boxes index into it, so a click
	// resolves against exactly what was on screen even if the buffer has since
	// grown or been trimmed. Both View and the click handler run on the UI
	// goroutine, so lastRender needs no lock.
	m.lastRender = m.snapshot()
	lines := layout(m.lastRender, m.chatWidth(), m.styles, m.gfx)
	vh := m.viewportHeight()

	// Take the window ending `scroll` lines from the bottom.
	end := len(lines) - m.scroll
	if end < 0 {
		end = 0
	}
	start := end - vh
	if start < 0 {
		start = 0
	}
	window := lines[start:end]

	// Hits are recorded against absolute line numbers; the click handler works
	// in screen rows, so rebase them and drop those scrolled out of view.
	m.hits = m.hits[:0]
	rows := make([]string, 0, vh)
	for i, l := range window {
		if l.hit != nil {
			h := *l.hit
			h.row = i
			m.hits = append(m.hits, h)
		}
		rows = append(rows, l.text)
	}
	// Pad so the status bar stays pinned to the bottom on a short backlog.
	for len(rows) < vh {
		rows = append(rows, "")
	}

	body := strings.Join(rows, "\n")
	if m.card != nil {
		body = m.renderCard(body)
	}

	out := body
	if m.send != nil {
		out += "\n" + m.inputLine()
	}
	frame := out + "\n" + m.statusBar()

	// Upload any newly-loaded images once, at the very top of the frame, before
	// the placeholder cells that reference them. The upload sequences are
	// zero-width, so they do not disturb layout.
	if m.gfx != nil {
		frame = m.gfx.FlushUploads() + frame
	}
	return frame
}

// inputLine renders the message composer. It shows the text field when logged
// in; the caller only calls this when send is set, so there is no not-logged-in
// branch here.
func (m *Model) inputLine() string {
	return m.input.View()
}

// StreamStats is the live channel status shown in the status bar.
type StreamStats struct {
	Live       bool
	Viewers    int
	AvgViewers int
	Uptime     time.Duration
}

func (m *Model) statusBar() string {
	// Left: channel and, when live, the stream stats.
	left := fmt.Sprintf(" #%s ", m.channel)
	if m.stats != nil {
		if s := m.stats(); s.Live {
			left += fmt.Sprintf("· %s viewers (avg %s) · up %s ",
				humanCount(s.Viewers), humanCount(s.AvgViewers), shortDuration(s.Uptime))
		} else {
			left += "· offline "
		}
	}

	var parts []string
	if m.emotes != nil {
		parts = append(parts, fmt.Sprintf("%d emotes", m.emotes.Len()))
	}
	if m.clients != nil {
		parts = append(parts, fmt.Sprintf("%d overlay", m.clients()))
	}
	if m.mod == nil {
		parts = append(parts, "not logged in")
	}
	if m.scroll > 0 {
		parts = append(parts, fmt.Sprintf("scrolled %d", m.scroll))
	} else {
		parts = append(parts, "live")
	}
	if m.err != nil {
		parts = append(parts, m.err.Error())
	}

	right := " " + strings.Join(parts, " · ") + " "
	gap := m.width - lipglossWidth(left) - lipglossWidth(right)
	if gap < 0 {
		gap = 0
	}
	return m.styles.status.Width(m.width).Render(left + strings.Repeat(" ", gap) + right)
}

// humanCount formats a viewer count compactly: 1234 -> "1.2k".
func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// shortDuration formats an uptime as "3h 24m" or "24m".
func shortDuration(d time.Duration) string {
	h := int(d.Hours())
	mnt := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, mnt)
	}
	return fmt.Sprintf("%dm", mnt)
}
