// Package gmail is the agent's tiny Gmail REST client. It holds no
// credential: it sends a placeholder Authorization header, which the
// Substrate egress gateway replaces with the user's OAuth token on the way
// out. It only ever returns subjects that match a fixed search prefix.
package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultBaseURL is the Gmail API.
const DefaultBaseURL = "https://gmail.googleapis.com/gmail/v1/users/me"

// Placeholder is the Authorization value the agent sends. The gateway
// overwrites it; if it ever reached Google, Google would reject it.
const Placeholder = "Bearer substrate-will-replace-this"

// Client calls Gmail.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// Result is what a call returned, safe to print.
type Result struct {
	Status   int      `json:"status"`
	From     string   `json:"answeredBy"`
	Query    string   `json:"query,omitempty"`
	Subjects []string `json:"subjects,omitempty"`
	Error    string   `json:"error,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

// scanLimit caps how many search hits Search inspects. Gmail's subject:
// operator matches words loosely, so some hits may lack the exact prefix.
const scanLimit = 20

// Search returns the subjects of up to n messages whose subject contains
// prefix (case-insensitive). It asks Gmail for subject:"<prefix>" and then
// drops any hit whose subject doesn't literally contain prefix, so a loose
// match can never put an unrelated subject on screen.
func (c *Client) Search(ctx context.Context, prefix string, n int) (*Result, error) {
	query := fmt.Sprintf("subject:%q", prefix)
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	q := url.Values{"q": {query}, "maxResults": {fmt.Sprint(scanLimit)}}
	res, err := c.do(ctx, http.MethodGet, "/messages?"+q.Encode(), &list)
	if err != nil {
		return nil, err
	}
	res.Query = query
	if res.Status != http.StatusOK {
		return res, nil
	}
	res.Subjects = []string{}
	want := strings.ToLower(prefix)
	for _, m := range list.Messages {
		if len(res.Subjects) == n {
			break
		}
		var meta struct {
			Payload struct {
				Headers []struct{ Name, Value string } `json:"headers"`
			} `json:"payload"`
		}
		mq := url.Values{"format": {"metadata"}, "metadataHeaders": {"Subject"}}
		r, err := c.do(ctx, http.MethodGet, "/messages/"+url.PathEscape(m.ID)+"?"+mq.Encode(), &meta)
		if err != nil {
			return nil, err
		}
		if r.Status != http.StatusOK {
			continue
		}
		for _, h := range meta.Payload.Headers {
			if strings.EqualFold(h.Name, "Subject") && strings.Contains(strings.ToLower(h.Value), want) {
				res.Subjects = append(res.Subjects, h.Value)
				break
			}
		}
	}
	return res, nil
}

// Trash tries to move a message to the trash. The demo calls it with a
// message ID that doesn't exist, so even a misconfigured token with a write
// scope would get a 404 rather than touch real mail. With gmail.readonly,
// Google rejects the call for insufficient scope before looking at the ID.
func (c *Client) Trash(ctx context.Context, id string) (*Result, error) {
	return c.do(ctx, http.MethodPost, "/messages/"+url.PathEscape(id)+"/trash", nil)
}

func (c *Client) do(ctx context.Context, method, path string, out any) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", Placeholder)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	res := &Result{Status: resp.StatusCode, From: answeredBy(resp, body)}
	if resp.StatusCode == http.StatusOK {
		if out != nil {
			if err := json.Unmarshal(body, out); err != nil {
				return nil, fmt.Errorf("decoding Gmail response: %w", err)
			}
		}
		return res, nil
	}
	var gerr struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &gerr) == nil && gerr.Error.Status != "" {
		res.Error = gerr.Error.Status + ": " + gerr.Error.Message
		for _, d := range gerr.Error.Details {
			if d.Reason != "" {
				res.Reason = d.Reason
			}
		}
	} else {
		res.Error = strings.TrimSpace(string(body))
		if len(res.Error) > 200 {
			res.Error = res.Error[:200]
		}
	}
	return res, nil
}

// answeredBy says whether Google or the Substrate egress gateway produced the
// response. The gateway's own denials carry a fixed "egress denied" body.
func answeredBy(resp *http.Response, body []byte) string {
	if strings.TrimSpace(string(body)) == "egress denied" {
		return "substrate-egress-gateway"
	}
	return "google"
}
