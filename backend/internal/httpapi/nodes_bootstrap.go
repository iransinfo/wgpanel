package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"wgpanel-api/internal/store"
)

type bootstrapSelfNodeRequest struct {
	Name             string  `json:"name"`
	NodeGroup        string  `json:"node_group"`
	Region           string  `json:"region"`
	PublicEndpoint   string  `json:"public_endpoint"`
	WGSubnet         string  `json:"wg_subnet"`
	CapacityMaxPeers int     `json:"capacity_max_peers"`
	PublicKey        *string `json:"public_key"`
}

type bootstrapSelfNodeResponse struct {
	NodeID    string `json:"node_id"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// handleBootstrapSelfNode combines CreateNode + join-token generation into one
// call, gated by X-Internal-Token like /internal/admins - install.sh uses this to
// self-register the panel's own server as its first WireGuard node (docs/STORY-09-
// multi-node-accounts.md), rather than requiring a separate "log in, click Add
// Node, copy a token" round trip for what is otherwise the exact same operation an
// admin would do through the UI. Install-time convenience only; the resulting node
// behaves identically to one created through the admin panel.
func (s *Server) handleBootstrapSelfNode(w http.ResponseWriter, r *http.Request) {
	var req bootstrapSelfNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Name == "" || req.PublicEndpoint == "" || req.WGSubnet == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name, public_endpoint, and wg_subnet are required")
		return
	}
	if req.NodeGroup == "" {
		req.NodeGroup = "default"
	}
	if req.CapacityMaxPeers <= 0 {
		req.CapacityMaxPeers = 250
	}

	ctx := r.Context()
	node, err := s.Store.CreateNode(ctx, req.Name, req.NodeGroup, req.Region, req.PublicEndpoint, req.WGSubnet, req.CapacityMaxPeers, req.PublicKey)
	if errors.Is(err, store.ErrNodeNameTaken) {
		writeJSONError(w, http.StatusConflict, "node_name_taken", "a node with this name already exists")
		return
	}
	if err != nil {
		s.Logger.Error("create_node_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create node")
		return
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		s.Logger.Error("generate_join_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not generate token")
		return
	}
	rawToken := hex.EncodeToString(buf)

	ttlMinutes, err := strconv.Atoi(s.NodeJoinTokenTTLMinutes)
	if err != nil || ttlMinutes <= 0 {
		ttlMinutes = 30
	}
	expiresAt := time.Now().Add(time.Duration(ttlMinutes) * time.Minute)

	if err := s.Store.SetJoinToken(ctx, node.ID, rawToken, expiresAt); err != nil {
		s.Logger.Error("set_join_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not set join token")
		return
	}

	if err := s.Store.InsertAuditLog(ctx, "install.sh", "node.bootstrapped_self", node.ID, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}

	writeJSON(w, http.StatusCreated, bootstrapSelfNodeResponse{
		NodeID:    node.ID,
		Token:     rawToken,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	})
}
