// Package identity maps a user's email to the Substrate atespace that holds
// that user's actors, and parses the actor identities Substrate presents.
//
// The demo's rule is one atespace per user. The atespace name is derived from
// the email, and it is also the user key the app and the credential provider
// pass to Agent Auth Manager. Nothing stores the binding: an actor's identity
// names its atespace, and the atespace names the user.
package identity

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// TrustDomain is the SPIFFE trust domain Substrate gives actors.
const TrustDomain = "substrate-actor.local"

// maxLabel is the longest DNS label, which is also the longest atespace name.
const maxLabel = 63

var (
	emailRE = regexp.MustCompile(`^[a-z0-9._+-]+@[a-z0-9-]+(\.[a-z0-9-]+)+$`)
	labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
)

// AtespaceForEmail returns the atespace that holds email's actors: "u-" plus
// the local part, so alice@gmail.com becomes u-alice. It rejects anything that
// isn't a plain lowercase address, so two emails can't silently collide
// through case or Unicode folding. Addresses with the same local part on
// different domains do collide; the app rejects such a user list (ParseUsers).
func AtespaceForEmail(email string) (string, error) {
	if !emailRE.MatchString(email) {
		return "", fmt.Errorf("email %q must be a lowercase address like alice@gmail.com", email)
	}
	local, _, _ := strings.Cut(email, "@")
	r := strings.NewReplacer(".", "-", "_", "-", "+", "-")
	ns := "u-" + r.Replace(local)
	for strings.Contains(ns, "--") {
		ns = strings.ReplaceAll(ns, "--", "-")
	}
	ns = strings.TrimRight(ns, "-")
	if len(ns) > maxLabel || !labelRE.MatchString(ns) {
		return "", fmt.Errorf("email %q does not map to a valid atespace name (got %q)", email, ns)
	}
	return ns, nil
}

// Actor names one actor.
type Actor struct {
	Atespace string
	Name     string
}

func (a Actor) String() string { return a.Atespace + "/" + a.Name }

// FromSPIFFEID parses spiffe://substrate-actor.local/actor/<atespace>/<name>,
// the form the egress gateway passes to credential providers.
func FromSPIFFEID(id string) (Actor, error) {
	u, err := url.Parse(id)
	if err != nil {
		return Actor{}, fmt.Errorf("invalid actor SPIFFE ID %q: %w", id, err)
	}
	if u.Scheme != "spiffe" || u.Host != TrustDomain || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return Actor{}, fmt.Errorf("actor SPIFFE ID %q is not in trust domain %s", id, TrustDomain)
	}
	return fromPath(strings.TrimPrefix(u.Path, "/"), id)
}

func fromPath(p, orig string) (Actor, error) {
	parts := strings.Split(p, "/")
	if len(parts) != 3 || parts[0] != "actor" {
		return Actor{}, fmt.Errorf("%q does not name an actor as actor/<atespace>/<name>", orig)
	}
	for _, s := range parts[1:] {
		if !labelRE.MatchString(s) || len(s) > maxLabel {
			return Actor{}, fmt.Errorf("%q has an invalid atespace or actor name %q", orig, s)
		}
	}
	return Actor{Atespace: parts[1], Name: parts[2]}, nil
}
