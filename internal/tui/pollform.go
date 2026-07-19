package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Poll form rows, in focus order. Rows 0-5 and 7-8 are text inputs; the
// channel-points toggle and the start button are handled directly.
const (
	pollRowTitle    = 0
	pollRowChoice1  = 1 // ..through pollRowChoice1+4
	pollRowCPToggle = 6
	pollRowPoints   = 7
	pollRowDuration = 8
	pollRowStart    = 9
)

// pollForm mirrors Twitch's own "Create a New Poll" popup: question, up to
// five responses, optional channel-points voting, duration. Opened by a bare
// /poll; the one-line /poll "title" ... form still works for the quick case.
type pollForm struct {
	inputs [9]textinput.Model // indexed by row; 6 (toggle) unused
	cp     bool               // channel-points voting enabled
	focus  int
	errMsg string
}

func newPollForm() *pollForm {
	f := &pollForm{}
	mk := func(row int, placeholder string, limit, width int) {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.CharLimit = limit
		ti.Width = width
		f.inputs[row] = ti
	}
	mk(pollRowTitle, "question", 60, cardWidth-4)
	for i := 0; i < 5; i++ {
		mk(pollRowChoice1+i, fmt.Sprintf("response %d", i+1), 25, cardWidth-8)
	}
	mk(pollRowPoints, "200", 7, 8)
	mk(pollRowDuration, "1m", 6, 8)
	f.inputs[pollRowTitle].Focus()
	return f
}

// input returns the text input for a row, nil for the toggle and start rows.
func (f *pollForm) input(row int) *textinput.Model {
	if row == pollRowCPToggle || row == pollRowStart {
		return nil
	}
	return &f.inputs[row]
}

func (f *pollForm) setFocus(row int) {
	if in := f.input(f.focus); in != nil {
		in.Blur()
	}
	f.focus = row
	if in := f.input(row); in != nil {
		in.Focus()
	}
}

func (m *Model) pollFormKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.pollForm
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.pollForm = nil
		return m, nil
	case "up", "shift+tab":
		f.setFocus((f.focus + pollRowStart) % (pollRowStart + 1))
		return m, nil
	case "down", "tab":
		f.setFocus((f.focus + 1) % (pollRowStart + 1))
		return m, nil
	case "enter":
		if f.focus == pollRowStart {
			return m, m.startPoll()
		}
		f.setFocus(f.focus + 1)
		return m, nil
	case " ":
		if f.focus == pollRowCPToggle {
			f.cp = !f.cp
			return m, nil
		}
	}
	if in := f.input(f.focus); in != nil {
		var cmd tea.Cmd
		*in, cmd = in.Update(msg)
		return m, cmd
	}
	return m, nil
}

// startPoll validates locally only what would otherwise submit garbage (an
// empty question, fewer than two choices); value ranges are Helix's job and
// its errors read fine in the status bar.
func (m *Model) startPoll() tea.Cmd {
	f := m.pollForm
	title := strings.TrimSpace(f.inputs[pollRowTitle].Value())
	var choices []string
	for i := 0; i < 5; i++ {
		if c := strings.TrimSpace(f.inputs[pollRowChoice1+i].Value()); c != "" {
			choices = append(choices, c)
		}
	}
	if title == "" || len(choices) < 2 {
		f.errMsg = "need a question and at least 2 responses"
		return nil
	}
	dur, err := parseDur(f.inputs[pollRowDuration].Value(), 60)
	if err != nil {
		f.errMsg = "bad duration (try 90 or 2m)"
		return nil
	}
	points := 0
	if f.cp {
		points = 200
		if v := strings.TrimSpace(f.inputs[pollRowPoints].Value()); v != "" {
			if points, err = strconv.Atoi(v); err != nil {
				f.errMsg = "channel points per vote must be a number"
				return nil
			}
		}
	}

	m.pollForm = nil
	return m.voteCreate("poll", func(ctx context.Context) error {
		return m.chanMgr.CreatePoll(ctx, title, choices, dur, points)
	}, "poll started: "+title)
}

// renderPollForm draws the form panel beside chat, the same shape as the user
// card.
func (m *Model) renderPollForm(body string) string {
	s := m.styles
	f := m.pollForm

	focused := func(row int, label string) string {
		if f.focus == row {
			return s.cardKey.Render("› ") + label
		}
		return "  " + label
	}

	var b strings.Builder
	b.WriteString(s.cardTitle.Render("create a poll") + "\n\n")
	b.WriteString(s.cardLabel.Render("question") + "\n")
	b.WriteString(focused(pollRowTitle, f.inputs[pollRowTitle].View()) + "\n\n")
	b.WriteString(s.cardLabel.Render("responses (min 2)") + "\n")
	for i := 0; i < 5; i++ {
		b.WriteString(focused(pollRowChoice1+i, f.inputs[pollRowChoice1+i].View()) + "\n")
	}
	b.WriteString("\n")
	check := "[ ]"
	if f.cp {
		check = "[x]"
	}
	b.WriteString(focused(pollRowCPToggle, check+" channel points voting") + "\n")
	b.WriteString(focused(pollRowPoints, "points per vote "+f.inputs[pollRowPoints].View()) + "\n")
	b.WriteString(focused(pollRowDuration, "duration "+f.inputs[pollRowDuration].View()) + "\n\n")
	start := "start poll"
	if f.focus == pollRowStart {
		start = s.cardKey.Render("› [ start poll ]")
	} else {
		start = "  [ " + start + " ]"
	}
	b.WriteString(start + "\n")
	if f.errMsg != "" {
		b.WriteString("\n" + s.danger.Render(f.errMsg) + "\n")
	}
	b.WriteString("\n" + s.dim.Render("↑/↓ move · space toggle · enter next/start · esc cancel"))

	panel := s.cardBorder.Width(cardWidth).Render(strings.TrimRight(b.String(), "\n"))
	panel = lipgloss.NewStyle().Height(m.viewportHeight()).Render(panel)
	left := lipgloss.NewStyle().
		Width(m.chatWidth()).
		Height(m.viewportHeight()).
		Render(body)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", cardGutter), panel)
}
