package youtube

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyxnix/crow/internal/chat"
)

// The innertube get_live_chat response, narrowed to the fields we render.
type liveChatResponse struct {
	ContinuationContents struct {
		LiveChatContinuation struct {
			// Each entry is {"timedContinuationData": {...}} or a sibling variant;
			// decoding as a one-key map handles them all.
			Continuations []map[string]struct {
				Continuation string `json:"continuation"`
				TimeoutMs    int    `json:"timeoutMs"`
			} `json:"continuations"`
			Actions []struct {
				AddChatItemAction struct {
					Item struct {
						LiveChatTextMessageRenderer                          *messageRenderer          `json:"liveChatTextMessageRenderer"`
						LiveChatPaidMessageRenderer                          *messageRenderer          `json:"liveChatPaidMessageRenderer"`
						LiveChatMembershipItemRenderer                       *messageRenderer          `json:"liveChatMembershipItemRenderer"`
						LiveChatSponsorshipsGiftPurchaseAnnouncementRenderer *giftAnnouncementRenderer `json:"liveChatSponsorshipsGiftPurchaseAnnouncementRenderer"`
					} `json:"item"`
				} `json:"addChatItemAction"`
				MarkChatItemAsDeletedAction struct {
					TargetItemID string `json:"targetItemId"`
				} `json:"markChatItemAsDeletedAction"`
				MarkChatItemsByAuthorAsDeletedAction struct {
					ExternalChannelID string `json:"externalChannelId"`
				} `json:"markChatItemsByAuthorAsDeletedAction"`
			} `json:"actions"`
		} `json:"liveChatContinuation"`
	} `json:"continuationContents"`
}

// messageRenderer covers both plain and paid (superchat) messages; the paid
// variant just adds the amount.
type messageRenderer struct {
	ID            string `json:"id"`
	TimestampUsec string `json:"timestampUsec"`
	AuthorName    struct {
		SimpleText string `json:"simpleText"`
	} `json:"authorName"`
	AuthorExternalChannelID string `json:"authorExternalChannelId"`
	AuthorBadges            []struct {
		LiveChatAuthorBadgeRenderer struct {
			CustomThumbnail *struct {
				Thumbnails []struct {
					URL string `json:"url"`
				} `json:"thumbnails"`
			} `json:"customThumbnail"`
			Icon *struct {
				IconType string `json:"iconType"`
			} `json:"icon"`
			Tooltip string `json:"tooltip"`
		} `json:"liveChatAuthorBadgeRenderer"`
	} `json:"authorBadges"`
	Message struct {
		Runs []messageRun `json:"runs"`
	} `json:"message"`
	PurchaseAmountText struct {
		SimpleText string `json:"simpleText"`
	} `json:"purchaseAmountText"`
	// Membership items carry their event line in headers instead of Message:
	// milestones use headerPrimaryText ("Member for 6 months"), new members
	// only headerSubtext ("Welcome to ... membership!").
	HeaderPrimaryText struct {
		Runs []messageRun `json:"runs"`
	} `json:"headerPrimaryText"`
	HeaderSubtext struct {
		SimpleText string       `json:"simpleText"`
		Runs       []messageRun `json:"runs"`
	} `json:"headerSubtext"`
	ContextMenuEndpoint struct {
		LiveChatItemContextMenuEndpoint struct {
			Params string `json:"params"`
		} `json:"liveChatItemContextMenuEndpoint"`
	} `json:"contextMenuEndpoint"`
}

// giftAnnouncementRenderer is a gifted-memberships purchase announcement, whose
// author and text live under a header renderer rather than the message shape.
type giftAnnouncementRenderer struct {
	ID                      string `json:"id"`
	TimestampUsec           string `json:"timestampUsec"`
	AuthorExternalChannelID string `json:"authorExternalChannelId"`
	Header                  struct {
		LiveChatSponsorshipsHeaderRenderer struct {
			AuthorName struct {
				SimpleText string `json:"simpleText"`
			} `json:"authorName"`
			PrimaryText struct {
				Runs []messageRun `json:"runs"` // "Gifted 5 ... memberships"
			} `json:"primaryText"`
		} `json:"liveChatSponsorshipsHeaderRenderer"`
	} `json:"header"`
}

type messageRun struct {
	Text  string `json:"text"`
	Emoji *struct {
		EmojiID   string   `json:"emojiId"`
		Shortcuts []string `json:"shortcuts"`
		Image     struct {
			Thumbnails []struct {
				URL string `json:"url"`
			} `json:"thumbnails"`
		} `json:"image"`
		IsCustomEmoji bool `json:"isCustomEmoji"`
	} `json:"emoji"`
}

// parseChat converts one get_live_chat response into messages, moderation
// events, the next continuation ("" when the chat is over) and the polling
// delay YouTube requested. It never fails: unrecognized shapes just yield
// nothing, which also covers a stream ending mid-poll.
func parseChat(data []byte, channel string) ([]chat.Message, []chat.ModEvent, string, time.Duration) {
	var r liveChatResponse
	json.Unmarshal(data, &r)
	lc := r.ContinuationContents.LiveChatContinuation

	next, wait := "", 2*time.Second
	for _, c := range lc.Continuations {
		for _, v := range c {
			if v.Continuation != "" {
				next = v.Continuation
				if v.TimeoutMs > 0 {
					wait = time.Duration(v.TimeoutMs) * time.Millisecond
				}
			}
		}
	}
	// Clamp: never hammer the endpoint, never lag a live chat by half a minute.
	wait = min(max(wait, time.Second), 15*time.Second)

	var msgs []chat.Message
	var events []chat.ModEvent
	for _, a := range lc.Actions {
		if mr := a.AddChatItemAction.Item.LiveChatTextMessageRenderer; mr != nil {
			msgs = append(msgs, toMessage(mr, channel))
		}
		if mr := a.AddChatItemAction.Item.LiveChatPaidMessageRenderer; mr != nil {
			m := toMessage(mr, channel)
			m.Alert = chat.AlertSuperchat
			m.AlertText = mr.AuthorName.SimpleText + " sent " + mr.PurchaseAmountText.SimpleText
			msgs = append(msgs, m)
		}
		if mr := a.AddChatItemAction.Item.LiveChatMembershipItemRenderer; mr != nil {
			m := toMessage(mr, channel)
			m.Alert = chat.AlertMember
			if event := runsText(mr.HeaderPrimaryText.Runs); event != "" {
				m.AlertText = mr.AuthorName.SimpleText + " — " + event // milestone
			} else {
				m.AlertText = mr.AuthorName.SimpleText + " became a member"
			}
			msgs = append(msgs, m)
		}
		if gr := a.AddChatItemAction.Item.LiveChatSponsorshipsGiftPurchaseAnnouncementRenderer; gr != nil {
			hr := gr.Header.LiveChatSponsorshipsHeaderRenderer
			msgs = append(msgs, chat.Message{
				ID:          gr.ID,
				Platform:    chat.YouTube,
				Channel:     channel,
				AuthorID:    gr.AuthorExternalChannelID,
				Author:      hr.AuthorName.SimpleText,
				AuthorLogin: gr.AuthorExternalChannelID,
				Alert:       chat.AlertGiftMember,
				AlertText:   hr.AuthorName.SimpleText + " " + runsText(hr.PrimaryText.Runs),
				At:          usecTime(gr.TimestampUsec),
			})
		}
		if id := a.MarkChatItemAsDeletedAction.TargetItemID; id != "" {
			events = append(events, chat.ModEvent{Kind: chat.DeleteMessage, MessageID: id})
		}
		if uid := a.MarkChatItemsByAuthorAsDeletedAction.ExternalChannelID; uid != "" {
			events = append(events, chat.ModEvent{Kind: chat.ClearUser, UserID: uid, Login: uid})
		}
	}
	return msgs, events, next, wait
}

// toMessage flattens a renderer into a chat.Message: runs become one text
// string, custom emoji become chat.Emotes with rune positions into it, and
// author badges become role flags plus (for members) badge images.
func toMessage(mr *messageRenderer, channel string) chat.Message {
	var text strings.Builder
	runes := 0
	write := func(s string) {
		text.WriteString(s)
		runes += utf8.RuneCountInString(s)
	}
	// A superchat leads with its amount so it stands out the way it does on
	// YouTube; the body may be empty, which leaves just the amount.
	if amt := mr.PurchaseAmountText.SimpleText; amt != "" {
		write(amt + " ")
	}

	var emotes []chat.Emote
	for _, run := range mr.Message.Runs {
		e := run.Emoji
		if e == nil {
			write(run.Text)
			continue
		}
		if !e.IsCustomEmoji {
			// A standard emoji's ID is the character itself; keep it as text.
			write(e.EmojiID)
			continue
		}
		name := e.EmojiID
		if len(e.Shortcuts) > 0 {
			name = e.Shortcuts[0]
		}
		u := ""
		if n := len(e.Image.Thumbnails); n > 0 {
			u = e.Image.Thumbnails[n-1].URL
		}
		start := runes
		write(name)
		emotes = append(emotes, chat.Emote{
			Name: name, ID: e.EmojiID, URL: u, Provider: "youtube",
			Start: start, End: runes,
		})
	}

	m := chat.Message{
		ID:       mr.ID,
		Platform: chat.YouTube,
		Channel:  channel,
		AuthorID: mr.AuthorExternalChannelID,
		Author:   mr.AuthorName.SimpleText,
		// YouTube has no separate login; the channel ID is the stable name, and
		// setting it keeps overlay per-user removal working.
		AuthorLogin: mr.AuthorExternalChannelID,
		Text:        text.String(),
		Emotes:      emotes,
		ModParams:   mr.ContextMenuEndpoint.LiveChatItemContextMenuEndpoint.Params,
		At:          usecTime(mr.TimestampUsec),
	}
	for _, b := range mr.AuthorBadges {
		br := b.LiveChatAuthorBadgeRenderer
		if br.Icon != nil {
			switch br.Icon.IconType {
			case "OWNER":
				m.Broadcaster = true
			case "MODERATOR":
				m.Moderator = true
			}
		}
		if br.CustomThumbnail != nil { // channel-member badge, an image
			m.Subscriber = true
			if n := len(br.CustomThumbnail.Thumbnails); n > 0 {
				m.Badges = append(m.Badges, chat.Badge{Name: br.Tooltip, URL: br.CustomThumbnail.Thumbnails[n-1].URL})
			}
		}
	}
	return m
}

// runsText flattens runs to plain text, using emoji shortcuts for emoji runs.
func runsText(rs []messageRun) string {
	var b strings.Builder
	for _, r := range rs {
		switch {
		case r.Text != "":
			b.WriteString(r.Text)
		case r.Emoji != nil && len(r.Emoji.Shortcuts) > 0:
			b.WriteString(r.Emoji.Shortcuts[0])
		}
	}
	return b.String()
}

// usecTime reads timestampUsec (Unix microseconds), falling back to now.
func usecTime(v string) time.Time {
	us, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Now()
	}
	return time.UnixMicro(us)
}
