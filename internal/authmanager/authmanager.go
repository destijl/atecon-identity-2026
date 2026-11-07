// Package authmanager is a minimal client for Google Cloud Agent Auth Manager's
// 3-legged OAuth credential API (agentidentitycredentials.googleapis.com).
//
// The client never logs or returns tokens in errors.
package authmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// DefaultBaseURL is the Auth Manager credentials endpoint.
const DefaultBaseURL = "https://agentidentitycredentials.googleapis.com/v1"

// TokenSource returns a Google access token for calling Auth Manager.
type TokenSource func(ctx context.Context) (string, error)

// Client calls credentials:retrieve and credentials:finalize for one auth
// provider.
type Client struct {
	// Provider is projects/<p>/locations/<l>/authProviders/<name>.
	Provider string
	BaseURL  string
	HTTP     *http.Client
	Token    TokenSource
}

// Result is what retrieve returned. Exactly one of Token and Consent is set.
type Result struct {
	Token   *Token
	Consent *Consent
}

// Token is an OAuth access token Auth Manager holds for the user.
type Token struct {
	Value  string
	Expiry time.Time
}

// Consent says the user must consent before Auth Manager will issue a token.
type Consent struct {
	AuthorizationURL string
	Nonce            string
	// UID identifies this consent attempt; Auth Manager echoes it to the
	// continue URI as the uuid query parameter.
	UID string
}

// ErrConsentRejected means the user declined consent.
var ErrConsentRejected = errors.New("auth manager: user rejected consent")

// ErrPending means consent is in progress and no token exists yet.
var ErrPending = errors.New("auth manager: consent pending")

type retrieveRequest struct {
	UserID      string   `json:"userId"`
	Scopes      []string `json:"scopes,omitempty"`
	ContinueURI string   `json:"continueUri,omitempty"`
}

// The API is proto-JSON, so either field-name style may come back.
type retrieveResponse struct {
	Success *struct {
		Token       string    `json:"token"`
		ExpireTime  time.Time `json:"expireTime"`
		ExpireTime2 time.Time `json:"expire_time"`
	} `json:"success"`
	Consent  *consentJSON `json:"uriConsentRequired"`
	Consent2 *consentJSON `json:"uri_consent_required"`
	Pending  *struct{}    `json:"pending"`
	Rejected *struct{}    `json:"consentRejected"`
	Reject2  *struct{}    `json:"consent_rejected"`
}

type consentJSON struct {
	AuthorizationURI  string `json:"authorizationUri"`
	AuthorizationURI2 string `json:"authorization_uri"`
	ConsentNonce      string `json:"consentNonce"`
	ConsentNonce2     string `json:"consent_nonce"`
	UID               string `json:"uid"`
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Retrieve asks Auth Manager for userID's token with the given scopes.
// continueURI is where the browser lands after consent; empty uses the
// provider's default.
func (c *Client) Retrieve(ctx context.Context, userID string, scopes []string, continueURI string) (*Result, error) {
	var resp retrieveResponse
	if err := c.post(ctx, "credentials:retrieve", retrieveRequest{UserID: userID, Scopes: scopes, ContinueURI: continueURI}, &resp); err != nil {
		return nil, err
	}
	switch {
	case resp.Success != nil && resp.Success.Token != "":
		exp := resp.Success.ExpireTime
		if exp.IsZero() {
			exp = resp.Success.ExpireTime2
		}
		return &Result{Token: &Token{Value: resp.Success.Token, Expiry: exp}}, nil
	case resp.Consent != nil || resp.Consent2 != nil:
		cj := resp.Consent
		if cj == nil {
			cj = resp.Consent2
		}
		u := firstNonEmpty(cj.AuthorizationURI, cj.AuthorizationURI2)
		if u == "" {
			return nil, errors.New("auth manager: consent required but no authorization URI returned")
		}
		return &Result{Consent: &Consent{AuthorizationURL: u, Nonce: firstNonEmpty(cj.ConsentNonce, cj.ConsentNonce2), UID: cj.UID}}, nil
	case resp.Rejected != nil || resp.Reject2 != nil:
		return nil, ErrConsentRejected
	case resp.Pending != nil:
		return nil, ErrPending
	default:
		return nil, errors.New("auth manager: retrieve returned no recognizable result")
	}
}

type finalizeRequest struct {
	UserID                string `json:"userId"`
	UserIDValidationState string `json:"userIdValidationState"`
	ConsentNonce          string `json:"consentNonce"`
}

// Finalize completes consent after the browser returns to the continue URI.
func (c *Client) Finalize(ctx context.Context, userID, validationState, nonce string) error {
	return c.post(ctx, "credentials:finalize", finalizeRequest{UserID: userID, UserIDValidationState: validationState, ConsentNonce: nonce}, nil)
}

func (c *Client) post(ctx context.Context, method string, in, out any) error {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+c.Provider+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != nil {
		tok, err := c.Token(ctx)
		if err != nil {
			return fmt.Errorf("auth manager: getting caller token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("auth manager %s: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("auth manager %s: reading response: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		// Error bodies are Google API errors; they carry no tokens.
		return fmt.Errorf("auth manager %s: HTTP %d: %s", method, resp.StatusCode, truncate(data, 512))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("auth manager %s: decoding response: %w", method, err)
	}
	return nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// MetadataTokenSource returns tokens from the GKE metadata server. With
// Workload Identity and no Google service account annotation on the
// Kubernetes service account, the token is a federated token for the KSA
// principal, which is what the Auth Manager IAM binding names.
func MetadataTokenSource(hc *http.Client) TokenSource {
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	var mu sync.Mutex
	var cached string
	var exp time.Time
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if cached != "" && time.Until(exp) > 2*time.Minute {
			return cached, nil
		}
		u := "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token?" +
			url.Values{"scopes": {"https://www.googleapis.com/auth/cloud-platform"}}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Metadata-Flavor", "Google")
		resp, err := hc.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("metadata server: HTTP %d", resp.StatusCode)
		}
		var t struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
			return "", err
		}
		cached, exp = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
		return cached, nil
	}
}
