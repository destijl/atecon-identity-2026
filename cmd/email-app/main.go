// Command email-app is the demo's application: it runs on plain Kubernetes,
// outside Substrate, hands actors consent links for their user, and
// finalizes the grant when the user's browser returns from Google.
package main

import (
	"crypto/tls"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/destijl/atecon-identity-2026/internal/actorjwt"
	"github.com/destijl/atecon-identity-2026/internal/authmanager"
	"github.com/destijl/atecon-identity-2026/internal/emailapp"
	"github.com/destijl/atecon-identity-2026/internal/tlsutil"
)

func main() {
	internal := flag.String("internal-address", ":8080", "actor-facing listener (/consent)")
	public := flag.String("public-address", ":8090", "browser-facing listener (/oauth/continue)")
	authProvider := flag.String("auth-provider", "", "projects/<p>/locations/<l>/authProviders/<name> (required)")
	scope := flag.String("scope", "https://www.googleapis.com/auth/gmail.readonly", "OAuth scope requested")
	continueURL := flag.String("continue-url", "", "public URL of /oauth/continue (required)")
	users := flag.String("users", "", "comma-separated users as email[=google-account] (required)")
	issuer := flag.String("actor-jwt-issuer", "https://idp.ate-system.svc", "actor JWT issuer")
	audience := flag.String("actor-jwt-audience", "email-app", "audience the egress gateway puts in actor JWTs for this app")
	jwksURL := flag.String("jwks-url", "https://idp.ate-system.svc/openid/v1/jwks", "actor JWT JWKS")
	jwksCA := flag.String("jwks-ca-file", "/run/servicedns-ca/trust-bundle.pem", "CA for the JWKS server cert")
	driver := flag.String("driver-address", ":8070", "operator listener (/agent/{op}); reach it with kubectl port-forward only")
	routerURL := flag.String("router-url", "https://atenet-router.ate-system.svc:443", "Substrate ingress router")
	routerName := flag.String("router-server-name", "atenet-router.ate-system.svc", "router TLS server name")
	identityBundle := flag.String("identity-bundle", "/run/podidentity.podcert.ate.dev/credential-bundle.pem", "this pod's identity cert+key, presented to the router")
	driveActor := flag.String("drive-actor", "", "<atespace>/<actor> the operator listener drives")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if *authProvider == "" || *continueURL == "" || *users == "" {
		slog.Error("--auth-provider, --continue-url and --users are required")
		os.Exit(2)
	}
	userMap, err := emailapp.ParseUsers(strings.Split(*users, ",")...)
	if err != nil {
		slog.Error("bad --users", "err", err)
		os.Exit(2)
	}
	pool, err := tlsutil.LoadPool(*jwksCA)
	if err != nil {
		slog.Error("loading JWKS CA", "err", err)
		os.Exit(1)
	}
	jwksClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}

	app := &emailapp.App{
		AuthManager: &authmanager.Client{Provider: *authProvider, Token: authmanager.MetadataTokenSource(nil)},
		Verifier: &actorjwt.Verifier{
			Issuer:   *issuer,
			Audience: *audience,
			Keys:     &actorjwt.HTTPKeys{URL: *jwksURL, Client: jwksClient},
		},
		Scope:       *scope,
		ContinueURL: *continueURL,
		Users:       userMap,
	}
	if *driveActor != "" {
		rc, err := emailapp.RouterClient(*identityBundle, *jwksCA, *routerName)
		if err != nil {
			slog.Error("router client", "err", err)
			os.Exit(1)
		}
		d := &emailapp.Driver{RouterURL: *routerURL, Actor: *driveActor, Client: rc}
		go func() {
			srv := &http.Server{Addr: *driver, Handler: d.Handler(), ReadHeaderTimeout: 10 * time.Second}
			slog.Error("driver listener exited", "err", srv.ListenAndServe())
			os.Exit(1)
		}()
	}
	go func() {
		srv := &http.Server{Addr: *public, Handler: app.PublicHandler(), ReadHeaderTimeout: 10 * time.Second}
		slog.Error("public listener exited", "err", srv.ListenAndServe())
		os.Exit(1)
	}()
	slog.Info("email-app serving", "internal", *internal, "public", *public, "continue_url", *continueURL, "users", len(userMap))
	srv := &http.Server{Addr: *internal, Handler: app.InternalHandler(), ReadHeaderTimeout: 10 * time.Second}
	slog.Error("internal listener exited", "err", srv.ListenAndServe())
	os.Exit(1)
}
