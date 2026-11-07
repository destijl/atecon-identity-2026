package authmanager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const provider = "projects/p/locations/us-central1/authProviders/gmail-connector"

func newServer(t *testing.T, wantPath string, reply string, check func(map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer caller" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		if check != nil {
			check(body)
		}
		_, _ = w.Write([]byte(reply))
	}))
}

func client(url string) *Client {
	return &Client{Provider: provider, BaseURL: url, Token: func(context.Context) (string, error) { return "caller", nil }}
}

func TestRetrieve(t *testing.T) {
	tests := []struct {
		name        string
		reply       string
		wantToken   string
		wantConsent string
		wantErr     error
	}{
		{name: "success", reply: `{"success":{"token":"tok","expireTime":"2030-01-01T00:00:00Z"}}`, wantToken: "tok"},
		{name: "consent camel", reply: `{"uriConsentRequired":{"authorizationUri":"https://accounts.google.com/x","consentNonce":"n"}}`, wantConsent: "https://accounts.google.com/x"},
		{name: "consent snake", reply: `{"uri_consent_required":{"authorization_uri":"https://a/y","consent_nonce":"n"}}`, wantConsent: "https://a/y"},
		{name: "rejected", reply: `{"consentRejected":{}}`, wantErr: ErrConsentRejected},
		{name: "pending", reply: `{"pending":{}}`, wantErr: ErrPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(t, "/"+provider+"/credentials:retrieve", tt.reply, func(b map[string]any) {
				if b["userId"] != "alice-gmail-com" || b["continueUri"] != "https://app/cb" {
					t.Errorf("request body = %v", b)
				}
			})
			defer srv.Close()
			res, err := client(srv.URL).Retrieve(context.Background(), "alice-gmail-com", []string{"s"}, "https://app/cb")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if tt.wantToken != "" && (res.Token == nil || res.Token.Value != tt.wantToken) {
				t.Errorf("token = %+v", res.Token)
			}
			if tt.wantConsent != "" && (res.Consent == nil || res.Consent.AuthorizationURL != tt.wantConsent || res.Consent.Nonce != "n") {
				t.Errorf("consent = %+v", res.Consent)
			}
		})
	}
}

func TestRetrieveHTTPErrorHasNoSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":403,"message":"denied"}}`, http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := client(srv.URL).Retrieve(context.Background(), "u", nil, "")
	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "caller") {
		t.Fatalf("err = %v", err)
	}
}

func TestFinalize(t *testing.T) {
	srv := newServer(t, "/"+provider+"/credentials:finalize", `{}`, func(b map[string]any) {
		if b["userId"] != "u" || b["userIdValidationState"] != "st" || b["consentNonce"] != "n" {
			t.Errorf("finalize body = %v", b)
		}
	})
	defer srv.Close()
	if err := client(srv.URL).Finalize(context.Background(), "u", "st", "n"); err != nil {
		t.Fatal(err)
	}
}
