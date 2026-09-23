// Package caddyadmin pushes a live domain change to Caddy's admin API over a Unix
// domain socket (docs/STORY-10-monitoring-and-domain-management.md, Part 2).
//
// A Unix socket, not TCP, is deliberate: Caddy's admin API has no built-in
// authentication of its own - reachability alone is authorization to fully
// reconfigure the reverse proxy (including routing to /internal/* and bypassing
// INTERNAL_API_TOKEN) or read back issued TLS private keys. A socket file shared
// only between the caddy and api containers (never frontend, a weaker trust
// boundary - see deploy/docker-compose.yml's wgpanel_caddy_admin volume) scopes
// reachability to filesystem/mount permissions instead of "anything on the compose
// network can do this."
//
// Apply re-POSTs Caddy's *entire* config via its documented config-adapter
// support on /load (Content-Type: text/caddyfile lets Caddy's own Caddyfile adapter
// convert it, the same mechanism `caddy reload` uses) rather than surgically
// patching a JSON path - Caddy diffs the new config against the running one and only
// touches what changed (e.g. provisioning a cert for a newly added domain), so this
// is both simpler and more robust than tracking positional JSON paths that would
// silently drift if the Caddyfile's structure ever changes for an unrelated reason.
package caddyadmin

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"text/template"
	"time"
)

// caddyfileTemplate mirrors deploy/Caddyfile's structure exactly for the panel site
// block (proxy targets are fixed - only the domains, port and ACME account email are
// ever pushed live). Keep the two in sync if either changes. The subscription block
// has no static-Caddyfile counterpart: it exists only in the database, so it is
// pushed live on settings changes and re-applied from the DB on API startup (see
// httpapi.ReapplyDomainConfig).
//
// The subscription block deliberately serves ONLY /api/v1/sub/* and 404s everything
// else: the whole point of a separate subscription origin is that the links handed
// to end users neither reveal nor expose the admin panel, so nothing but the
// capability endpoints may answer on it.
var caddyfileTemplate = template.Must(template.New("caddyfile").Parse(`{
	admin unix/{{.AdminSocket}}|0666
}

{{.Domain}} {
	tls {{.Email}}

	handle /api/* {
		reverse_proxy api:8080
	}

	handle {
		reverse_proxy frontend:80
	}
}
{{if .SubAddress}}
{{.SubAddress}} {
	tls {{.Email}}

	handle /api/v1/sub/* {
		reverse_proxy api:8080
	}

	handle {
		respond 404
	}
}
{{end}}`))

// Client talks to Caddy's admin API over a Unix domain socket.
type Client struct {
	httpClient *http.Client
	socketPath string
}

// New builds a Client for the admin API socket at socketPath. It does not verify
// the socket exists yet - Apply's caller is expected to treat a dial failure as
// "Caddy isn't wired up in this deployment" and degrade gracefully, not fatally.
func New(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// Config is what Apply renders into a full Caddyfile: the panel's domain plus the
// optional separate subscription origin.
type Config struct {
	Domain string
	Email  string
	// SubDomain, when non-empty, adds a second site block serving only the
	// subscription capability endpoints (/api/v1/sub/*) on its own domain, with its
	// own automatically-provisioned certificate.
	SubDomain string
	// SubPort is the public HTTPS port for the subscription block. 0 and 443 both
	// mean the default (no explicit port in the site address, so it shares :443 with
	// the panel block via SNI). Any other port must also be published host-side -
	// see deploy/docker-compose.yml's SUB_PORT mapping.
	SubPort int
}

// subAddress is the Caddy site address for the subscription block, or "" when the
// feature is off.
func (c Config) subAddress() string {
	if c.SubDomain == "" {
		return ""
	}
	if c.SubPort != 0 && c.SubPort != 443 {
		return fmt.Sprintf("%s:%d", c.SubDomain, c.SubPort)
	}
	return c.SubDomain
}

// Apply pushes a full Caddyfile-derived config live via Caddy's /load endpoint.
// Returns an error if the socket is unreachable or Caddy rejects the config -
// callers should log this, not fail the whole settings update (the values are still
// persisted in panel_settings either way; a later successful call, or the API's
// startup re-apply, would apply them).
func (c *Client) Apply(ctx context.Context, cfg Config) error {
	if cfg.Domain == "" {
		return fmt.Errorf("domain must not be empty")
	}

	var body bytes.Buffer
	if err := caddyfileTemplate.Execute(&body, struct{ AdminSocket, Domain, Email, SubAddress string }{
		AdminSocket: c.socketPath,
		Domain:      cfg.Domain,
		Email:       cfg.Email,
		SubAddress:  cfg.subAddress(),
	}); err != nil {
		return fmt.Errorf("render caddyfile: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/load", &body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "text/caddyfile")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call caddy admin api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("caddy admin api rejected config: status %d", resp.StatusCode)
	}
	return nil
}
