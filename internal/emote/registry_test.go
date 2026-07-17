package emote

import (
	"testing"

	"github.com/Nyxnix/crow/internal/chat"
)

// testRegistry is loaded directly, bypassing the network.
func testRegistry(es ...Emote) *Registry {
	r := New()
	for _, e := range es {
		r.byName[e.Name] = e
	}
	return r
}

func TestApplyFindsEmotesByWord(t *testing.T) {
	r := testRegistry(
		Emote{Name: "HOLY", ID: "1", URL: "https://cdn/holy", Provider: SevenTV},
		Emote{Name: "EZ", ID: "2", URL: "https://cdn/ez", Provider: BTTV},
	)

	m := chat.Message{Text: "HOLY that was EZ"}
	r.Apply(&m)

	if len(m.Emotes) != 2 {
		t.Fatalf("got %d emotes, want 2: %+v", len(m.Emotes), m.Emotes)
	}
	if e := m.Emotes[0]; e.Name != "HOLY" || e.Start != 0 || e.End != 4 {
		t.Errorf("first = %q [%d,%d), want HOLY [0,4)", e.Name, e.Start, e.End)
	}
	if e := m.Emotes[1]; e.Name != "EZ" || e.Start != 14 || e.End != 16 {
		t.Errorf("second = %q [%d,%d), want EZ [14,16)", e.Name, e.Start, e.End)
	}
}

// An emote name must match a whole word. Substring matching would turn every
// message containing "EZ" inside a word into an image.
func TestApplyRequiresWholeWord(t *testing.T) {
	r := testRegistry(Emote{Name: "EZ", ID: "1", URL: "https://cdn/ez"})
	for _, text := range []string{"EZreal", "sneEZe", "prefixEZ"} {
		m := chat.Message{Text: text}
		r.Apply(&m)
		if len(m.Emotes) != 0 {
			t.Errorf("Apply(%q) matched %+v, want no match", text, m.Emotes)
		}
	}
}

// Emote names are case-sensitive across all three providers.
func TestApplyIsCaseSensitive(t *testing.T) {
	r := testRegistry(Emote{Name: "Kappa", ID: "1", URL: "https://cdn/k"})
	m := chat.Message{Text: "kappa KAPPA Kappa"}
	r.Apply(&m)
	if len(m.Emotes) != 1 || m.Emotes[0].Start != 12 {
		t.Errorf("got %+v, want only the exact-case match at 12", m.Emotes)
	}
}

// Twitch already positioned its own emotes. A third-party emote sharing that
// name must not produce a second image on the same word.
func TestApplySkipsWordsCoveredByNativeEmotes(t *testing.T) {
	r := testRegistry(Emote{Name: "Kappa", ID: "7tv", URL: "https://cdn/7tv-kappa"})
	m := chat.Message{
		Text:   "hello Kappa",
		Emotes: []chat.Emote{{Name: "Kappa", ID: "25", URL: "https://twitch/25", Start: 6, End: 11}},
	}
	r.Apply(&m)

	if len(m.Emotes) != 1 {
		t.Fatalf("got %d emotes, want the native one only: %+v", len(m.Emotes), m.Emotes)
	}
	if m.Emotes[0].ID != "25" {
		t.Errorf("kept %q, want the native Twitch emote to win", m.Emotes[0].ID)
	}
}

// Positions index runes. A CJK or emoji prefix must not shift emote offsets.
func TestApplyIsRuneIndexed(t *testing.T) {
	r := testRegistry(Emote{Name: "PogU", ID: "1", URL: "https://cdn/pogu"})
	m := chat.Message{Text: "日本語 PogU"}
	r.Apply(&m)

	if len(m.Emotes) != 1 {
		t.Fatalf("got %d emotes, want 1", len(m.Emotes))
	}
	// "日本語 " is 4 runes; byte indexing would report 10.
	if e := m.Emotes[0]; e.Start != 4 || e.End != 8 {
		t.Errorf("PogU at [%d,%d), want [4,8)", e.Start, e.End)
	}
}

func TestApplyResultsAreSorted(t *testing.T) {
	r := testRegistry(Emote{Name: "b", ID: "1", URL: "https://cdn/b"})
	// A native emote late in the string, a third-party one early: the merge must
	// still come out ascending, because renderers walk it in one pass.
	m := chat.Message{
		Text:   "b x Kappa",
		Emotes: []chat.Emote{{Name: "Kappa", Start: 4, End: 9}},
	}
	r.Apply(&m)
	for i := 1; i < len(m.Emotes); i++ {
		if m.Emotes[i-1].Start > m.Emotes[i].Start {
			t.Fatalf("not sorted: %+v", m.Emotes)
		}
	}
}

func TestApplyCarriesZeroWidth(t *testing.T) {
	r := testRegistry(
		Emote{Name: "Clap", ID: "1", URL: "https://cdn/clap"},
		Emote{Name: "RainTime", ID: "2", URL: "https://cdn/rain", ZeroWidth: true},
	)
	m := chat.Message{Text: "Clap RainTime"}
	r.Apply(&m)

	if len(m.Emotes) != 2 {
		t.Fatalf("got %+v", m.Emotes)
	}
	if m.Emotes[0].ZeroWidth {
		t.Error("Clap marked zero-width")
	}
	if !m.Emotes[1].ZeroWidth {
		t.Error("RainTime lost its zero-width flag; the overlay would render it beside Clap, not on it")
	}
}

// An empty registry must not touch the message: chat renders before the six
// provider calls finish.
func TestApplyOnEmptyRegistryIsNoop(t *testing.T) {
	r := New()
	m := chat.Message{Text: "HOLY"}
	r.Apply(&m)
	if len(m.Emotes) != 0 {
		t.Errorf("got %+v, want none", m.Emotes)
	}
}

func TestApplyHandlesRepeatsAndSpacing(t *testing.T) {
	r := testRegistry(Emote{Name: "EZ", ID: "1", URL: "https://cdn/ez"})
	m := chat.Message{Text: "  EZ   EZ  "}
	r.Apply(&m)
	if len(m.Emotes) != 2 {
		t.Fatalf("got %d, want 2 across runs of spaces: %+v", len(m.Emotes), m.Emotes)
	}
	if m.Emotes[0].Start != 2 || m.Emotes[1].Start != 7 {
		t.Errorf("positions = %d,%d, want 2,7", m.Emotes[0].Start, m.Emotes[1].Start)
	}
}

func TestSevenTVSetToEmotes(t *testing.T) {
	set := sevenTVSet{Emotes: []sevenTVEmote{{
		ID:   "abc",
		Name: "Test",
		Data: sevenTVData{
			Flags: sevenTVZeroWidth,
			Host: sevenTVHost{
				URL: "//cdn.7tv.app/emote/abc",
				Files: []sevenTVFile{
					{"1x.webp", "WEBP", 32},
					{"4x.webp", "WEBP", 128},
					// AVIF is wider but must lose: it is not reliably animated
					// in OBS's browser engine.
					{"4x.avif", "AVIF", 256},
				},
			},
		},
	}}}

	got := set.toEmotes()
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].URL != "https://cdn.7tv.app/emote/abc/4x.webp" {
		t.Errorf("URL = %q, want the widest WebP with the protocol filled in", got[0].URL)
	}
	if !got[0].ZeroWidth {
		t.Error("flag 256 must decode as zero-width")
	}
}

// An emote with no WebP at all has no usable image and must be dropped rather
// than yielding a URL that 404s in the overlay.
func TestSevenTVSetSkipsEmotesWithNoWebP(t *testing.T) {
	set := sevenTVSet{Emotes: []sevenTVEmote{{
		ID:   "abc",
		Name: "Test",
		Data: sevenTVData{Host: sevenTVHost{
			URL:   "//cdn.7tv.app/emote/abc",
			Files: []sevenTVFile{{"4x.avif", "AVIF", 256}},
		}},
	}}}
	if got := set.toEmotes(); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestBestFFZURLFallsBack(t *testing.T) {
	if got := bestFFZURL(map[string]string{"1": "https://cdn/1", "4": "https://cdn/4"}); got != "https://cdn/4" {
		t.Errorf("got %q, want the 4x", got)
	}
	// Not every FFZ emote publishes a 4x.
	if got := bestFFZURL(map[string]string{"1": "https://cdn/1"}); got != "https://cdn/1" {
		t.Errorf("got %q, want the 1x fallback rather than nothing", got)
	}
	if got := bestFFZURL(map[string]string{"2": "//cdn/2"}); got != "https://cdn/2" {
		t.Errorf("got %q, want a protocol-relative URL fixed up", got)
	}
	if got := bestFFZURL(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
