package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Nyxnix/crow/internal/config"
)

// appMode is which screen the App is showing.
type appMode int

const (
	modeSplash   appMode = iota // startup / add-channel prompt
	modeChat                    // the tabbed chat view
	modeSettings                // configuration
)

// tabBarHeight is the one row the tab strip occupies above each chat view.
const tabBarHeight = 1

// TabFactory opens a channel: it wires that channel's connections and returns a
// ready chat Model plus a teardown to stop them. Provided by the host (main).
type TabFactory func(channel string) (*Model, func())

// tab is one open channel.
type tab struct {
	id      int
	channel string
	model   *Model
	close   func()
}

// App is the root bubbletea model. It owns the open channels (tabs), the splash
// and settings screens, and routes input to the active tab.
type App struct {
	mode   appMode
	tabs   []*tab
	active int
	nextID int

	width, height int
	styles        *styles

	open   TabFactory
	redraw chan struct{}

	login string // Twitch login, empty when anonymous
	cfg   config.Config
	save  func(config.Config)

	// Inline login (device flow), provided by the host. requestCode starts it
	// and returns a code to show plus an opaque handle; pollLogin blocks until
	// the user approves and returns their login. Nil disables inline login.
	requestCode func() (code, url string, handle any, err error)
	pollLogin   func(handle any) (login string, err error)
	logout      func() // clears the stored token

	splash   splashState
	settings settingsState

	// pending holds startup channels until the first size message, when they are
	// opened.
	pending []string
}

// AppOptions configures a new App.
type AppOptions struct {
	Factory  TabFactory
	Login    string // "" when anonymous
	Config   config.Config
	Save     func(config.Config)
	Channels []string // channels to open at startup; empty shows the splash

	RequestCode func() (code, url string, handle any, err error)
	PollLogin   func(handle any) (login string, err error)
	Logout      func()
}

func NewApp(o AppOptions) *App {
	a := &App{
		styles:      newStyles(),
		open:        o.Factory,
		login:       o.Login,
		cfg:         o.Config,
		save:        o.Save,
		requestCode: o.RequestCode,
		pollLogin:   o.PollLogin,
		logout:      o.Logout,
		redraw:      make(chan struct{}, 1),
	}
	a.splash = newSplashState(o.Login != "")
	if len(o.Channels) == 0 {
		a.mode = modeSplash
	}
	// Channels open in Init, once we have a window size.
	a.pending = o.Channels
	return a
}

func (a *App) requestRedraw() {
	select {
	case a.redraw <- struct{}{}:
	default:
	}
}

// RequestRedraw lets host-side goroutines (a channel's reader, its stats
// poller) ask the App to re-render.
func (a *App) RequestRedraw() { a.requestRedraw() }

func (a *App) Init() tea.Cmd {
	return tea.Batch(waitAppRedraw(a.redraw), a.splash.input.Focus())
}

// appRedrawMsg wakes the render loop when host state changes off the UI
// goroutine.
type appRedrawMsg struct{}

func waitAppRedraw(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return appRedrawMsg{}
	}
}

// sessionMsg carries a message destined for one tab's model, so a background
// tab's command results route to it rather than the active tab.
type sessionMsg struct {
	id    int
	inner tea.Msg
}

// tag wraps a command so its result is delivered to tab id. Never wrap a Batch;
// the model only ever returns single commands, which keeps this safe.
func (a *App) tag(id int, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		inner := cmd()
		if inner == nil {
			return nil
		}
		return sessionMsg{id: id, inner: inner}
	}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return a.onResize(msg)

	case appRedrawMsg:
		return a, waitAppRedraw(a.redraw)

	case sessionMsg:
		t := a.tabByID(msg.id)
		if t == nil {
			return a, nil // tab was closed while its command was in flight
		}
		nm, cmd := t.model.Update(msg.inner)
		t.model = nm.(*Model)
		return a, a.tag(msg.id, cmd)

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
		switch a.mode {
		case modeSplash:
			return a.splashKey(msg)
		case modeSettings:
			return a.settingsKey(msg)
		default:
			return a.chatKey(msg)
		}

	case tea.MouseMsg:
		if a.mode == modeChat {
			return a.routeMouse(msg)
		}
		return a, nil

	// Splash's inline-login flow.
	case deviceCodeMsg, tokenMsg, loginErrMsg:
		return a.splashLoginUpdate(msg)
	}

	// Anything else (cursor blink for the splash input) goes to the splash when
	// it is showing, otherwise it is ignored.
	if a.mode == modeSplash {
		var cmd tea.Cmd
		a.splash.input, cmd = a.splash.input.Update(msg)
		return a, cmd
	}
	return a, nil
}

// chatKey handles the tab-management keys, then routes everything else to the
// active tab's model. The global keys use Ctrl so they don't collide with
// typing in the message input.
func (a *App) chatKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+s":
		a.mode = modeSettings
		a.settings = newSettingsState(a.login, &a.cfg)
		return a, nil
	case "ctrl+t":
		// Add-channel prompt: reuse the splash, returnable to chat on Esc.
		a.mode = modeSplash
		a.splash = newSplashState(a.login != "")
		a.splash.canCancel = len(a.tabs) > 0
		return a, a.splash.input.Focus()
	case "ctrl+w":
		a.closeActiveTab()
		return a, nil
	// ctrl+n (next) / ctrl+b (back) are the primary switch keys — adjacent and
	// not grabbed by the OS. The arrows are a bonus for terminals where they get
	// through (macOS binds ctrl+arrow to Mission Control, so it never does there).
	case "ctrl+n", "ctrl+right", "ctrl+shift+right":
		a.switchTab(a.active + 1)
		return a, nil
	case "ctrl+b", "ctrl+left", "ctrl+shift+left":
		a.switchTab(a.active - 1)
		return a, nil
	}
	// Ctrl+1..9 jumps to a tab.
	if len(msg.String()) == 6 && strings.HasPrefix(msg.String(), "ctrl+") {
		if d := msg.String()[5]; d >= '1' && d <= '9' {
			a.switchTab(int(d - '1'))
			return a, nil
		}
	}

	t := a.activeTab()
	if t == nil {
		return a, nil
	}
	nm, cmd := t.model.Update(msg)
	t.model = nm.(*Model)
	return a, a.tag(t.id, cmd)
}

// switchTab makes tab index i active, wrapping around the ends.
func (a *App) switchTab(i int) {
	if len(a.tabs) == 0 {
		return
	}
	a.active = (i%len(a.tabs) + len(a.tabs)) % len(a.tabs)
}

// routeMouse sends a mouse event to the active tab, translated into the model's
// own coordinate space below the tab bar.
func (a *App) routeMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	t := a.activeTab()
	if t == nil {
		return a, nil
	}
	msg.Y -= tabBarHeight
	if msg.Y < 0 {
		return a, nil // a click on the tab bar itself
	}
	nm, cmd := t.model.Update(msg)
	t.model = nm.(*Model)
	return a, a.tag(t.id, cmd)
}

func (a *App) onResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	a.width, a.height = msg.Width, msg.Height

	// Give every tab's model the chat area's size (below the tab bar).
	for _, t := range a.tabs {
		t.model.Update(tea.WindowSizeMsg{Width: a.width, Height: a.chatHeight()})
	}
	a.splash.input.Width = min(a.width-4, 40)

	// Open any startup channels now that we know the size.
	if len(a.pending) > 0 {
		chans := a.pending
		a.pending = nil
		var cmds []tea.Cmd
		for _, ch := range chans {
			cmds = append(cmds, a.openTab(ch))
		}
		return a, tea.Batch(cmds...)
	}
	return a, nil
}

// chatHeight is the model viewport height, leaving a row for the tab bar.
func (a *App) chatHeight() int {
	h := a.height - tabBarHeight
	if h < 1 {
		return 1
	}
	return h
}

// openTab opens a channel, makes it active, and returns its model's start
// command (tagged). It switches to chat mode.
func (a *App) openTab(channel string) tea.Cmd {
	channel = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(channel)), "#")
	if channel == "" {
		return nil
	}
	// Focus an existing tab for this channel rather than opening a duplicate.
	for i, t := range a.tabs {
		if t.channel == channel {
			a.active = i
			a.mode = modeChat
			return nil
		}
	}

	model, closeFn := a.open(channel)
	if a.width > 0 {
		model.Update(tea.WindowSizeMsg{Width: a.width, Height: a.chatHeight()})
	}
	t := &tab{id: a.nextID, channel: channel, model: model, close: closeFn}
	a.nextID++
	a.tabs = append(a.tabs, t)
	a.active = len(a.tabs) - 1
	a.mode = modeChat
	a.persist()
	return a.tag(t.id, model.Init())
}

// closeActiveTab stops the active tab's connections and removes it. With no
// tabs left it returns to the splash.
func (a *App) closeActiveTab() {
	if len(a.tabs) == 0 {
		return
	}
	t := a.tabs[a.active]
	t.close()
	a.tabs = append(a.tabs[:a.active], a.tabs[a.active+1:]...)
	if a.active >= len(a.tabs) {
		a.active = len(a.tabs) - 1
	}
	if len(a.tabs) == 0 {
		a.active = 0
		a.mode = modeSplash
		a.splash = newSplashState(a.login != "")
	}
	a.persist()
}

func (a *App) tabByID(id int) *tab {
	for _, t := range a.tabs {
		if t.id == id {
			return t
		}
	}
	return nil
}

func (a *App) activeTab() *tab {
	if a.active < 0 || a.active >= len(a.tabs) {
		return nil
	}
	return a.tabs[a.active]
}

// persist saves the open channels so the next launch reopens them.
func (a *App) persist() {
	chans := make([]string, 0, len(a.tabs))
	for _, t := range a.tabs {
		chans = append(chans, t.channel)
	}
	a.cfg.Channels = chans
	if a.save != nil {
		a.save(a.cfg)
	}
}

func (a *App) View() string {
	if a.width == 0 {
		return ""
	}
	switch a.mode {
	case modeSplash:
		return a.splashView()
	case modeSettings:
		return a.settingsView()
	default:
		return a.chatView()
	}
}

// chatView draws the tab bar above the active tab's chat.
func (a *App) chatView() string {
	t := a.activeTab()
	if t == nil {
		return a.splashView()
	}
	return a.tabBar() + "\n" + t.model.View()
}

// tabBar renders the strip of open channels, the active one highlighted.
func (a *App) tabBar() string {
	var b strings.Builder
	for i, t := range a.tabs {
		label := " #" + t.channel + " "
		if i == a.active {
			b.WriteString(a.styles.tabActive.Render(label))
		} else {
			b.WriteString(a.styles.tabInactive.Render(label))
		}
	}
	hint := a.styles.dim.Render(" ^T new · ^W close · ^N/^B switch · ^S settings ")
	used := lipgloss.Width(b.String()) + lipgloss.Width(hint)
	if gap := a.width - used; gap > 0 {
		b.WriteString(strings.Repeat(" ", gap))
	}
	b.WriteString(hint)
	return a.styles.tabBar.Width(a.width).Render(b.String())
}
