// Command agent is the WGPanel node agent - runs on each WireGuard server,
// registers with the control plane using a join token, then heartbeats over
// HTTPS+mTLS (docs/STORY-04-node-agent-mtls.md). Installed by deploy/install-node.sh.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	id, err := loadOrRegister(cfg, logger)
	if err != nil {
		return err
	}
	logger.Info("registered", "node_id", id.NodeID)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runHeartbeatLoop(ctx, cfg, id, logger)
	return nil
}
