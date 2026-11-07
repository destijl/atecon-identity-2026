package audit

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"
)

func auditor(env []string, files []string, metadataUp bool) *Auditor {
	return &Auditor{
		Environ: func() []string { return env },
		Paths:   DefaultPaths,
		Home:    "/home/agent",
		Exists:  func(p string) bool { return slices.Contains(files, p) },
		Dial: func(context.Context, string, string) (net.Conn, error) {
			if metadataUp {
				c, s := net.Pipe()
				s.Close()
				return c, nil
			}
			return nil, errors.New("no route")
		},
		Metadata: "metadata.google.internal:80",
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name     string
		env      []string
		files    []string
		metadata bool
		wantZero bool
		wantEnv  []string
		wantFile []string
	}{
		{name: "clean", env: []string{"PATH=/bin", "HOME=/home/agent", "SSL_CERT_FILE=/run/ate/x.pem"}, wantZero: true},
		{name: "token env", env: []string{"GMAIL_TOKEN=fake", "PATH=/bin"}, wantEnv: []string{"GMAIL_TOKEN"}},
		{name: "api key env", env: []string{"GEMINI_API_KEY=x"}, wantEnv: []string{"GEMINI_API_KEY"}},
		{name: "sa token", files: []string{"/var/run/secrets/kubernetes.io/serviceaccount/token"}, wantFile: []string{"/var/run/secrets/kubernetes.io/serviceaccount/token"}},
		{name: "gcloud in home", files: []string{"/home/agent/.config/gcloud"}, wantFile: []string{"/home/agent/.config/gcloud"}},
		{name: "metadata reachable", metadata: true},
	}
	for _, tt := range tests {
		r := auditor(tt.env, tt.files, tt.metadata).Run(context.Background())
		if r.ZeroAmbientVerified != tt.wantZero {
			t.Errorf("%s: zeroAmbientVerified = %v, want %v (%+v)", tt.name, r.ZeroAmbientVerified, tt.wantZero, r)
		}
		if tt.wantEnv != nil && !slices.Equal(r.SecretEnvVars, tt.wantEnv) {
			t.Errorf("%s: env = %v, want %v", tt.name, r.SecretEnvVars, tt.wantEnv)
		}
		if tt.wantFile != nil && !slices.Equal(r.CredentialFiles, tt.wantFile) {
			t.Errorf("%s: files = %v, want %v", tt.name, r.CredentialFiles, tt.wantFile)
		}
	}
}
