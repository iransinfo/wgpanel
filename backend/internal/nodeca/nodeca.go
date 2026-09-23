// Package nodeca is the control-plane's internal Certificate Authority for node
// agent mTLS (docs/STORY-04-node-agent-mtls.md). It exists instead of a gRPC+public-CA
// setup because a private internal CA is the standard way to do fleet mTLS for
// hosts that don't have real public DNS names.
package nodeca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// ServerName is the fixed identity the control-plane's own TLS cert presents. It is
// not a real DNS name - agents dial whatever address/IP they were configured with,
// but always set this as tls.Config.ServerName, since the actual trust boundary is
// "does this cert chain to our CA," not "does the hostname resolve."
const ServerName = "wgpanel-control-plane"

type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	key     *ecdsa.PrivateKey
}

// LoadOrCreate loads a CA persisted under dir, generating and persisting a new one
// on first run. The CA is the one long-lived secret here - server certs and node
// certs are both cheaply re-derivable from it and are not persisted.
func LoadOrCreate(dir string) (*CA, error) {
	certPath := filepath.Join(dir, "ca-cert.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")

	if certBytes, err := os.ReadFile(certPath); err == nil {
		keyBytes, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read CA key: %w", err)
		}
		return parseCA(certBytes, keyBytes)
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create CA dir: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "wgpanel-internal-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create CA cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal CA key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		return nil, fmt.Errorf("write CA cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return nil, fmt.Errorf("write CA key: %w", err)
	}

	return parseCA(certPEM, keyPEM)
}

func parseCA(certPEM, keyPEM []byte) (*CA, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("decode CA cert PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("decode CA key PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA key: %w", err)
	}

	return &CA{Cert: cert, CertPEM: certPEM, key: key}, nil
}

// SignCSR issues a client-auth certificate for csrPEM. The Subject CommonName is
// always commonName - the caller's already-authorized identity (e.g. a node ID from
// a redeemed join token) - never whatever the CSR itself requested. A CSR only
// proves "the requester holds this private key," not "this requester is who they
// claim" - that authorization decision belongs to the caller, made before SignCSR is
// ever invoked, not to unverified fields inside the CSR.
func (ca *CA) SignCSR(csrPEM []byte, commonName string, validity time.Duration) (certPEM, fingerprint string, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return "", "", fmt.Errorf("decode CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", "", fmt.Errorf("parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return "", "", fmt.Errorf("invalid CSR signature: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return "", "", err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, csr.PublicKey, ca.key)
	if err != nil {
		return "", "", fmt.Errorf("sign certificate: %w", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return string(pemBytes), Fingerprint(certDER), nil
}

// ServerCert generates a fresh TLS server certificate for the mTLS listener, signed
// by this CA. Not persisted: agents trust the CA itself (handed out at registration
// time), not a pinned server certificate, so regenerating this on every process
// start changes nothing an agent depends on.
func (ca *CA) ServerCert(validity time.Duration) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate server key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: ServerName},
		DNSNames:     []string{ServerName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("sign server cert: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER, ca.Cert.Raw},
		PrivateKey:  key,
	}, nil
}

// Pool returns a cert pool containing just this CA - used both as tls.Config.ClientCAs
// (to verify agent certs) and handed to agents as the root they should trust.
func (ca *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// Fingerprint is the hex-encoded SHA-256 of a certificate's DER bytes, stored per
// node so a heartbeat can be checked against the specific certificate that was
// issued for it, not just "signed by our CA" (see STORY-04's fingerprint-mismatch
// rejection requirement).
func Fingerprint(certDER []byte) string {
	sum := sha256.Sum256(certDER)
	return hex.EncodeToString(sum[:])
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}
