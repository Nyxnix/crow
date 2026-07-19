package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/Nyxnix/crow/internal/chat"
)

// Live poll/prediction display: after crow starts one (or finds one running
// at tab open), the model refetches its status every few seconds and renders
// it as a fixed block above the input line, lingering briefly on the final
// numbers. One block at a time; a poll and a prediction running together show
// whichever updated last.

const (
	voteRefresh = 3 * time.Second
	voteLinger  = 8 * time.Second
)

// voteStarted reports a successful poll/prediction creation and kicks off the
// watch. voteMsg carries a status fetch; voteTick/voteClear schedule the next
// fetch and the final dismissal. gen guards against stale timers after a new
// poll replaces the watched one.
type voteStarted struct{ kind, okText string }
type voteMsg struct {
	gen  int
	gone bool
	poll chat.Poll
}
type voteTick struct{ gen int }
type voteClear struct{ gen int }

func voteActive(p chat.Poll) bool {
	return p.Status == "ACTIVE" || (p.Kind == "prediction" && p.Status == "LOCKED")
}

// onVote handles the watch messages; ok reports whether msg was one of them.
func (m *Model) onVote(msg tea.Msg) (cmd tea.Cmd, ok bool) {
	switch msg := msg.(type) {
	case voteStarted:
		m.notice, m.noticeErr = msg.okText, false
		m.voteGen++
		return m.fetchVote(msg.kind, m.voteGen), true

	case voteMsg:
		if msg.gen != m.voteGen {
			return nil, true
		}
		if msg.gone {
			m.vote = nil
			return nil, true
		}
		p := msg.poll
		m.vote = &p
		gen := m.voteGen
		if voteActive(p) {
			return tea.Tick(voteRefresh, func(time.Time) tea.Msg { return voteTick{gen} }), true
		}
		return tea.Tick(voteLinger, func(time.Time) tea.Msg { return voteClear{gen} }), true

	case voteTick:
		if msg.gen != m.voteGen || m.vote == nil {
			return nil, true
		}
		return m.fetchVote(m.vote.Kind, msg.gen), true

	case voteClear:
		if msg.gen == m.voteGen {
			m.vote = nil
		}
		return nil, true
	}
	return nil, false
}

func (m *Model) fetchVote(kind string, gen int) tea.Cmd {
	ch := m.chanMgr
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var p chat.Poll
		var err error
		if kind == "prediction" {
			p, err = ch.PredictionStatus(ctx)
		} else {
			p, err = ch.PollStatus(ctx)
		}
		if err != nil || p.Status == "" {
			return voteMsg{gen: gen, gone: true}
		}
		return voteMsg{gen: gen, poll: p}
	}
}

// checkVotes runs once at tab open: a poll or prediction may already be
// running, started from the website or a previous crow session.
func (m *Model) checkVotes() tea.Cmd {
	ch := m.chanMgr
	gen := m.voteGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if p, err := ch.PollStatus(ctx); err == nil && voteActive(p) {
			return voteMsg{gen: gen, poll: p}
		}
		if p, err := ch.PredictionStatus(ctx); err == nil && voteActive(p) {
			return voteMsg{gen: gen, poll: p}
		}
		return nil
	}
}

// voteLines renders the block: a header row plus one row per choice. Plain
// scale-1 text for the same reason as the pin row (see pinLine).
func (m *Model) voteLines() []string {
	v := m.vote
	head := "POLL "
	if v.Kind == "prediction" {
		head = "PREDICTION "
	}
	head += v.Title
	switch {
	case v.Status == "LOCKED":
		head += " · locked"
	case !voteActive(*v):
		head += " · final"
	default:
		if s := int(time.Until(v.EndsAt).Seconds()); !v.EndsAt.IsZero() && s > 0 {
			head += fmt.Sprintf(" · %d:%02d left", s/60, s%60)
		}
	}
	lines := []string{m.styles.pin.Width(m.width).Render(runewidth.Truncate(head, m.width, "…"))}

	total, top := 0, 0
	for _, c := range v.Choices {
		total += c.Votes
		if c.Votes > top {
			top = c.Votes
		}
	}
	for _, c := range v.Choices {
		pct, bar := 0, 0
		if total > 0 {
			pct = c.Votes * 100 / total
		}
		if top > 0 {
			bar = c.Votes * 12 / top
		}
		row := fmt.Sprintf("  %-16s %-12s %d (%d%%)",
			runewidth.Truncate(c.Title, 16, "…"), strings.Repeat("█", bar), c.Votes, pct)
		lines = append(lines, runewidth.Truncate(row, m.width, "…"))
	}
	return lines
}
