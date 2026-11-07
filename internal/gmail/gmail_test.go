package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchFiltersToPrefixAndSendsPlaceholder(t *testing.T) {
	subjects := map[string]string{
		"m1": "Agent Substrate Day: welcome",
		"m2": "Your bank PIN is 1234 (agent substrate day)", // loose Gmail match
		"m3": "agent substrate day: schedule",
		"m4": "Agent Substrate Day: talks",
		"m5": "Agent Substrate Day: never fetched",
	}
	var fetched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != Placeholder {
			t.Errorf("Authorization = %q, want placeholder", got)
		}
		switch {
		case r.URL.Path == "/messages":
			if got := r.URL.Query().Get("q"); got != `subject:"Agent Substrate Day:"` {
				t.Errorf("q = %q", got)
			}
			_, _ = w.Write([]byte(`{"messages":[{"id":"m1"},{"id":"m2"},{"id":"m3"},{"id":"m4"},{"id":"m5"}]}`))
		case strings.HasPrefix(r.URL.Path, "/messages/"):
			id := strings.TrimPrefix(r.URL.Path, "/messages/")
			fetched = append(fetched, id)
			_ = json.NewEncoder(w).Encode(map[string]any{"payload": map[string]any{"headers": []map[string]string{{"name": "Subject", "value": subjects[id]}}}})
		}
	}))
	defer srv.Close()
	res, err := (&Client{BaseURL: srv.URL}).Search(context.Background(), "Agent Substrate Day:", 3)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(res)
	if strings.Contains(string(out), "PIN") {
		t.Errorf("result leaks a non-matching subject: %s", out)
	}
	want := []string{"Agent Substrate Day: welcome", "agent substrate day: schedule", "Agent Substrate Day: talks"}
	if res.Status != 200 || res.From != "google" || strings.Join(res.Subjects, "|") != strings.Join(want, "|") {
		t.Errorf("result = %s", out)
	}
	if len(fetched) != 4 {
		t.Errorf("fetched %v, want to stop after 3 matches", fetched)
	}
}

func TestSearchNoMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"resultSizeEstimate":0}`))
	}))
	defer srv.Close()
	res, err := (&Client{BaseURL: srv.URL}).Search(context.Background(), "Agent Substrate Day:", 3)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || res.Subjects == nil || len(res.Subjects) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestEgressDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("egress denied"))
	}))
	defer srv.Close()
	res, err := (&Client{BaseURL: srv.URL}).Search(context.Background(), "Agent Substrate Day:", 3)
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
