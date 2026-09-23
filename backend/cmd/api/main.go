// Command api is the WGPanel control-plane API - the walking-skeleton story
// (docs/STORY-01-control-plane-walking-skeleton.md).
package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/caddyadmin"
	"wgpanel-api/internal/config"
	"wgpanel-api/internal/httpapi"
	"wgpanel-api/internal/nodeca"
	"wgpanel-api/internal/redisclient"
	"wgpanel-api/internal/store"
)

// caDataDir is where the internal CA persists its keypair - the /data volume already
// mounted in deploy/docker-compose.yml (wgpanel_api_data).
const caDataDir = "/data/ca"

func main() {
	// healthcheck subcommand: `wgpanel-api healthcheck` hits the local /healthz and
	// exits 0/1, so Docker's HEALTHCHECK doesn't need wget/curl in the final image
	// (deploy/docker-compose.yml task D13/D14).
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return err
	}

	if err := bootstrapFirstAdmin(ctx, db, logger, cfg.AdminBootstrapUsername); err != nil {
		return fmt.Errorf("bootstrap first admin: %w", err)
	}

	rdb, err := redisclient.Open(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()

	ca, err := nodeca.LoadOrCreate(caDataDir)
	if err != nil {
		return fmt.Errorf("load/create internal CA: %w", err)
	}

	srv := &httpapi.Server{
		Store:                   db,
		Redis:                   rdb,
		CA:                      ca,
		JWTSecret:               cfg.JWTSecret,
		InternalAPIToken:        cfg.InternalAPIToken,
		NodeJoinTokenTTLMinutes: cfg.NodeAgentJoinTokenTTLMin,
		AccountKeyEncryptionKey: cfg.AccountKeyEncryptionKey,
		APIHMACMasterKey:        cfg.APIHMACMasterKey,
		Logger:                  logger,
		// Always constructed - caddyadmin.Client only fails at call time (a dial
		// against a socket that doesn't exist), not at construction, so this is safe
		// even in deployments with no Caddy/no shared admin-socket volume.
		CaddyAdmin:      caddyadmin.New(cfg.CaddyAdminSocket),
		AdminACLEmail:   cfg.AdminACLEmail,
		BootPanelDomain: cfg.PanelDomain,
		CADataDir:       caDataDir,
		NodeAgentPort:   cfg.NodeAgentPort,
	}

	httpServer := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	agentTLSConfig, err := srv.AgentTLSConfig()
	if err != nil {
		return fmt.Errorf("build agent TLS config: %w", err)
	}
	agentServer := &http.Server{
		Addr:              ":" + cfg.NodeAgentPort,
		Handler:           srv.AgentRoutes(),
		TLSConfig:         agentTLSConfig,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go srv.RunOfflineSweepLoop(ctx)
	// Re-push any UI-managed domain config (panel domain changed from Settings, or
	// the DB-only subscription origin) to Caddy, which always boots from the static
	// Caddyfile - see ReapplyDomainConfig for why this must happen on every start.
	go srv.ReapplyDomainConfig(ctx)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "port", cfg.HTTPPort)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		logger.Info("agent_listening", "port", cfg.NodeAgentPort)
		if err := agentServer.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = agentServer.Shutdown(shutdownCtx)
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// bootstrapFirstAdmin guarantees a super_admin exists after the very first successful
// boot against a fresh database, rather than relying on install.sh's follow-up CLI call
// racing against migrations/health (see docs/STORY-08-settings-and-bootstrap-admin.md).
// Gated on AdminCount == 0 so it only ever fires once per database - re-running the API,
// or calling `wgpanel create-admin` afterward for additional operators, never re-triggers
// it. The generated password is shown exactly once, matching how API key secrets are
// handled elsewhere in this project - it is never persisted anywhere but the argon2id
// hash in the admins table.
func bootstrapFirstAdmin(ctx context.Context, db *store.Store, logger *slog.Logger, username string) error {
	count, err := db.AdminCount(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	password, err := randomPassword()
	if err != nil {
		return fmt.Errorf("generate password: %w", err)
	}
	hash, err := authcrypto.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if _, err := db.CreateAdmin(ctx, username, hash, "super_admin"); err != nil {
		return fmt.Errorf("create admin: %w", err)
	}

	logger.Warn("bootstrap_admin_created", "username", username)

	// Deliberately also a plain, un-JSON-encoded banner: this is a one-time secret meant
	// for a human reading `docker compose logs api` / `wgpanel logs api` right after
	// install, not a structured log field to be scraped and stored by a log aggregator.
	fmt.Fprintln(os.Stdout, "==================================================================")
	fmt.Fprintln(os.Stdout, " WGPanel: initial super admin created - save these credentials now")
	fmt.Fprintln(os.Stdout, " (shown once - they are never stored or displayed again)")
	fmt.Fprintln(os.Stdout, "==================================================================")
	fmt.Fprintf(os.Stdout, " WGPANEL_INITIAL_ADMIN_USERNAME=%s\n", username)
	fmt.Fprintf(os.Stdout, " WGPANEL_INITIAL_ADMIN_PASSWORD=%s\n", password)
	fmt.Fprintln(os.Stdout, "==================================================================")

	return nil
}

// randomPassword returns a 20-character base32 (Crockford-ish, no padding) secret -
// upper/lower alphanumeric only, so it's safe to eyeball and retype from a terminal
// without worrying about shell-special characters.
func randomPassword() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)), nil
}

func runHealthcheck() int {
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8080"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://localhost:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
