// Command callback-proxy is the email app's public HTTPS front door. It runs
// on Cloud Run, which gives it a Google-managed TLS certificate, and forwards
// only GET /oauth/continue to the email app's callback listener on an
// internal load balancer in the VPC (Cloud Run direct VPC egress). The email
// app itself stays on GKE with no public endpoint.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

// handler forwards GET /oauth/continue (with its query) to target and
// refuses everything else.
func handler(target *url.URL) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = "/oauth/continue"
			r.Out.URL.RawPath = ""
			r.Out.Header.Del("Cookie")
			r.Out.Header.Del("Authorization")
		},
		Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second},
	}
	mux := http.NewServeMux()
	mux.Handle("GET /oauth/continue", rp)
	// Not /healthz: Cloud Run reserves paths ending in z on run.app.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func main() {
	listen := flag.String("listen", ":"+envOr("PORT", "8080"), "listen address")
	targetFlag := flag.String("target", os.Getenv("TARGET_URL"), "email app callback base URL, e.g. http://10.128.0.50")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	target, err := url.Parse(*targetFlag)
	if err != nil || target.Scheme == "" || target.Host == "" {
		slog.Error("--target / TARGET_URL must be an absolute URL", "value", *targetFlag)
		os.Exit(2)
	}
	slog.Info("callback proxy serving", "listen", *listen, "target", target.String())
	srv := &http.Server{Addr: *listen, Handler: handler(target), ReadHeaderTimeout: 10 * time.Second}
	slog.Error("exited", "err", srv.ListenAndServe())
	os.Exit(1)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
