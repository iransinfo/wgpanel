package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"wgpanel-api/internal/caddyadmin"
	"wgpanel-api/internal/store"
)

type settingsResponse struct {
	PublicBaseURL       *string  `json:"public_base_url"`
	DefaultDataQuotaGB  *float64 `json:"default_data_quota_gb"`
	DefaultDeviceLimit  *int     `json:"default_device_limit"`
	DefaultNodeCapacity int      `json:"default_node_capacity"`
	SupportContact      *string  `json:"support_contact"`
	PanelDomain         *string  `json:"panel_domain"`
	ClientDNS           string   `json:"client_dns"`
	SubDomain           *string  `json:"sub_domain"`
	SubPort             *int     `json:"sub_port"`
}

func toSettingsResponse(p store.PanelSettings) settingsResponse {
	return settingsResponse{
		PublicBaseURL:       p.PublicBaseURL,
		DefaultDataQuotaGB:  p.DefaultDataQuotaGB,
		DefaultDeviceLimit:  p.DefaultDeviceLimit,
		DefaultNodeCapacity: p.DefaultNodeCapacity,
		SupportContact:      p.SupportContact,
		PanelDomain:         p.PanelDomain,
		ClientDNS:           p.ClientDNS,
		SubDomain:           p.SubDomain,
		SubPort:             p.SubPort,
	}
}

// handleGetSettings is readable by any admin role (including support) - it's
// configuration display, not a mutating action, so it doesn't need requireRole.
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.GetSettings(r.Context())
	if err != nil {
		s.Logger.Error("get_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch settings")
		return
	}
	writeJSON(w, http.StatusOK, toSettingsResponse(settings))
}

type updateSettingsRequest struct {
	PublicBaseURL       *string  `json:"public_base_url"`
	DefaultDataQuotaGB  *float64 `json:"default_data_quota_gb"`
	DefaultDeviceLimit  *int     `json:"default_device_limit"`
	DefaultNodeCapacity *int     `json:"default_node_capacity"`
	SupportContact      *string  `json:"support_contact"`
	// PanelDomain, when non-nil/non-empty, both persists and triggers a live push to
	// Caddy's admin API (docs/STORY-10-monitoring-and-domain-management.md) - see
	// domain_live_applied/domain_apply_error on the response.
	PanelDomain *string `json:"panel_domain"`
	// ClientDNS is the DNS = line baked into generated wg-quick configs (migration
	// 0018). Non-nil/non-empty replaces it; takes effect on the next config download,
	// not retroactively for configs already handed out.
	ClientDNS *string `json:"client_dns"`
	// SubDomain/SubPort configure the separate subscription origin (migration 0019)
	// and trigger the same live Caddy push as PanelDomain. Unlike the fields above,
	// they are clearable: sub_domain "" turns the feature off, sub_port 0 resets to
	// the default 443 - because "back to off/default" must be expressible where
	// omitted already means "unchanged".
	SubDomain *string `json:"sub_domain"`
	SubPort   *int    `json:"sub_port"`
}

type updateSettingsResponse struct {
	settingsResponse
	// DomainLiveApplied/DomainApplyError are only meaningful when this request
	// included panel_domain, sub_domain or sub_port - both stay zero-valued
	// otherwise. A false/non-nil here does NOT mean the change was rejected: it's
	// still persisted in panel_settings either way, and a later successful update
	// (or the API's startup re-apply) would still apply it. This just tells the
	// admin whether it took effect *live*, right now, without a restart.
	DomainLiveApplied bool    `json:"domain_live_applied"`
	DomainApplyError  *string `json:"domain_apply_error"`
}

// handleUpdateSettings is super_admin-only (wired via requireRole in server.go) -
// these are panel-wide defaults, the same trust tier as API keys and join tokens.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req updateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.DefaultNodeCapacity != nil && *req.DefaultNodeCapacity <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "default_node_capacity must be positive")
		return
	}
	if req.PanelDomain != nil && *req.PanelDomain == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "panel_domain must not be empty")
		return
	}
	if req.ClientDNS != nil && *req.ClientDNS == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "client_dns must not be empty")
		return
	}
	// 0 is the explicit "reset to default 443" sentinel (see updateSettingsRequest).
	if req.SubPort != nil && *req.SubPort != 0 && (*req.SubPort < 1 || *req.SubPort > 65535) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "sub_port must be between 1 and 65535")
		return
	}

	ctx := r.Context()

	settings, err := s.Store.UpdateSettings(ctx, store.UpdateSettingsParams{
		PublicBaseURL:       req.PublicBaseURL,
		DefaultDataQuotaGB:  req.DefaultDataQuotaGB,
		DefaultDeviceLimit:  req.DefaultDeviceLimit,
		DefaultNodeCapacity: req.DefaultNodeCapacity,
		SupportContact:      req.SupportContact,
		PanelDomain:         req.PanelDomain,
		ClientDNS:           req.ClientDNS,
		SubDomain:           req.SubDomain,
		SubPort:             req.SubPort,
	})
	if err != nil {
		s.Logger.Error("update_settings_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update settings")
		return
	}

	if identity, ok := callerIdentityFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "settings.updated", "panel_settings", nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	resp := updateSettingsResponse{settingsResponse: toSettingsResponse(settings)}
	if req.PanelDomain != nil || req.SubDomain != nil || req.SubPort != nil {
		if err := s.applyDomainConfig(ctx, settings); err != nil {
			s.Logger.Error("caddy_apply_domain_config_failed", "error", err)
			msg := err.Error()
			resp.DomainApplyError = &msg
		} else {
			resp.DomainLiveApplied = true
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// domainConfigFromSettings translates the persisted settings row into the Caddy
// config to push. The panel domain falls back to the boot-time PANEL_DOMAIN env
// (Caddy needs a complete config on /load, and on most installs the domain only
// ever lives in deploy/.env - the DB row stays NULL until someone changes it from
// the UI). An error means no valid config can be rendered at all.
func (s *Server) domainConfigFromSettings(settings store.PanelSettings) (caddyadmin.Config, error) {
	domain := s.BootPanelDomain
	if settings.PanelDomain != nil && *settings.PanelDomain != "" {
		domain = *settings.PanelDomain
	}
	if domain == "" {
		return caddyadmin.Config{}, errors.New("no panel domain configured (set one in Settings first)")
	}

	cfg := caddyadmin.Config{Domain: domain, Email: s.AdminACLEmail}
	if settings.SubDomain != nil && *settings.SubDomain != "" {
		cfg.SubDomain = *settings.SubDomain
		if settings.SubPort != nil {
			cfg.SubPort = *settings.SubPort
		}
		// Same domain on the same port would be a duplicate Caddy site block, which
		// Caddy rejects as ambiguous - and it's pointless anyway: the panel's /api/*
		// proxy already serves subscription URLs on the panel origin.
		if cfg.SubDomain == domain && (cfg.SubPort == 0 || cfg.SubPort == 443) {
			return caddyadmin.Config{}, fmt.Errorf("subscription domain %s:443 is already served by the panel - use a different domain or port", domain)
		}
	}
	return cfg, nil
}

func (s *Server) applyDomainConfig(ctx context.Context, settings store.PanelSettings) error {
	cfg, err := s.domainConfigFromSettings(settings)
	if err != nil {
		return err
	}
	return s.CaddyAdmin.Apply(ctx, cfg)
}

// ReapplyDomainConfig re-pushes the UI-managed domain config from the database to
// Caddy after a restart. Caddy always boots from the static deploy/Caddyfile, which
// only knows the .env PANEL_DOMAIN - a panel domain changed from the Settings UI,
// and the subscription origin (which exists ONLY in the database), would otherwise
// silently vanish on every stack restart. Skipped entirely when nothing was ever
// set from the UI, so deployments that manage the domain purely via .env behave
// exactly as before. Best-effort with retries: Caddy may still be starting (compose
// only orders it after the api with service_started), and a deployment without the
// admin socket just logs and moves on, same as a failed live push.
func (s *Server) ReapplyDomainConfig(ctx context.Context) {
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		s.Logger.Error("reapply_domain_config_get_settings_failed", "error", err)
		return
	}
	if settings.PanelDomain == nil && settings.SubDomain == nil {
		return
	}

	const attempts = 5
	for i := 1; i <= attempts; i++ {
		err := s.applyDomainConfig(ctx, settings)
		if err == nil {
			s.Logger.Info("reapply_domain_config_applied")
			return
		}
		s.Logger.Warn("reapply_domain_config_failed", "attempt", i, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
