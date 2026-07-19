package tui

import (
	"sort"
	"strings"
)

// completion is one in-progress Tab cycle through candidates for the word
// being completed. Any key other than Tab drops it, so the offsets below are
// only ever used against an input the cycle itself last wrote.
type completion struct {
	cands []string
	idx   int
	start int // rune offset of the word start in the input
	end   int // rune offset just past the applied candidate
}

// completeTab completes the word before the cursor: an "@word" against recent
// chatters (keeping the @), a bare word against emote names first and then
// chatters — a bare word mid-sentence is usually an emote, and "@" is how you
// ask for a user. Repeated Tab cycles through the matches.
func (m *Model) completeTab() {
	if m.comp != nil {
		m.comp.idx = (m.comp.idx + 1) % len(m.comp.cands)
		m.applyCompletion()
		return
	}

	val := []rune(m.input.Value())
	pos := m.input.Position()
	if pos > len(val) {
		pos = len(val)
	}
	start := pos
	for start > 0 && val[start-1] != ' ' {
		start--
	}
	word := string(val[start:pos])
	if word == "" {
		return
	}

	var cands []string
	switch {
	case start == 0 && strings.HasPrefix(word, "/"):
		// A leading slash at the start of the input is a command.
		p := strings.ToLower(word[1:])
		for _, name := range commandNames {
			if strings.HasPrefix(name, p) {
				cands = append(cands, "/"+name)
			}
		}
	case strings.HasPrefix(word, "@"):
		for _, u := range m.userCandidates(word[1:]) {
			cands = append(cands, "@"+u)
		}
	default:
		cands = append(m.emoteCandidates(word), m.userCandidates(word)...)
	}
	if len(cands) == 0 {
		return
	}
	m.comp = &completion{cands: cands, start: start, end: pos}
	m.applyCompletion()
}

// applyCompletion replaces the completed span with the current candidate and
// puts the cursor after it.
func (m *Model) applyCompletion() {
	c := m.comp
	val := []rune(m.input.Value())
	if c.end > len(val) {
		c.end = len(val)
	}
	cand := []rune(c.cands[c.idx])
	out := append(append(append([]rune{}, val[:c.start]...), cand...), val[c.end:]...)
	m.input.SetValue(string(out))
	c.end = c.start + len(cand)
	m.input.SetCursor(c.end)
}

// userCandidates matches recent chatters by display name or login,
// case-insensitively, returning display names. The buffer scan mirrors the
// user card's: at historyLimit a linear pass is instant.
func (m *Model) userCandidates(prefix string) []string {
	p := strings.ToLower(prefix)
	seen := map[string]bool{}
	var out []string
	for _, msg := range m.snapshot() {
		if msg.Author == "" {
			continue
		}
		key := strings.ToLower(msg.AuthorLogin)
		if key == "" {
			key = strings.ToLower(msg.Author)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if strings.HasPrefix(strings.ToLower(msg.Author), p) ||
			strings.HasPrefix(strings.ToLower(msg.AuthorLogin), p) {
			out = append(out, msg.Author)
		}
	}
	sort.Strings(out)
	return out
}

// emoteCandidates matches loaded emote names case-insensitively but inserts
// the exact name — emote lookup is case-sensitive, and completion is how the
// user gets the case right.
// ponytail: Twitch first-party emotes aren't in the registry (they arrive
// positioned per-message), so only third-party names complete.
func (m *Model) emoteCandidates(prefix string) []string {
	if m.emotes == nil {
		return nil
	}
	p := strings.ToLower(prefix)
	var out []string
	for _, e := range m.emotes.All() {
		if strings.HasPrefix(strings.ToLower(e.Name), p) {
			out = append(out, e.Name)
		}
	}
	sort.Strings(out)
	return out
}
