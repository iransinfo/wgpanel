package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"wgpanel-api/internal/store"
)

type createNodeRequest struct {
	Name             string  `json:"name"`
	NodeGroup        string  `json:"node_group"`
	Region           string  `json:"region"` // optional steering label, e.g. "eu" / "us-east" (migration 0017)
	PublicEndpoint   string  `json:"public_endpoint"`
	WGSubnet         string  `json:"wg_subnet"`
	CapacityMaxPeers int     `json:"capacity_max_peers"`
	PublicKey        *string `json:"public_key"` // the node's own WG server public key, if already known (see migration 0006)
}

type nodeResponse struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	NodeGroup        string  `json:"node_group"`
	Region           string  `json:"region"`
	PublicEndpoint   string  `json:"public_endpoint"`
	WGSubnet         string  `json:"wg_subnet"`
	CapacityMaxPeers int     `json:"capacity_max_peers"`
	Status           string  `json:"status"`
	PublicKey        *string `json:"public_key"`
	CreatedAt        string  `json:"created_at"`
}

func toNodeResponse(n store.Node) nodeResponse {
	return nodeResponse{
		ID:               n.ID,
		Name:             n.Name,
		NodeGroup:        n.NodeGroup,
		Region:           n.Region,
		PublicEndpoint:   n.PublicEndpoint,
		WGSubnet:         n.WGSubnet,
		CapacityMaxPeers: n.CapacityMaxPeers,
		Status:           n.Status,
		PublicKey:        n.PublicKey,
		CreatedAt:        n.CreatedAt.Format(time.RFC3339),
	}
}

// handleCreateNode is admin-only (mounted behind requireAdmin in server.go).
func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var req createNodeRequest
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

	if admin, ok := adminFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, admin.Username, "node.created", node.Name, nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	writeJSON(w, http.StatusCreated, toNodeResponse(node))
}

type updateNodeRequest struct {
	Name             *string `json:"name"`
	NodeGroup        *string `json:"node_group"`
	Region           *string `json:"region"` // "" explicitly clears the region label
	PublicEndpoint   *string `json:"public_endpoint"`
	CapacityMaxPeers *int    `json:"capacity_max_peers"`
}

// handleUpdateNode is operator-or-above, same tier as creating a node. Deliberately
// doesn't allow changing wg_subnet (see store.UpdateNodeParams's doc comment) - the
// admin panel's "edit node" action only ever needed name/group/endpoint/capacity in
// practice, and subnet changes need a bigger re-IP-allocation operation this doesn't
// attempt to build.
func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Name != nil && *req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name must not be empty")
		return
	}
	if req.PublicEndpoint != nil && *req.PublicEndpoint == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "public_endpoint must not be empty")
		return
	}
	if req.CapacityMaxPeers != nil && *req.CapacityMaxPeers <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "capacity_max_peers must be positive")
		return
	}

	ctx := r.Context()
	node, err := s.Store.UpdateNode(ctx, id, store.UpdateNodeParams{
		Name:             req.Name,
		NodeGroup:        req.NodeGroup,
		Region:           req.Region,
		PublicEndpoint:   req.PublicEndpoint,
		CapacityMaxPeers: req.CapacityMaxPeers,
	})
	if errors.Is(err, store.ErrNodeNotFound) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	}
	if errors.Is(err, store.ErrNodeNameTaken) {
		writeJSONError(w, http.StatusConflict, "node_name_taken", "a node with this name already exists")
		return
	}
	if err != nil {
		s.Logger.Error("update_node_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update node")
		return
	}

	if admin, ok := adminFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, admin.Username, "node.updated", node.Name, nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	writeJSON(w, http.StatusOK, toNodeResponse(node))
}

// handleListNodes serves both the admin panel and bot/reseller API keys
// (docs/PRD-telegram-bot-api.md §7: "GET /api/v1/nodes ... for bot to choose or
// display"). An API-key caller only sees nodes in its own node_groups; an admin
// sees everything. This endpoint was admin-only until the STORY audit found bots
// had no way to see nodes at all despite the PRD specifying this exact surface.
func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())

	nodes, err := s.Store.ListNodes(r.Context())
	if err != nil {
		s.Logger.Error("list_nodes_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list nodes")
		return
	}

	out := make([]nodeResponse, 0, len(nodes))
	for _, n := range nodes {
		if !identity.IsAdmin && !stringSliceContains(identity.NodeGroups, n.NodeGroup) {
			continue
		}
		out = append(out, toNodeResponse(n))
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

// handleGetNode - see handleListNodes. A node outside the caller's node_groups is
// reported as 404, matching the same "don't leak existence across scopes" rule
// already applied to accounts (docs/PRD-telegram-bot-api.md §5.2).
func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	identity, _ := callerIdentityFromContext(r.Context())
	id := r.PathValue("id")

	node, err := s.Store.GetNode(r.Context(), id)
	if errors.Is(err, store.ErrNodeNotFound) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_node_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch node")
		return
	}
	if !identity.IsAdmin && !stringSliceContains(identity.NodeGroups, node.NodeGroup) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	}
	writeJSON(w, http.StatusOK, toNodeResponse(node))
}

type joinTokenResponse struct {
	Token     string  `json:"token"`
	ExpiresAt *string `json:"expires_at"` // null for an unlimited token - it never expires
	Unlimited bool    `json:"unlimited"`
	// PanelAddr is the exact host:port install-node.sh must be given as the
	// "control plane address" (panel domain + NODE_AGENT_PORT). Null when the
	// deployment has no domain configured. Shown next to the token because
	// pointing the agent at the panel's WEB port instead is the most common
	// node-onboarding mistake - the agent then gets the SPA's HTML back.
	PanelAddr *string `json:"panel_addr"`
}

// controlPlaneAddr assembles the host:port a node agent must dial: the panel
// domain (live setting, falling back to the boot-time PANEL_DOMAIN, falling back
// to the Host the admin is browsing the panel on right now - an IP-only
// deployment has no configured domain, but the browser's Host reaches this
// server by definition) plus the agent listener's NODE_AGENT_PORT.
func (s *Server) controlPlaneAddr(ctx context.Context, r *http.Request) *string {
	host := s.BootPanelDomain
	if settings, err := s.Store.GetSettings(ctx); err == nil && settings.PanelDomain != nil && *settings.PanelDomain != "" {
		host = *settings.PanelDomain
	}
	if host == "" {
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		} else {
			host = r.Host
		}
	}
	if host == "" || s.NodeAgentPort == "" {
		return nil
	}
	addr := net.JoinHostPort(host, s.NodeAgentPort)
	return &addr
}

type generateJoinTokenRequest struct {
	// Unlimited requests a reusable, non-expiring token instead of the normal
	// single-use/TTL-limited one - see store.SetUnlimitedJoinToken's doc comment for
	// when this is the right choice (re-registering an already-registered node's
	// agent) versus the default (onboarding a brand-new node).
	Unlimited bool `json:"unlimited"`
}

// handleGenerateJoinToken is admin-only. The raw token is returned exactly once;
// only its hash is ever stored (docs/STORY-02-node-directory-join-tokens.md).
func (s *Server) handleGenerateJoinToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	var req generateJoinTokenRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // an empty/absent body just means "not unlimited" - the common case

	if _, err := s.Store.GetNode(ctx, id); errors.Is(err, store.ErrNodeNotFound) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	} else if err != nil {
		s.Logger.Error("get_node_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch node")
		return
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		s.Logger.Error("generate_join_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not generate token")
		return
	}
	rawToken := hex.EncodeToString(buf)

	var expiresAt *string
	if req.Unlimited {
		if err := s.Store.SetUnlimitedJoinToken(ctx, id, rawToken); err != nil {
			s.Logger.Error("set_join_token_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not set join token")
			return
		}
	} else {
		ttlMinutes, err := strconv.Atoi(s.NodeJoinTokenTTLMinutes)
		if err != nil || ttlMinutes <= 0 {
			ttlMinutes = 30
		}
		expiry := time.Now().Add(time.Duration(ttlMinutes) * time.Minute)
		if err := s.Store.SetJoinToken(ctx, id, rawToken, expiry); err != nil {
			s.Logger.Error("set_join_token_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not set join token")
			return
		}
		formatted := expiry.Format(time.RFC3339)
		expiresAt = &formatted
	}

	if admin, ok := adminFromContext(ctx); ok {
		action := "node.join_token_issued"
		if req.Unlimited {
			action = "node.unlimited_join_token_issued"
		}
		if err := s.Store.InsertAuditLog(ctx, admin.Username, action, id, nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	writeJSON(w, http.StatusCreated, joinTokenResponse{
		Token:     rawToken,
		ExpiresAt: expiresAt,
		Unlimited: req.Unlimited,
		PanelAddr: s.controlPlaneAddr(ctx, r),
	})
}

type redeemJoinTokenRequest struct {
	Token string `json:"token"`
}

// handleRedeemJoinToken is NOT behind requireAdmin - the token itself is the
// credential, presented by install-node.sh / the future node agent, not a logged-in
// admin. See docs/STORY-02-node-directory-join-tokens.md.
func (s *Server) handleRedeemJoinToken(w http.ResponseWriter, r *http.Request) {
	var req redeemJoinTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}

	ctx := r.Context()
	node, err := s.Store.RedeemJoinToken(ctx, req.Token)
	if errors.Is(err, store.ErrInvalidOrUsedToken) {
		writeJSONError(w, http.StatusUnauthorized, "invalid_token", "join token is invalid, expired, or already used")
		return
	}
	if err != nil {
		s.Logger.Error("redeem_join_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not redeem token")
		return
	}

	if err := s.Store.InsertAuditLog(ctx, "node-agent", "node.registered", node.ID, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}

	writeJSON(w, http.StatusOK, toNodeResponse(node))
}
