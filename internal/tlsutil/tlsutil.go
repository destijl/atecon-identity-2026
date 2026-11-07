// Package tlsutil loads Kubernetes pod-certificate credential bundles (leaf
// certificate chain and private key concatenated in one PEM file) and builds
// mTLS configs that re-read them on every handshake, so kubelet rotation
// needs no restart.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"slices"
)

// LoadBundle reads a cert-and-key PEM bundle.
func LoadBundle(path string) (*tls.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(data, data)
	if err != nil {
		return nil, fmt.Errorf("parsing credential bundle %s: %w", path, err)
	}
	return &cert, nil
}

// LoadPool reads a PEM file of CA certificates.
func LoadPool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no certificates in %s", path)
	}
	return pool, nil
}

// ServerConfig is a TLS 1.3 server config that requires a client certificate
// chaining to clientCA and carrying one of allowedURIs as a URI SAN.
func ServerConfig(bundlePath, clientCAPath string, allowedURIs []string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			cert, err := LoadBundle(bundlePath)
			if err != nil {
				return nil, err
			}
			pool, err := LoadPool(clientCAPath)
			if err != nil {
				return nil, err
			}
			return &tls.Config{
				MinVersion:       tls.VersionTLS13,
				Certificates:     []tls.Certificate{*cert},
				ClientAuth:       tls.RequireAndVerifyClientCert,
				ClientCAs:        pool,
				VerifyConnection: VerifyPeerURI(allowedURIs),
			}, nil
		},
	}
}

// VerifyPeerURI accepts a connection only if the verified peer leaf has
// exactly one URI SAN and it is in allowed.
func VerifyPeerURI(allowed []string) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("no client certificate")
		}
		uris := cs.PeerCertificates[0].URIs
		if len(uris) != 1 {
			return fmt.Errorf("client certificate has %d URI SANs, want 1", len(uris))
		}
		if !slices.Contains(allowed, uris[0].String()) {
			return fmt.Errorf("client %s is not allowed", uris[0])
		}
		return nil
	}
}
