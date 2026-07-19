package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// helpLine is everything runCommand understands, shown by /help and pointed at
// on typos. /delete acts on the target's latest message in the buffer, not a
// message id — nobody can type those.
const helpLine = "/timeout /ban /unban /delete · /clear /slow[off] /followers[off] /emoteonly[off] /uniquechat[off] · /announce /poll /prediction /raid · /vip /unvip /mod /unmod"

// cmdNotice returns an immediate actionResult, for outcomes decided before any
// network call (usage errors, missing capability).
func cmdNotice(text string, err bool) tea.Cmd {
	return func() tea.Msg { return actionResult{text: text, err: err} }
}

// runCommand parses and dispatches one typed "/command" line. Anything that
// talks to the network reuses the card's runAction so a slow call can't freeze
// rendering; validation Helix enforces server-side (slow-mode bounds, choice
// counts, broadcaster-only endpoints) is surfaced from its error, not
// duplicated here.
func (m *Model) runCommand(line string) tea.Cmd {
	args := splitQuoted(line)
	cmd := strings.ToLower(strings.TrimPrefix(args[0], "/"))
	args = args[1:]

	switch cmd {
	case "help":
		return cmdNotice(helpLine, false)

	// Moderation routes through the Moderator interface, so it works on both
	// Twitch and YouTube tabs.
	case "timeout", "ban", "unban", "delete":
		if m.mod == nil {
			return cmdNotice("log in to moderate", true)
		}
		return m.modCommand(cmd, args)

	case "clear":
		return m.chanAction(func(ctx context.Context) error {
			return m.chanMgr.ClearChat(ctx)
		}, "chat cleared")

	case "slow":
		secs, err := parseDur(first(args), 30)
		if err != nil {
			return cmdNotice("usage: /slow [seconds]", true)
		}
		return m.chanSettings(map[string]any{"slow_mode": true, "slow_mode_wait_time": secs},
			fmt.Sprintf("slow mode: %ds", secs))
	case "slowoff":
		return m.chanSettings(map[string]any{"slow_mode": false}, "slow mode off")
	case "followers":
		secs, err := parseDur(first(args), 0)
		if err != nil {
			return cmdNotice("usage: /followers [duration, e.g. 10m]", true)
		}
		return m.chanSettings(map[string]any{"follower_mode": true, "follower_mode_duration": secs / 60},
			"follower mode on")
	case "followersoff":
		return m.chanSettings(map[string]any{"follower_mode": false}, "follower mode off")
	case "emoteonly":
		return m.chanSettings(map[string]any{"emote_mode": true}, "emote-only on")
	case "emoteonlyoff":
		return m.chanSettings(map[string]any{"emote_mode": false}, "emote-only off")
	case "uniquechat":
		return m.chanSettings(map[string]any{"unique_chat_mode": true}, "unique chat on")
	case "uniquechatoff":
		return m.chanSettings(map[string]any{"unique_chat_mode": false}, "unique chat off")

	case "announce":
		// The announcement is the raw rest of the line: quotes are content here.
		text := strings.TrimSpace(strings.TrimPrefix(line, "/announce"))
		if text == "" {
			return cmdNotice("usage: /announce <text>", true)
		}
		return m.chanAction(func(ctx context.Context) error {
			return m.chanMgr.Announce(ctx, text)
		}, "announced")

	case "poll":
		// Bare /poll opens the interactive form, mirroring Twitch's own popup;
		// the quoted one-liner stays for the quick case.
		if len(args) == 0 {
			if m.chanMgr == nil {
				return cmdNotice("twitch only (needs a single logged-in twitch tab)", true)
			}
			m.pollForm = newPollForm()
			return nil
		}
		title, choices, dur, ok := parseVote(args, 60)
		if !ok {
			return cmdNotice(`usage: /poll "title" "choice1" "choice2" [..] [duration], or bare /poll for the form`, true)
		}
		return m.voteCreate("poll", func(ctx context.Context) error {
			return m.chanMgr.CreatePoll(ctx, title, choices, dur, 0)
		}, "poll created")
	case "prediction":
		title, outcomes, win, ok := parseVote(args, 120)
		if !ok {
			return cmdNotice(`usage: /prediction "title" "outcome1" "outcome2" [..] [window]`, true)
		}
		return m.voteCreate("prediction", func(ctx context.Context) error {
			return m.chanMgr.CreatePrediction(ctx, title, outcomes, win)
		}, "prediction created")

	case "raid":
		if len(args) != 1 {
			return cmdNotice("usage: /raid <channel>", true)
		}
		target := strings.ToLower(strings.TrimPrefix(args[0], "@"))
		return m.chanAction(func(ctx context.Context) error {
			id, err := m.chanMgr.ResolveUser(ctx, target)
			if err != nil {
				return err
			}
			return m.chanMgr.Raid(ctx, id)
		}, "raiding "+target)

	case "vip", "unvip", "mod", "unmod":
		if len(args) != 1 {
			return cmdNotice("usage: /"+cmd+" <user>", true)
		}
		target := args[0]
		on := cmd == "vip" || cmd == "mod"
		vip := cmd == "vip" || cmd == "unvip"
		return m.chanAction(func(ctx context.Context) error {
			id, err := m.resolveTarget(ctx, target)
			if err != nil {
				return err
			}
			if vip {
				return m.chanMgr.SetVIP(ctx, id, on)
			}
			return m.chanMgr.SetMod(ctx, id, on)
		}, cmd+" "+target)
	}
	return cmdNotice("unknown command /"+cmd+" (try /help)", true)
}

// modCommand handles the commands the Moderator interface covers. Target
// resolution happens inside the async action, since it may need a lookup.
func (m *Model) modCommand(cmd string, args []string) tea.Cmd {
	if len(args) == 0 {
		return cmdNotice("usage: /"+cmd+" <user>", true)
	}
	target, rest := args[0], args[1:]
	mod := m.mod

	switch cmd {
	case "timeout":
		secs := 600
		if len(rest) > 0 {
			// A leading duration is optional; a reason starting with a number
			// would be eaten, same ambiguity Twitch's own /timeout has.
			if n, err := parseDur(rest[0], 600); err == nil {
				secs, rest = n, rest[1:]
			}
		}
		reason := strings.Join(rest, " ")
		return runAction(func(ctx context.Context) error {
			id, err := m.resolveTarget(ctx, target)
			if err != nil {
				return err
			}
			return mod.Timeout(ctx, id, secs, reason)
		}, "timed out "+target)
	case "ban":
		reason := strings.Join(rest, " ")
		return runAction(func(ctx context.Context) error {
			id, err := m.resolveTarget(ctx, target)
			if err != nil {
				return err
			}
			return mod.Ban(ctx, id, reason)
		}, "banned "+target)
	case "unban":
		return runAction(func(ctx context.Context) error {
			id, err := m.resolveTarget(ctx, target)
			if err != nil {
				return err
			}
			return mod.Unban(ctx, id)
		}, "unbanned "+target)
	case "delete":
		return runAction(func(ctx context.Context) error {
			id := m.lastMessageID(target)
			if id == "" {
				return fmt.Errorf("no recent deletable message from %s", target)
			}
			return mod.DeleteMessage(ctx, id)
		}, "message deleted")
	}
	return nil
}

// chanAction guards the Twitch-only commands: tabs without a channel manager
// (YouTube, combined, not logged in) fail with one clear message instead of a
// nil deref. Auth failures get a re-login pointer, since every one of these
// commands needs scopes older tokens were never granted.
func (m *Model) chanAction(fn func(context.Context) error, okText string) tea.Cmd {
	if m.chanMgr == nil {
		return cmdNotice("twitch only (needs a single logged-in twitch tab)", true)
	}
	return runAction(func(ctx context.Context) error {
		return scopeHint(fn(ctx))
	}, okText)
}

// scopeHint appends a re-login pointer to auth failures: the commands need
// scopes older tokens were never granted.
func scopeHint(err error) error {
	if err != nil && (strings.HasPrefix(err.Error(), "401") || strings.HasPrefix(err.Error(), "403")) {
		return fmt.Errorf("%s — re-login may be needed: crow logout && crow login", err)
	}
	return err
}

// voteCreate starts a poll or prediction; on success the live status block
// takes over from the plain ok notice (see vote.go).
func (m *Model) voteCreate(kind string, fn func(context.Context) error, okText string) tea.Cmd {
	if m.chanMgr == nil {
		return cmdNotice("twitch only (needs a single logged-in twitch tab)", true)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := scopeHint(fn(ctx)); err != nil {
			return actionResult{text: err.Error(), err: true}
		}
		return voteStarted{kind: kind, okText: okText}
	}
}

func (m *Model) chanSettings(patch map[string]any, okText string) tea.Cmd {
	return m.chanAction(func(ctx context.Context) error {
		return m.chanMgr.UpdateChatSettings(ctx, patch)
	}, okText)
}

// resolveTarget turns a typed name into a platform user ID: the recent buffer
// first (the only option on YouTube, the fast path on Twitch), then a Twitch
// lookup for someone who hasn't spoken.
func (m *Model) resolveTarget(ctx context.Context, name string) (string, error) {
	name = strings.ToLower(strings.TrimPrefix(name, "@"))
	msgs := m.snapshot()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].AuthorID == "" {
			continue
		}
		if strings.ToLower(msgs[i].AuthorLogin) == name || strings.ToLower(msgs[i].Author) == name {
			return msgs[i].AuthorID, nil
		}
	}
	if m.chanMgr != nil {
		return m.chanMgr.ResolveUser(ctx, name)
	}
	return "", fmt.Errorf("%s not in recent chat", name)
}

// lastMessageID finds the target's most recent not-yet-deleted message that
// carries an id.
func (m *Model) lastMessageID(name string) string {
	name = strings.ToLower(strings.TrimPrefix(name, "@"))
	msgs := m.snapshot()
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.ID == "" || msg.Deleted {
			continue
		}
		if strings.ToLower(msg.AuthorLogin) == name || strings.ToLower(msg.Author) == name {
			return msg.ID
		}
	}
	return ""
}

// parseVote reads the shared /poll and /prediction shape: a title, two or more
// choices, and an optional trailing duration.
func parseVote(args []string, defSecs int) (title string, choices []string, secs int, ok bool) {
	secs = defSecs
	if len(args) > 0 {
		if n, err := parseDur(args[len(args)-1], -1); err == nil && n > 0 {
			secs, args = n, args[:len(args)-1]
		}
	}
	if len(args) < 3 {
		return "", nil, 0, false
	}
	return args[0], args[1:], secs, true
}

// parseDur reads a duration as bare seconds ("90") or a Go duration ("10m");
// empty means def.
func parseDur(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return int(d.Seconds()), nil
}

// splitQuoted splits on spaces, keeping double-quoted runs as one token with
// the quotes stripped. ponytail: no escapes inside quotes — poll titles don't
// need them.
func splitQuoted(s string) []string {
	var out []string
	var cur strings.Builder
	inQ, quoted := false, false
	flush := func() {
		if quoted || cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
			quoted = false
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
			quoted = true
		case r == ' ' && !inQ:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
