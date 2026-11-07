// Command authmanager-provider is a Substrate credential provider that hands
// the egress gateway a user's OAuth access token held by Google Cloud Agent
// Auth Manager. The user is the actor's atespace (see internal/identity).
package main

import (
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/destijl/atecon-identity-2026/internal/authmanager"
	"github.com/destijl/atecon-identity-2026/internal/provider"
	"github.com/destijl/atecon-identity-2026/internal/tlsutil"
	credprovider "github.com/destijl/atecon-identity-2026/third_party/credprovider"
)

func main() {
	listen := flag.String("listen-address", ":50051", "gRPC listen address")
	health := flag.String("health-address", ":9091", "HTTP /readyz address")
	name := flag.String("provider-name", "authmanager.demo", "host part of the ate-secret:// URIs served")
	scopes := flag.String("scopes", "gmail.readonly", "comma-separated short Google scope names served")
	authProvider := flag.String("auth-provider", "", "projects/<p>/locations/<l>/authProviders/<name> (required)")
	bundle := flag.String("server-cred-bundle", "/run/servicedns.podcert.ate.dev/credential-bundle.pem", "serving cert+key bundle")
	clientCA := flag.String("client-ca-file", "/run/podidentity.podcert.ate.dev/trust-bundle.pem", "CA for client certs")
	allowed := flag.String("allowed-clients", "spiffe://cluster.local/ns/ate-system/sa/atenet-egress", "comma-separated client SPIFFE IDs allowed to call")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if *authProvider == "" {
		slog.Error("--auth-provider is required")
		os.Exit(2)
	}

	sc := map[string]bool{}
	for _, s := range strings.Split(*scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			sc[s] = true
		}
	}
	srv := &provider.Server{
		Name:   *name,
		Scopes: sc,
		AuthManager: &authmanager.Client{
			Provider: *authProvider,
			Token:    authmanager.MetadataTokenSource(nil),
		},
	}

	creds := credentials.NewTLS(tlsutil.ServerConfig(*bundle, *clientCA, strings.Split(*allowed, ",")))
	gs := grpc.NewServer(grpc.Creds(creds))
	credprovider.RegisterCredentialProviderServer(gs, srv)

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
		slog.Error("health server exited", "err", http.ListenAndServe(*health, mux))
	}()

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
	slog.Info("authmanager credential provider serving", "addr", *listen, "provider", *name, "scopes", *scopes, "auth_provider", *authProvider)
	if err := gs.Serve(lis); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
