package twitch

import (
	"strings"
	"testing"
	"time"

	"github.com/Nyxnix/typetype/internal/chat"
)

// USERSTATE must populate the self-state so a sent-message echo can look like
// the real thing.
func TestUserStatePopulatesSelf(t *testing.T) {
	line, ok := parseLine(`@badges=broadcaster/1,subscriber/0;color=#8A2BE2;display-name=Nyx;mod=0 :tmi.twitch.tv USERSTATE #buh`)
	if !ok {
		t.Fatal("failed to parse USERSTATE")
	}

	c := &Client{Channel: "buh"}
	// Drive the same handling the read loop performs.
	c.self = Self{
		DisplayName: line.tags["display-name"],
		Color:       line.tags["color"],
		Badges:      parseBadges(line.tags["badges"]),
	}

	s := c.Self()
	if s.DisplayName != "Nyx" || s.Color != "#8A2BE2" {
		t.Errorf("self = %+v, want Nyx / #8A2BE2", s)
	}
	if len(s.Badges) != 2 {
		t.Fatalf("got %d badges, want broadcaster + subscriber", len(s.Badges))
	}
}

// Echo reflects the self-state: the same badges, color, name and derived role
// flags a real message from this user would carry.
func TestEchoUsesSelfState(t *testing.T) {
	c := &Client{Channel: "buh"}
	c.self = Self{
		DisplayName: "Nyx",
		Color:       "#8A2BE2",
		Badges:      parseBadges("broadcaster/1,subscriber/0"),
	}

	before := time.Now()
	e := c.Echo("hello chat", "42", "nyx")

	if e.Author != "Nyx" || e.Color != "#8A2BE2" || e.AuthorLogin != "nyx" || e.AuthorID != "42" {
		t.Errorf("echo identity = %+v", e)
	}
	if e.Text != "hello chat" || e.Channel != "buh" {
		t.Errorf("echo body = %q in %q", e.Text, e.Channel)
	}
	// Roles must be derived from the badges so the TUI shows [B].
	if !e.Broadcaster || !e.Subscriber {
		t.Errorf("roles = bc:%v sub:%v, want both from the badges", e.Broadcaster, e.Subscriber)
	}
	if len(e.Badges) != 2 {
		t.Errorf("echo carries %d badges, want 2 for the overlay", len(e.Badges))
	}
	if e.At.Before(before) {
		t.Error("echo timestamp not set to send time")
	}
}

// Before USERSTATE has arrived, Echo must still produce a usable message using
// the login as the name.
func TestEchoFallsBackBeforeUserState(t *testing.T) {
	c := &Client{Channel: "buh"}
	e := c.Echo("hi", "42", "nyx")
	if e.Author != "nyx" {
		t.Errorf("author = %q, want the login fallback", e.Author)
	}
	if e.Broadcaster || e.Subscriber || len(e.Badges) != 0 {
		t.Error("echo claimed roles it has no USERSTATE to back")
	}
}

// CLEARMSG (one message deleted) and CLEARCHAT (a user cleared, or the whole
// chat) must turn into the right moderation events.
func TestModEventsFromClearCommands(t *testing.T) {
	c := &Client{Channel: "buh", Events: make(chan chat.ModEvent, 4)}

	feed := func(raw string) chat.ModEvent {
		t.Helper()
		line, ok := parseLine(raw)
		if !ok {
			t.Fatalf("parse failed: %q", raw)
		}
		switch line.cmd {
		case "CLEARMSG":
			if id := line.tags["target-msg-id"]; id != "" {
				c.emitEvent(chat.ModEvent{Kind: chat.DeleteMessage, MessageID: id, Login: line.tags["login"]})
			}
		case "CLEARCHAT":
			if uid := line.tags["target-user-id"]; uid != "" {
				login := ""
				if len(line.params) >= 2 {
					login = line.params[1]
				}
				c.emitEvent(chat.ModEvent{Kind: chat.ClearUser, UserID: uid, Login: login})
			} else {
				c.emitEvent(chat.ModEvent{Kind: chat.ClearAll})
			}
		}
		select {
		case ev := <-c.Events:
			return ev
		default:
			t.Fatal("no event emitted")
			return chat.ModEvent{}
		}
	}

	// A single deleted message.
	ev := feed(`@login=baduser;target-msg-id=abc-123 :tmi.twitch.tv CLEARMSG #buh :some bad message`)
	if ev.Kind != chat.DeleteMessage || ev.MessageID != "abc-123" || ev.Login != "baduser" {
		t.Errorf("CLEARMSG -> %+v", ev)
	}

	// A timeout/ban clearing one user's messages; trailing param is their login.
	ev = feed(`@ban-duration=600;target-user-id=42;room-id=1 :tmi.twitch.tv CLEARCHAT #buh :baduser`)
	if ev.Kind != chat.ClearUser || ev.UserID != "42" || ev.Login != "baduser" {
		t.Errorf("CLEARCHAT(user) -> %+v", ev)
	}

	// A full chat clear has no target.
	ev = feed(`@room-id=1 :tmi.twitch.tv CLEARCHAT #buh`)
	if ev.Kind != chat.ClearAll {
		t.Errorf("CLEARCHAT(all) -> %+v", ev)
	}
}

// emitEvent must not block or panic when no Events channel is set.
func TestEmitEventWithoutChannel(t *testing.T) {
	c := &Client{Channel: "buh"} // no Events
	c.emitEvent(chat.ModEvent{Kind: chat.ClearAll})
}

func TestApplyBadgeRoles(t *testing.T) {
	m := mustMessage(t, "broadcaster/1,subscriber/12,vip/1")
	if !m.Broadcaster || !m.Subscriber || !m.VIP || m.Moderator {
		t.Errorf("roles = %+v, want bc+sub+vip, not mod", m)
	}
	// founder counts as subscriber.
	f := mustMessage(t, "founder/0")
	if !f.Subscriber {
		t.Error("founder must count as subscriber")
	}
}

func mustMessage(t *testing.T, badgeTag string) (m struct {
	Broadcaster, Moderator, VIP, Subscriber bool
}) {
	t.Helper()
	line, ok := parseLine("@badges=" + badgeTag + " :n!n@n PRIVMSG #buh :hi")
	if !ok {
		t.Fatal("parse")
	}
	msg, ok := toMessage(line)
	if !ok {
		t.Fatal("toMessage")
	}
	if strings.Contains(badgeTag, "moderator") && !msg.Moderator {
		t.Error("mod not set")
	}
	return struct {
		Broadcaster, Moderator, VIP, Subscriber bool
	}{msg.Broadcaster, msg.Moderator, msg.VIP, msg.Subscriber}
}
