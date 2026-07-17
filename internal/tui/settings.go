package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Nyxnix/typetype/internal/chat"
	"github.com/Nyxnix/typetype/internal/config"
)

// settingsState is the Ctrl+S configuration screen. It edits the overlay
// address and channel and shows the login, with the current config saved on
// exit.
type settingsState struct {
	login   string
	addr    textinput.Model
	channel textinput.Model
	sel     int // 0 addr, 1 channel, 2 logout (when logged in)
}

func newSettingsState(login string, cfg config.Config) settingsState {
	addr := textinput.New()
	addr.Prompt = ""
	addr.SetValue(cfg.OverlayAddr)
	addr.CharLimit = 40
	addr.Focus()

	ch := textinput.New()
	ch.Prompt = ""
	ch.SetValue(cfg.OverlayChannel)
	ch.Placeholder = "(first open channel)"
	ch.CharLimit = 40

	return settingsState{login: login, addr: addr, channel: ch}
}

// settingsRows is how many selectable rows there are (logout only when logged
// in).
func (s settingsState) rows() int {
	if s.login != "" {
		return 3
	}
	return 2
}

func (a *App) settingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.settings
	switch msg.String() {
	case "esc":
		// Save and return to chat (or the splash if nothing is open).
		a.cfg.OverlayAddr = strings.TrimSpace(st.addr.Value())
		a.cfg.OverlayChannel = strings.TrimSpace(st.channel.Value())
		if a.save != nil {
			a.save(a.cfg)
		}
		if len(a.tabs) > 0 {
			a.mode = modeChat
		} else {
			a.mode = modeSplash
		}
		return a, nil

	case "up", "shift+tab":
		st.sel = (st.sel - 1 + st.rows()) % st.rows()
		st.refocus()
		return a, nil
	case "down", "tab":
		st.sel = (st.sel + 1) % st.rows()
		st.refocus()
		return a, nil

	case "enter":
		if st.sel == 2 && a.logout != nil { // logout row
			a.logout()
			a.login = ""
			st.login = ""
			st.sel = 0
			st.refocus()
		}
		return a, nil
	}

	// Editing the focused text field.
	var cmd tea.Cmd
	switch st.sel {
	case 0:
		st.addr, cmd = st.addr.Update(msg)
	case 1:
		st.channel, cmd = st.channel.Update(msg)
	}
	return a, cmd
}

// refocus points the text cursor at the selected field, blurring the others.
func (s *settingsState) refocus() {
	s.addr.Blur()
	s.channel.Blur()
	switch s.sel {
	case 0:
		s.addr.Focus()
	case 1:
		s.channel.Focus()
	}
}

func (a *App) settingsView() string {
	s := a.styles
	st := &a.settings
	var b strings.Builder

	b.WriteString(s.splashTitle.Render("Settings") + "\n\n")

	row := func(i int, label, val string) {
		marker := "  "
		lbl := s.cardLabel.Render(label)
		if st.sel == i {
			marker = s.key.Render("› ")
			lbl = s.cardTitle.Render(label)
		}
		b.WriteString(marker + lbl + "  " + val + "\n")
	}

	row(0, "overlay address", st.addr.View())
	row(1, "overlay channel", st.channel.View())

	b.WriteString("\n")
	if st.login != "" {
		who := s.name(chat.Message{Author: st.login}).Render(st.login)
		row(2, "logged in as "+who, s.dim.Render("enter to log out"))
	} else {
		b.WriteString("  " + s.dim.Render("browsing anonymously") + "\n")
	}

	b.WriteString("\n" + s.dim.Render("config: "+config.Path()) + "\n")
	b.WriteString(s.dim.Render("overlay changes apply on restart") + "\n")
	b.WriteString("\n" + s.key.Render("↑/↓") + s.dim.Render(" move · ") +
		s.key.Render("esc") + s.dim.Render(" save & close"))

	box := s.cardBorder.Render(b.String())
	return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center, box)
}
