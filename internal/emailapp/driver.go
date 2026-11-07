package emailapp

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/destijl/atecon-identity-2026/internal/tlsutil"
)

// AgentOps are the agent endpoints the driver forwards, with their methods.
var AgentOps = map[string]string{"audit": http.MethodGet, "list": http.MethodGet, "consent": http.MethodGet, "trash": http.MethodPost}

// Driver lets the operator drive the user's agent through the app. The app
// reaches its agent the way any Substrate client does: through the ingress
// router, over mTLS with the app's own pod identity, naming the actor in the
// ate-target-actor header.
type Driver struct {
	// RouterURL is https://atenet-router.ate-system.svc:443.
	RouterURL string
	// Actor is <atespace>/<name> of the agent to drive.
	Actor  string
	Client *http.Client
}

// Handler serves GET|POST /agent/{op}.
func (d *Driver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/{op}", func(w http.ResponseWriter, r *http.Request) {
		op := r.PathValue("op")
		method, ok := AgentOps[op]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), method, d.RouterURL+"/"+op, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("ate-target-actor", d.Actor)
		resp, err := d.Client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf("calling agent through router: %v", err), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for _, h := range []string{"Content-Type"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

// RouterClient is an mTLS client for atenet-router: it presents the pod
// identity bundle (re-read each handshake) and verifies the router's
// servicedns certificate.
func RouterClient(bundlePath, caPath, serverName string) (*http.Client, error) {
	pool, err := tlsutil.LoadPool(caPath)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		ServerName: serverName,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return tlsutil.LoadBundle(bundlePath)
		},
	}
	return &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}}, nil
}

// SortedOps lists the driver operations, for usage text.
func SortedOps() []string {
	ops := make([]string, 0, len(AgentOps))
	for k := range AgentOps {
		ops = append(ops, k)
	}
	slices.Sort(ops)
	return ops
}
