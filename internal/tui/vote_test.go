package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Nyxnix/crow/internal/chat"
)

func activePoll() chat.Poll {
	return chat.Poll{
		Kind:   "poll",
		Title:  "Best letter?",
		Status: "ACTIVE",
		EndsAt: time.Now().Add(time.Minute),
		Choices: []chat.PollChoice{
			{Title: "a", Votes: 12},
			{Title: "b", Votes: 4},
		},
	}
}

func TestVoteBlockLifecycle(t *testing.T) {
	ch := &fakeChan{}
	m, _ := commandModel(t, &fakeMod{}, ch)
	base := m.viewportHeight()

	// An active poll shows the block, shrinks the viewport, and keeps polling.
	_, cmd := m.Update(voteMsg{gen: 0, poll: activePoll()})
	if m.vote == nil || cmd == nil {
		t.Fatal("active poll not displayed or watch not rescheduled")
	}
	if got := m.viewportHeight(); got != base-3 {
		t.Errorf("viewport = %d, want %d (header + 2 choices)", got, base-3)
	}
	view := m.View()
	for _, want := range []string{"POLL Best letter?", "12 (75%)", "4 (25%)"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q", want)
		}
	}

	// Completion keeps the final numbers up (linger), then a clear removes it.
	done := activePoll()
	done.Status = "COMPLETED"
	m.Update(voteMsg{gen: 0, poll: done})
	if !strings.Contains(m.View(), "· final") {
		t.Error("completed poll not marked final")
	}
	m.Update(voteClear{gen: 0})
	if m.vote != nil {
		t.Fatal("clear did not remove the block")
	}
	if got := m.viewportHeight(); got != base {
		t.Errorf("viewport = %d, want %d after clear", got, base)
	}
}

func TestVoteStartedBeginsWatch(t *testing.T) {
	ch := &fakeChan{pollState: activePoll()}
	m, _ := commandModel(t, &fakeMod{}, ch)

	_, cmd := m.Update(voteStarted{kind: "poll", okText: "poll created"})
	if cmd == nil {
		t.Fatal("no fetch scheduled")
	}
	if !strings.Contains(m.statusBar(), "poll created") {
		t.Error("ok notice not shown")
	}
	msg, ok := cmd().(voteMsg)
	if !ok || msg.gen != m.voteGen || msg.poll.Title != "Best letter?" {
		t.Fatalf("fetch = %#v", msg)
	}

	// A stale generation (an older poll's timer) is ignored.
	m.Update(voteMsg{gen: msg.gen, poll: msg.poll})
	m.Update(voteClear{gen: msg.gen - 1})
	if m.vote == nil {
		t.Fatal("stale clear removed the live block")
	}
}
