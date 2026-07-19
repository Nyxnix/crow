package youtube

import (
	"testing"
	"time"

	"github.com/Nyxnix/crow/internal/chat"
)

// A trimmed real-shaped get_live_chat response: a text message with a custom
// emoji and a unicode emoji, a superchat, a member+moderator author, and both
// deletion actions.
const sample = `{
"continuationContents": {"liveChatContinuation": {
  "continuations": [{"timedContinuationData": {"timeoutMs": 4000, "continuation": "NEXT"}}],
  "actions": [
    {"addChatItemAction": {"item": {"liveChatTextMessageRenderer": {
      "id": "msg1",
      "timestampUsec": "1721000000000000",
      "authorName": {"simpleText": "ねこ Nyx"},
      "authorExternalChannelId": "UCabc",
      "authorBadges": [
        {"liveChatAuthorBadgeRenderer": {"icon": {"iconType": "MODERATOR"}, "tooltip": "Moderator"}},
        {"liveChatAuthorBadgeRenderer": {"customThumbnail": {"thumbnails": [{"url": "small"}, {"url": "https://yt.example/member.png"}]}, "tooltip": "Member (1 year)"}}
      ],
      "message": {"runs": [
        {"text": "hi "},
        {"emoji": {"emojiId": "UC/x", "shortcuts": [":wave:"], "isCustomEmoji": true,
                   "image": {"thumbnails": [{"url": "https://yt.example/wave24.png"}, {"url": "https://yt.example/wave48.png"}]}}},
        {"emoji": {"emojiId": "🎉", "isCustomEmoji": false, "image": {"thumbnails": [{"url": "x"}]}}}
      ]}
    }}}},
    {"addChatItemAction": {"item": {"liveChatPaidMessageRenderer": {
      "id": "sc1",
      "timestampUsec": "1721000001000000",
      "authorName": {"simpleText": "Rich"},
      "authorExternalChannelId": "UCdef",
      "purchaseAmountText": {"simpleText": "$5.00"},
      "message": {"runs": [{"text": "take my money"}]}
    }}}},
    {"markChatItemAsDeletedAction": {"targetItemId": "msg0"}},
    {"markChatItemsByAuthorAsDeletedAction": {"externalChannelId": "UCbad"}}
  ]
}}}`

func TestParseChat(t *testing.T) {
	msgs, events, next, wait := parseChat([]byte(sample), "yt-tab")

	if next != "NEXT" || wait != 4*time.Second {
		t.Fatalf("continuation = %q, wait = %v", next, wait)
	}
	if len(msgs) != 2 || len(events) != 2 {
		t.Fatalf("got %d msgs, %d events", len(msgs), len(events))
	}

	m := msgs[0]
	if m.Platform != chat.YouTube || m.ID != "msg1" || m.Channel != "yt-tab" ||
		m.Author != "ねこ Nyx" || m.AuthorID != "UCabc" || m.AuthorLogin != "UCabc" {
		t.Errorf("message basics wrong: %+v", m)
	}
	if m.Text != "hi :wave:🎉" {
		t.Errorf("text = %q", m.Text)
	}
	if !m.Moderator || !m.Subscriber || m.Broadcaster {
		t.Errorf("roles wrong: %+v", m)
	}
	if len(m.Badges) != 1 || m.Badges[0].URL != "https://yt.example/member.png" {
		t.Errorf("badges = %+v", m.Badges)
	}
	if len(m.Emotes) != 1 {
		t.Fatalf("emotes = %+v", m.Emotes)
	}
	e := m.Emotes[0]
	// ":wave:" starts after "hi " = rune 3, ends before the unicode emoji.
	if e.Start != 3 || e.End != 9 || e.URL != "https://yt.example/wave48.png" || e.Name != ":wave:" {
		t.Errorf("emote = %+v", e)
	}
	if got := m.At.UnixMicro(); got != 1721000000000000 {
		t.Errorf("At = %d", got)
	}

	sc := msgs[1]
	if sc.Text != "$5.00 take my money" || sc.ID != "sc1" {
		t.Errorf("superchat = %+v", sc)
	}
	if sc.Alert != chat.AlertSuperchat || sc.AlertText != "Rich sent $5.00" {
		t.Errorf("superchat alert = %q %q", sc.Alert, sc.AlertText)
	}

	if events[0].Kind != chat.DeleteMessage || events[0].MessageID != "msg0" {
		t.Errorf("delete event = %+v", events[0])
	}
	if events[1].Kind != chat.ClearUser || events[1].UserID != "UCbad" || events[1].Login != "UCbad" {
		t.Errorf("clear event = %+v", events[1])
	}
}

// Membership items and gift-purchase announcements become member alerts.
func TestParseChatMemberships(t *testing.T) {
	const sample = `{
"continuationContents": {"liveChatContinuation": {
  "actions": [
    {"addChatItemAction": {"item": {"liveChatMembershipItemRenderer": {
      "id": "mem1",
      "timestampUsec": "1721000002000000",
      "authorName": {"simpleText": "NewFan"},
      "authorExternalChannelId": "UCnew",
      "headerSubtext": {"runs": [{"text": "Welcome to Nyx memberships!"}]}
    }}}},
    {"addChatItemAction": {"item": {"liveChatMembershipItemRenderer": {
      "id": "mem2",
      "timestampUsec": "1721000003000000",
      "authorName": {"simpleText": "OldFan"},
      "authorExternalChannelId": "UCold",
      "headerPrimaryText": {"runs": [{"text": "Member for "}, {"text": "6"}, {"text": " months"}]},
      "message": {"runs": [{"text": "still here"}]}
    }}}},
    {"addChatItemAction": {"item": {"liveChatSponsorshipsGiftPurchaseAnnouncementRenderer": {
      "id": "gift1",
      "timestampUsec": "1721000004000000",
      "authorExternalChannelId": "UCgift",
      "header": {"liveChatSponsorshipsHeaderRenderer": {
        "authorName": {"simpleText": "Santa"},
        "primaryText": {"runs": [{"text": "Gifted "}, {"text": "5"}, {"text": " Nyx memberships"}]}
      }}
    }}}}
  ]
}}}`

	msgs, _, _, _ := parseChat([]byte(sample), "yt-tab")
	if len(msgs) != 3 {
		t.Fatalf("got %d msgs, want 3", len(msgs))
	}

	if m := msgs[0]; m.Alert != chat.AlertMember || m.AlertText != "NewFan became a member" {
		t.Errorf("new member alert = %q %q", m.Alert, m.AlertText)
	}
	if m := msgs[1]; m.Alert != chat.AlertMember || m.AlertText != "OldFan — Member for 6 months" ||
		m.Text != "still here" {
		t.Errorf("milestone alert = %q %q text=%q", m.Alert, m.AlertText, m.Text)
	}
	if m := msgs[2]; m.Alert != chat.AlertGiftMember || m.AlertText != "Santa Gifted 5 Nyx memberships" ||
		m.Author != "Santa" || m.AuthorID != "UCgift" || m.ID != "gift1" {
		t.Errorf("gift alert = %+v", m)
	}
}

func TestParseChatGarbage(t *testing.T) {
	msgs, events, next, wait := parseChat([]byte(`{"weird": true}`), "c")
	if msgs != nil || events != nil || next != "" || wait != 2*time.Second {
		t.Errorf("garbage should parse to nothing: %v %v %q %v", msgs, events, next, wait)
	}
}
