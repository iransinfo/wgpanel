package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type identity struct {
	NodeID     string
	TLSCert    tls.Certificate
	CACertPool *x509.CertPool
}

// loadOrRegister reuses a prior registration persisted under cfg.StateDir if one
// exists, otherwise performs a fresh registration (which requires WGPANEL_JOIN_TOKEN).
func loadOrRegister(cfg config, logger *slog.Logger) (*identity, error) {
	paths := statePaths(cfg.StateDir)

	if allExist(paths.cert, paths.key, paths.ca, paths.nodeID) {
		logger.Info("using existing registration", "state_dir", cfg.StateDir)
		return loadIdentity(paths)
	}

	if cfg.JoinToken == "" {
		return nil, fmt.Errorf("no existing registration in %s and WGPANEL_JOIN_TOKEN is not set", cfg.StateDir)
	}

	logger.Info("registering with control plane", "panel_addr", cfg.PanelAddr)
	return register(cfg, paths)
}

type paths struct {
	cert, key, ca, nodeID string
}

func statePaths(stateDir string) paths {
	return paths{
		cert:   filepath.Join(stateDir, "client-cert.pem"),
		key:    filepath.Join(stateDir, "client-key.pem"),
		ca:     filepath.Join(stateDir, "ca-cert.pem"),
		nodeID: filepath.Join(stateDir, "node-id.txt"),
	}
}

func allExist(paths ...string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

// register generates a keypair locally (the private key never leaves this
// process), submits a CSR + the join token, and persists what /agent/register
// returns. The bootstrap call itself skips server certificate verification - the
// join token is the trust anchor for this one exchange, since the agent has no CA
// to verify against yet (docs/STORY-04-node-agent-mtls.md design note).
func register(cfg config, p paths) (*identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cfg.NodeName},
	}, key)
	if err != nil {
		return nil, fmt.Errorf("create CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	body := map[string]string{
		"join_token": cfg.JoinToken,
		"csr_pem":    string(csrPEM),
	}
	if cfg.WGPublicKey != "" {
		body["wg_public_key"] = cfg.WGPublicKey
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	bootstrapClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // deliberate - see doc comment above
		Timeout:   15 * time.Second,
	}

	resp, err := bootstrapClient.Post("https://"+cfg.PanelAddr+"/agent/register", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("register request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		// An HTML body (or a bare 405) means we reached a web server, not the
		// agent API: WGPANEL_PANEL_ADDR points at the panel's WEB port instead of
		// NODE_AGENT_PORT. Say so - the raw nginx/SPA error page explains nothing.
		if resp.StatusCode == http.StatusMethodNotAllowed ||
			strings.Contains(resp.Header.Get("Content-Type"), "text/html") ||
			bytes.Contains(bytes.ToLower(body), []byte("<html")) {
			return nil, fmt.Errorf("register rejected: %s - this address answers like the panel's web UI, not the node-agent API; WGPANEL_PANEL_ADDR must use the panel's NODE_AGENT_PORT (48443 by default, see the panel server's .env), not the web/HTTPS port", resp.Status)
		}
		return nil, fmt.Errorf("register rejected: %s: %s", resp.Status, string(body))
	}

	var respBody struct {
		NodeID           string `json:"node_id"`
		CertificatePEM   string `json:"certificate_pem"`
		CACertificatePEM string `json:"ca_certificate_pem"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return nil, fmt.Errorf("decode register response: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	writes := map[string][]byte{
		p.cert:   []byte(respBody.CertificatePEM),
		p.key:    keyPEM,
		p.ca:     []byte(respBody.CACertificatePEM),
		p.nodeID: []byte(respBody.NodeID),
	}
	for path, content := range writes {
		if err := os.WriteFile(path, content, 0600); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}

	return loadIdentity(p)
}

func loadIdentity(p paths) (*identity, error) {
	tlsCert, err := tls.LoadX509KeyPair(p.cert, p.key)
	if err != nil {
		return nil, fmt.Errorf("load client keypair: %w", err)
	}

	caBytes, err := os.ReadFile(p.ca)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("parse CA cert")
	}

	nodeIDBytes, err := os.ReadFile(p.nodeID)
	if err != nil {
		return nil, fmt.Errorf("read node id: %w", err)
	}

	return &identity{NodeID: string(nodeIDBytes), TLSCert: tlsCert, CACertPool: pool}, nil
}
