// Package provider implements a Substrate credential provider
// (substrate.credprovider.v1.CredentialProvider) backed by Google Cloud
// Agent Auth Manager.
//
// URIs look like ate-secret://<provider-name>/gmail-oauth/<scope>, for example
// ate-secret://authmanager.demo/gmail-oauth/gmail.readonly. (Substrate requires at
// least two path segments.) The user is never in the URI:
// it is derived from the actor's atespace, which comes from the SPIFFE ID the
// egress gateway verified. An actor therefore can only ever get its own
// user's token, and only for scopes this provider allows.
package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/destijl/atecon-identity-2026/internal/authmanager"
	"github.com/destijl/atecon-identity-2026/internal/identity"
	credprovider "github.com/destijl/atecon-identity-2026/third_party/credprovider"
)

// GoogleScopePrefix is prepended to the URI tail to form the OAuth scope.
const GoogleScopePrefix = "https://www.googleapis.com/auth/"

// Retriever is the subset of the Auth Manager client the provider needs.
type Retriever interface {
	Retrieve(ctx context.Context, userID string, scopes []string, continueURI string) (*authmanager.Result, error)
}

// Server is the credential provider.
type Server struct {
	credprovider.UnimplementedCredentialProviderServer

	// Name is the host part of the URIs this provider serves.
	Name string
	// Scopes are the URI tails (short scope names) the provider will serve.
	Scopes map[string]bool
	// AuthManager fetches tokens.
	AuthManager Retriever
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[cacheKey]authmanager.Token
}

type cacheKey struct{ user, scope string }

// refreshBefore is how long before expiry a cached token is refetched.
const refreshBefore = 2 * time.Minute

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// FetchSecret returns the user's access token, or PermissionDenied (which the
// gateway turns into a 403) when the user hasn't consented yet.
func (s *Server) FetchSecret(ctx context.Context, req *credprovider.FetchSecretRequest) (*credprovider.FetchSecretResponse, error) {
	actor, err := identity.FromSPIFFEID(req.GetActorSpiffeId())
	if err != nil {
		slog.WarnContext(ctx, "rejected: bad actor identity", "spiffe_id", req.GetActorSpiffeId(), "err", err)
		return nil, status.Error(codes.PermissionDenied, "invalid actor identity")
	}
	scope, err := s.parseURI(req.GetUri())
	if err != nil {
		slog.WarnContext(ctx, "rejected: bad credential URI", "actor", actor.String(), "uri", req.GetUri(), "err", err)
		return nil, status.Error(codes.NotFound, err.Error())
	}
	user := actor.Atespace
	log := slog.With("actor", req.GetActorSpiffeId(), "user", user, "scope", scope)

	if tok, ok := s.cached(user, scope); ok {
		log.InfoContext(ctx, "credential injected", "source", "cache")
		return &credprovider.FetchSecretResponse{OpaqueBytes: []byte(tok)}, nil
	}

	res, err := s.AuthManager.Retrieve(ctx, user, []string{GoogleScopePrefix + scope}, "")
	switch {
	case errors.Is(err, authmanager.ErrPending), errors.Is(err, authmanager.ErrConsentRejected):
		log.InfoContext(ctx, "no grant: consent not complete", "reason", err.Error())
		return nil, status.Error(codes.PermissionDenied, "consent required")
	case err != nil:
		log.ErrorContext(ctx, "auth manager call failed", "err", err)
		return nil, status.Error(codes.Unavailable, "auth manager unavailable")
	case res.Consent != nil:
		// The actor can't follow a URL from here (the gateway answers with a
		// fixed 403), so the app hands out consent links instead.
		log.InfoContext(ctx, "no grant: consent required")
		return nil, status.Error(codes.PermissionDenied, "consent required")
	case res.Token == nil || res.Token.Value == "":
		log.ErrorContext(ctx, "auth manager returned no token")
		return nil, status.Error(codes.Unavailable, "no token")
	}
	s.store(user, scope, *res.Token)
	log.InfoContext(ctx, "credential injected", "source", "auth-manager", "expires", res.Token.Expiry.Format(time.RFC3339))
	return &credprovider.FetchSecretResponse{OpaqueBytes: []byte(res.Token.Value)}, nil
}

func (s *Server) parseURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("unparseable URI: %w", err)
	}
	if u.Scheme != "ate-secret" || u.Host != s.Name || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("URI must be ate-secret://%s/gmail-oauth/<scope>", s.Name)
	}
	scope, ok := strings.CutPrefix(u.Path, "/gmail-oauth/")
	if !ok || !s.Scopes[scope] {
		return "", fmt.Errorf("scope %q is not served", scope)
	}
	return scope, nil
}

func (s *Server) cached(user, scope string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.cache[cacheKey{user, scope}]
	if !ok || t.Expiry.IsZero() || s.now().Add(refreshBefore).After(t.Expiry) {
		return "", false
	}
	return t.Value, true
}

func (s *Server) store(user, scope string, t authmanager.Token) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[cacheKey]authmanager.Token{}
	}
	s.cache[cacheKey{user, scope}] = t
}
