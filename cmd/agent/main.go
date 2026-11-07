// Command agent is the email agent that runs inside a Substrate actor. It
// holds no credentials. It exposes a small HTTP API the demo drives:
//
//	GET  /audit    look for ambient credentials in this sandbox
//	GET  /list     list 5 recent messages (subjects redacted)
//	POST /trash    try to trash a message (a nonexistent one)
//	GET  /consent  ask the email app for a consent link for this agent's user
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/destijl/atecon-identity-2026/internal/audit"
	"github.com/destijl/atecon-identity-2026/internal/gmail"
)

func main() {
	addr := flag.String("address", ":80", "listen address")
	gmailURL := flag.String("gmail-url", gmail.DefaultBaseURL, "Gmail API base")
	appURL := flag.String("email-app-url", "http://email-app.email-app.svc.cluster.local:8080", "email app internal URL")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	hc := &http.Client{Timeout: 20 * time.Second}
	g := &gmail.Client{BaseURL: *gmailURL, HTTP: hc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /audit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, audit.New().Run(r.Context()))
	})
	mux.HandleFunc("GET /list", func(w http.ResponseWriter, r *http.Request) {
		res, err := g.List(r.Context(), 5)
		reply(w, res, err)
	})
	mux.HandleFunc("POST /trash", func(w http.ResponseWriter, r *http.Request) {
		res, err := g.Trash(r.Context(), "demo-nonexistent-message")
		reply(w, res, err)
	})
	mux.HandleFunc("GET /consent", func(w http.ResponseWriter, r *http.Request) {
		consent(w, r.Context(), hc, *appURL)
	})
	slog.Info("email agent listening", "addr", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	slog.Error("exited", "err", srv.ListenAndServe())
	os.Exit(1)
}

// consent asks the email app for a consent link. The agent sends a
// placeholder Authorization header; the egress gateway replaces it with an
// actor JWT naming this actor, which is how the app knows who is asking.
func consent(w http.ResponseWriter, ctx context.Context, hc *http.Client, appURL string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, appURL+"/consent", nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	req.Header.Set("Authorization", "Bearer substrate-will-replace-this")
	resp, err := hc.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func reply(w http.ResponseWriter, res *gmail.Result, err error) {
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
