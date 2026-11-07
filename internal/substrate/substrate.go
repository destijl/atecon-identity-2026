// Package substrate is email-app's client for the Substrate control-plane API
// (ate-api). When a user finishes signing up, the app creates the user's
// atespace, the agent's actor template, the actor, and the actor's egress
// policy.
//
// email-app authenticates with a projected ServiceAccount token
// (system:serviceaccount:email-app:email-app) and needs the global owner role,
// because creating atespaces requires it. A multi-tenant app creates one
// atespace per user, so it can't be scoped to a single atespace.
package substrate

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/destijl/atecon-identity-2026/internal/tlsutil"
	ateapi "github.com/destijl/atecon-identity-2026/third_party/ateapi"
)

// Control is the subset of ateapi.ControlClient the provisioner calls.
type Control interface {
	CreateAtespace(ctx context.Context, in *ateapi.CreateAtespaceRequest, opts ...grpc.CallOption) (*ateapi.Atespace, error)
	CreateActorTemplate(ctx context.Context, in *ateapi.CreateActorTemplateRequest, opts ...grpc.CallOption) (*ateapi.ActorTemplate, error)
	CreateActor(ctx context.Context, in *ateapi.CreateActorRequest, opts ...grpc.CallOption) (*ateapi.Actor, error)
	CreateActorEgressPolicy(ctx context.Context, in *ateapi.CreateActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapi.EgressPolicy, error)
}

// Provisioner creates a user's agent.
type Provisioner struct {
	Client Control
	// Template is the agent's actor template. Its metadata.name is kept and
	// metadata.atespace is set to the user's atespace.
	Template *ateapi.ActorTemplate
	// Policy is the egress policy given to the actor.
	Policy *ateapi.EgressPolicy
	// ActorName names the agent's actor in every user's atespace.
	ActorName string
}

// Provision creates atespace, the actor template, the actor and its egress
// policy. Each step tolerates AlreadyExists, so retrying after a partial
// failure, or signing up again, is safe.
func (p *Provisioner) Provision(ctx context.Context, atespace string) error {
	if _, err := p.Client.CreateAtespace(ctx, &ateapi.CreateAtespaceRequest{
		Atespace: &ateapi.Atespace{Metadata: &ateapi.ResourceMetadata{Name: atespace}},
	}); ignoreExists(err) != nil {
		return fmt.Errorf("creating atespace %s: %w", atespace, err)
	}

	tmpl := proto.Clone(p.Template).(*ateapi.ActorTemplate)
	if tmpl.Metadata == nil {
		tmpl.Metadata = &ateapi.ResourceMetadata{}
	}
	tmpl.Metadata.Atespace = atespace
	if _, err := p.Client.CreateActorTemplate(ctx, &ateapi.CreateActorTemplateRequest{ActorTemplate: tmpl}); ignoreExists(err) != nil {
		return fmt.Errorf("creating actor template %s/%s: %w", atespace, tmpl.Metadata.Name, err)
	}

	ref := &ateapi.ObjectRef{Atespace: atespace, Name: p.ActorName}
	if _, err := p.Client.CreateActor(ctx, &ateapi.CreateActorRequest{Actor: &ateapi.Actor{
		Metadata:      &ateapi.ResourceMetadata{Atespace: atespace, Name: p.ActorName},
		ActorTemplate: &ateapi.ObjectRef{Atespace: atespace, Name: tmpl.Metadata.Name},
	}}); ignoreExists(err) != nil {
		return fmt.Errorf("creating actor %s/%s: %w", atespace, p.ActorName, err)
	}

	pol := proto.Clone(p.Policy).(*ateapi.EgressPolicy)
	pol.Metadata = &ateapi.ResourceMetadata{Atespace: atespace, Name: "default"}
	if _, err := p.Client.CreateActorEgressPolicy(ctx, &ateapi.CreateActorEgressPolicyRequest{Actor: ref, EgressPolicy: pol}); ignoreExists(err) != nil {
		return fmt.Errorf("creating egress policy for %s/%s: %w", atespace, p.ActorName, err)
	}
	slog.InfoContext(ctx, "actor created", "actor", atespace+"/"+p.ActorName, "template", tmpl.Metadata.Name, "egressRules", len(pol.GetRules()))
	return nil
}

func ignoreExists(err error) error {
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}
	return err
}

// LoadTemplate reads an actor template in protojson.
func LoadTemplate(path string) (*ateapi.ActorTemplate, error) {
	t := &ateapi.ActorTemplate{}
	return t, loadJSON(path, t)
}

// LoadPolicy reads an egress policy in protojson.
func LoadPolicy(path string) (*ateapi.EgressPolicy, error) {
	p := &ateapi.EgressPolicy{}
	return p, loadJSON(path, p)
}

func loadJSON(path string, m proto.Message) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := protojson.Unmarshal(b, m); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Dial connects to ate-api over TLS, verifying its servicedns certificate
// against caPath, and sends the token in tokenPath (re-read on every call,
// since the kubelet rotates projected tokens) as a bearer token.
func Dial(target, serverName, caPath, tokenPath string) (*grpc.ClientConn, error) {
	pool, err := tlsutil.LoadPool(caPath)
	if err != nil {
		return nil, err
	}
	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: serverName})
	return grpc.NewClient(target, grpc.WithTransportCredentials(creds), grpc.WithPerRPCCredentials(fileToken(tokenPath)))
}

type fileToken string

func (f fileToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	b, err := os.ReadFile(string(f))
	if err != nil {
		return nil, fmt.Errorf("reading ate-api token: %w", err)
	}
	return map[string]string{"authorization": "Bearer " + strings.TrimSpace(string(b))}, nil
}

func (fileToken) RequireTransportSecurity() bool { return true }
