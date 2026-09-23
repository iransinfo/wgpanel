// Package config loads the control-plane API's configuration from environment
// variables. The variable names here must match deploy/docker-compose.yml exactly.
package config

import (
	"fmt"
	"os"
)

type Config struct {
	PostgresDSN              string
	RedisURL                 string
	JWTSecret                string
	APIHMACMasterKey         string
	NodeAgentPort            string
	NodeAgentJoinTokenTTLMin string
	InternalAPIToken         string
	AccountKeyEncryptionKey  string
	HTTPPort                 string
	AdminBootstrapUsername   string
	// CaddyAdminSocket/AdminACLEmail (docs/STORY-10-monitoring-and-domain-management.md)
	// are optional - if the socket path doesn't exist (e.g. a dev stack without Caddy,
	// or an install that never mounted the shared volume), live domain push is simply
	// skipped and a Settings domain change only persists to the database, exactly like
	// before this feature existed. Neither is in `required` below.
	CaddyAdminSocket string
	AdminACLEmail    string
	// PanelDomain is the boot-time domain Caddy was started with, used only as the
	// fallback for rendering a complete config on live domain pushes (see
	// httpapi.Server.BootPanelDomain). Optional for the same reason as the two above.
	PanelDomain string
}

// Load reads and validates configuration from the environment. It fails fast on
// any missing required variable rather than starting with a zero-value secret.
func Load() (Config, error) {
	cfg := Config{
		PostgresDSN:              os.Getenv("POSTGRES_DSN"),
		RedisURL:                 os.Getenv("REDIS_URL"),
		JWTSecret:                os.Getenv("JWT_SECRET"),
		APIHMACMasterKey:         os.Getenv("API_HMAC_MASTER_KEY"),
		NodeAgentPort:            os.Getenv("NODE_AGENT_PORT"),
		NodeAgentJoinTokenTTLMin: os.Getenv("NODE_AGENT_JOIN_TOKEN_TTL_MIN"),
		InternalAPIToken:         os.Getenv("INTERNAL_API_TOKEN"),
		AccountKeyEncryptionKey:  os.Getenv("ACCOUNT_KEY_ENCRYPTION_KEY"),
		HTTPPort:                 envOrDefault("HTTP_PORT", "8080"),
		AdminBootstrapUsername:   envOrDefault("ADMIN_BOOTSTRAP_USERNAME", "admin"),
		CaddyAdminSocket:         envOrDefault("CADDY_ADMIN_SOCKET", "/admin/caddy-admin.sock"),
		AdminACLEmail:            os.Getenv("ADMIN_ACL_EMAIL"),
		PanelDomain:              os.Getenv("PANEL_DOMAIN"),
	}

	required := map[string]string{
		"POSTGRES_DSN":               cfg.PostgresDSN,
		"REDIS_URL":                  cfg.RedisURL,
		"JWT_SECRET":                 cfg.JWTSecret,
		"API_HMAC_MASTER_KEY":        cfg.APIHMACMasterKey,
		"INTERNAL_API_TOKEN":         cfg.InternalAPIToken,
		"ACCOUNT_KEY_ENCRYPTION_KEY": cfg.AccountKeyEncryptionKey,
	}
	for name, val := range required {
		if val == "" {
			return Config{}, fmt.Errorf("missing required environment variable %s", name)
		}
	}

	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
