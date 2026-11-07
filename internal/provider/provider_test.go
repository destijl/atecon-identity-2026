package provider

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/destijl/atecon-identity-2026/internal/authmanager"
	credprovider "github.com/destijl/atecon-identity-2026/third_party/credprovider"
)

type fakeAM struct {
	res   *authmanager.Result
	err   error
	calls []string
}

func (f *fakeAM) Retrieve(_ context.Context, user string, scopes []string, _ string) (*authmanager.Result, error) {
	f.calls = append(f.calls, user+" "+strings.Join(scopes, ","))
	return f.res, f.err
}

const aliceID = "spiffe://substrate-actor.local/actor/alice-gmail-com/email-agent"

func newServer(am *fakeAM, now time.Time) *Server {
	return &Server{
		Name:        "authmanager.demo",
		Scopes:      map[string]bool{"gmail.readonly": true},
		AuthManager: am,
		Now:         func() time.Time { return now },
	}
}

func fetch(s *Server, uri, id string) (*credprovider.FetchSecretResponse, error) {
	return s.FetchSecret(context.Background(), &credprovider.FetchSecretRequest{Uri: uri, ActorSpiffeId: id})
}

func TestFetchSecret(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	tok := &authmanager.Result{Token: &authmanager.Token{Value: "fake-access-token", Expiry: now.Add(time.Hour)}}
	consent := &authmanager.Result{Consent: &authmanager.Consent{AuthorizationURL: "https://accounts.google.com/x"}}
	tests := []struct {
		name     string
		uri, id  string
		am       *fakeAM
		want     string
		wantCode codes.Code
		wantCall string
	}{
		{name: "token", uri: "ate-secret://authmanager.demo/oauth/gmail.readonly", id: aliceID, am: &fakeAM{res: tok}, want: "fake-access-token",
			wantCall: "alice-gmail-com https://www.googleapis.com/auth/gmail.readonly"},
		{name: "consent required", uri: "ate-secret://authmanager.demo/oauth/gmail.readonly", id: aliceID, am: &fakeAM{res: consent}, wantCode: codes.PermissionDenied},
		{name: "pending", uri: "ate-secret://authmanager.demo/oauth/gmail.readonly", id: aliceID, am: &fakeAM{err: authmanager.ErrPending}, wantCode: codes.PermissionDenied},
		{name: "auth manager down", uri: "ate-secret://authmanager.demo/oauth/gmail.readonly", id: aliceID, am: &fakeAM{err: errors.New("boom")}, wantCode: codes.Unavailable},
		{name: "scope not served", uri: "ate-secret://authmanager.demo/oauth/gmail.modify", id: aliceID, am: &fakeAM{res: tok}, wantCode: codes.NotFound},
		{name: "full scope url not allowed", uri: "ate-secret://authmanager.demo/https://mail.google.com/", id: aliceID, am: &fakeAM{res: tok}, wantCode: codes.NotFound},
		{name: "missing oauth segment", uri: "ate-secret://authmanager.demo/gmail.readonly", id: aliceID, am: &fakeAM{res: tok}, wantCode: codes.NotFound},
		{name: "other provider", uri: "ate-secret://k8s.io/gmail.readonly", id: aliceID, am: &fakeAM{res: tok}, wantCode: codes.NotFound},
		{name: "user in URI is ignored as path", uri: "ate-secret://authmanager.demo/bob-gmail-com/gmail.readonly", id: aliceID, am: &fakeAM{res: tok}, wantCode: codes.NotFound},
		{name: "bad actor id", uri: "ate-secret://authmanager.demo/oauth/gmail.readonly", id: "spiffe://cluster.local/ns/x/sa/y", am: &fakeAM{res: tok}, wantCode: codes.PermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(tt.am, now)
			resp, err := fetch(s, tt.uri, tt.id)
			if got := status.Code(err); got != tt.wantCode {
				t.Fatalf("code = %v (%v), want %v", got, err, tt.wantCode)
			}
			if tt.want != "" && string(resp.GetOpaqueBytes()) != tt.want {
				t.Errorf("secret = %q, want %q", resp.GetOpaqueBytes(), tt.want)
			}
			if tt.wantCall != "" && (len(tt.am.calls) != 1 || tt.am.calls[0] != tt.wantCall) {
				t.Errorf("auth manager calls = %q, want [%q]", tt.am.calls, tt.wantCall)
			}
			if tt.wantCode == codes.NotFound && len(tt.am.calls) != 0 {
				t.Errorf("auth manager called for a rejected URI")
			}
		})
	}
}

// Each actor gets its own atespace's token: bob's actor never sees alice's.
func TestUserComesFromActorIdentity(t *testing.T) {
	now := time.Now()
	am := &fakeAM{res: &authmanager.Result{Token: &authmanager.Token{Value: "t", Expiry: now.Add(time.Hour)}}}
	s := newServer(am, now)
	if _, err := fetch(s, "ate-secret://authmanager.demo/oauth/gmail.readonly", "spiffe://substrate-actor.local/actor/bob-gmail-com/email-agent"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(am.calls[0], "bob-gmail-com ") {
		t.Errorf("retrieve user = %q, want bob-gmail-com", am.calls[0])
	}
}

func TestCache(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	am := &fakeAM{res: &authmanager.Result{Token: &authmanager.Token{Value: "t1", Expiry: now.Add(10 * time.Minute)}}}
	s := newServer(am, now)
	uri := "ate-secret://authmanager.demo/oauth/gmail.readonly"
	for i := 0; i < 3; i++ {
		if _, err := fetch(s, uri, aliceID); err != nil {
			t.Fatal(err)
		}
	}
	if len(am.calls) != 1 {
		t.Errorf("auth manager calls = %d, want 1 (cached)", len(am.calls))
	}
	s.Now = func() time.Time { return now.Add(9 * time.Minute) } // inside refreshBefore
	if _, err := fetch(s, uri, aliceID); err != nil {
		t.Fatal(err)
	}
	if len(am.calls) != 2 {
		t.Errorf("auth manager calls = %d, want 2 (refetch near expiry)", len(am.calls))
	}
}

func TestLogsNeverContainToken(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)
	now := time.Now()
	am := &fakeAM{res: &authmanager.Result{Token: &authmanager.Token{Value: "fake-token-MUST-NOT-BE-LOGGED", Expiry: now.Add(time.Hour)}}}
	if _, err := fetch(newServer(am, now), "ate-secret://authmanager.demo/oauth/gmail.readonly", aliceID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "MUST-NOT-BE-LOGGED") {
		t.Errorf("log contains token: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"user":"alice-gmail-com"`) || !strings.Contains(buf.String(), "credential injected") {
		t.Errorf("log missing audit fields: %s", buf.String())
	}
}
