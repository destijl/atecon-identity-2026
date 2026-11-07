package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func leaf(t *testing.T, uris ...string) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "x"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	for _, u := range uris {
		pu, _ := url.Parse(u)
		tmpl.URIs = append(tmpl.URIs, pu)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c
}

func TestVerifyPeerURI(t *testing.T) {
	const egress = "spiffe://cluster.local/ns/ate-system/sa/atenet-egress"
	v := VerifyPeerURI([]string{egress})
	tests := []struct {
		name    string
		certs   []*x509.Certificate
		wantErr bool
	}{
		{name: "egress gateway", certs: []*x509.Certificate{leaf(t, egress)}},
		{name: "other workload", certs: []*x509.Certificate{leaf(t, "spiffe://cluster.local/ns/default/sa/x")}, wantErr: true},
		{name: "two URIs", certs: []*x509.Certificate{leaf(t, egress, "spiffe://cluster.local/ns/a/sa/b")}, wantErr: true},
		{name: "no URI", certs: []*x509.Certificate{leaf(t)}, wantErr: true},
		{name: "no cert", wantErr: true},
	}
	for _, tt := range tests {
		if err := v(tls.ConnectionState{PeerCertificates: tt.certs}); (err != nil) != tt.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestLoadBundle(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kder, _ := x509.MarshalECPrivateKey(key)
	p := filepath.Join(t.TempDir(), "bundle.pem")
	data := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})...)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(p); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("LoadBundle(missing) succeeded")
	}
}
