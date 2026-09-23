// Package httpapi wires together the public and /internal HTTP surfaces for the
// control-plane API's walking-skeleton story (docs/STORY-01-control-plane-walking-skeleton.md).
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/redis/go-redis/v9"

	"wgpanel-api/internal/caddyadmin"
	"wgpanel-api/internal/nodeca"
	"wgpanel-api/internal/store"
)

type Server struct {
	Store                   *store.Store
	Redis                   *redis.Client
	CA                      *nodeca.CA
	JWTSecret               string
	InternalAPIToken        string
	NodeJoinTokenTTLMinutes string
	AccountKeyEncryptionKey string
	APIHMACMasterKey        string
	Logger                  *slog.Logger
	// CaddyAdmin/AdminACLEmail (docs/STORY-10-monitoring-and-domain-management.md)
	// push a live domain change to Caddy when set via PATCH /api/v1/settings. Nil
	// CaddyAdmin means this deployment has no Caddy admin socket wired up - domain
	// changes still persist, just without the live push (see handleUpdateSettings).
	CaddyAdmin    *caddyadmin.Client
	AdminACLEmail string
	// BootPanelDomain is the PANEL_DOMAIN env the stack was started with - the
	// fallback panel domain for rendering a complete Caddy config when
	// panel_settings.panel_domain was never set from the UI (a pushed config must
	// always include the panel site block, e.g. when only a subscription domain is
	// being configured). See domainConfigFromSettings.
	BootPanelDomain string
	// CADataDir is where the node-mTLS CA keypair lives on disk (cmd/api's
	// caDataDir) so backup can include it and restore can replace it. Empty
	// disables the CA portion of backup/restore.
	CADataDir string
	// NodeAgentPort is the NODE_AGENT_PORT this API's agent listener is published
	// on - surfaced in the join-token response so the panel can show the exact
	// control-plane address install-node.sh needs (see controlPlaneAddr).
	NodeAgentPort string
}

// Routes builds the full handler tree: public routes (proxied by Caddy), the
// /internal/* group (gated by internalOnly - see middleware.go), and the admin-only
// node/account-management routes (gated by requireAdmin) - wrapped in a logging
// middleware that never logs request/response bodies (see loggingMiddleware).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/refresh", s.handleRefresh)
	mux.HandleFunc("POST /api/v1/nodes/join", s.handleRedeemJoinToken)

	// Subscription endpoints are deliberately unauthenticated: the 192-bit random
	// token in the path IS the credential (a capability URL), so client apps can
	// poll for their current config with no panel account. loggingMiddleware redacts
	// the token from access logs (see redactPath).
	mux.HandleFunc("GET /api/v1/sub/{token}", s.handleSubscriptionConfig)
	mux.HandleFunc("GET /api/v1/sub/{token}/nodes", s.handleSubscriptionNodes)

	mux.Handle("GET /internal/healthz", s.internalOnly(http.HandlerFunc(s.handleInternalHealthz)))
	mux.Handle("POST /internal/admins", s.internalOnly(http.HandlerFunc(s.handleCreateAdmin)))
	// install.sh's core-server self-registration (docs/STORY-09-multi-node-accounts.md) -
	// same X-Internal-Token gate as /internal/admins, since it's the same "trusted
	// install-time bootstrap" trust tier.
	mux.Handle("POST /internal/nodes/bootstrap-self", s.internalOnly(http.HandlerFunc(s.handleBootstrapSelfNode)))

	// Node create is operator-or-above; join-token generation is super-admin-only - it
	// mints new trust into the system, same tier as issuing an API key
	// (docs/PRD-security-access-control.md §7).
	mux.Handle("POST /api/v1/nodes", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleCreateNode))))
	mux.Handle("PATCH /api/v1/nodes/{id}", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleUpdateNode))))
	mux.Handle("POST /api/v1/nodes/{id}/join-token", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleGenerateJoinToken))))
	// Read-only node visibility is shared with bot/reseller API keys (scoped to their
	// node_groups) - docs/PRD-telegram-bot-api.md §7. Mutating node routes stay admin-only.
	mux.Handle("GET /api/v1/nodes", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleListNodes))))
	mux.Handle("GET /api/v1/nodes/{id}", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetNode))))
	// docs/STORY-10-monitoring-and-domain-management.md - node CPU/RAM history chart.
	mux.Handle("GET /api/v1/nodes/{id}/metrics", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetNodeMetrics))))

	// Account endpoints serve both the admin panel (JWT) and bot/reseller clients
	// (HMAC-signed API key) through the same handlers - docs/PRD-account-management.md
	// §1 ("one unambiguous account lifecycle... whether triggered from the admin UI or
	// the bot API"). requirePermission enforces both API-key permission sets and the
	// admin support-role's read-only restriction (docs/PRD-security-access-control.md §4).
	mux.Handle("POST /api/v1/accounts", s.requireAdminOrAPIKey(s.requirePermission("create", http.HandlerFunc(s.handleCreateAccount))))
	mux.Handle("GET /api/v1/accounts", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleListAccounts))))
	mux.Handle("GET /api/v1/accounts/{id}", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetAccount))))
	mux.Handle("PATCH /api/v1/accounts/{id}", s.requireAdminOrAPIKey(s.requirePermission("update", http.HandlerFunc(s.handleUpdateAccount))))
	mux.Handle("POST /api/v1/accounts/{id}/suspend", s.requireAdminOrAPIKey(s.requirePermission("suspend", http.HandlerFunc(s.handleSuspendAccount))))
	mux.Handle("POST /api/v1/accounts/{id}/enable", s.requireAdminOrAPIKey(s.requirePermission("suspend", http.HandlerFunc(s.handleEnableAccount))))
	mux.Handle("POST /api/v1/accounts/{id}/renew", s.requireAdminOrAPIKey(s.requirePermission("update", http.HandlerFunc(s.handleRenewAccount))))
	mux.Handle("DELETE /api/v1/accounts/{id}", s.requireAdminOrAPIKey(s.requirePermission("delete", http.HandlerFunc(s.handleDeleteAccount))))
	mux.Handle("GET /api/v1/accounts/{id}/config", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetAccountConfig))))
	// docs/STORY-10-monitoring-and-domain-management.md - account usage-over-time chart.
	mux.Handle("GET /api/v1/accounts/{id}/usage", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleGetAccountUsage))))
	// Device tracking (PRD-account-management.md §6.4) and node steering - read-tier,
	// shared with bot API keys like every other account read.
	mux.Handle("GET /api/v1/accounts/{id}/devices", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleListAccountDevices))))
	mux.Handle("GET /api/v1/accounts/{id}/steer", s.requireAdminOrAPIKey(s.requirePermission("read", http.HandlerFunc(s.handleSteerAccount))))
	// Rotating the subscription URL is an account mutation, same tier as PATCH.
	mux.Handle("POST /api/v1/accounts/{id}/subscription/rotate", s.requireAdminOrAPIKey(s.requirePermission("update", http.HandlerFunc(s.handleRotateSubscriptionToken))))

	// API key issuance is super-admin-only, same reasoning as join-token generation above.
	mux.Handle("POST /api/v1/api-keys", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleCreateAPIKey))))
	mux.Handle("GET /api/v1/api-keys", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleListAPIKeys))))
	mux.Handle("POST /api/v1/api-keys/{id}/revoke", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleRevokeAPIKey))))
	mux.Handle("POST /api/v1/api-keys/{id}/rotate", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleRotateAPIKey))))

	// Admin user management (PRD-admin-panel-ux.md §3.6) - super-admin-only, same tier
	// as API keys and join tokens: all three mint new trust into the system.
	mux.Handle("POST /api/v1/admins", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleCreateAdminUser))))
	mux.Handle("GET /api/v1/admins", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleListAdminUsers))))
	mux.Handle("PATCH /api/v1/admins/{id}", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdateAdminUser))))
	mux.Handle("DELETE /api/v1/admins/{id}", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleDeleteAdminUser))))

	// Audit Log (PRD-admin-panel-ux.md §3.7) - a global view, super-admin-only.
	mux.Handle("GET /api/v1/audit-log", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleListAuditLog))))

	// Panel-wide Settings (docs/STORY-08-settings-and-bootstrap-admin.md) - readable by
	// any admin role (it's just configuration display), editable only by super_admin
	// since these are panel-wide defaults, same trust tier as API keys/join tokens.
	mux.Handle("GET /api/v1/settings", s.requireAdmin(http.HandlerFunc(s.handleGetSettings)))
	mux.Handle("PATCH /api/v1/settings", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleUpdateSettings))))

	// Backup & restore (see backup.go's doc comment) - both directions are
	// super_admin-only: the decrypted file contains admin password hashes, the CA
	// private key, the .env encryption keys and every subscription token, and
	// restore replaces ALL panel state. Download is a POST because the encryption
	// password rides in the request body.
	mux.Handle("POST /api/v1/backup", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleDownloadBackup))))
	mux.Handle("POST /api/v1/backup/restore", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleRestoreBackup))))

	return s.loggingMiddleware(mux)
}
