package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Nyxnix/crow/internal/chat"
	"github.com/Nyxnix/crow/internal/config"
)

// The two settings pages: a short top menu, and the overlay sub-menu.
const (
	pageMain = iota
	pageOverlay
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

	enump    *int     // cycle with enter/←/→
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
	rows     [2][]*srow
	sel      [2]int
	alignIdx *int // heap-backed so it survives this value being copied on return
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

	main := []*srow{
		{label: "overlay settings", open: pageOverlay},
	}
	if login != "" {
		main = append(main, &srow{label: "log out"})
	}

	overlay := []*srow{
		{label: "overlay", boolp: &cfg.OverlayEnabled},
		{label: "address", ti: addr, commit: func(s string) { cfg.OverlayAddr = strings.TrimSpace(s) }},
		{label: "channel", ti: channel, commit: func(s string) { cfg.OverlayChannel = strings.TrimSpace(s) }},
		{label: "align", enump: alignIdx, enumOpts: []string{"bottom", "top"}},
		{label: "font", enump: &o.Font, enumOpts: fontNames},
		{label: "size", ti: size, commit: atoiKeep(&o.Size)},
		{label: "outline", ti: stroke, commit: atoiKeep(&o.Stroke)},
		{label: "fade (s)", ti: fade, commit: atoiKeep(&o.Fade)},
		{label: "max messages", ti: max, commit: atoiKeep(&o.Max)},
		{label: "animate", boolp: &o.Animate},
		{label: "badges", boolp: &o.Badges},
		{label: "hide ! commands", boolp: &o.HideCommands},
		{label: "hide bots (csv)", ti: bots, commit: func(s string) { o.Bots = strings.TrimSpace(s) }},
		{label: "overlay url", display: cfg.OverlayURL},
	}

	st := settingsState{login: login, cfg: cfg, alignIdx: alignIdx}
	st.rows[pageMain] = main
	st.rows[pageOverlay] = overlay
	st.refocus()
	return st
}

func (a *App) settingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.settings
	rows := st.rows[st.page]
	sel := &st.sel[st.page]
	cur := rows[*sel]

	switch msg.String() {
	case "esc":
		// From a sub-page, back out to the top menu; from the top, save & close.
		if st.page != pageMain {
			st.page = pageMain
			st.refocus()
			return a, nil
		}
		st.save(a)
		if len(a.tabs) > 0 {
			a.mode = modeChat
		} else {
			a.mode = modeSplash
		}
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

func (a *App) settingsView() string {
	s := a.styles
	st := &a.settings
	var b strings.Builder

	title := "Settings"
	if st.page == pageOverlay {
		title = "Overlay"
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
	if st.page == pageOverlay {
		b.WriteString(s.dim.Render("changes apply on restart") + "\n")
		b.WriteString("\n" + s.key.Render("↑/↓") + s.dim.Render(" move · ") +
			s.key.Render("←/→/enter") + s.dim.Render(" change · ") +
			s.key.Render("esc") + s.dim.Render(" back"))
	} else {
		b.WriteString(s.dim.Render("config: "+config.Path()) + "\n")
		b.WriteString("\n" + s.key.Render("↑/↓") + s.dim.Render(" move · ") +
			s.key.Render("enter") + s.dim.Render(" open · ") +
			s.key.Render("esc") + s.dim.Render(" save & close"))
	}

	box := s.cardBorder.Render(b.String())
	return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center, box)
}
