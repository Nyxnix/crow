package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBarePollOpensForm(t *testing.T) {
	m, _ := commandModel(t, &fakeMod{}, &fakeChan{})
	typeRunes(m, "/poll")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("bare /poll ran a command instead of opening the form")
	}
	if m.pollForm == nil {
		t.Fatal("no poll form opened")
	}

	// Without a channel manager it explains itself instead.
	m2, _ := commandModel(t, &fakeMod{}, nil)
	res := run(t, m2, "/poll")
	if !res.err || m2.pollForm != nil {
		t.Errorf("nil chanMgr: err=%v form=%v", res.err, m2.pollForm != nil)
	}
}

func TestPollFormSubmits(t *testing.T) {
	ch := &fakeChan{}
	m, _ := commandModel(t, &fakeMod{}, ch)
	m.pollForm = newPollForm()
	f := m.pollForm

	f.inputs[pollRowTitle].SetValue("Best letter?")
	f.inputs[pollRowChoice1].SetValue("a")
	f.inputs[pollRowChoice1+1].SetValue("b")
	f.inputs[pollRowDuration].SetValue("2m")

	// Toggle channel points on and set the per-vote cost.
	f.setFocus(pollRowCPToggle)
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !f.cp {
		t.Fatal("space did not toggle channel-points voting")
	}
	f.inputs[pollRowPoints].SetValue("500")

	f.setFocus(pollRowStart)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("start did not produce a command")
	}
	if m.pollForm != nil {
		t.Error("form stayed open after submit")
	}
	if res := cmd().(actionResult); res.err {
		t.Fatalf("submit failed: %s", res.text)
	}
	if ch.pollT != "Best letter?" || len(ch.pollC) != 2 || ch.pollDur != 120 || ch.pollPoints != 500 {
		t.Errorf("poll = %q %v %ds %dpts", ch.pollT, ch.pollC, ch.pollDur, ch.pollPoints)
	}
}

func TestPollFormValidates(t *testing.T) {
	m, _ := commandModel(t, &fakeMod{}, &fakeChan{})
	m.pollForm = newPollForm()
	f := m.pollForm
	f.inputs[pollRowTitle].SetValue("only one choice")
	f.inputs[pollRowChoice1].SetValue("a")

	f.setFocus(pollRowStart)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("an invalid poll produced a command")
	}
	if m.pollForm == nil || f.errMsg == "" {
		t.Fatal("form closed or no error shown on invalid input")
	}
}
