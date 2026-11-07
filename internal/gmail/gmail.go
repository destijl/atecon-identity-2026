// Package gmail is the agent's tiny Gmail REST client. It holds no
// credential: it sends a placeholder Authorization header, which the
// Substrate egress gateway replaces with the user's OAuth token on the way
// out. Message content is never returned; subjects are redacted to lengths.
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

// Message is one redacted message.
type Message struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
}

// Result is what a call returned, safe to print.
type Result struct {
	Status   int       `json:"status"`
	From     string    `json:"answeredBy"`
	Messages []Message `json:"messages,omitempty"`
	Error    string    `json:"error,omitempty"`
	Reason   string    `json:"reason,omitempty"`
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

// List returns up to n message IDs with redacted subjects.
func (c *Client) List(ctx context.Context, n int) (*Result, error) {
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	res, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/messages?maxResults=%d", n), &list)
	if err != nil || res.Status != http.StatusOK {
		return res, err
	}
	res.Messages = []Message{}
	for _, m := range list.Messages {
		var meta struct {
			Payload struct {
				Headers []struct{ Name, Value string } `json:"headers"`
			} `json:"payload"`
		}
		q := url.Values{"format": {"metadata"}, "metadataHeaders": {"Subject"}}
		r, err := c.do(ctx, http.MethodGet, "/messages/"+url.PathEscape(m.ID)+"?"+q.Encode(), &meta)
		if err != nil {
			return nil, err
		}
		subj := "(unavailable)"
		if r.Status == http.StatusOK {
			subj = Redact("")
			for _, h := range meta.Payload.Headers {
				if strings.EqualFold(h.Name, "Subject") {
					subj = Redact(h.Value)
				}
			}
		}
		res.Messages = append(res.Messages, Message{ID: RedactID(m.ID), Subject: subj})
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

// Redact hides a subject, keeping only its length.
func Redact(s string) string {
	return fmt.Sprintf("[redacted, %d chars]", len([]rune(s)))
}

// RedactID keeps the first 4 characters of a message ID.
func RedactID(id string) string {
	if len(id) <= 4 {
		return id
	}
	return id[:4] + "…"
}
