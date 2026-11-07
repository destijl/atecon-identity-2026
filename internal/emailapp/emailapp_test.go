package emailapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/destijl/atecon-identity-2026/internal/authmanager"
	"github.com/destijl/atecon-identity-2026/internal/identity"
)

type fakeAM struct {
	res       *authmanager.Result
	err       error
	retrieves []string
	finalized []string
	finErr    error
}

func (f *fakeAM) Retrieve(_ context.Context, user string, scopes []string, cont string) (*authmanager.Result, error) {
	f.retrieves = append(f.retrieves, user+"|"+strings.Join(scopes, ",")+"|"+cont)
	return f.res, f.err
}

func (f *fakeAM) Finalize(_ context.Context, user, state, nonce string) error {
	f.finalized = append(f.finalized, user+"|"+state+"|"+nonce)
	return f.finErr
}

type fakeVerifier struct {
	actor identity.Actor
	err   error
}

func (f fakeVerifier) FromRequest(*http.Request) (identity.Actor, error) { return f.actor, f.err }

func newApp(am *fakeAM, v ActorVerifier) *App {
	users, _ := ParseUsers("alice@gmail.com")
	return &App{AuthManager: am, Verifier: v, Scope: "https://www.googleapis.com/auth/gmail.readonly", ContinueURL: "http://1.2.3.4/oauth/continue", Users: users}
}

var alice = identity.Actor{Atespace: "alice-gmail-com", Name: "email-agent"}

func consentRes() *authmanager.Result {
	return &authmanager.Result{Consent: &authmanager.Consent{AuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth?x", Nonce: "nonce1", UID: "uid1"}}
}

func TestConsent(t *testing.T) {
	tests := []struct {
		name       string
		am         *fakeAM
		v          fakeVerifier
		wantCode   int
		wantStatus string
	}{
		{name: "consent required", am: &fakeAM{res: consentRes()}, v: fakeVerifier{actor: alice}, wantCode: 200, wantStatus: "consent_required"},
		{name: "already granted", am: &fakeAM{res: &authmanager.Result{Token: &authmanager.Token{Value: "t"}}}, v: fakeVerifier{actor: alice}, wantCode: 200, wantStatus: "already_granted"},
		{name: "no actor jwt", am: &fakeAM{res: consentRes()}, v: fakeVerifier{err: errors.New("missing")}, wantCode: 401},
		{name: "unknown user", am: &fakeAM{res: consentRes()}, v: fakeVerifier{actor: identity.Actor{Atespace: "bob-gmail-com", Name: "x"}}, wantCode: 403},
		{name: "auth manager down", am: &fakeAM{err: errors.New("down")}, v: fakeVerifier{actor: alice}, wantCode: 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newApp(tt.am, tt.v)
			rec := httptest.NewRecorder()
			app.InternalHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/consent", nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body)
			}
			if tt.wantCode != 200 {
				return
			}
			var out ConsentResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if out.Status != tt.wantStatus || out.User != "alice@gmail.com" || out.Actor != "alice-gmail-com/email-agent" {
				t.Errorf("response = %+v", out)
			}
			if tt.wantStatus == "consent_required" && !strings.HasPrefix(out.AuthorizationURL, "https://accounts.google.com/") {
				t.Errorf("authorizationUrl = %q", out.AuthorizationURL)
			}
			if got := tt.am.retrieves[0]; got != "alice-gmail-com|https://www.googleapis.com/auth/gmail.readonly|http://1.2.3.4/oauth/continue" {
				t.Errorf("retrieve = %q", got)
			}
		})
	}
}

func TestContinueFinalizesOnce(t *testing.T) {
	am := &fakeAM{res: consentRes()}
	app := newApp(am, fakeVerifier{actor: alice})
	app.InternalHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/consent", nil))

	cb := "/oauth/continue?user_id_validation_state=STATE&uuid=uid1&auth_provider_name=x"
	rec := httptest.NewRecorder()
	app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("GET", cb, nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Access granted") || !strings.Contains(rec.Body.String(), "alice@gmail.com") {
		t.Fatalf("continue = %d %s", rec.Code, rec.Body)
	}
	if len(am.finalized) != 1 || am.finalized[0] != "alice-gmail-com|STATE|nonce1" {
		t.Errorf("finalize = %q", am.finalized)
	}
	// Replay is refused.
	rec = httptest.NewRecorder()
	app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("GET", cb, nil))
	if rec.Code != 400 || len(am.finalized) != 1 {
		t.Errorf("replay: code %d, finalize calls %d", rec.Code, len(am.finalized))
	}
}

func TestContinueRejects(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		query string
		age   time.Duration
		finE  error
		want  int
	}{
		{name: "unknown uid", query: "user_id_validation_state=S&uuid=other", want: 400},
		{name: "missing state", query: "uuid=uid1", want: 400},
		{name: "expired", query: "user_id_validation_state=S&uuid=uid1", age: time.Hour, want: 400},
		{name: "finalize fails", query: "user_id_validation_state=S&uuid=uid1", finE: errors.New("bad state"), want: 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			am := &fakeAM{res: consentRes(), finErr: tt.finE}
			app := newApp(am, fakeVerifier{actor: alice})
			app.Now = func() time.Time { return now }
			app.InternalHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/consent", nil))
			app.Now = func() time.Time { return now.Add(tt.age) }
			rec := httptest.NewRecorder()
			app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/continue?"+tt.query, nil))
			if rec.Code != tt.want {
				t.Errorf("code = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// Auth Manager may not report a uid with the consent; the newest link for the
// only pending user still finalizes, and an older link is superseded.
func TestContinueWithoutUID(t *testing.T) {
	am := &fakeAM{res: &authmanager.Result{Consent: &authmanager.Consent{AuthorizationURL: "https://accounts.google.com/x", Nonce: "n1"}}}
	app := newApp(am, fakeVerifier{actor: alice})
	app.InternalHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/consent", nil))
	am.res = &authmanager.Result{Consent: &authmanager.Consent{AuthorizationURL: "https://accounts.google.com/x", Nonce: "n2"}}
	app.InternalHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/consent", nil))
	rec := httptest.NewRecorder()
	app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/continue?user_id_validation_state=S&uuid=server-uuid", nil))
	if rec.Code != 200 || len(am.finalized) != 1 || am.finalized[0] != "alice-gmail-com|S|n2" {
		t.Errorf("code %d, finalize %q", rec.Code, am.finalized)
	}
}

func TestSurfacesAreSeparate(t *testing.T) {
	app := newApp(&fakeAM{res: consentRes()}, fakeVerifier{actor: alice})
	rec := httptest.NewRecorder()
	app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/consent", nil))
	if rec.Code != 404 && rec.Code != 405 {
		t.Errorf("public /consent = %d, want 404/405", rec.Code)
	}
	rec = httptest.NewRecorder()
	app.InternalHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/continue?uuid=x&user_id_validation_state=y", nil))
	if rec.Code != 404 && rec.Code != 405 {
		t.Errorf("internal /oauth/continue = %d, want 404/405", rec.Code)
	}
}

func TestParseUsers(t *testing.T) {
	m, err := ParseUsers("alice@gmail.com=real.person@gmail.com", "bob@gmail.com")
	if err != nil || m["alice-gmail-com"] != (User{"alice@gmail.com", "real.person@gmail.com"}) || m["bob-gmail-com"] != (User{Email: "bob@gmail.com"}) {
		t.Errorf("ParseUsers = %v, %v", m, err)
	}
	if _, err := ParseUsers("a.b@gmail.com", "a-b@gmail.com"); err == nil {
		t.Error("colliding emails accepted")
	}
	if _, err := ParseUsers(); err == nil {
		t.Error("empty accepted")
	}
}

func TestLoginHint(t *testing.T) {
	users, _ := ParseUsers("alice@gmail.com=real.person@gmail.com")
	app := newApp(&fakeAM{res: consentRes()}, fakeVerifier{actor: alice})
	app.Users = users
	rec := httptest.NewRecorder()
	app.InternalHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/consent", nil))
	var out ConsentResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !strings.Contains(out.AuthorizationURL, "login_hint=real.person%40gmail.com") || out.User != "alice@gmail.com" {
		t.Errorf("response = %+v", out)
	}
}
