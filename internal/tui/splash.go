package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Nyxnix/typetype/internal/chat"
)

// splashState is the startup / add-channel screen: it collects a channel to
// open and, when not logged in, offers an inline Twitch login.
type splashState struct {
	input     textinput.Model
	loggedIn  bool
	canCancel bool // opened as an add-channel prompt; Esc returns to chat

	// inline login flow
	loggingIn bool
	loginCode string
	loginURL  string
	loginErr  string
	handle    any // opaque device-code handle passed back to PollLogin
}

func newSplashState(loggedIn bool) splashState {
	ti := textinput.New()
	ti.Prompt = "channel › "
	ti.Placeholder = "e.g. caedrel"
	ti.CharLimit = 40
	ti.Focus()
	return splashState{input: ti, loggedIn: loggedIn}
}

// Login-flow messages, produced by the host-provided RequestCode/PollLogin.
type deviceCodeMsg struct {
	code, url string
	handle    any
}
type tokenMsg struct{ login string }
type loginErrMsg struct{ err string }

func (a *App) splashKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		channel := strings.TrimSpace(a.splash.input.Value())
		if channel == "" {
			if a.splash.canCancel {
				a.mode = modeChat
			}
			return a, nil
		}
		return a, a.openTab(channel)

	case "esc":
		if a.splash.canCancel {
			a.mode = modeChat
		}
		return a, nil

	case "ctrl+l":
		// Start the inline login unless already logged in or in flight.
		if a.splash.loggedIn || a.splash.loggingIn || a.requestCode == nil {
			return a, nil
		}
		a.splash.loggingIn = true
		a.splash.loginErr = ""
		return a, a.startLoginCmd()
	}

	var cmd tea.Cmd
	a.splash.input, cmd = a.splash.input.Update(msg)
	return a, cmd
}

// splashLoginUpdate advances the inline login flow.
func (a *App) splashLoginUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case deviceCodeMsg:
		a.splash.loginCode = msg.code
		a.splash.loginURL = msg.url
		a.splash.handle = msg.handle
		return a, a.pollLoginCmd(msg.handle)

	case tokenMsg:
		a.login = msg.login
		a.splash.loggedIn = true
		a.splash.loggingIn = false
		a.splash.loginCode = ""
		return a, nil

	case loginErrMsg:
		a.splash.loggingIn = false
		a.splash.loginErr = msg.err
		return a, nil
	}
	return a, nil
}

func (a *App) startLoginCmd() tea.Cmd {
	return func() tea.Msg {
		code, url, handle, err := a.requestCode()
		if err != nil {
			return loginErrMsg{err.Error()}
		}
		return deviceCodeMsg{code: code, url: url, handle: handle}
	}
}

func (a *App) pollLoginCmd(handle any) tea.Cmd {
	return func() tea.Msg {
		login, err := a.pollLogin(handle)
		if err != nil {
			return loginErrMsg{err.Error()}
		}
		return tokenMsg{login: login}
	}
}

func (a *App) splashView() string {
	s := a.styles
	var b strings.Builder

	b.WriteString(s.splashTitle.Render("TypeType") + "\n")
	b.WriteString(s.dim.Render("terminal Twitch chat with an OBS overlay") + "\n\n")

	// Login status / inline login.
	switch {
	case a.splash.loggedIn:
		b.WriteString("logged in as " + s.name(chat.Message{Author: a.login}).Render(a.login) + "\n\n")
	case a.splash.loginCode != "":
		b.WriteString(s.cardLabel.Render("to log in, open") + " " + a.splash.loginURL + "\n")
		b.WriteString(s.cardLabel.Render("and enter code") + " " + s.key.Render(a.splash.loginCode) + "\n")
		b.WriteString(s.dim.Render("waiting for approval…") + "\n\n")
	case a.splash.loggingIn:
		b.WriteString(s.dim.Render("starting login…") + "\n\n")
	default:
		b.WriteString(s.dim.Render("browsing anonymously — ") +
			s.key.Render("^L") + s.dim.Render(" to log in for sending & moderation") + "\n\n")
	}
	if a.splash.loginErr != "" {
		b.WriteString(s.danger.Render("login failed: "+a.splash.loginErr) + "\n\n")
	}

	b.WriteString(a.splash.input.View() + "\n")
	hint := "enter to join"
	if a.splash.canCancel {
		hint += " · esc to cancel"
	}
	b.WriteString("\n" + s.dim.Render(hint))

	// Center the block in the window.
	box := lipgloss.NewStyle().Padding(1, 2).Render(b.String())
	return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center, box)
}
