package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"wgpanel-api/internal/steering"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// newSubscriptionToken mints the per-account capability for GET /api/v1/sub/{token}:
// 24 random bytes hex-encoded (48 chars, 192 bits) - far beyond online-guessing reach,
// which is the entire security model of an unauthenticated capability URL.
func newSubscriptionToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

var errNodeMissingPublicKey = errors.New("node has no public key")

// renderPeerConfig renders the ready-to-import wg-quick config for one of an
// account's peers - shared by the admin/bot config endpoint and the subscription
// endpoint so the two can never drift apart on tunnel parameters. See
// handleGetAccountConfig's doc comments for why DNS and MTU=1280 are set the way
// they are - those hard-won notes live on the template below.
func (s *Server) renderPeerConfig(ctx context.Context, accountID string, ns *string, target store.AccountPeerWithNode) (string, error) {
	if target.NodePublicKey == nil || *target.NodePublicKey == "" {
		return "", errNodeMissingPublicKey
	}

	encryptedPriv, err := s.Store.GetAccountPrivateKey(ctx, accountID, ns)
	if err != nil {
		return "", err
	}
	privateKey, err := wgkeys.Decrypt(s.AccountKeyEncryptionKey, encryptedPriv)
	if err != nil {
		return "", err
	}

	// DNS is required (full tunnel makes the client's original resolver unreachable)
	// and MTU 1280 is the safe floor that avoids black-hole fragmentation on paths
	// whose real MTU is below WireGuard's optimistic default - both found the hard way
	// against real clients (see the original notes in handleGetAccountConfig's history,
	// docs/STORY-09/STORY-10). The DNS server itself is a panel setting (migration
	// 0018), because the right resolver depends on where the exit node egresses: the
	// Cloudflare default is unreachable on networks that filter it, which presents as
	// "connected but no internet" - the tunnel works, name resolution silently doesn't.
	dns := "1.1.1.1, 1.0.0.1"
	if settings, err := s.Store.GetSettings(ctx); err == nil && settings.ClientDNS != "" {
		dns = settings.ClientDNS
	} else if err != nil {
		// A settings read failure shouldn't block config delivery - fall back to the
		// historical default rather than failing the whole request.
		s.Logger.Warn("get_settings_for_config_dns_failed", "error", err)
	}

	// AllowedIPs carries BOTH families on purpose. Without ::/0, a dual-stack client
	// (most mobile carriers, many home ISPs) keeps its native IPv6 route outside the
	// tunnel, so every IPv6-capable site sees the client's real address while the user
	// believes they're fully tunneled - the classic commercial-VPN IPv6 leak. The nodes
	// currently egress IPv4-only, so including ::/0 makes the client route its IPv6 into
	// the tunnel where it is dropped (black-holed) rather than leaked: the client falls
	// back to IPv4 and no traffic escapes unprotected. If/when nodes gain real IPv6
	// egress (v6 address + NAT66), this line already carries it end to end - no client
	// reconfig needed.
	return fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/32
DNS = %s
MTU = 1280

[Peer]
PublicKey = %s
Endpoint = %s
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
`, privateKey, target.AssignedIP, dns, *target.NodePublicKey, target.NodePublicEndpoint), nil
}

// resolveSubscriptionAccount is the shared front half of both /sub/{token} handlers:
// token -> account, with suspended/expired accounts refused. Writes the error
// response itself and returns ok=false when the caller should just return.
func (s *Server) resolveSubscriptionAccount(w http.ResponseWriter, r *http.Request) (store.Account, bool) {
	token := r.PathValue("token")
	account, err := s.Store.GetAccountBySubscriptionToken(r.Context(), token)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", "unknown subscription")
		return store.Account{}, false
	}
	if err != nil {
		s.Logger.Error("subscription_lookup_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not resolve subscription")
		return store.Account{}, false
	}
	if account.Status != "active" {
		// The client app gets a clear, machine-readable reason instead of a config
		// that silently stopped handshaking (the agent already dropped the peer).
		writeJSONError(w, http.StatusForbidden, "account_suspended", "this account is currently suspended")
		return store.Account{}, false
	}
	return account, true
}

// handleSubscriptionConfig serves the account's current wg-quick config to anyone
// holding the subscription token - no panel credentials involved. Node choice:
// ?node_id= pins one explicitly; otherwise steering picks the currently-best node
// (optionally biased by ?region=), which is exactly what makes the URL a "always
// fetch a working config" endpoint rather than a static file.
func (s *Server) handleSubscriptionConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account, ok := s.resolveSubscriptionAccount(w, r)
	if !ok {
		return
	}

	peers, err := s.Store.ListAccountPeersWithNode(ctx, account.ID, nil)
	if err != nil {
		s.Logger.Error("list_account_peers_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch account peers")
		return
	}
	if len(peers) == 0 {
		writeJSONError(w, http.StatusConflict, "no_peers", "this account has no node peers yet")
		return
	}

	target, ok := s.pickSubscriptionPeer(ctx, w, account.ID, peers, r.URL.Query().Get("node_id"), r.URL.Query().Get("region"))
	if !ok {
		return
	}

	config, err := s.renderPeerConfig(ctx, account.ID, nil, target)
	if errors.Is(err, errNodeMissingPublicKey) {
		writeJSONError(w, http.StatusConflict, "node_missing_public_key", "the selected node has no public key set yet")
		return
	}
	if err != nil {
		s.Logger.Error("render_subscription_config_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not render config")
		return
	}

	// application/octet-stream, NOT text/plain: this endpoint is downloaded straight
	// from mobile browsers, and Android Chrome renames text/plain attachments whose
	// extension it doesn't associate with that type - "x.conf" would land in Downloads
	// as "x.conf.txt", which the WireGuard app refuses to import.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", confFilename(account.Label)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(config))
}

// pickSubscriptionPeer chooses which of the account's peers to render: an explicit
// ?node_id= wins; otherwise the steering rank's recommendation. Falls back to the
// first peer if steering has no candidates (every node still missing its public key -
// the render step then reports that specific problem rather than a generic one).
func (s *Server) pickSubscriptionPeer(ctx context.Context, w http.ResponseWriter, accountID string, peers []store.AccountPeerWithNode, nodeIDParam, region string) (store.AccountPeerWithNode, bool) {
	if nodeIDParam != "" {
		for _, p := range peers {
			if p.NodeID == nodeIDParam {
				return p, true
			}
		}
		writeJSONError(w, http.StatusNotFound, "node_not_found", "this account has no peer on that node")
		return store.AccountPeerWithNode{}, false
	}

	candidates, err := s.Store.SteerCandidatesForAccount(ctx, accountID, nil)
	if err != nil {
		s.Logger.Error("steer_candidates_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not select a node")
		return store.AccountPeerWithNode{}, false
	}
	ranked := steering.Rank(candidates, region)
	if len(ranked) > 0 {
		for _, p := range peers {
			if p.NodeID == ranked[0].NodeID {
				return p, true
			}
		}
	}
	return peers[0], true
}

var confFilenameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// confFilename derives a safe attachment filename from the account label. WireGuard
// clients derive the tunnel name from this, and wg-quick tunnel names max out at 15
// chars - keep the stem within that so imports don't fail or get truncated oddly.
func confFilename(label string) string {
	stem := confFilenameUnsafe.ReplaceAllString(strings.ToLower(label), "-")
	if len(stem) > 15 {
		stem = stem[:15]
	}
	// Trim AFTER truncating so a cut that lands on a separator doesn't leave a trailing
	// "-"/"." right before the extension (e.g. "very-long-name-.conf").
	stem = strings.Trim(stem, "-.")
	if stem == "" {
		stem = "wgpanel"
	}
	return stem + ".conf"
}

type subscriptionNodeResponse struct {
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	Region      string `json:"region"`
	Online      bool   `json:"online"`
	Recommended bool   `json:"recommended"`
	// ConfigPath is the ready-to-fetch path for this specific node's config, so a
	// client app can offer a node picker without constructing URLs itself.
	ConfigPath string `json:"config_path"`
}

// handleSubscriptionNodes lets a client app enumerate the nodes available to this
// subscription (steering-ranked, best first) - the machine-readable companion to
// handleSubscriptionConfig's "just give me the best one".
func (s *Server) handleSubscriptionNodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account, ok := s.resolveSubscriptionAccount(w, r)
	if !ok {
		return
	}

	candidates, err := s.Store.SteerCandidatesForAccount(ctx, account.ID, nil)
	if err != nil {
		s.Logger.Error("steer_candidates_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list nodes")
		return
	}
	ranked := steering.Rank(candidates, r.URL.Query().Get("region"))

	out := make([]subscriptionNodeResponse, 0, len(ranked))
	for _, c := range ranked {
		out = append(out, subscriptionNodeResponse{
			NodeID:      c.NodeID,
			Name:        c.Name,
			Region:      c.Region,
			Online:      c.Online,
			Recommended: c.Recommended,
			ConfigPath:  "/api/v1/sub/" + account.SubscriptionToken + "?node_id=" + c.NodeID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

// handleRotateSubscriptionToken invalidates the account's current subscription URL
// and mints a new one - the recovery path for a leaked/shared link. Update-tier
// permission, same as every other account mutation.
func (s *Server) handleRotateSubscriptionToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)

	newToken, err := newSubscriptionToken()
	if err != nil {
		s.Logger.Error("generate_subscription_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not generate token")
		return
	}

	account, err := s.Store.RotateSubscriptionToken(ctx, r.PathValue("id"), ns, newToken)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	}
	if err != nil {
		s.Logger.Error("rotate_subscription_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not rotate subscription token")
		return
	}

	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.subscription_rotated", account.ID, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	s.respondWithAccount(w, r, http.StatusOK, account, ns)
}
