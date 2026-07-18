// Package ivr fetches the user facts the official Twitch mod card shows —
// account age, follow date, subscription tier and months — from the community
// IVR API (api.ivr.fi).
//
// Twitch exposes none of this to third-party apps officially: the subscription
// and follow data on the real mod card come from Twitch's private GraphQL,
// which rejects third-party tokens. IVR is the public, unauthenticated source
// that Chatterino uses for the same purpose. It is a third party, so a failure
// here degrades the card rather than breaking it.
package ivr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// baseURL is a var so tests can point it at a stand-in server.
var baseURL = "https://api.ivr.fi/v2"

// CardInfo is the extra user detail shown on the moderation card, beyond what
// the local message buffer provides.
type CardInfo struct {
	CreatedAt  time.Time  // account creation
	AvatarURL  string     // profile picture
	FollowedAt *time.Time // nil if not following, or the channel hides it
	SubTier    string     // "1", "2", "3", or "" if not subscribed
	SubMonths  int        // cumulative months, 0 if not subscribed
	SubHidden  bool       // the viewer hides their subscription status
}

type Client struct {
	HTTP *http.Client
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 8 * time.Second}
}

// CardInfo fetches account and subscription facts for userLogin in
// channelLogin. The two upstream calls run concurrently; whichever succeeds
// contributes its fields, so a missing subage still yields the account age.
func (c *Client) CardInfo(ctx context.Context, userLogin, channelLogin string) (CardInfo, error) {
	var (
		info    CardInfo
		user    userResp
		sub     subage
		errUser error
		errSub  error
		wg      sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		user, errUser = c.user(ctx, userLogin)
	}()
	go func() {
		defer wg.Done()
		sub, errSub = c.subage(ctx, userLogin, channelLogin)
	}()
	wg.Wait()

	if errUser == nil {
		if t, err := time.Parse(time.RFC3339, user.CreatedAt); err == nil {
			info.CreatedAt = t
		}
		info.AvatarURL = user.Logo
	}
	if errSub == nil {
		info.SubHidden = sub.StatusHidden
		if sub.FollowedAt != nil && *sub.FollowedAt != "" {
			if t, err := time.Parse(time.RFC3339, *sub.FollowedAt); err == nil {
				info.FollowedAt = &t
			}
		}
		// meta is present only for an active paid/prime sub.
		if sub.Meta != nil && sub.Meta.Tier != "" {
			info.SubTier = sub.Meta.Tier
		}
		if sub.Cumulative != nil {
			info.SubMonths = sub.Cumulative.Months
		}
	}

	// Only a total failure of both calls is worth reporting; a partial result is
	// still useful on the card.
	if errUser != nil && errSub != nil {
		return info, fmt.Errorf("ivr: %v; %v", errUser, errSub)
	}
	return info, nil
}

type userResp struct {
	CreatedAt string `json:"createdAt"`
	Logo      string `json:"logo"`
}

func (c *Client) user(ctx context.Context, login string) (userResp, error) {
	// The user endpoint returns an array of matches.
	var out []userResp
	if err := c.getJSON(ctx, baseURL+"/twitch/user?login="+url.QueryEscape(login), &out); err != nil {
		return userResp{}, err
	}
	if len(out) == 0 {
		return userResp{}, fmt.Errorf("no user")
	}
	return out[0], nil
}

type subage struct {
	StatusHidden bool    `json:"statusHidden"`
	FollowedAt   *string `json:"followedAt"`
	Cumulative   *struct {
		Months int `json:"months"`
	} `json:"cumulative"`
	Meta *struct {
		Tier string `json:"tier"`
	} `json:"meta"`
}

func (c *Client) subage(ctx context.Context, user, channel string) (subage, error) {
	var out subage
	u := fmt.Sprintf("%s/twitch/subage/%s/%s", baseURL, url.PathEscape(user), url.PathEscape(channel))
	err := c.getJSON(ctx, u, &out)
	return out, err
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", endpoint, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
