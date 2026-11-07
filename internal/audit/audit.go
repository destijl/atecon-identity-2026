// Package audit checks a process environment for ambient credentials: secret-
// looking environment variables, well-known credential files, a Kubernetes
// service account token, and a reachable cloud metadata server.
package audit

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Report is the audit result. Values of secrets are never included.
type Report struct {
	SecretEnvVars       []string `json:"secretEnvVars"`
	CredentialFiles     []string `json:"credentialFiles"`
	MetadataServer      string   `json:"metadataServer"`
	ZeroAmbientVerified bool     `json:"zeroAmbientVerified"`
}

var secretEnv = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|API_?KEY|ACCESS_?KEY|PRIVATE_?KEY|AUTH)`)

// DefaultPaths are files and directories that commonly hold credentials.
var DefaultPaths = []string{
	"/var/run/secrets/kubernetes.io/serviceaccount/token",
	"/var/run/secrets",
	"/run/secrets",
	"~/.config/gcloud",
	"~/.aws/credentials",
	"~/.azure",
	"~/.docker/config.json",
	"~/.kube/config",
	"~/.netrc",
	"~/.git-credentials",
	"~/.ssh",
	"/etc/gmail",
}

// Auditor runs the checks. Fields are overridable for tests.
type Auditor struct {
	Environ  func() []string
	Paths    []string
	Home     string
	Exists   func(string) bool
	Dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	Metadata string // host:port of the metadata server
}

// New returns an Auditor for the real process.
func New() *Auditor {
	home, _ := os.UserHomeDir()
	d := &net.Dialer{Timeout: 2 * time.Second}
	return &Auditor{
		Environ:  os.Environ,
		Paths:    DefaultPaths,
		Home:     home,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		Dial:     d.DialContext,
		Metadata: "metadata.google.internal:80",
	}
}

// Run audits and reports.
func (a *Auditor) Run(ctx context.Context) Report {
	r := Report{SecretEnvVars: []string{}, CredentialFiles: []string{}}
	for _, kv := range a.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if secretEnv.MatchString(name) {
			r.SecretEnvVars = append(r.SecretEnvVars, name)
		}
	}
	sort.Strings(r.SecretEnvVars)
	for _, p := range a.Paths {
		if strings.HasPrefix(p, "~/") {
			if a.Home == "" {
				continue
			}
			p = filepath.Join(a.Home, p[2:])
		}
		if a.Exists(p) {
			r.CredentialFiles = append(r.CredentialFiles, p)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if c, err := a.Dial(ctx, "tcp", a.Metadata); err == nil {
		c.Close()
		r.MetadataServer = "reachable"
	} else {
		r.MetadataServer = "unreachable"
	}
	r.ZeroAmbientVerified = len(r.SecretEnvVars) == 0 && len(r.CredentialFiles) == 0 && r.MetadataServer == "unreachable"
	return r
}
