package emailapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/destijl/atecon-identity-2026/internal/authmanager"
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

type fakeProvisioner struct {
	calls []string
	err   error
}

func (f *fakeProvisioner) Provision(_ context.Context, atespace string) error {
	f.calls = append(f.calls, atespace)
	return f.err
}

func newApp(am *fakeAM) (*App, *fakeProvisioner) {
	users, _ := ParseUsers("alice@gmail.com")
	p := &fakeProvisioner{}
	return &App{AuthManager: am, Provisioner: p, Scope: "https://www.googleapis.com/auth/gmail.readonly", ContinueURL: "http://1.2.3.4/oauth/continue", Users: users}, p
}

func signup(app *App, email string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	app.HandleSignup(rec, httptest.NewRequest("POST", "/signup?email="+email, nil))
	return rec
}

func consentRes() *authmanager.Result {
	return &authmanager.Result{Consent: &authmanager.Consent{AuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth?x", Nonce: "nonce1", UID: "uid1"}}
}

func TestSignup(t *testing.T) {
	tests := []struct {
		name          string
		email         string
		am            *fakeAM
		wantCode      int
		wantStatus    string
		wantProvision bool
	}{
		{name: "consent required", email: "alice@gmail.com", am: &fakeAM{res: consentRes()}, wantCode: 200, wantStatus: "consent_required"},
		{name: "already granted", email: "alice@gmail.com", am: &fakeAM{res: &authmanager.Result{Token: &authmanager.Token{Value: "t"}}}, wantCode: 200, wantStatus: "already_granted", wantProvision: true},
		{name: "unknown user", email: "bob@gmail.com", am: &fakeAM{res: consentRes()}, wantCode: 404},
		{name: "not an email", email: "u-alice", am: &fakeAM{res: consentRes()}, wantCode: 404},
		{name: "auth manager down", email: "alice@gmail.com", am: &fakeAM{err: errors.New("down")}, wantCode: 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, prov := newApp(tt.am)
			rec := signup(app, tt.email)
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body)
			}
			if (len(prov.calls) > 0) != tt.wantProvision {
				t.Errorf("provision calls = %q, want provision %v", prov.calls, tt.wantProvision)
			}
			if tt.wantCode != 200 {
				return
			}
			var out SignupResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if out.Status != tt.wantStatus || out.User != "alice@gmail.com" || out.Atespace != "u-alice" {
				t.Errorf("response = %+v", out)
			}
			if tt.wantStatus == "consent_required" && !strings.HasPrefix(out.AuthorizationURL, "https://accounts.google.com/") {
				t.Errorf("authorizationUrl = %q", out.AuthorizationURL)
			}
			if got := tt.am.retrieves[0]; got != "u-alice|https://www.googleapis.com/auth/gmail.readonly|http://1.2.3.4/oauth/continue" {
				t.Errorf("retrieve = %q", got)
			}
		})
	}
}

func TestContinueFinalizesOnce(t *testing.T) {
	am := &fakeAM{res: consentRes()}
	app, prov := newApp(am)
	signup(app, "alice@gmail.com")
	if len(prov.calls) != 0 {
		t.Fatalf("agent created before consent: %q", prov.calls)
	}

	cb := "/oauth/continue?user_id_validation_state=STATE&uuid=uid1&auth_provider_name=x"
	rec := httptest.NewRecorder()
	app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("GET", cb, nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Access granted") || !strings.Contains(rec.Body.String(), "Your email agent is ready") {
		t.Fatalf("continue = %d %s", rec.Code, rec.Body)
	}
	if len(am.finalized) != 1 || am.finalized[0] != "u-alice|STATE|nonce1" {
		t.Errorf("finalize = %q", am.finalized)
	}
	if len(prov.calls) != 1 || prov.calls[0] != "u-alice" {
		t.Errorf("provision = %q, want [u-alice]", prov.calls)
	}
	// Replay is refused.
	rec = httptest.NewRecorder()
	app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("GET", cb, nil))
	if rec.Code != 400 || len(am.finalized) != 1 || len(prov.calls) != 1 {
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
			app, prov := newApp(am)
			app.Now = func() time.Time { return now }
			signup(app, "alice@gmail.com")
			app.Now = func() time.Time { return now.Add(tt.age) }
			rec := httptest.NewRecorder()
			app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/continue?"+tt.query, nil))
			if rec.Code != tt.want {
				t.Errorf("code = %d, want %d", rec.Code, tt.want)
			}
			if len(prov.calls) != 0 {
				t.Errorf("agent created on a failed continue: %q", prov.calls)
			}
		})
	}
}

// Auth Manager may not report a uid with the consent; the newest link for the
// only pending user still finalizes, and an older link is superseded.
func TestContinueWithoutUID(t *testing.T) {
	am := &fakeAM{res: &authmanager.Result{Consent: &authmanager.Consent{AuthorizationURL: "https://accounts.google.com/x", Nonce: "n1"}}}
	app, _ := newApp(am)
	signup(app, "alice@gmail.com")
	am.res = &authmanager.Result{Consent: &authmanager.Consent{AuthorizationURL: "https://accounts.google.com/x", Nonce: "n2"}}
	signup(app, "alice@gmail.com")
	rec := httptest.NewRecorder()
	app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/continue?user_id_validation_state=S&uuid=server-uuid", nil))
	if rec.Code != 200 || len(am.finalized) != 1 || am.finalized[0] != "u-alice|S|n2" {
		t.Errorf("code %d, finalize %q", rec.Code, am.finalized)
	}
}

func TestContinueProvisionFails(t *testing.T) {
	am := &fakeAM{res: consentRes()}
	app, prov := newApp(am)
	prov.err = errors.New("ate-api down")
	signup(app, "alice@gmail.com")
	rec := httptest.NewRecorder()
	app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/oauth/continue?user_id_validation_state=S&uuid=uid1", nil))
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "could not create your agent") {
		t.Errorf("continue = %d %s", rec.Code, rec.Body)
	}
}

func TestPublicHasNoSignup(t *testing.T) {
	app, _ := newApp(&fakeAM{res: consentRes()})
	rec := httptest.NewRecorder()
	app.PublicHandler().ServeHTTP(rec, httptest.NewRequest("POST", "/signup?email=alice@gmail.com", nil))
	if rec.Code != 404 && rec.Code != 405 {
		t.Errorf("public /signup = %d, want 404/405", rec.Code)
	}
}

func TestParseUsers(t *testing.T) {
	m, err := ParseUsers("alice@gmail.com=real.person@gmail.com", "bob@gmail.com")
	if err != nil || m["u-alice"] != (User{"alice@gmail.com", "real.person@gmail.com"}) || m["u-bob"] != (User{Email: "bob@gmail.com"}) {
		t.Errorf("ParseUsers = %v, %v", m, err)
	}
	if _, err := ParseUsers("alice@gmail.com", "alice@example.com"); err == nil {
		t.Error("colliding emails accepted")
	}
	if _, err := ParseUsers(); err == nil {
		t.Error("empty accepted")
	}
}

func TestLoginHint(t *testing.T) {
	users, _ := ParseUsers("alice@gmail.com=real.person@gmail.com")
	app, _ := newApp(&fakeAM{res: consentRes()})
	app.Users = users
	rec := signup(app, "alice@gmail.com")
	var out SignupResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !strings.Contains(out.AuthorizationURL, "login_hint=real.person%40gmail.com") || !strings.Contains(out.AuthorizationURL, "prompt=consent") || out.User != "alice@gmail.com" {
		t.Errorf("response = %+v", out)
	}
}
