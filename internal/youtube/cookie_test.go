package youtube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyxnix/crow/internal/chat"
)

func TestCookieAuthSAPISIDHASH(t *testing.T) {
	a := &CookieAuth{Cookies: "VISITOR_INFO=x; SAPISID=mysapisid; SSID=y"}
	if !a.Valid() {
		t.Fatal("should be valid with SAPISID present")
	}
	if (&CookieAuth{Cookies: "VISITOR_INFO=x"}).Valid() {
		t.Error("should be invalid without SAPISID")
	}
	// __Secure-3PAPISID is an accepted alternative source.
	if !(&CookieAuth{Cookies: "__Secure-3PAPISID=z"}).Valid() {
		t.Error("should accept __Secure-3PAPISID")
	}

	req, _ := http.NewRequest(http.MethodPost, "https://www.youtube.com/x", nil)
	a.decorate(req)
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "SAPISIDHASH ") {
		t.Fatalf("auth header = %q", auth)
	}
	// Shape: "SAPISIDHASH <ts>_<40 hex sha1>".
	rest := strings.TrimPrefix(auth, "SAPISIDHASH ")
	ts, hash, ok := strings.Cut(rest, "_")
	if !ok || ts == "" || len(hash) != 40 {
		t.Errorf("malformed hash %q", rest)
	}
	if req.Header.Get("Cookie") != a.Cookies {
		t.Error("cookie header not set")
	}
}

// TestCookieSendAndMod drives the whole cookie path: prepare (fetch chat page
// for key + send params), send, then a mod remove via the context menu.
func TestCookieSendAndMod(t *testing.T) {
	var sentText, moderated string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/watch" || r.URL.Path == "/live_chat":
			// The chat page: embeds the API key and the send-params.
			w.Write([]byte(`<html>"INNERTUBE_API_KEY":"KEY123" ` +
				`"sendLiveChatMessageEndpoint":{"params":"SENDPARAMS"} ` +
				`<link rel="canonical" href="https://www.youtube.com/watch?v=vid00000001"></html>`))
		case strings.Contains(r.URL.Path, "send_message"):
			var body struct {
				Params      string `json:"params"`
				RichMessage struct {
					TextSegments []struct {
						Text string `json:"text"`
					} `json:"textSegments"`
				} `json:"richMessage"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Params != "SENDPARAMS" {
				t.Errorf("send params = %q", body.Params)
			}
			sentText = body.RichMessage.TextSegments[0].Text
			w.Write([]byte(`{"actions":[{"addChatItemAction":{"item":{"liveChatTextMessageRenderer":{}}}}]}`))
		case strings.Contains(r.URL.Path, "get_item_context_menu"):
			if got := r.URL.Query().Get("params"); got != "TOKEN9" {
				t.Errorf("menu token = %q", got)
			}
			w.Write([]byte(`{"items":[{"menuServiceItemRenderer":{` +
				`"text":{"text":"Remove"},"serviceEndpoint":{"params":"REMOVEPARAMS"}}}]}`))
		case strings.Contains(r.URL.Path, "/moderate"):
			var body struct {
				Params string `json:"params"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			moderated = body.Params
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	}))
	defer srv.Close()
	oldBase := base
	base = srv.URL
	defer func() { base = oldBase }()

	ctx := context.Background()
	auth := &CookieAuth{Cookies: "SAPISID=s"}
	sender := &CookieSender{Video: "vid00000001", Auth: auth}

	if err := sender.Send(ctx, "hello world"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if sentText != "hello world" {
		t.Errorf("sent %q", sentText)
	}

	mod := &CookieMod{Sender: sender}
	// No token held yet: should error rather than silently no-op.
	if err := mod.DeleteMessage(ctx, "msg9"); err == nil {
		t.Error("delete without a held token should error")
	}
	mod.Observe(chat.Message{ID: "msg9", AuthorID: "UCx", ModParams: "TOKEN9"})
	if err := mod.DeleteMessage(ctx, "msg9"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if moderated != "REMOVEPARAMS" {
		t.Errorf("moderated with %q", moderated)
	}
}

func TestApproxCount(t *testing.T) {
	cases := map[string]int{"1.23M": 1230000, "45.6K": 45600, "12": 12, "2B": 2000000000, "1,234": 1234}
	for in, want := range cases {
		if got := approxCount(in); got != want {
			t.Errorf("approxCount(%q) = %d, want %d", in, got, want)
		}
	}
}
