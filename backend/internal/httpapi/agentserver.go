package httpapi

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"wgpanel-api/internal/nodeca"
	"wgpanel-api/internal/store"
)

// nodeCertValidity is how long an issued node agent certificate is valid for.
// Rotation/renewal before expiry isn't built yet - out of scope per STORY-04.
const nodeCertValidity = 365 * 24 * time.Hour

// AgentTLSConfig builds the TLS config for the node-agent-facing listener:
// VerifyClientCertIfGiven lets /agent/register succeed with no client cert (the join
// token is that bootstrap call's credential) while still verifying one if presented,
// which is what makes /agent/heartbeat's cert-required check meaningful.
func (s *Server) AgentTLSConfig() (*tls.Config, error) {
	serverCert, err := s.CA.ServerCert(365 * 24 * time.Hour)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    s.CA.Pool(),
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// AgentRoutes is the node-agent-facing surface, served on NODE_AGENT_PORT with the
// TLS config above - entirely separate from Routes()'s admin/bot-facing surface.
func (s *Server) AgentRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /agent/register", s.handleAgentRegister)
	mux.HandleFunc("POST /agent/heartbeat", s.handleAgentHeartbeat)
	return s.loggingMiddleware(mux)
}

type agentRegisterRequest struct {
	JoinToken string `json:"join_token"`
	CSRPEM    string `json:"csr_pem"`
	// WGPublicKey is the node's own WireGuard server public key, if install-node.sh
	// generated one (setup_wireguard()) - optional so older agents/manual redemptions
	// without a real interface yet still work.
	WGPublicKey string `json:"wg_public_key"`
}

type agentRegisterResponse struct {
	NodeID           string `json:"node_id"`
	CertificatePEM   string `json:"certificate_pem"`
	CACertificatePEM string `json:"ca_certificate_pem"`
}

// handleAgentRegister redeems a join token (reusing store.RedeemJoinToken from
// STORY-02 unchanged) and signs the agent's CSR - but the issued certificate's
// CommonName is always the redeemed node's real ID, never whatever the CSR
// requested (see nodeca.SignCSR). No client certificate is required for this call;
// the join token is the credential for this one bootstrap exchange.
func (s *Server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	var req agentRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.JoinToken == "" || req.CSRPEM == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "join_token and csr_pem are required")
		return
	}

	ctx := r.Context()
	node, err := s.Store.RedeemJoinToken(ctx, req.JoinToken)
	if errors.Is(err, store.ErrInvalidOrUsedToken) {
		writeJSONError(w, http.StatusUnauthorized, "invalid_token", "join token is invalid, expired, or already used")
		return
	}
	if err != nil {
		s.Logger.Error("redeem_join_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not redeem token")
		return
	}

	certPEM, fingerprint, err := s.CA.SignCSR([]byte(req.CSRPEM), node.ID, nodeCertValidity)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_csr", "could not sign the provided CSR")
		return
	}

	if err := s.Store.RecordNodeCertFingerprint(ctx, node.ID, fingerprint); err != nil {
		s.Logger.Error("record_fingerprint_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not finalize registration")
		return
	}

	if err := s.Store.SetNodePublicKey(ctx, node.ID, req.WGPublicKey); err != nil {
		s.Logger.Error("set_node_public_key_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not finalize registration")
		return
	}

	if err := s.Store.InsertAuditLog(ctx, "node-agent", "node.agent_registered", node.ID, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}

	writeJSON(w, http.StatusCreated, agentRegisterResponse{
		NodeID:           node.ID,
		CertificatePEM:   certPEM,
		CACertificatePEM: string(s.CA.CertPEM),
	})
}

type agentHeartbeatTraffic struct {
	PublicKey     string     `json:"public_key"`
	ReceiveBytes  int64      `json:"rx_bytes"`
	TransmitBytes int64      `json:"tx_bytes"`
	LastHandshake *time.Time `json:"last_handshake"`
	// Endpoint is the peer's current client source "ip:port" from the kernel, "" if
	// unknown - device tracking input (PRD-account-management.md §6.4). Absent from
	// older agents' reports, which simply means no device sightings from that node.
	Endpoint string `json:"endpoint"`
}

type agentHeartbeatRequest struct {
	PeerCount     int                     `json:"peer_count"`
	LoadAvg       float64                 `json:"load_avg"`
	Traffic       []agentHeartbeatTraffic `json:"traffic"`
	CPUPercent    *float32                `json:"cpu_percent"`
	MemUsedBytes  *int64                  `json:"mem_used_bytes"`
	MemTotalBytes *int64                  `json:"mem_total_bytes"`
}

type agentHeartbeatPeer struct {
	PublicKey  string   `json:"public_key"`
	AllowedIPs []string `json:"allowed_ips"`
	// BandwidthLimitMbps rides along with the desired peer so the agent can apply tc
	// shaping in the same reconcile pass; nil/omitted = unshaped. Older agents ignore
	// the field entirely (peers still sync, just without rate enforcement).
	BandwidthLimitMbps *int `json:"bandwidth_limit_mbps,omitempty"`
}

type agentHeartbeatResponse struct {
	Status string               `json:"status"`
	Peers  []agentHeartbeatPeer `json:"peers"`
}

// handleAgentHeartbeat requires a client certificate that both chains to our CA
// (enforced by the TLS handshake itself - a connection with an invalid client cert
// never reaches this handler) and matches the fingerprint pinned for that node id at
// registration (enforced here - closes the gap a bare "signed by our CA" check would
// leave for a node that was re-registered while an old cert is still floating around).
//
// The response carries the full desired peer list for this node (docs/STORY-09-
// multi-node-accounts.md) - the agent applies it wholesale via wgctrl's
// ReplacePeers, piggybacked on this same ~10s cycle rather than a separate push
// channel. This is also how suspend/delete enforcement happens: an account that
// stops being 'active' simply stops appearing in the next response.
func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		writeJSONError(w, http.StatusUnauthorized, "client_certificate_required", "this endpoint requires a client certificate issued by /agent/register")
		return
	}
	cert := r.TLS.PeerCertificates[0]
	nodeID := cert.Subject.CommonName
	fingerprint := nodeca.Fingerprint(cert.Raw)

	var req agentHeartbeatRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // malformed/absent body isn't fatal - traffic/metrics are best-effort

	ctx := r.Context()
	err := s.Store.RecordHeartbeat(ctx, nodeID, fingerprint)
	switch {
	case errors.Is(err, store.ErrNodeNotFound):
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node matches this certificate's identity")
		return
	case errors.Is(err, store.ErrFingerprintMismatch):
		writeJSONError(w, http.StatusUnauthorized, "certificate_superseded", "this certificate is no longer valid for this node")
		return
	case err != nil:
		s.Logger.Error("record_heartbeat_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not record heartbeat")
		return
	}

	// Real traffic accounting + node health (docs/STORY-10-monitoring-and-domain-
	// management.md) - best-effort: a failure here shouldn't fail the heartbeat
	// itself (the node's online status and peer reconciliation are more important
	// than one tick of monitoring data), just gets logged.
	if len(req.Traffic) > 0 || req.CPUPercent != nil || req.MemUsedBytes != nil {
		traffic := make([]store.PeerTrafficReport, 0, len(req.Traffic))
		for _, t := range req.Traffic {
			traffic = append(traffic, store.PeerTrafficReport{
				PublicKey:     t.PublicKey,
				ReceiveBytes:  t.ReceiveBytes,
				TransmitBytes: t.TransmitBytes,
				LastHandshake: t.LastHandshake,
				Endpoint:      t.Endpoint,
			})
		}
		var metrics *store.NodeMetricsReport
		if req.CPUPercent != nil || req.MemUsedBytes != nil {
			metrics = &store.NodeMetricsReport{
				CPUPercent:    req.CPUPercent,
				MemUsedBytes:  req.MemUsedBytes,
				MemTotalBytes: req.MemTotalBytes,
			}
		}
		if err := s.Store.IngestHeartbeatTelemetry(ctx, nodeID, traffic, metrics); err != nil {
			s.Logger.Error("ingest_heartbeat_telemetry_failed", "error", err)
		}
	}

	desired, err := s.Store.ListDesiredPeersForNode(ctx, nodeID)
	if err != nil {
		s.Logger.Error("list_desired_peers_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not compute desired peers")
		return
	}
	peers := make([]agentHeartbeatPeer, 0, len(desired))
	for _, p := range desired {
		peers = append(peers, agentHeartbeatPeer{
			PublicKey:          p.PublicKey,
			AllowedIPs:         []string{p.AssignedIP + "/32"},
			BandwidthLimitMbps: p.BandwidthLimitMbps,
		})
	}

	writeJSON(w, http.StatusOK, agentHeartbeatResponse{Status: "ok", Peers: peers})
}
