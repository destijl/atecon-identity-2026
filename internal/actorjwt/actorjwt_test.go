package actorjwt

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	iss = "https://idp.ate-system.svc"
	aud = "email-app"
)

type signer struct {
	key *ecdsa.PrivateKey
	kid string
}

func newSigner(t *testing.T, kid string) signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signer{k, kid}
}

func (s signer) jwk() jose.JSONWebKey {
	return jose.JSONWebKey{Key: &s.key.PublicKey, KeyID: s.kid, Algorithm: "ES256", Use: "sig"}
}

func (s signer) sign(t *testing.T, c jwt.Claims) string {
	t.Helper()
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.key}, (&jose.SignerOptions{}).WithHeader("kid", s.kid))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(sg).Claims(c).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerify(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	good := newSigner(t, "k1")
	evil := newSigner(t, "k1")
	v := &Verifier{Issuer: iss, Audience: aud, Keys: &StaticKeys{Set: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{good.jwk()}}}, Now: func() time.Time { return now }}
	base := jwt.Claims{Issuer: iss, Audience: jwt.Audience{aud}, Subject: "actor/alice-gmail-com/email-agent", Expiry: jwt.NewNumericDate(now.Add(10 * time.Minute)), IssuedAt: jwt.NewNumericDate(now)}
	with := func(f func(*jwt.Claims)) jwt.Claims { c := base; f(&c); return c }

	tests := []struct {
		name    string
		tok     string
		wantErr bool
	}{
		{name: "valid", tok: good.sign(t, base)},
		{name: "wrong key", tok: evil.sign(t, base), wantErr: true},
		{name: "wrong issuer", tok: good.sign(t, with(func(c *jwt.Claims) { c.Issuer = "https://evil" })), wantErr: true},
		{name: "wrong audience", tok: good.sign(t, with(func(c *jwt.Claims) { c.Audience = jwt.Audience{"gmail"} })), wantErr: true},
		{name: "expired", tok: good.sign(t, with(func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(now.Add(-time.Minute)) })), wantErr: true},
		{name: "no expiry", tok: good.sign(t, with(func(c *jwt.Claims) { c.Expiry = nil })), wantErr: true},
		{name: "worker sub", tok: good.sign(t, with(func(c *jwt.Claims) { c.Subject = "worker-for-actor/alice-gmail-com/email-agent" })), wantErr: true},
		{name: "garbage", tok: "not.a.jwt", wantErr: true},
	}
	for _, tt := range tests {
		got, err := v.Verify(context.Background(), tt.tok)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tt.name, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got.String() != "alice-gmail-com/email-agent" {
			t.Errorf("%s: actor = %v", tt.name, got)
		}
	}
}

func TestHTTPKeysCaches(t *testing.T) {
	s := newSigner(t, "k")
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{s.jwk()}})
	}))
	defer srv.Close()
	k := &HTTPKeys{URL: srv.URL, Client: srv.Client()}
	for i := 0; i < 3; i++ {
		set, err := k.Keys(context.Background())
		if err != nil || len(set.Keys) != 1 {
			t.Fatalf("Keys() = %v, %v", set, err)
		}
	}
	if hits != 1 {
		t.Errorf("JWKS fetched %d times, want 1", hits)
	}
}
