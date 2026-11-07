// Package emailapp is the "application" in the demo: it runs on plain
// Kubernetes, outside Substrate, and owns the user relationship.
//
// Sign-up happens before any agent exists:
//
//   - POST /signup (operator listener): a user signs up. The app asks Auth
//     Manager for a consent link for the user's atespace and gmail.readonly.
//   - GET /oauth/continue (public, behind the Cloud Run front door): the
//     user's browser lands here after Google consent. The app finalizes the
//     grant with Auth Manager, then creates the user's atespace, agent actor
//     and egress policy through the Substrate API.
package emailapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/destijl/atecon-identity-2026/internal/authmanager"
	"github.com/destijl/atecon-identity-2026/internal/identity"
)

// AuthManager is the subset of the Auth Manager client the app needs.
type AuthManager interface {
	Retrieve(ctx context.Context, userID string, scopes []string, continueURI string) (*authmanager.Result, error)
	Finalize(ctx context.Context, userID, validationState, nonce string) error
}

// Provisioner creates a user's agent once the user has consented.
type Provisioner interface {
	Provision(ctx context.Context, atespace string) error
}

// App is the email app.
type App struct {
	AuthManager AuthManager
	Provisioner Provisioner
	// Scope is the full OAuth scope requested.
	Scope string
	// ContinueURL is the public /oauth/continue URL.
	ContinueURL string
	// Users maps atespace to the user.
	Users map[string]User
	// PendingTTL bounds how long an unfinished consent is remembered.
	PendingTTL time.Duration
	Now        func() time.Time

	mu sync.Mutex
	// pending holds the latest unfinished consent per user (atespace). Only
	// the newest link works: each retrieve starts a new attempt with a new
	// nonce, and Auth Manager checks it at finalize.
	pending map[string]pendingConsent
}

type pendingConsent struct {
	uid     string
	nonce   string
	created time.Time
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *App) ttl() time.Duration {
	if a.PendingTTL == 0 {
		return 30 * time.Minute
	}
	return a.PendingTTL
}

func (a *App) remember(user string, p pendingConsent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		a.pending = map[string]pendingConsent{}
	}
	a.pending[user] = p
}

// take returns and forgets the pending consent the callback is for. Auth
// Manager echoes the attempt's uid; if retrieve didn't report one, the app
// falls back to the only pending consent. Finalize still checks the nonce
// against the encrypted validation state, so a wrong match just fails.
func (a *App) take(uid string) (string, pendingConsent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var user string
	for u, p := range a.pending {
		if a.now().Sub(p.created) > a.ttl() {
			delete(a.pending, u)
			continue
		}
		if p.uid != "" && p.uid == uid {
			user = u
		}
	}
	if user == "" && len(a.pending) == 1 {
		for u, p := range a.pending {
			if p.uid == "" {
				user = u
			}
		}
	}
	if user == "" {
		return "", pendingConsent{}, false
	}
	p := a.pending[user]
	delete(a.pending, user)
	return user, p, true
}

// User is one app user.
type User struct {
	// Email is the user's name in the app: the atespace is derived from it,
	// and it is what screens and logs show.
	Email string
	// GoogleAccount, if set, is sent to Google as login_hint so the consent
	// screen preselects that account. The account that consents is the
	// mailbox the agent reads; Auth Manager keys the grant by atespace.
	GoogleAccount string
}

// HandleSignup serves POST /signup?email=<user>. It is mounted on the
// operator listener, standing in for the app's own sign-up page.
func (a *App) HandleSignup(w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")
	user, err := identity.AtespaceForEmail(email)
	u, ok := a.Users[user]
	if err != nil || !ok {
		http.Error(w, "unknown user", http.StatusNotFound)
		return
	}
	res, err := a.AuthManager.Retrieve(r.Context(), user, []string{a.Scope}, a.ContinueURL)
	if err != nil {
		slog.ErrorContext(r.Context(), "signup: auth manager", "user", u.Email, "err", err)
		http.Error(w, "auth manager unavailable", http.StatusBadGateway)
		return
	}
	out := SignupResponse{User: u.Email, Atespace: user}
	if res.Consent == nil {
		// Already granted: go straight to creating the agent.
		out.Status = "already_granted"
		if err := a.Provisioner.Provision(r.Context(), user); err != nil {
			slog.ErrorContext(r.Context(), "signup: creating agent", "user", u.Email, "err", err)
			http.Error(w, "could not create the agent", http.StatusBadGateway)
			return
		}
	} else {
		out.Status = "consent_required"
		out.AuthorizationURL = authURL(res.Consent.AuthorizationURL, u.GoogleAccount)
		a.remember(user, pendingConsent{uid: res.Consent.UID, nonce: res.Consent.Nonce, created: a.now()})
	}
	slog.InfoContext(r.Context(), "signup", "user", u.Email, "atespace", user, "status", out.Status)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// PublicHandler serves the browser-facing callback.
func (a *App) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/continue", a.handleContinue)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

// SignupResponse is what /signup returns.
type SignupResponse struct {
	User             string `json:"user"`
	Atespace         string `json:"atespace"`
	Status           string `json:"status"` // consent_required | already_granted
	AuthorizationURL string `json:"authorizationUrl,omitempty"`
}

var donePage = template.Must(template.New("done").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Email app</title>
<style>body{font-family:system-ui,sans-serif;max-width:40em;margin:4em auto;color:#202124}
.ok{color:#188038}.err{color:#c5221f}code{background:#f1f3f4;padding:.1em .3em}</style></head>
<body>{{if .OK}}<h1 class="ok">✓ Access granted</h1>
<p>Your email agent is ready and can <b>read</b> mail for <code>{{.Email}}</code>.</p>
<p>The agent never sees the token: Substrate's egress gateway adds it to the agent's requests to Gmail.</p>
<p>You can close this tab.</p>
{{else}}<h1 class="err">Consent not completed</h1><p>{{.Msg}}</p>{{end}}
</body></html>`))

func (a *App) handleContinue(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state, uid := q.Get("user_id_validation_state"), q.Get("uuid")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = donePage.Execute(w, map[string]any{"OK": false, "Msg": msg})
	}
	if state == "" || uid == "" {
		fail(http.StatusBadRequest, "Missing consent parameters.")
		return
	}
	user, p, ok := a.take(uid)
	if !ok {
		fail(http.StatusBadRequest, "This consent link has expired or was already used. Sign up again.")
		return
	}
	if err := a.AuthManager.Finalize(r.Context(), user, state, p.nonce); err != nil {
		slog.ErrorContext(r.Context(), "finalize failed", "user", a.Users[user].Email, "err", err)
		fail(http.StatusBadGateway, "Google Cloud Auth Manager could not complete the grant.")
		return
	}
	slog.InfoContext(r.Context(), "consent finalized", "user", a.Users[user].Email)
	if err := a.Provisioner.Provision(r.Context(), user); err != nil {
		slog.ErrorContext(r.Context(), "creating agent failed", "user", a.Users[user].Email, "err", err)
		fail(http.StatusBadGateway, "Access was granted, but the app could not create your agent.")
		return
	}
	_ = donePage.Execute(w, map[string]any{"OK": true, "Email": a.Users[user].Email})
}

// authURL adjusts a Google authorization URL for the demo. prompt=consent
// makes Google show the consent screen even if the account granted this
// client before (revoking the Auth Manager grant doesn't revoke Google's).
// login_hint, if set, preselects the account.
func authURL(raw, account string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("prompt", "consent")
	if account != "" {
		q.Set("login_hint", account)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ParseUsers builds the atespace → user map from entries of the form
// email or email=google-account.
func ParseUsers(entries ...string) (map[string]User, error) {
	m := map[string]User{}
	for _, e := range entries {
		email, account, _ := strings.Cut(strings.TrimSpace(e), "=")
		ns, err := identity.AtespaceForEmail(email)
		if err != nil {
			return nil, err
		}
		if prev, dup := m[ns]; dup {
			return nil, fmt.Errorf("emails %q and %q map to the same atespace %q", prev.Email, email, ns)
		}
		m[ns] = User{Email: email, GoogleAccount: account}
	}
	if len(m) == 0 {
		return nil, errors.New("no users configured")
	}
	return m, nil
}
