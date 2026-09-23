package main

import (
	"fmt"
	"os"
)

// config reads the env vars deploy/install-node.sh writes to agent.env
// (docs/STORY-04-node-agent-mtls.md). WGPublicKey was added once install-node.sh
// started actually creating the server's WireGuard interface instead of just
// installing the agent - see setup_wireguard() there.
type config struct {
	PanelAddr   string
	JoinToken   string
	NodeName    string
	WGInterface string
	WGPublicKey string // this node's own WireGuard server public key, if install-node.sh generated one
	StateDir    string
}

func loadConfig() (config, error) {
	cfg := config{
		PanelAddr:   os.Getenv("WGPANEL_PANEL_ADDR"),
		JoinToken:   os.Getenv("WGPANEL_JOIN_TOKEN"),
		NodeName:    os.Getenv("WGPANEL_NODE_NAME"),
		WGInterface: envOrDefault("WGPANEL_WG_INTERFACE", "wg0"),
		WGPublicKey: os.Getenv("WGPANEL_WG_PUBLIC_KEY"),
		StateDir:    envOrDefault("WGPANEL_STATE_DIR", "/etc/wgpanel/state"),
	}
	if cfg.PanelAddr == "" {
		return config{}, fmt.Errorf("WGPANEL_PANEL_ADDR is required (host:port of the control plane)")
	}
	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
