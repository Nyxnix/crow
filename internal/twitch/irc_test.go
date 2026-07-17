package twitch

import "testing"

// A realistic tagged PRIVMSG, the shape everything else depends on.
const privmsg = `@badge-info=subscriber/14;badges=broadcaster/1,subscriber/12;color=#1E90FF;display-name=Nyx;emotes=25:6-10;id=abc-123;tmi-sent-ts=1700000000000;user-id=4242 :nyx!nyx@nyx.tmi.twitch.tv PRIVMSG #buh :hello Kappa world`

func TestToMessage(t *testing.T) {
	line, ok := parseLine(privmsg)
	if !ok {
		t.Fatal("parseLine rejected a valid PRIVMSG")
	}
	m, ok := toMessage(line)
	if !ok {
		t.Fatal("toMessage rejected a valid PRIVMSG")
	}

	if m.Author != "Nyx" || m.AuthorLogin != "nyx" || m.AuthorID != "4242" {
		t.Errorf("author = %q/%q/%q, want Nyx/nyx/4242", m.Author, m.AuthorLogin, m.AuthorID)
	}
	if m.Channel != "buh" || m.Text != "hello Kappa world" || m.Color != "#1E90FF" {
		t.Errorf("channel/text/color = %q/%q/%q", m.Channel, m.Text, m.Color)
	}
	if !m.Broadcaster || !m.Subscriber || m.Moderator || m.VIP {
		t.Errorf("roles = bc:%v sub:%v mod:%v vip:%v, want bc+sub only",
			m.Broadcaster, m.Subscriber, m.Moderator, m.VIP)
	}
	if m.At.UnixMilli() != 1700000000000 {
		t.Errorf("At = %v, want the tmi-sent-ts value", m.At)
	}

	// The emote range must convert from Twitch's inclusive end to our exclusive
	// one, and Name must be sliced back out of the text rather than trusted.
	if len(m.Emotes) != 1 {
		t.Fatalf("got %d emotes, want 1", len(m.Emotes))
	}
	if e := m.Emotes[0]; e.Name != "Kappa" || e.Start != 6 || e.End != 11 || e.ID != "25" {
		t.Errorf("emote = %q [%d,%d) id=%q, want Kappa [6,11) id=25", e.Name, e.Start, e.End, e.ID)
	}
}

// Emote indexes count runes, not bytes. A multi-byte prefix shifts every byte
// offset but must leave the rune offsets alone.
func TestParseEmotesIsRuneIndexed(t *testing.T) {
	text := "日本語 Kappa"
	got := parseEmotes("25:4-8", text)
	if len(got) != 1 {
		t.Fatalf("got %d emotes, want 1", len(got))
	}
	if got[0].Name != "Kappa" {
		t.Errorf("Name = %q, want Kappa (byte-indexed slicing would mangle this)", got[0].Name)
	}
}

// An out-of-range index would panic the slice, so it must be dropped instead.
func TestParseEmotesRejectsBadRanges(t *testing.T) {
	for _, tag := range []string{"25:0-999", "25:5-2", "25:-1-3", "25:x-y", "25:garbage", "25"} {
		if got := parseEmotes(tag, "short"); len(got) != 0 {
			t.Errorf("parseEmotes(%q) = %v, want none", tag, got)
		}
	}
}

func TestUnescapeTag(t *testing.T) {
	cases := map[string]string{
		`plain`:             `plain`,
		`a\sb`:              `a b`,
		`a\:b`:              `a;b`,
		`a\\b`:              `a\b`,
		`\s\:\\`:            ` ;\`,
		`a\qb`:              `aqb`, // unknown escape: the backslash is dropped
		`trailing\`:         `trailing`,
		`Ronni\sMore\sTest`: `Ronni More Test`,
	}
	for in, want := range cases {
		if got := unescapeTag(in); got != want {
			t.Errorf("unescapeTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseLine(t *testing.T) {
	// PING has no tags and no prefix.
	l, ok := parseLine("PING :tmi.twitch.tv")
	if !ok || l.cmd != "PING" {
		t.Errorf("PING parsed as ok=%v cmd=%q", ok, l.cmd)
	}

	// A trailing param keeps its spaces and any leading colons after the first.
	l, ok = parseLine(":nyx!nyx@nyx.tmi.twitch.tv PRIVMSG #buh :a : b  c")
	if !ok {
		t.Fatal("rejected a valid untagged PRIVMSG")
	}
	if l.nick != "nyx" || l.cmd != "PRIVMSG" {
		t.Errorf("nick/cmd = %q/%q, want nyx/PRIVMSG", l.nick, l.cmd)
	}
	if len(l.params) != 2 || l.params[1] != "a : b  c" {
		t.Errorf("params = %q, want [#buh, %q]", l.params, "a : b  c")
	}

	// Lines that can't yield a command must be rejected, not half-parsed.
	for _, bad := range []string{"", "@only=tags", ":only-a-prefix"} {
		if _, ok := parseLine(bad); ok {
			t.Errorf("parseLine(%q) accepted, want rejected", bad)
		}
	}
}
