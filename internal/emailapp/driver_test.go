package emailapp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDriverForwards(t *testing.T) {
	var gotPath, gotActor, gotMethod string
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotActor, gotMethod = r.URL.Path, r.Header.Get("ate-target-actor"), r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer router.Close()
	d := &Driver{RouterURL: router.URL, Actor: "u-alice/mail-agent", Client: router.Client()}

	tests := []struct {
		method, path string
		want         int
		wantFwd      string
	}{
		{method: "GET", path: "/agent/search", want: 200, wantFwd: "/search"},
		{method: "POST", path: "/agent/trash", want: 200, wantFwd: "/trash"},
		{method: "GET", path: "/agent/trash", want: 405},
		{method: "GET", path: "/agent/readyz", want: 404},
		{method: "GET", path: "/agent/../admin", want: 307}, // ServeMux cleans the path; never forwarded
	}
	for _, tt := range tests {
		gotPath, gotActor, gotMethod = "", "", ""
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
		if rec.Code != tt.want {
			b, _ := io.ReadAll(rec.Body)
			t.Errorf("%s %s = %d, want %d (%s)", tt.method, tt.path, rec.Code, tt.want, b)
			continue
		}
		if tt.wantFwd != "" && (gotPath != tt.wantFwd || gotActor != "u-alice/mail-agent" || gotMethod != tt.method) {
			t.Errorf("%s %s forwarded as %s %s actor=%q", tt.method, tt.path, gotMethod, gotPath, gotActor)
		}
		if tt.wantFwd == "" && gotPath != "" {
			t.Errorf("%s %s was forwarded", tt.method, tt.path)
		}
	}
}
