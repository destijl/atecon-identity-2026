package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListRedactsAndSendsPlaceholder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != Placeholder {
			t.Errorf("Authorization = %q, want placeholder", got)
		}
		switch {
		case r.URL.Path == "/messages":
			if r.URL.Query().Get("maxResults") != "5" {
				t.Errorf("maxResults = %q", r.URL.Query().Get("maxResults"))
			}
			_, _ = w.Write([]byte(`{"messages":[{"id":"18c0ffee1234"},{"id":"18d"}]}`))
		case strings.HasPrefix(r.URL.Path, "/messages/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"payload": map[string]any{"headers": []map[string]string{{"name": "Subject", "value": "Your bank PIN is 1234"}}}})
		}
	}))
	defer srv.Close()
	res, err := (&Client{BaseURL: srv.URL}).List(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(res)
	if strings.Contains(string(out), "PIN") || strings.Contains(string(out), "c0ffee") {
		t.Errorf("result leaks content: %s", out)
	}
	if res.Status != 200 || len(res.Messages) != 2 || res.Messages[0].Subject != "[redacted, 21 chars]" || res.Messages[0].ID != "18c0…" {
		t.Errorf("result = %s", out)
	}
	if res.From != "google" {
		t.Errorf("answeredBy = %q", res.From)
	}
}

func TestEgressDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("egress denied"))
	}))
	defer srv.Close()
	res, err := (&Client{BaseURL: srv.URL}).List(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 403 || res.From != "substrate-egress-gateway" || res.Error != "egress denied" {
		t.Errorf("result = %+v", res)
	}
}

func TestTrashScopeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/messages/demo-nonexistent/trash" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes.","details":[{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}]}}`))
	}))
	defer srv.Close()
	res, err := (&Client{BaseURL: srv.URL}).Trash(context.Background(), "demo-nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 403 || res.From != "google" || res.Reason != "ACCESS_TOKEN_SCOPE_INSUFFICIENT" || !strings.HasPrefix(res.Error, "PERMISSION_DENIED") {
		t.Errorf("result = %+v", res)
	}
}
