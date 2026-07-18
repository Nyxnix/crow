package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Nyxnix/crow/internal/chat"
	"github.com/Nyxnix/crow/internal/config"
	"github.com/Nyxnix/crow/internal/kitty"
)

// The settings pages: a short top menu, the overlay sub-menu (plumbing and
// filters), and its appearance sub-sub-menu (what the overlay looks like).
const (
	pageMain = iota
	pageOverlay
	pageAppearance
	pageYouTube
	pageCount
)

// fontNames labels the overlay's font list, index-matched to the FONTS array in
// overlay.html.
var fontNames = []string{"Inter", "Roboto", "System", "Comic Sans", "Monospace"}

// srow is one line on a settings page. Exactly one behavior field is set, which
// decides how the row renders and what a keypress on it does: text edits an
// input, boolp toggles, enump cycles a fixed list, display is a read-only value
// (the overlay URL), and open navigates to another page. The "log out" row sets
// none and is matched by label.
type srow struct {
	label string

	ti     *textinput.Model // text field
	commit func(string)     // parse ti.Value() back into the config on save

	boolp *bool // toggle with enter/space

	enump    *int // cycle with enter/←/→
	enumOpts []string

	display func() string // read-only value

	open int // page to jump to on enter; 0 (pageMain) means "not a link"
}

// settingsState is the Ctrl+S configuration screen. It binds its rows directly
// to the App's config so toggles and cycles take effect immediately; text
// fields are parsed back on save.
type settingsState struct {
	login    string
	cfg      *config.Config
	page     int
	rows     [pageCount][]*srow
	sel      [pageCount]int
	alignIdx *int // heap-backed so it survives this value being copied on return
	scaleIdx *int // chat text scale minus one (0 = 1x); heap-backed like alignIdx

	// YouTube login page state. Two independent methods share this page.
	// ytCookies is the cookie draft (read by the verify action mid-edit);
	// ytClientID/ytClientSecret are the OAuth credentials. The *Authed probes
	// are stamped by the App after construction. Status/busy are shared because
	// only one login runs at a time.
	ytCookies       *textinput.Model
	ytClientID      *textinput.Model
	ytClientSecret  *textinput.Model
	ytCookiesAuthed func() bool
	ytOAuthAuthed   func() bool
	ytBusy          bool
	ytStatus        string
	ytErr           bool
	ytHandle        any // opaque OAuth device-code handle between start and poll
}

func newInput(val string, limit int) *textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = limit
	ti.SetValue(val)
	return &ti
}

func newSettingsState(login string, cfg *config.Config) settingsState {
	o := &cfg.Overlay

	// Bound text fields, parsed back into the config on save.
	addr := newInput(cfg.OverlayAddr, 40)
	channel := newInput(cfg.OverlayChannel, 40)
	bots := newInput(o.Bots, 200)
	size := newInput(strconv.Itoa(o.Size), 4)
	stroke := newInput(strconv.Itoa(o.Stroke), 4)
	fade := newInput(strconv.Itoa(o.Fade), 4)
	max := newInput(strconv.Itoa(o.Max), 4)

	// atoiKeep parses s, leaving *p unchanged if s isn't a number, so a typo
	// doesn't silently zero a setting.
	atoiKeep := func(p *int) func(string) {
		return func(s string) {
			if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				*p = n
			}
		}
	}

	alignIdx := new(int)
	if o.Align == "top" {
		*alignIdx = 1
	}

	scaleIdx := new(int)
	if cfg.ChatScale > 1 {
		*scaleIdx = cfg.ChatScale - 1
	}

	main := []*srow{
		{label: "overlay settings", open: pageOverlay},
		{label: "appearance", open: pageAppearance},
		{label: "youtube login", open: pageYouTube},
	}
	if login != "" {
		main = append(main, &srow{label: "log out"})
	}

	// The YouTube page exposes both login methods. Method 1 (recommended):
	// paste the youtube.com cookie header and verify — authenticates through the
	// same innertube endpoints the site uses, no Google project or quota. The
	// cookies field has no commit func on purpose: it is a draft the verify row
	// saves only on success, so "logged in" means verified, not "typed".
	// Method 2: a Google OAuth client (Data API) — client id/secret auto-commit
	// (the stored token, not these, is the auth gate) and the login row runs the
	// device flow.
	ytCookies := newInput(cfg.YouTubeCookies, 4000)
	ytClientID := newInput(cfg.YouTubeClientID, 120)
	ytClientSecret := newInput(cfg.YouTubeClientSecret, 120)
	yt := []*srow{
		{label: "cookies", ti: ytCookies},
		{label: "verify cookies"},
		{label: "client id", ti: ytClientID, commit: func(s string) { cfg.YouTubeClientID = strings.TrimSpace(s) }},
		{label: "client secret", ti: ytClientSecret, commit: func(s string) { cfg.YouTubeClientSecret = strings.TrimSpace(s) }},
		{label: "google login"},
	}

	// The overlay page owns everything the browser source shows; the look
	// options apply live to connected sources as they change.
	overlay := []*srow{
		{label: "overlay", boolp: &cfg.OverlayEnabled},
		{label: "address", ti: addr, commit: func(s string) { cfg.OverlayAddr = strings.TrimSpace(s) }},
		{label: "channel", ti: channel, commit: func(s string) { cfg.OverlayChannel = strings.TrimSpace(s) }},
		{label: "align", enump: alignIdx, enumOpts: []string{"bottom", "top"}},
		{label: "font", enump: &o.Font, enumOpts: fontNames},
		{label: "size (px)", ti: size, commit: atoiKeep(&o.Size)},
		{label: "outline", ti: stroke, commit: atoiKeep(&o.Stroke)},
		{label: "fade (s)", ti: fade, commit: atoiKeep(&o.Fade)},
		{label: "max messages", ti: max, commit: atoiKeep(&o.Max)},
		{label: "animate", boolp: &o.Animate},
		{label: "badges", boolp: &o.Badges},
		{label: "hide ! commands", boolp: &o.HideCommands},
		{label: "hide bots (csv)", ti: bots, commit: func(s string) { o.Bots = strings.TrimSpace(s) }},
		{label: "overlay url", display: cfg.OverlayURL},
	}

	// The appearance page is crow's own look. Terminal text comes in whole-cell
	// multiples (kitty's text sizing protocol), not arbitrary pixels.
	var appearance []*srow
	if kitty.TextSizing() {
		appearance = append(appearance, &srow{label: "chat text size", enump: scaleIdx, enumOpts: []string{"1×", "2×", "3×"}})
	} else {
		appearance = append(appearance, &srow{label: "chat text size", display: func() string { return "needs kitty" }})
	}

	st := settingsState{login: login, cfg: cfg, alignIdx: alignIdx, scaleIdx: scaleIdx,
		ytCookies: ytCookies, ytClientID: ytClientID, ytClientSecret: ytClientSecret}
	st.rows[pageMain] = main
	st.rows[pageOverlay] = overlay
	st.rows[pageAppearance] = appearance
	st.rows[pageYouTube] = yt
	st.refocus()
	return st
}

// settingsKey handles a key on the settings screen, then saves: every change
// persists and pushes to connected overlays immediately, so tweaking a value
// restyles OBS as you type. Saving on non-changes too is fine — the overlay
// broadcast dedupes and the config file is tiny.
func (a *App) settingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	model, cmd := a.settingsKeyInner(msg)
	a.settings.save(a)
	return model, cmd
}

func (a *App) settingsKeyInner(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.settings
	rows := st.rows[st.page]
	sel := &st.sel[st.page]
	cur := rows[*sel]

	switch msg.String() {
	case "esc":
		switch st.page {
		case pageAppearance, pageOverlay, pageYouTube:
			st.page = pageMain
		default:
			if len(a.tabs) > 0 {
				a.mode = modeChat
			} else {
				a.mode = modeSplash
			}
		}
		st.refocus()
		return a, nil

	case "up", "shift+tab":
		*sel = (*sel - 1 + len(rows)) % len(rows)
		st.refocus()
		return a, nil
	case "down", "tab":
		*sel = (*sel + 1) % len(rows)
		st.refocus()
		return a, nil

	case "left":
		if cur.enump != nil {
			*cur.enump = (*cur.enump - 1 + len(cur.enumOpts)) % len(cur.enumOpts)
		}
		return a, nil
	case "right":
		if cur.enump != nil {
			*cur.enump = (*cur.enump + 1) % len(cur.enumOpts)
		}
		return a, nil

	case "enter", " ":
		switch {
		case cur.open != pageMain:
			st.page = cur.open
			st.refocus()
		case cur.boolp != nil:
			*cur.boolp = !*cur.boolp
		case cur.enump != nil:
			*cur.enump = (*cur.enump + 1) % len(cur.enumOpts)
		case cur.label == "verify cookies" && st.page == pageYouTube:
			return a, st.ytCookieAction(a)
		case cur.label == "google login" && st.page == pageYouTube:
			return a, st.ytOAuthAction(a)
		case cur.label == "log out" && a.logout != nil:
			a.logout()
			a.login = ""
			st.login = ""
			st.rows[pageMain] = st.rows[pageMain][:len(st.rows[pageMain])-1] // drop logout row
			if *sel >= len(st.rows[pageMain]) {
				*sel = len(st.rows[pageMain]) - 1
			}
			st.refocus()
		}
		return a, nil
	}

	// Otherwise, edit the focused text field.
	if cur.ti != nil {
		var cmd tea.Cmd
		*cur.ti, cmd = cur.ti.Update(msg)
		return a, cmd
	}
	return a, nil
}

// YouTube login messages. ytCodeMsg shows an OAuth device code; ytDoneMsg
// reports a successful login (persistCookies distinguishes the cookie method,
// which must save its draft); ytErrMsg reports a failure.
type ytCodeMsg struct {
	code, url string
	handle    any
}
type ytDoneMsg struct {
	name           string
	persistCookies bool
}
type ytErrMsg struct{ err string }

// ytCookieAction handles enter on "verify cookies": log out when cookie auth
// is active, otherwise verify the pasted cookies against youtube.com.
func (s *settingsState) ytCookieAction(a *App) tea.Cmd {
	if s.ytCookiesAuthed != nil && s.ytCookiesAuthed() {
		if a.ytCookiesLogout != nil {
			a.ytCookiesLogout()
		}
		a.cfg.YouTubeCookies = ""
		s.ytCookies.SetValue("")
		s.ytStatus, s.ytErr = "cookies logged out", false
		return nil
	}
	if s.ytBusy || a.ytVerify == nil {
		return nil
	}
	cookies := strings.TrimSpace(s.ytCookies.Value())
	if cookies == "" {
		s.ytStatus, s.ytErr = "paste your youtube.com cookies first", true
		return nil
	}
	s.ytBusy, s.ytErr = true, false
	s.ytStatus = "verifying cookies…"
	return func() tea.Msg {
		name, err := a.ytVerify(cookies)
		if err != nil {
			return ytErrMsg{err.Error()}
		}
		return ytDoneMsg{name: name, persistCookies: true}
	}
}

// ytOAuthAction handles enter on "google login": log out when OAuth is active,
// otherwise start the device flow with the client id/secret as typed.
func (s *settingsState) ytOAuthAction(a *App) tea.Cmd {
	if s.ytOAuthAuthed != nil && s.ytOAuthAuthed() {
		if a.ytOAuthLogout != nil {
			a.ytOAuthLogout()
		}
		s.ytStatus, s.ytErr = "google logged out", false
		return nil
	}
	if s.ytBusy || a.ytOAuthStart == nil {
		return nil
	}
	id := strings.TrimSpace(s.ytClientID.Value())
	secret := strings.TrimSpace(s.ytClientSecret.Value())
	if id == "" || secret == "" {
		s.ytStatus, s.ytErr = "enter a client id and secret first", true
		return nil
	}
	s.ytBusy, s.ytErr = true, false
	s.ytStatus = "requesting device code…"
	return func() tea.Msg {
		code, url, handle, err := a.ytOAuthStart(id, secret)
		if err != nil {
			return ytErrMsg{err.Error()}
		}
		return ytCodeMsg{code: code, url: url, handle: handle}
	}
}

// ytLoginUpdate advances both YouTube login flows; called from App.Update.
func (a *App) ytLoginUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	st := &a.settings
	switch msg := msg.(type) {
	case ytCodeMsg:
		st.ytHandle = msg.handle
		st.ytStatus = "open " + msg.url + " and enter code " + msg.code
		return a, func() tea.Msg {
			name, err := a.ytOAuthPoll(msg.handle)
			if err != nil {
				return ytErrMsg{err.Error()}
			}
			return ytDoneMsg{name: name}
		}
	case ytDoneMsg:
		st.ytBusy, st.ytErr = false, false
		st.ytStatus = "logged in as " + msg.name
		if msg.persistCookies {
			// Persist only now that the cookies are proven good. This message
			// arrives outside settingsKey, so save explicitly.
			a.cfg.YouTubeCookies = strings.TrimSpace(st.ytCookies.Value())
			st.save(a)
		}
	case ytErrMsg:
		st.ytBusy, st.ytErr = false, true
		st.ytStatus = msg.err
	}
	return a, nil
}

// save commits every text row into the config, writes back the align enum, and
// persists.
func (s *settingsState) save(a *App) {
	for _, page := range s.rows {
		for _, r := range page {
			if r.ti != nil && r.commit != nil {
				r.commit(r.ti.Value())
			}
		}
	}
	if *s.alignIdx == 1 {
		s.cfg.Overlay.Align = "top"
	} else {
		s.cfg.Overlay.Align = "bottom"
	}
	s.cfg.ChatScale = *s.scaleIdx + 1
	// Chat scale applies live: re-stamp every open tab.
	for _, t := range a.tabs {
		t.model.scale = a.chatScale()
	}
	if a.save != nil {
		a.save(*s.cfg)
	}
}

// refocus points the text cursor at the selected row's field on the current
// page, blurring every other field.
func (s *settingsState) refocus() {
	for _, page := range s.rows {
		for _, r := range page {
			if r.ti != nil {
				r.ti.Blur()
			}
		}
	}
	if cur := s.rows[s.page][s.sel[s.page]]; cur.ti != nil {
		cur.ti.Focus()
	}
}

// ytActionVal renders a YouTube login row's right-hand value from its authed
// probe and the page's shared busy flag.
func ytActionVal(s *styles, authed func() bool, busy bool) string {
	switch {
	case authed != nil && authed():
		return s.key.Render("logged in") + s.dim.Render(" · enter to log out")
	case busy:
		return s.dim.Render("working…")
	default:
		return s.dim.Render("enter to log in")
	}
}

func (a *App) settingsView() string {
	s := a.styles
	st := &a.settings
	var b strings.Builder

	title := "Settings"
	switch st.page {
	case pageOverlay:
		title = "Overlay"
	case pageAppearance:
		title = "Appearance"
	case pageYouTube:
		title = "YouTube"
	}
	b.WriteString(s.splashTitle.Render(title) + "\n\n")

	onOff := func(v bool) string {
		if v {
			return s.key.Render("on")
		}
		return s.dim.Render("off")
	}

	rows := st.rows[st.page]
	for i, r := range rows {
		marker := "  "
		lbl := s.cardLabel.Render(r.label)
		if st.sel[st.page] == i {
			marker = s.key.Render("› ")
			lbl = s.cardTitle.Render(r.label)
		}

		var val string
		switch {
		case r.open != pageMain:
			val = s.dim.Render("›")
		case r.ti != nil:
			val = r.ti.View()
		case r.boolp != nil:
			val = onOff(*r.boolp)
		case r.enump != nil:
			val = s.cardTitle.Render("‹ " + r.enumOpts[*r.enump] + " ›")
		case r.display != nil:
			val = s.name(chat.Message{Author: "url"}).Render(r.display())
		case r.label == "verify cookies" && st.page == pageYouTube:
			val = ytActionVal(s, st.ytCookiesAuthed, st.ytBusy)
		case r.label == "google login" && st.page == pageYouTube:
			val = ytActionVal(s, st.ytOAuthAuthed, st.ytBusy)
		case r.label == "log out":
			who := s.name(chat.Message{Author: st.login}).Render(st.login)
			lbl = s.cardLabel.Render("logged in as " + who)
			if st.sel[st.page] == i {
				lbl = s.cardTitle.Render("logged in as " + who)
			}
			val = s.dim.Render("enter to log out")
		}

		b.WriteString(marker + lbl + "  " + val + "\n")
	}

	b.WriteString("\n")
	switch st.page {
	case pageYouTube:
		b.WriteString(s.dim.Render("two ways to send & moderate as your account; a tab uses cookies") + "\n")
		b.WriteString(s.dim.Render("if set, else Google. cookies: a logged-in youtube.com tab ›") + "\n")
		b.WriteString(s.dim.Render("devtools › Network › copy the Cookie header. google: a \"TVs and") + "\n")
		b.WriteString(s.dim.Render("Limited Input devices\" OAuth client from console.cloud.google.com.") + "\n")
		if st.ytStatus != "" {
			b.WriteString("\n")
			if st.ytErr {
				b.WriteString(s.danger.Render(st.ytStatus) + "\n")
			} else {
				b.WriteString(s.key.Render(st.ytStatus) + "\n")
			}
		}
		b.WriteString("\n" + s.key.Render("↑/↓") + s.dim.Render(" move · ") +
			s.key.Render("enter") + s.dim.Render(" log in/out · ") +
			s.key.Render("esc") + s.dim.Render(" back"))
	case pageOverlay:
		b.WriteString(s.dim.Render("on/off, address & channel apply on restart") + "\n")
		b.WriteString("\n" + s.key.Render("↑/↓") + s.dim.Render(" move · ") +
			s.key.Render("←/→/enter") + s.dim.Render(" change · ") +
			s.key.Render("esc") + s.dim.Render(" back"))
	case pageAppearance:
		b.WriteString(s.dim.Render("applies live") + "\n")
		b.WriteString("\n" + s.key.Render("↑/↓") + s.dim.Render(" move · ") +
			s.key.Render("←/→/enter") + s.dim.Render(" change · ") +
			s.key.Render("esc") + s.dim.Render(" back"))
	default:
		b.WriteString(s.dim.Render("config: "+config.Path()) + "\n")
		b.WriteString("\n" + s.key.Render("↑/↓") + s.dim.Render(" move · ") +
			s.key.Render("enter") + s.dim.Render(" open · ") +
			s.key.Render("esc") + s.dim.Render(" save & close"))
	}

	box := s.cardBorder.Render(b.String())
	return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center, box)
}
