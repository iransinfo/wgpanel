package nodeca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"
)

func generateTestCSR(t *testing.T, spoofedCN string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: spoofedCN},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
}

func parseCertPEM(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatal("failed to decode certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}

func TestLoadOrCreatePersistsAndReloadsSameCA(t *testing.T) {
	dir := t.TempDir()

	ca1, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	ca2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate (reload): %v", err)
	}

	if ca1.Cert.SerialNumber.Cmp(ca2.Cert.SerialNumber) != 0 {
		t.Fatal("expected reloading the CA from disk to return the same certificate")
	}
}

func TestSignCSRIgnoresSpoofedCommonName(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	csrPEM := generateTestCSR(t, "attacker-supplied-identity")
	certPEM, fingerprint, err := ca.SignCSR(csrPEM, "real-authorized-node-id", time.Hour)
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}
	if fingerprint == "" {
		t.Fatal("expected a non-empty fingerprint")
	}

	cert := parseCertPEM(t, certPEM)
	if cert.Subject.CommonName != "real-authorized-node-id" {
		t.Fatalf("expected CN to be the authorized identity, got %q", cert.Subject.CommonName)
	}

	// The issued cert must actually chain to the CA.
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("issued certificate does not verify against the CA pool: %v", err)
	}
}

func TestFingerprintIsStableAndDistinct(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	_, fp1, err := ca.SignCSR(generateTestCSR(t, "node-a"), "node-a", time.Hour)
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}
	_, fp2, err := ca.SignCSR(generateTestCSR(t, "node-b"), "node-b", time.Hour)
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}

	if fp1 == fp2 {
		t.Fatal("expected different certificates to have different fingerprints")
	}
}

func TestServerCertChainsToCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	tlsCert, err := ca.ServerCert(time.Hour)
	if err != nil {
		t.Fatalf("ServerCert: %v", err)
	}

	cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}
	if cert.Subject.CommonName != ServerName {
		t.Fatalf("expected CN %q, got %q", ServerName, cert.Subject.CommonName)
	}

	if _, err := cert.Verify(x509.VerifyOptions{
		DNSName:   ServerName,
		Roots:     ca.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("server cert does not verify against the CA pool: %v", err)
	}
}
