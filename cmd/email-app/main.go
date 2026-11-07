// Command email-app is the demo's application: it runs on plain Kubernetes,
// outside Substrate. A user signs up, consents to gmail.readonly, and the app
// then creates the user's atespace, agent actor and egress policy.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/destijl/atecon-identity-2026/internal/authmanager"
	"github.com/destijl/atecon-identity-2026/internal/emailapp"
	"github.com/destijl/atecon-identity-2026/internal/substrate"
	ateapi "github.com/destijl/atecon-identity-2026/third_party/ateapi"
)

func main() {
	public := flag.String("public-address", ":8090", "browser-facing listener (/oauth/continue)")
	operator := flag.String("operator-address", ":8070", "operator listener (/signup, /agent/{op}); reach it with kubectl port-forward only")
	authProvider := flag.String("auth-provider", "", "projects/<p>/locations/<l>/authProviders/<name> (required)")
	scope := flag.String("scope", "https://www.googleapis.com/auth/gmail.readonly", "OAuth scope requested")
	continueURL := flag.String("continue-url", "", "public URL of /oauth/continue (required)")
	users := flag.String("users", "", "comma-separated users as email[=google-account] (required)")
	serviceDNSCA := flag.String("servicedns-ca-file", "/run/servicedns-ca/trust-bundle.pem", "CA for Substrate service certificates (ate-api, router)")
	ateAPI := flag.String("ate-api", "api.ate-system.svc:443", "Substrate API address")
	ateAPIName := flag.String("ate-api-server-name", "api.ate-system.svc", "Substrate API TLS server name")
	ateAPIToken := flag.String("ate-api-token-file", "/run/ate-api-token/token", "projected ServiceAccount token for the Substrate API")
	templatePath := flag.String("agent-template", "/etc/email-app/actor-template.json", "the agent's actor template (protojson)")
	policyPath := flag.String("agent-egress-policy", "/etc/email-app/egress-policy.json", "the agent's egress policy (protojson)")
	agentName := flag.String("agent-name", "mail-agent", "actor name of each user's agent")
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
	tmpl, err := substrate.LoadTemplate(*templatePath)
	if err != nil {
		slog.Error("loading actor template", "err", err)
		os.Exit(1)
	}
	policy, err := substrate.LoadPolicy(*policyPath)
	if err != nil {
		slog.Error("loading egress policy", "err", err)
		os.Exit(1)
	}
	conn, err := substrate.Dial(*ateAPI, *ateAPIName, *serviceDNSCA, *ateAPIToken)
	if err != nil {
		slog.Error("Substrate API client", "err", err)
		os.Exit(1)
	}

	app := &emailapp.App{
		AuthManager: &authmanager.Client{Provider: *authProvider, Token: authmanager.MetadataTokenSource(nil)},
		Provisioner: &substrate.Provisioner{
			Client:    ateapi.NewControlClient(conn),
			Template:  tmpl,
			Policy:    policy,
			ActorName: *agentName,
		},
		Scope:       *scope,
		ContinueURL: *continueURL,
		Users:       userMap,
	}

	opMux := http.NewServeMux()
	opMux.HandleFunc("POST /signup", app.HandleSignup)
	opMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if *driveActor != "" {
		rc, err := emailapp.RouterClient(*identityBundle, *serviceDNSCA, *routerName)
		if err != nil {
			slog.Error("router client", "err", err)
			os.Exit(1)
		}
		d := &emailapp.Driver{RouterURL: *routerURL, Actor: *driveActor, Client: rc}
		opMux.Handle("/agent/", d.Handler())
	}
	go func() {
		srv := &http.Server{Addr: *operator, Handler: opMux, ReadHeaderTimeout: 10 * time.Second}
		slog.Error("operator listener exited", "err", srv.ListenAndServe())
		os.Exit(1)
	}()
	slog.Info("email app listening", "public", *public, "operator", *operator)
	srv := &http.Server{Addr: *public, Handler: app.PublicHandler(), ReadHeaderTimeout: 10 * time.Second}
	slog.Error("public listener exited", "err", srv.ListenAndServe())
	os.Exit(1)
}
