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
	"github.com/mattn/go-runewidth"

	"github.com/Nyxnix/crow/internal/chat"
	"github.com/Nyxnix/crow/internal/emote"
	"github.com/Nyxnix/crow/internal/kitty"
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

// ChannelManager runs the Twitch-only slash commands (chat modes, polls,
// raids, roles). Like Moderator it is defined by what the TUI needs and is nil
// on tabs that aren't a single logged-in Twitch channel, where those commands
// explain themselves instead of running.
type ChannelManager interface {
	UpdateChatSettings(ctx context.Context, patch map[string]any) error
	Announce(ctx context.Context, text string) error
	CreatePoll(ctx context.Context, title string, choices []string, durationSecs int) error
	CreatePrediction(ctx context.Context, title string, outcomes []string, windowSecs int) error
	Raid(ctx context.Context, toBroadcasterID string) error
	SetVIP(ctx context.Context, userID string, on bool) error
	SetMod(ctx context.Context, userID string, on bool) error
	ClearChat(ctx context.Context) error
	ResolveUser(ctx context.Context, login string) (string, error)
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

	hits      []hit
	emoteHits []ehit
	card      *card
	emoteCard *emoteCard

	// lastRender is the message snapshot the current hit boxes index into, so a
	// click maps to the right message regardless of buffer changes since render.
	lastRender []chat.Message

	emotes  *emote.Registry
	mod     Moderator
	chanMgr ChannelManager
	info    InfoProvider
	stats   func() StreamStats // live viewer/uptime stats, nil to hide
	clients func() int         // connected overlay browser sources

	// comp is the in-progress Tab completion cycle, nil between cycles.
	comp *completion

	// notice is the latest slash-command outcome, shown in the status bar when
	// no card is open to display it. Cleared on the next keypress.
	notice    string
	noticeErr bool

	// pinned is the message pinned from the user card, shown as a fixed row
	// above the input. A copy, so it survives the ring buffer trimming past it.
	pinned *chat.Message

	// overlayUnclaimed reports that the overlay is pinned but no open tab
	// matched the pin, so it is silently blank; nil to skip the warning.
	overlayUnclaimed func() bool

	// send delivers a typed message to Twitch. Nil when not logged in, which is
	// also what decides whether the input line is shown and focused.
	send  func(string)
	input textinput.Model

	// gfx renders badge images inline on terminals that support the kitty
	// graphics protocol; nil elsewhere, where badges fall back to text.
	gfx *kitty.Cache

	// prefetched marks that the emote registry's images were warmed into gfx,
	// which happens once, on the first Append after the registry loads.
	prefetched bool

	// scale draws chat lines at this multiple via kitty's text sizing protocol
	// (1 = normal). Set by the App from config, only on terminals that speak it.
	scale int

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
	Chan    ChannelManager
	Info    InfoProvider
	Clients func() int

	// OverlayUnclaimed reports that the overlay pin matches no open tab, which
	// the status bar warns about. Leave nil to omit the check.
	OverlayUnclaimed func() bool

	// Login is the logged-in user's name, for highlighting messages that
	// mention them. Empty disables the highlight.
	Login string

	// Stats returns the channel's live viewer count, uptime and session average
	// for the status bar. Leave nil to omit them.
	Stats func() StreamStats

	// Send delivers a typed message. Leave nil for a read-only (not logged in)
	// session; the input line then shows a hint instead of a prompt.
	Send func(string)

	// SendLimit caps typed message length; 0 means Twitch's 500. YouTube caps
	// at 200, and stopping typing beats a send the platform silently rejects.
	SendLimit int

	// OnRedraw asks the host to re-render after Append/ApplyModEvent or an image
	// load. Required for a hosted model; defaults to a no-op.
	OnRedraw func()
}

func NewModel(o Options) *Model {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "Send a message…"
	// The platform's hard cap on a chat line; stop typing rather than have the
	// message silently truncated on send.
	ti.CharLimit = 500
	if o.SendLimit > 0 {
		ti.CharLimit = o.SendLimit
	}
	if o.Send != nil {
		ti.Focus()
	}

	onRedraw := o.OnRedraw
	if onRedraw == nil {
		onRedraw = func() {}
	}
	st := newStyles()
	st.login = strings.ToLower(o.Login)
	m := &Model{
		channel: o.Channel,
		styles:  st,
		emotes:  o.Emotes,
		mod:     o.Mod,
		chanMgr: o.Chan,
		info:    o.Info,
		stats:   o.Stats,
		clients: o.Clients,

		overlayUnclaimed: o.OverlayUnclaimed,
		send:             o.Send,
		input:            ti,
		onRedraw:         onRedraw,
	}
	if kitty.Supported() {
		// One cache for the whole process, not per tab: every tab shares the
		// terminal's single image-id space, so per-model caches would assign
		// the same ids to different images and the last upload would hijack
		// the other tabs' placeholders. onRedraw is the App's redraw for every
		// model, so the first one works for all.
		gfxOnce.Do(func() { sharedGfx = kitty.New(onRedraw) })
		m.gfx = sharedGfx
	}
	return m
}

var (
	sharedGfx *kitty.Cache
	gfxOnce   sync.Once
)

// Append adds a received message. It is safe to call from a reader goroutine
// while the UI goroutine renders, and asks the host to redraw.
func (m *Model) Append(msg chat.Message) {
	m.mu.Lock()
	m.appendLocked(msg)
	// The registry swaps all its emotes in at once, so Len>0 means the load
	// finished: warm the image cache once so emotes render instantly on their
	// first appearance instead of popping in a beat later.
	warm := !m.prefetched && m.gfx != nil && m.emotes != nil && m.emotes.Len() > 0
	if warm {
		m.prefetched = true
	}
	rows := 0
	if m.scale > 1 {
		rows = m.scale
	}
	m.mu.Unlock()
	if warm {
		go m.prefetchEmotes(rows)
	}
	m.onRedraw()
}

// prefetchEmotes fetches every loaded emote's image into the kitty cache.
// Uploads stay deferred inside the cache until an emote actually renders, and
// a small worker pool keeps the fetch burst polite to the provider CDNs.
// ponytail: everything is held decoded in memory for the session; add a cap or
// LRU if huge animated 7TV sets ever make that hurt.
func (m *Model) prefetchEmotes(rows int) {
	work := make(chan emote.Emote)
	for i := 0; i < 4; i++ {
		go func() {
			for e := range work {
				url := e.URL
				if e.Animated {
					url = kitty.AnimatedURL(url)
				}
				m.gfx.Prefetch(url, rows)
			}
		}()
	}
	for _, e := range m.emotes.All() {
		work <- e
	}
	close(work)
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
		} else {
			// No card to show it in (slash commands typed at the input): the
			// status bar carries the outcome instead.
			m.notice, m.noticeErr = msg.text, msg.err
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
	// Any keypress retires the previous command's status-bar notice: it has
	// been seen, and the user is doing the next thing.
	m.notice = ""

	// The emote card is a passive popup: any key dismisses it.
	if m.emoteCard != nil {
		m.emoteCard = nil
		return m, nil
	}
	// The card owns the keyboard while it's open.
	if m.card != nil {
		return m.cardKey(msg)
	}

	// Keys that mean the same thing in either mode.
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "pgup":
		m.scrollBy(m.viewportHeight() / 2 / m.effScale())
		return m, nil
	case "pgdown":
		m.scrollBy(-m.viewportHeight() / 2 / m.effScale())
		return m, nil
	}

	// Logged in: the input is focused, so letters type. Scrolling is by wheel,
	// PgUp/PgDn, and the arrows (which a single-line input ignores). Quit is
	// ctrl+c only, since 'q' is a character someone may want to type.
	if m.send != nil {
		if msg.String() == "tab" {
			m.completeTab()
			return m, nil
		}
		m.comp = nil // any non-Tab key ends the completion cycle
		switch msg.String() {
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.Reset()
			// A leading "/" is a command, except /me, which Twitch IRC still
			// turns into an ACTION when sent as a message.
			if strings.HasPrefix(text, "/") && strings.Fields(text)[0] != "/me" {
				return m, m.runCommand(text)
			}
			m.send(text)
			m.scroll = 0 // snap to live so the user sees their own message land
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
	// A click anywhere while a popup is open dismisses it, matching how the
	// same overlay behaves on Twitch itself.
	if m.emoteCard != nil {
		m.emoteCard = nil
		return m, nil
	}
	if m.card != nil {
		m.card = nil
		return m, nil
	}
	if e := m.emoteHitAt(msg.X, msg.Y); e != nil {
		m.emoteCard = &emoteCard{emote: e.emote}
		return m, nil
	}
	if h := m.hitAt(msg.X, msg.Y); h != nil {
		return m, m.openCard(h.msg)
	}
	return m, nil
}

// emoteHitAt finds the emote, if any, under a click.
func (m *Model) emoteHitAt(x, y int) *ehit {
	for i := range m.emoteHits {
		e := m.emoteHits[i]
		if e.row == y && x >= e.x0 && x < e.x1 {
			return &m.emoteHits[i]
		}
	}
	return nil
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
	if m.pinned != nil {
		h-- // pinned message row
	}
	if h < 1 {
		return 1
	}
	return h
}

// chatWidth is the width available to message text, which shrinks when the card
// takes the right-hand column.
func (m *Model) chatWidth() int {
	if m.card == nil && m.emoteCard == nil {
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

// effScale is the scale chat actually renders at right now. The cards overlay
// chat by column-composited strings whose width measurement can't see through
// OSC 66 wrapping, so an open card drops chat to 1x until it closes.
func (m *Model) effScale() int {
	if m.scale > 1 && m.card == nil && m.emoteCard == nil {
		return m.scale
	}
	return 1
}

// maxScroll is how far back the current layout allows. It re-lays out to find
// out, which is cheap at this history size and avoids caching a number that
// goes stale on every resize or card toggle.
func (m *Model) maxScroll() int {
	scale := m.effScale()
	lines := layout(m.snapshot(), m.chatWidth(), m.styles, m.gfx, scale)
	if n := len(lines) - m.viewportHeight()/scale; n > 0 {
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
	scale := m.effScale()
	lines := layout(m.lastRender, m.chatWidth(), m.styles, m.gfx, scale)
	vh := m.viewportHeight()
	vhl := vh / scale // logical lines that fit; each occupies scale rows
	if vhl < 1 {
		vhl = 1
	}

	// Take the window ending `scroll` lines from the bottom.
	end := len(lines) - m.scroll
	if end < 0 {
		end = 0
	}
	start := end - vhl
	if start < 0 {
		start = 0
	}
	window := lines[start:end]

	// Hits are recorded against absolute line numbers; the click handler works
	// in screen rows, so rebase them (a scaled line spans scale rows, all
	// clickable) and drop those scrolled out of view.
	m.hits = m.hits[:0]
	m.emoteHits = m.emoteHits[:0]
	rows := make([]string, 0, vh)
	for i, l := range window {
		for dy := 0; dy < scale; dy++ {
			if l.hit != nil {
				h := *l.hit
				h.row = i*scale + dy
				m.hits = append(m.hits, h)
			}
			for _, e := range l.emotes {
				e.row = i*scale + dy
				m.emoteHits = append(m.emoteHits, e)
			}
		}
		// At scale, every row clears itself first: bubbletea rewrites changed
		// lines in place, and writing scaled glyphs over stale multicell cells
		// makes kitty skip or space-fill unpredictably. An explicit erase gives
		// each rewrite a clean band.
		if scale > 1 {
			rows = append(rows, "\x1b[2K"+l.text)
		} else {
			rows = append(rows, l.text)
		}
		// Filler rows are never erased — they hold the bottom cells of the
		// scaled glyphs above, and kitty deletes a glyph whose cells an erase
		// touches. The rows sweep themselves clean with spaces instead.
		for dy := 1; dy < scale; dy++ {
			f := ""
			if dy-1 < len(l.fillers) {
				f = l.fillers[dy-1]
			}
			rows = append(rows, f)
		}
	}
	// Pad so the status bar stays pinned to the bottom on a short backlog.
	for len(rows) < vh {
		rows = append(rows, "")
	}

	body := strings.Join(rows, "\n")
	switch {
	case m.card != nil:
		body = m.renderCard(body)
	case m.emoteCard != nil:
		body = m.renderEmoteCard(body)
	}

	out := body
	if m.pinned != nil {
		out += "\n" + m.pinLine()
	}
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

// pinLine renders the pinned message as one fixed plain-text row: scale 1 and
// no emote images, because kitty's scaled runs and image placeholders desync
// when bubbletea rewrites a row in place (see layout.go).
func (m *Model) pinLine() string {
	text := m.pinned.Text
	if text == "" {
		text = m.pinned.AlertText
	}
	// Plain "PIN", no emoji: terminals and go-runewidth disagree on emoji
	// widths (see app.go), and a mis-measured full-width row gets truncated.
	row := runewidth.Truncate("PIN "+m.pinned.Author+": "+text, m.width, "…")
	return m.styles.pin.Width(m.width).Render(row)
}

// StreamStats is the live channel status shown in the status bar.
type StreamStats struct {
	Live       bool
	Viewers    int
	AvgViewers int
	Uptime     time.Duration
}

// specLabel shows a tab spec: a plain Twitch channel gets the familiar "#",
// combined and YouTube specs are shown as typed.
func specLabel(spec string) string {
	if strings.ContainsAny(spec, "+:") {
		return spec
	}
	return "#" + spec
}

func (m *Model) statusBar() string {
	// Left: channel and, when live, the stream stats.
	left := fmt.Sprintf(" %s ", specLabel(m.channel))
	if m.stats != nil {
		if s := m.stats(); s.Live {
			left += fmt.Sprintf("· %s viewers (avg %s) · up %s ",
				humanCount(s.Viewers), humanCount(s.AvgViewers), shortDuration(s.Uptime))
		} else {
			left += "· offline "
		}
	}

	var parts []string
	if m.notice != "" {
		n := m.notice
		if m.noticeErr {
			n = "error: " + n
		}
		parts = append(parts, n)
	}
	if m.emotes != nil {
		parts = append(parts, fmt.Sprintf("%d emotes", m.emotes.Len()))
	}
	if m.clients != nil {
		parts = append(parts, fmt.Sprintf("%d overlay", m.clients()))
	}
	if m.overlayUnclaimed != nil && m.overlayUnclaimed() {
		parts = append(parts, "overlay pin matches no tab")
	}
	if m.mod == nil && m.send == nil {
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
