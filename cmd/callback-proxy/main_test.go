package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestHandler(t *testing.T) {
	var gotPath, gotQuery, gotCookie string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotCookie = r.URL.Path, r.URL.RawQuery, r.Header.Get("Cookie")
		_, _ = w.Write([]byte("granted"))
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	h := handler(u)

	tests := []struct {
		method, path string
		want         int
		forwarded    bool
	}{
		{method: "GET", path: "/oauth/continue?user_id_validation_state=S&uuid=U", want: 200, forwarded: true},
		{method: "POST", path: "/oauth/continue", want: 405},
		{method: "GET", path: "/consent", want: 404},
		{method: "GET", path: "/agent/search", want: 404},
		{method: "GET", path: "/readyz", want: 200},
	}
	for _, tt := range tests {
		gotPath, gotQuery, gotCookie = "", "", ""
		req := httptest.NewRequest(tt.method, tt.path, nil)
		req.Header.Set("Cookie", "secret=1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.want)
		}
		if tt.forwarded != (gotPath != "") {
			t.Errorf("%s %s forwarded = %v", tt.method, tt.path, gotPath != "")
		}
		if tt.forwarded && (gotPath != "/oauth/continue" || gotQuery != "user_id_validation_state=S&uuid=U" || gotCookie != "") {
			t.Errorf("forwarded as %s?%s cookie=%q", gotPath, gotQuery, gotCookie)
		}
	}
}
