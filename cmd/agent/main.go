// Command agent is the email agent that runs inside a Substrate actor. It
// holds no credentials: the egress gateway adds the user's OAuth token to its
// Gmail requests. It exposes a small HTTP API the demo drives through the
// email app and the Substrate ingress router:
//
//	GET  /search   subjects of up to 3 messages matching the demo subject prefix
//	POST /trash    try to trash a message (a nonexistent one)
package main

import (
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/destijl/atecon-identity-2026/internal/gmail"
)

func main() {
	addr := flag.String("address", ":80", "listen address")
	gmailURL := flag.String("gmail-url", gmail.DefaultBaseURL, "Gmail API base")
	subjectPrefix := flag.String("subject-prefix", "Agent Substrate Day:", "only subjects containing this are searched for and shown")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	hc := &http.Client{Timeout: 20 * time.Second}
	g := &gmail.Client{BaseURL: *gmailURL, HTTP: hc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		res, err := g.Search(r.Context(), *subjectPrefix, 3)
		reply(w, res, err)
	})
	mux.HandleFunc("POST /trash", func(w http.ResponseWriter, r *http.Request) {
		res, err := g.Trash(r.Context(), "demo-nonexistent-message")
		reply(w, res, err)
	})
	slog.Info("email agent listening", "addr", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	slog.Error("exited", "err", srv.ListenAndServe())
	os.Exit(1)
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
