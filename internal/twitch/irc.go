// Package twitch connects to Twitch chat over IRC and converts what it reads
// into chat.Message values.
package twitch

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"math/rand"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nyxnix/crow/internal/chat"
)

const (
	ircAddr    = "irc.chat.twitch.tv:6697"
	emoteCDN   = "https://static-cdn.jtvnw.net/emoticons/v2/%s/default/dark/3.0"
	writeLimit = 20 * time.Millisecond // Twitch drops clients that flood the socket
)

// Client reads one channel's chat. Zero Nick/Token means an anonymous
// connection, which can read chat but not send or moderate.
type Client struct {
	Channel string
	Nick    string
	Token   string // OAuth token, without the "oauth:" prefix

	// Out receives every parsed message. Run closes it on return.
	Out chan chat.Message

	// OnRoomID is called with the channel's numeric Twitch ID once Twitch sends
	// ROOMSTATE after JOIN. This is the cheapest source of the channel ID: every
	// other route needs an API call, and the emote providers are all keyed by it.
	// It fires on every reconnect, so it must tolerate being called repeatedly.
	OnRoomID func(id string)

	// Events, when set, receives moderation actions (message deletions, user
	// timeouts/bans, chat clears) parsed from CLEARMSG and CLEARCHAT. Sends are
	// non-blocking, so a stalled consumer drops events rather than the read loop.
	Events chan chat.ModEvent

	// outbound carries messages queued by Send to the active session's writer.
	// Created lazily via sendChan so a caller that never sends pays nothing.
	outbound chan string
	sendOnce sync.Once

	// addr overrides the Twitch IRC endpoint. Empty means the real one; tests
	// point it at a local stand-in server.
	addr string

	// self holds the logged-in user's own presentation (badges, color, display
	// name) as reported by USERSTATE. Twitch does not echo our own PRIVMSGs, so
	// this is what lets a locally-injected echo look like the real thing.
	selfMu sync.RWMutex
	self   Self
}

// Self is the logged-in user's own chat presentation in the current channel.
type Self struct {
	DisplayName string
	Color       string
	Badges      []chat.Badge
}

// Self returns the user's own presentation from the latest USERSTATE. It is
// empty until the first one arrives, shortly after joining.
func (c *Client) Self() Self {
	c.selfMu.RLock()
	defer c.selfMu.RUnlock()
	return c.self
}

// Echo builds a chat.Message for a message the user just sent, so it can be
// shown locally: Twitch never sends our own PRIVMSGs back. It uses the latest
// USERSTATE so the echo carries the same badges, color and display name the
// message will show to everyone else. Falls back to login before USERSTATE has
// arrived.
func (c *Client) Echo(text, userID, login string) chat.Message {
	s := c.Self()
	author := s.DisplayName
	if author == "" {
		author = login
	}
	m := chat.Message{
		Platform:    chat.Twitch,
		Channel:     c.Channel,
		AuthorID:    userID,
		Author:      author,
		AuthorLogin: login,
		Color:       s.Color,
		Text:        text,
		Badges:      s.Badges,
		At:          time.Now(),
	}
	applyBadgeRoles(&m)
	return m
}

// serverAddr returns the endpoint to dial.
func (c *Client) serverAddr() string {
	if c.addr != "" {
		return c.addr
	}
	return ircAddr
}

// sendChan returns the outbound queue, creating it once. Both Send and the
// session writer go through here so the channel exists no matter which runs
// first.
func (c *Client) sendChan() chan string {
	c.sendOnce.Do(func() { c.outbound = make(chan string, 32) })
	return c.outbound
}

// emitEvent delivers a moderation event without blocking the read loop.
func (c *Client) emitEvent(ev chat.ModEvent) {
	if c.Events == nil {
		return
	}
	select {
	case c.Events <- ev:
	default: // ponytail: drop if the consumer is behind; deletions are best-effort UI
	}
}

// Send queues a chat message for the current connection. It never blocks: if
// the queue is full or there is no live connection, the message is dropped
// rather than stalling the caller (the TUI). Sending requires the client to
// have been created with a Nick and Token; an anonymous client silently drops.
func (c *Client) Send(text string) {
	if c.Token == "" || strings.TrimSpace(text) == "" {
		return
	}
	select {
	case c.sendChan() <- text:
	default: // ponytail: drop when backed up; a human types slower than Twitch's limit
	}
}

// Run connects and pumps messages into Out until ctx is cancelled, reconnecting
// with backoff whenever the connection drops. It only returns early if ctx ends.
func (c *Client) Run(ctx context.Context) error {
	defer close(c.Out)

	backoff := time.Second
	for {
		err := c.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Any session end that isn't a context cancel is a dropped connection.
		// Back off so a persistent failure (bad token, Twitch down) doesn't spin.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
		_ = err
	}
}

// session holds one connection open until it fails or ctx ends.
func (c *Client) session(ctx context.Context) error {
	// Tests point addr at a stand-in server with a self-signed cert, so skip
	// verification when an override is set; the real endpoint is verified.
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: c.addr != ""}}
	conn, err := d.DialContext(ctx, "tcp", c.serverAddr())
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// session-scoped context so the writer goroutine stops when this connection
	// ends, not only when the whole client does.
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()

	// Cancelling ctx won't interrupt a blocked Read on its own, so close the
	// socket out from under it, which makes the read fail and unwinds session.
	go func() {
		<-sctx.Done()
		conn.Close()
	}()

	// All socket writes go through writeLine. The read loop writes PONGs and the
	// writer goroutine writes PRIVMSGs, so without this mutex two goroutines
	// could write the same TLS connection at once, which corrupts the stream.
	var writeMu sync.Mutex
	writeLine := func(s string) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err := conn.Write([]byte(s + "\r\n"))
		return err
	}

	nick, pass := c.Nick, "oauth:"+c.Token
	if c.Token == "" {
		// justinfan<n> is Twitch's documented anonymous read-only login.
		nick, pass = fmt.Sprintf("justinfan%d", 10000+rand.Intn(89999)), "SCHMOOPIIE"
	}

	// tags carries the metadata we render (color, badges, emotes); commands
	// carries CLEARCHAT and friends, which we need for moderation feedback.
	for _, line := range []string{
		"CAP REQ :twitch.tv/tags twitch.tv/commands",
		"PASS " + pass,
		"NICK " + nick,
		"JOIN #" + strings.ToLower(c.Channel),
	} {
		if err := writeLine(line); err != nil {
			return fmt.Errorf("handshake: %w", err)
		}
		time.Sleep(writeLimit)
	}

	// Drain queued outgoing messages for as long as this connection lives. An
	// authenticated client only; an anonymous one has nothing to send.
	if c.Token != "" {
		channel := strings.ToLower(c.Channel)
		go func() {
			for {
				select {
				case <-sctx.Done():
					return
				case text := <-c.sendChan():
					if err := writeLine("PRIVMSG #" + channel + " :" + text); err != nil {
						return // socket is gone; the read loop will surface it
					}
					time.Sleep(writeLimit)
				}
			}
		}()
	}

	// Twitch lines cap at ~4KB of tags plus text; give the scanner room so a
	// long message never truncates into an unparseable line.
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 8192), 65536)

	for sc.Scan() {
		line, ok := parseLine(strings.TrimRight(sc.Text(), "\r"))
		if !ok {
			continue
		}
		switch line.cmd {
		case "PING":
			// Twitch pings every ~5min and disconnects if we don't echo it back.
			if err := writeLine("PONG :tmi.twitch.tv"); err != nil {
				return err
			}
		case "ROOMSTATE":
			if id := line.tags["room-id"]; id != "" && c.OnRoomID != nil {
				c.OnRoomID(id)
			}
		case "USERSTATE":
			// Twitch sends this on join and after each of our messages, carrying
			// our own badges, color and display name for this channel.
			c.selfMu.Lock()
			c.self = Self{
				DisplayName: line.tags["display-name"],
				Color:       line.tags["color"],
				Badges:      parseBadges(line.tags["badges"]),
			}
			c.selfMu.Unlock()
		case "CLEARMSG":
			// A single message was deleted by a moderator.
			if id := line.tags["target-msg-id"]; id != "" {
				c.emitEvent(chat.ModEvent{
					Kind:      chat.DeleteMessage,
					MessageID: id,
					Login:     line.tags["login"],
				})
			}
		case "CLEARCHAT":
			// With a target user this is a timeout or ban clearing that user's
			// messages; the trailing param is their login. With no target it is a
			// full chat clear.
			if uid := line.tags["target-user-id"]; uid != "" {
				login := ""
				if len(line.params) >= 2 {
					login = line.params[1]
				}
				c.emitEvent(chat.ModEvent{Kind: chat.ClearUser, UserID: uid, Login: login})
			} else {
				c.emitEvent(chat.ModEvent{Kind: chat.ClearAll})
			}
		case "PRIVMSG":
			msg, ok := toMessage(line)
			if !ok {
				continue
			}
			select {
			case c.Out <- msg:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	return net.ErrClosed
}

// ircLine is one parsed IRC protocol line.
type ircLine struct {
	tags   map[string]string
	nick   string
	cmd    string
	params []string
}

// parseLine splits an IRC line into tags, prefix, command and params. It
// reports false for lines too malformed to act on.
//
// Shape: @tag=val;tag2=val2 :nick!user@host COMMAND param :trailing param
func parseLine(raw string) (ircLine, bool) {
	var l ircLine
	s := raw

	if strings.HasPrefix(s, "@") {
		end := strings.IndexByte(s, ' ')
		if end < 0 {
			return l, false
		}
		l.tags = parseTags(s[1:end])
		s = s[end+1:]
	}

	if strings.HasPrefix(s, ":") {
		end := strings.IndexByte(s, ' ')
		if end < 0 {
			return l, false
		}
		prefix := s[1:end]
		l.nick = prefix
		if i := strings.IndexByte(prefix, '!'); i >= 0 {
			l.nick = prefix[:i]
		}
		s = s[end+1:]
	}

	for s != "" {
		// A param starting with ':' is the trailing param: it runs to end of
		// line and may contain spaces, so stop splitting once we hit it.
		if strings.HasPrefix(s, ":") {
			l.params = append(l.params, s[1:])
			break
		}
		end := strings.IndexByte(s, ' ')
		if end < 0 {
			l.params = append(l.params, s)
			break
		}
		if end > 0 {
			l.params = append(l.params, s[:end])
		}
		s = s[end+1:]
	}

	if len(l.params) == 0 {
		return l, false
	}
	l.cmd, l.params = l.params[0], l.params[1:]
	return l, true
}

// parseTags splits the IRCv3 tag string, unescaping each value.
func parseTags(s string) map[string]string {
	tags := make(map[string]string)
	for _, kv := range strings.Split(s, ";") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		tags[k] = unescapeTag(v)
	}
	return tags
}

// unescapeTag reverses IRCv3 tag escaping. The escape set is fixed by the spec:
// anything else after a backslash is that literal character, and a trailing
// lone backslash is dropped.
func unescapeTag(v string) string {
	if !strings.ContainsRune(v, '\\') {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' {
			b.WriteByte(v[i])
			continue
		}
		i++
		if i >= len(v) {
			break
		}
		switch v[i] {
		case ':':
			b.WriteByte(';')
		case 's':
			b.WriteByte(' ')
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		default:
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

// toMessage converts a PRIVMSG line into a chat.Message.
func toMessage(l ircLine) (chat.Message, bool) {
	if len(l.params) < 2 {
		return chat.Message{}, false
	}
	channel := strings.TrimPrefix(l.params[0], "#")
	text := l.params[1]

	author := l.tags["display-name"]
	if author == "" {
		author = l.nick
	}

	badges := parseBadges(l.tags["badges"])
	m := chat.Message{
		ID:          l.tags["id"],
		Platform:    chat.Twitch,
		Channel:     channel,
		AuthorID:    l.tags["user-id"],
		Author:      author,
		AuthorLogin: l.nick,
		Color:       l.tags["color"],
		Text:        text,
		Emotes:      parseEmotes(l.tags["emotes"], text),
		Badges:      badges,
		At:          tagTime(l.tags["tmi-sent-ts"]),
	}
	applyBadgeRoles(&m)
	return m, true
}

// applyBadgeRoles sets the role flags from a message's badges, so display and
// mod-action decisions read one place. Shared by PRIVMSG and the self-state
// built from USERSTATE.
func applyBadgeRoles(m *chat.Message) {
	for _, b := range m.Badges {
		switch b.Name {
		case "broadcaster":
			m.Broadcaster = true
		case "moderator":
			m.Moderator = true
		case "vip":
			m.VIP = true
		case "subscriber", "founder":
			m.Subscriber = true
		}
	}
}

// parseBadges reads the badges tag: "broadcaster/1,subscriber/12".
func parseBadges(tag string) []chat.Badge {
	if tag == "" {
		return nil
	}
	var out []chat.Badge
	for _, b := range strings.Split(tag, ",") {
		name, version, ok := strings.Cut(b, "/")
		if !ok || name == "" {
			continue
		}
		out = append(out, chat.Badge{Name: name, Version: version})
	}
	return out
}

// parseEmotes reads the emotes tag: "25:0-4,6-10/1902:12-16", where each range
// is inclusive and indexes runes rather than bytes. The returned emotes use
// exclusive ends, sorted by position so renderers can walk them in one pass.
func parseEmotes(tag, text string) []chat.Emote {
	if tag == "" {
		return nil
	}
	runes := []rune(text)
	var out []chat.Emote
	for _, spec := range strings.Split(tag, "/") {
		id, ranges, ok := strings.Cut(spec, ":")
		if !ok {
			continue
		}
		for _, r := range strings.Split(ranges, ",") {
			lo, hi, ok := strings.Cut(r, "-")
			if !ok {
				continue
			}
			start, err1 := strconv.Atoi(lo)
			end, err2 := strconv.Atoi(hi)
			// Drop ranges that don't land inside the text rather than trusting
			// them: a bad index here would panic the slice below.
			if err1 != nil || err2 != nil || start < 0 || end < start || end >= len(runes) {
				continue
			}
			out = append(out, chat.Emote{
				Name:  string(runes[start : end+1]),
				ID:    id,
				URL:   fmt.Sprintf(emoteCDN, id),
				Start: start,
				End:   end + 1,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// tagTime reads tmi-sent-ts, which is Unix milliseconds. It falls back to now
// so a message with a missing or junk timestamp still sorts sensibly.
func tagTime(v string) time.Time {
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Now()
	}
	return time.UnixMilli(ms)
}
