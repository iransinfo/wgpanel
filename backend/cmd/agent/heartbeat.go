package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	heartbeatInterval      = 10 * time.Second
	controlPlaneServerName = "wgpanel-control-plane" // matches nodeca.ServerName - not real DNS, see STORY-04

	// metricsEveryNTicks controls node CPU/RAM reporting cadence - deliberately
	// lower-frequency than the 10s peer-reconciliation tick (docs/STORY-10-monitoring-
	// and-domain-management.md): a dashboard health chart doesn't need 10s resolution,
	// and this cuts node_metrics' row volume 4x for free without touching the timing
	// suspend/quota enforcement latency actually depends on.
	metricsEveryNTicks = 4
)

type heartbeatPeerTraffic struct {
	PublicKey     string     `json:"public_key"`
	ReceiveBytes  int64      `json:"rx_bytes"`
	TransmitBytes int64      `json:"tx_bytes"`
	LastHandshake *time.Time `json:"last_handshake,omitempty"`
	// Endpoint is the peer's current client source "ip:port" from the same kernel
	// snapshot as the counters - the control plane's device-tracking input
	// (PRD-account-management.md §6.4). Omitted when the peer has never connected.
	Endpoint string `json:"endpoint,omitempty"`
}

type heartbeatRequest struct {
	PeerCount     int                    `json:"peer_count"`
	LoadAvg       float64                `json:"load_avg"`
	Traffic       []heartbeatPeerTraffic `json:"traffic,omitempty"`
	CPUPercent    *float32               `json:"cpu_percent,omitempty"`
	MemUsedBytes  *int64                 `json:"mem_used_bytes,omitempty"`
	MemTotalBytes *int64                 `json:"mem_total_bytes,omitempty"`
}

type heartbeatPeer struct {
	PublicKey  string   `json:"public_key"`
	AllowedIPs []string `json:"allowed_ips"`
	// BandwidthLimitMbps: per-account rate limit the control plane wants enforced on
	// this peer via tc (see tc.go); nil = unshaped.
	BandwidthLimitMbps *int `json:"bandwidth_limit_mbps"`
}

type heartbeatResponse struct {
	Status string          `json:"status"`
	Peers  []heartbeatPeer `json:"peers"`
}

func runHeartbeatLoop(ctx context.Context, cfg config, id *identity, logger *slog.Logger) {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{id.TLSCert},
				RootCAs:      id.CACertPool,
				ServerName:   controlPlaneServerName,
			},
		},
		Timeout: 15 * time.Second,
	}

	state := &heartbeatState{}
	state.tick(ctx, cfg, client, logger) // don't wait a full interval before the first one

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state.tick(ctx, cfg, client, logger)
		}
	}
}

// heartbeatState carries the previous tick's CPU sample (for computing a percentage
// from the delta) and a tick counter (for the lower-frequency metrics cadence) across
// heartbeat calls - both are meaningless on a single isolated reading.
//
// lastAppliedPeerSignature is the other piece of cross-tick state: it's what makes
// reconcilePeers idempotent-in-practice, not just idempotent-in-theory. See
// peerSignature's doc comment for why this exists - a real, previously-undetected
// bug (found only once a real client kept a session open across multiple heartbeat
// ticks to actually verify it, which nothing before this ever did).
type heartbeatState struct {
	tickCount                int
	prevCPU                  cpuSample
	havePrevCPU              bool
	lastAppliedPeerSignature string
}

func (s *heartbeatState) tick(ctx context.Context, cfg config, client *http.Client, logger *slog.Logger) {
	s.tickCount++

	snapshot, snapshotOK := readDeviceSnapshot(cfg.WGInterface)
	peerCount := 0
	var traffic []heartbeatPeerTraffic
	if snapshotOK {
		peerCount = len(snapshot.peers)
		for _, p := range snapshot.peers {
			t := heartbeatPeerTraffic{
				PublicKey:     p.PublicKey.String(),
				ReceiveBytes:  p.ReceiveBytes,
				TransmitBytes: p.TransmitBytes,
			}
			if !p.LastHandshakeTime.IsZero() {
				hs := p.LastHandshakeTime
				t.LastHandshake = &hs
			}
			if p.Endpoint != nil {
				t.Endpoint = p.Endpoint.String()
			}
			traffic = append(traffic, t)
		}
	}

	req := heartbeatRequest{PeerCount: peerCount, LoadAvg: 0, Traffic: traffic}
	if s.tickCount%metricsEveryNTicks == 1 {
		req.CPUPercent = s.readCPUPercent()
		if used, total, ok := readMemInfo(); ok {
			req.MemUsedBytes = &used
			req.MemTotalBytes = &total
		}
	}

	resp, ok := sendHeartbeat(ctx, cfg, client, logger, req, peerCount)
	if !ok {
		return
	}

	// Only actually touch the interface when the desired peer set changed since the
	// last tick we successfully applied - see peerSignature's doc comment for why
	// reapplying an unchanged set on every ~10s tick is not the harmless no-op it
	// looks like.
	sig := peerSignature(resp.Peers)
	if sig == s.lastAppliedPeerSignature {
		return
	}
	if err := reconcilePeers(cfg.WGInterface, resp.Peers); err != nil {
		logger.Warn("reconcile_peers_failed", "error", err)
		return
	}
	// Shaping failure (e.g. no tc binary on an old bare-metal install) must not hold
	// the signature back: that would force reconcilePeers to reapply ReplacePeers
	// every 10s - exactly the handshake-resetting behavior peerSignature exists to
	// prevent. Peers stay correct, just without rate enforcement, and the warn says so.
	if err := applyShaping(cfg.WGInterface, resp.Peers, logger); err != nil {
		logger.Warn("apply_shaping_failed", "error", err)
	}
	s.lastAppliedPeerSignature = sig
}

// peerSignature builds a stable (sorted, so response-ordering differences don't
// matter) string identity of a desired-peer list, used to skip reapplying
// ConfigureDevice when nothing has actually changed since the last tick.
//
// This exists because of a real bug found verifying this project's very first
// sustained real client connection (everything before this was either a
// single-shot curl test or a client that disconnected within one heartbeat tick):
// reconcilePeers's own doc comment claimed reapplying an unchanged peer list via
// ConfigureDevice(ReplacePeers: true) every ~10s tick was "the same semantics as
// `wg syncconf` ... unchanged peers aren't touched/re-handshaked" - that assumption
// turned out to be wrong in practice (observed directly: a peer's handshake time
// and learned endpoint reset to zero/none within a live session, tracking the
// heartbeat cadence, even though its public key and allowed-ips never changed
// tick to tick). Skipping the reapply entirely when the desired set is unchanged
// sidesteps whatever layer (this kernel's wireguard module, or wgctrl's netlink
// encoding of an unchanged ReplacePeers call) is actually causing that, rather than
// depending on a "should be a no-op" assumption that measurably wasn't one.
func peerSignature(peers []heartbeatPeer) string {
	entries := make([]string, 0, len(peers))
	for _, p := range peers {
		allowedIPs := append([]string(nil), p.AllowedIPs...)
		sort.Strings(allowedIPs)
		// The bandwidth limit is part of the signature so a limit change alone (same
		// peers, new rate) still triggers a reconcile pass - tc shaping is applied in
		// the same signature-gated step as ConfigureDevice.
		limit := "-"
		if p.BandwidthLimitMbps != nil {
			limit = strconv.Itoa(*p.BandwidthLimitMbps)
		}
		entries = append(entries, p.PublicKey+"|"+strings.Join(allowedIPs, ",")+"|"+limit)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

// readCPUPercent returns nil on the very first call (no prior sample to diff
// against) or if /proc/stat is unreadable - never a fabricated value.
func (s *heartbeatState) readCPUPercent() *float32 {
	cur, ok := readCPUSample()
	if !ok {
		s.havePrevCPU = false
		return nil
	}
	defer func() { s.prevCPU, s.havePrevCPU = cur, true }()

	if !s.havePrevCPU {
		return nil
	}
	return cpuPercentBetween(s.prevCPU, cur)
}

func sendHeartbeat(ctx context.Context, cfg config, client *http.Client, logger *slog.Logger, req heartbeatRequest, peerCount int) (heartbeatResponse, bool) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+cfg.PanelAddr+"/agent/heartbeat", bytes.NewReader(body))
	if err != nil {
		logger.Error("build_heartbeat_request_failed", "error", err)
		return heartbeatResponse{}, false
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		logger.Warn("heartbeat_failed", "error", err)
		return heartbeatResponse{}, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Warn("heartbeat_rejected", "status", resp.StatusCode)
		return heartbeatResponse{}, false
	}

	var respBody heartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		logger.Warn("heartbeat_response_decode_failed", "error", err)
		return heartbeatResponse{}, false
	}
	logger.Info("heartbeat_ok", "peer_count", peerCount, "desired_peers", len(respBody.Peers))
	return respBody, true
}

type deviceSnapshot struct {
	peers []wgtypes.Peer
}

// readDeviceSnapshot is the single wgctrl read per heartbeat tick - peer count,
// per-peer traffic counters, and last-handshake times all come from this one
// client.Device(iface) call, so they're mutually consistent within one tick rather
// than three independent snapshots (consolidated from Story 9's separate
// peer-count-only read per design review feedback). Returns ok=false (not an error)
// if the interface doesn't exist yet - true both after a host reboot before
// wg-quick@wg0 has run, and in any dev environment without a real interface at all.
func readDeviceSnapshot(iface string) (deviceSnapshot, bool) {
	client, err := wgctrl.New()
	if err != nil {
		return deviceSnapshot{}, false
	}
	defer client.Close()

	device, err := client.Device(iface)
	if err != nil {
		return deviceSnapshot{}, false
	}
	return deviceSnapshot{peers: device.Peers}, true
}

// reconcilePeers applies the control plane's desired peer list to the real WireGuard
// interface via wgctrl.ConfigureDevice with ReplacePeers: true (the desired list IS
// the complete membership; peers not in it are dropped). Only called when
// peerSignature has actually changed since the last successful call - see its doc
// comment for why calling this on every heartbeat tick regardless of whether
// anything changed is NOT the harmless no-op it looks like.
func reconcilePeers(iface string, desired []heartbeatPeer) error {
	client, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer client.Close()

	peerConfigs := make([]wgtypes.PeerConfig, 0, len(desired))
	for _, p := range desired {
		pubKey, err := wgtypes.ParseKey(p.PublicKey)
		if err != nil {
			continue // a malformed key from the control plane shouldn't abort every other peer
		}
		allowedIPs := make([]net.IPNet, 0, len(p.AllowedIPs))
		for _, cidr := range p.AllowedIPs {
			_, ipNet, err := net.ParseCIDR(cidr)
			if err != nil {
				continue
			}
			allowedIPs = append(allowedIPs, *ipNet)
		}
		peerConfigs = append(peerConfigs, wgtypes.PeerConfig{
			PublicKey:  pubKey,
			AllowedIPs: allowedIPs,
		})
	}

	return client.ConfigureDevice(iface, wgtypes.Config{
		ReplacePeers: true,
		Peers:        peerConfigs,
	})
}
