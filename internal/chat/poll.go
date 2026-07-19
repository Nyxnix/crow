package chat

import "time"

// Poll is a running (or just-finished) poll or prediction's live state, shown
// as a fixed block in the TUI while it runs.
type Poll struct {
	Kind    string // "poll" or "prediction"
	Title   string
	Status  string // Helix's strings: ACTIVE, COMPLETED, LOCKED, RESOLVED, ...
	Choices []PollChoice
	EndsAt  time.Time // when voting (or prediction entry) closes; zero if unknown
}

// PollChoice is one option: votes for polls, channel points for predictions.
type PollChoice struct {
	Title string
	Votes int
}
