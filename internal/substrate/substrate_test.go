package substrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ateapi "github.com/destijl/atecon-identity-2026/third_party/ateapi"
)

type fakeControl struct {
	calls    []string
	existing map[string]bool // call names that return AlreadyExists
	failOn   string
	actor    *ateapi.Actor
	tmpl     *ateapi.ActorTemplate
	policy   *ateapi.CreateActorEgressPolicyRequest
}

func (f *fakeControl) result(name string) error {
	f.calls = append(f.calls, name)
	switch {
	case name == f.failOn:
		return status.Error(codes.PermissionDenied, "denied")
	case f.existing[name]:
		return status.Error(codes.AlreadyExists, "exists")
	}
	return nil
}

func (f *fakeControl) CreateAtespace(_ context.Context, in *ateapi.CreateAtespaceRequest, _ ...grpc.CallOption) (*ateapi.Atespace, error) {
	return in.GetAtespace(), f.result("atespace:" + in.GetAtespace().GetMetadata().GetName())
}

func (f *fakeControl) CreateActorTemplate(_ context.Context, in *ateapi.CreateActorTemplateRequest, _ ...grpc.CallOption) (*ateapi.ActorTemplate, error) {
	f.tmpl = in.GetActorTemplate()
	return f.tmpl, f.result("template")
}

func (f *fakeControl) CreateActor(_ context.Context, in *ateapi.CreateActorRequest, _ ...grpc.CallOption) (*ateapi.Actor, error) {
	f.actor = in.GetActor()
	return f.actor, f.result("actor")
}

func (f *fakeControl) CreateActorEgressPolicy(_ context.Context, in *ateapi.CreateActorEgressPolicyRequest, _ ...grpc.CallOption) (*ateapi.EgressPolicy, error) {
	f.policy = in
	return in.GetEgressPolicy(), f.result("policy")
}

func newProvisioner(c Control) *Provisioner {
	return &Provisioner{
		Client:    c,
		Template:  &ateapi.ActorTemplate{Metadata: &ateapi.ResourceMetadata{Name: "mail-agent", Atespace: "ignored"}},
		Policy:    &ateapi.EgressPolicy{Rules: []*ateapi.EgressRule{{}}},
		ActorName: "mail-agent",
	}
}

func TestProvision(t *testing.T) {
	f := &fakeControl{}
	p := newProvisioner(f)
	if err := p.Provision(context.Background(), "u-alice"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, ","); got != "atespace:u-alice,template,actor,policy" {
		t.Errorf("calls = %s", got)
	}
	if f.tmpl.GetMetadata().GetAtespace() != "u-alice" || f.tmpl.GetMetadata().GetName() != "mail-agent" {
		t.Errorf("template metadata = %v", f.tmpl.GetMetadata())
	}
	if p.Template.GetMetadata().GetAtespace() != "ignored" {
		t.Error("Provision modified the shared template")
	}
	if f.actor.GetMetadata().GetAtespace() != "u-alice" || f.actor.GetMetadata().GetName() != "mail-agent" ||
		f.actor.GetActorTemplate().GetAtespace() != "u-alice" || f.actor.GetActorTemplate().GetName() != "mail-agent" {
		t.Errorf("actor = %v", f.actor)
	}
	if f.policy.GetActor().GetAtespace() != "u-alice" || f.policy.GetActor().GetName() != "mail-agent" ||
		f.policy.GetEgressPolicy().GetMetadata().GetName() != "default" || f.policy.GetEgressPolicy().GetMetadata().GetAtespace() != "u-alice" {
		t.Errorf("policy request = %v", f.policy)
	}
}

func TestProvisionIsIdempotent(t *testing.T) {
	f := &fakeControl{existing: map[string]bool{"atespace:u-alice": true, "template": true, "actor": true, "policy": true}}
	if err := newProvisioner(f).Provision(context.Background(), "u-alice"); err != nil {
		t.Errorf("Provision with everything present = %v, want nil", err)
	}
}

func TestProvisionStopsOnError(t *testing.T) {
	f := &fakeControl{failOn: "template"}
	err := newProvisioner(f).Provision(context.Background(), "u-alice")
	if status.Code(errors.Unwrap(err)) != codes.PermissionDenied {
		t.Errorf("err = %v, want PermissionDenied", err)
	}
	if got := strings.Join(f.calls, ","); got != "atespace:u-alice,template" {
		t.Errorf("calls = %s, want to stop at the template", got)
	}
}

// The ConfigMap in deploy/email-app.yaml is what the app loads in the
// cluster; parse it here so a field-name typo fails the test, not sign-up.
func TestDeployedAgentConfigParses(t *testing.T) {
	b, err := os.ReadFile("../../deploy/email-app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, key := range []string{"actor-template.json", "egress-policy.json"} {
		body := blockScalar(t, string(b), key)
		body = strings.ReplaceAll(body, "${AGENT_IMAGE}", "example.com/agent@sha256:0")
		if err := os.WriteFile(filepath.Join(dir, key), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tmpl, err := LoadTemplate(filepath.Join(dir, "actor-template.json"))
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.GetMetadata().GetName() != "mail-agent" || len(tmpl.GetContainers()) != 1 || tmpl.GetContainers()[0].GetImage() == "" {
		t.Errorf("template = %v", tmpl)
	}
	pol, err := LoadPolicy(filepath.Join(dir, "egress-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	rules := pol.GetRules()
	if len(rules) != 1 || rules[0].GetHttps().GetHostnames()[0] != "gmail.googleapis.com" ||
		rules[0].GetHttps().GetEffects().GetReplaceHeaders()[0].GetCredentialUri() != "ate-secret://authmanager.demo/gmail-oauth/gmail.readonly" {
		t.Errorf("policy = %v", pol)
	}
}

// blockScalar returns the "key: |" block under a ConfigMap's data.
func blockScalar(t *testing.T, doc, key string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != key+": |" {
			continue
		}
		indent := len(l) - len(strings.TrimLeft(l, " ")) + 2
		var out []string
		for _, b := range lines[i+1:] {
			if strings.TrimSpace(b) != "" && len(b)-len(strings.TrimLeft(b, " ")) < indent {
				break
			}
			if len(b) >= indent {
				b = b[indent:]
			}
			out = append(out, b)
		}
		return strings.Join(out, "\n")
	}
	t.Fatalf("no %s block", key)
	return ""
}
