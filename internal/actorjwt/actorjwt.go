// Package actorjwt verifies Substrate actor JWTs, which the egress gateway
// injects into an actor's requests (the replaceHeaders actorJwt effect).
package actorjwt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/destijl/atecon-identity-2026/internal/identity"
)

// KeySource returns the issuer's current verification keys.
type KeySource interface {
	Keys(ctx context.Context) (*jose.JSONWebKeySet, error)
}

// Verifier checks an actor JWT's signature, issuer, audience and lifetime.
type Verifier struct {
	Issuer   string
	Audience string
	Keys     KeySource
	Now      func() time.Time
}

// Verify returns the actor the token names.
func (v *Verifier) Verify(ctx context.Context, raw string) (identity.Actor, error) {
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256, jose.RS256})
	if err != nil {
		return identity.Actor{}, fmt.Errorf("parsing token: %w", err)
	}
	keys, err := v.Keys.Keys(ctx)
	if err != nil {
		return identity.Actor{}, fmt.Errorf("loading issuer keys: %w", err)
	}
	if len(tok.Headers) != 1 {
		return identity.Actor{}, errors.New("token must have one signature")
	}
	candidates := keys.Keys
	if kid := tok.Headers[0].KeyID; kid != "" {
		candidates = keys.Key(kid)
	}
	var claims jwt.Claims
	var verified bool
	for _, k := range candidates {
		if err := tok.Claims(k.Key, &claims); err == nil {
			verified = true
			break
		}
	}
	if !verified {
		return identity.Actor{}, errors.New("token signature does not verify")
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	if err := claims.ValidateWithLeeway(jwt.Expected{Issuer: v.Issuer, AnyAudience: jwt.Audience{v.Audience}, Time: now()}, 30*time.Second); err != nil {
		return identity.Actor{}, fmt.Errorf("token claims: %w", err)
	}
	if claims.Expiry == nil {
		return identity.Actor{}, errors.New("token has no expiry")
	}
	return identity.FromJWTSubject(claims.Subject)
}

// FromRequest extracts and verifies a Bearer actor JWT from r.
func (v *Verifier) FromRequest(r *http.Request) (identity.Actor, error) {
	h := r.Header.Get("Authorization")
	raw, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || raw == "" {
		return identity.Actor{}, errors.New("missing bearer token")
	}
	return v.Verify(r.Context(), raw)
}

// HTTPKeys fetches a JWKS over HTTP and caches it for TTL.
type HTTPKeys struct {
	URL    string
	Client *http.Client
	TTL    time.Duration

	mu      sync.Mutex
	set     *jose.JSONWebKeySet
	fetched time.Time
}

// Keys implements KeySource.
func (h *HTTPKeys) Keys(ctx context.Context) (*jose.JSONWebKeySet, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ttl := h.TTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if h.set != nil && time.Since(h.fetched) < ttl {
		return h.set, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS %s: HTTP %d", h.URL, resp.StatusCode)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("decoding JWKS: %w", err)
	}
	set.Keys = slices.DeleteFunc(set.Keys, func(k jose.JSONWebKey) bool { return !k.IsPublic() })
	h.set, h.fetched = &set, time.Now()
	return h.set, nil
}

// StaticKeys is a fixed key set, for tests.
type StaticKeys struct{ Set jose.JSONWebKeySet }

// Keys implements KeySource.
func (s *StaticKeys) Keys(context.Context) (*jose.JSONWebKeySet, error) {
	return &s.Set, nil
}
