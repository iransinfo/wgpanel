package main

import (
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
)

// Per-account bandwidth enforcement via tc, applied wholesale (teardown + rebuild)
// whenever the desired peer set changes - the same "the list IS the state" stance
// reconcilePeers takes with ReplacePeers, so there's no incremental drift to manage.
//
// Direction mapping on the WireGuard interface:
//   - egress  (packets leaving wg0 toward a peer) = the client's DOWNLOAD -> always
//     shaped with an HTB class per limited peer, matched on destination IP.
//   - ingress (packets arriving on wg0 from a peer) = the client's UPLOAD. Ingress
//     can't be shaped (queued) directly on the same device, so we mirror it onto an
//     IFB device and run a real HTB tree there - matched on source IP. Only if the
//     ifb kernel module is unavailable do we fall back to POLICING (drop above rate)
//     on wg0 ingress. Policing is a poor last resort: because it drops instead of
//     queuing, TCP upload backs off hard and settles well below the nominal rate -
//     fine as a fallback, wrong as the default (which is what it used to be).
//
// HTB's "default 0" sends unclassified traffic (peers without a limit) direct,
// completely unshaped - only limited peers ever pass through a class.

// shapedPeer is one peer that actually has a limit, extracted from the heartbeat
// response's desired-peer list.
type shapedPeer struct {
	ip   string // the peer's /32 tunnel address, without the mask
	mbps int
}

// shapedPeersFrom filters the desired-peer list down to the ones needing a tc class,
// sorted by IP for deterministic class ids run-to-run.
func shapedPeersFrom(peers []heartbeatPeer) []shapedPeer {
	var out []shapedPeer
	for _, p := range peers {
		if p.BandwidthLimitMbps == nil || *p.BandwidthLimitMbps <= 0 || len(p.AllowedIPs) == 0 {
			continue
		}
		ip, ok := strings.CutSuffix(p.AllowedIPs[0], "/32")
		if !ok || ip == "" {
			continue // shaping matches exact host addresses; anything else isn't ours to shape
		}
		out = append(out, shapedPeer{ip: ip, mbps: *p.BandwidthLimitMbps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ip < out[j].ip })
	return out
}

// policeBurstKB sizes the ingress policer's bucket at ~100ms of the allowed rate
// (mbps * 125000 bytes/s/mbit / 10, in KB), floored at 32KB so very low limits don't
// drop every burst of a couple of full-size packets. Only used on the policing
// fallback path (no ifb).
func policeBurstKB(mbps int) int {
	kb := mbps * 125000 / 10 / 1000
	if kb < 32 {
		kb = 32
	}
	return kb
}

// ifbDeviceName is the mirror device this iface's uploads are shaped on. Returns ""
// when the name wouldn't fit IFNAMSIZ (15) - the caller then uses the policing
// fallback instead of IFB.
func ifbDeviceName(iface string) string {
	dev := "ifb-" + iface
	if len(dev) > 15 {
		return ""
	}
	return dev
}

// buildShapingCommands returns the exact tc/ip invocations for a desired peer set -
// pure, so the command plan is unit-testable without a real interface. The teardown
// commands are all expected to possibly fail (nothing installed yet, device absent);
// applyShaping treats only the setup commands as fallible.
//
// useIFB selects the ingress strategy: true shapes uploads with an HTB tree on ifbDev
// (correct); false polices them on wg0 ingress (fallback for kernels without the ifb
// module). Egress/download shaping is identical either way.
func buildShapingCommands(iface, ifbDev string, shaped []shapedPeer, useIFB bool) (teardown, setup [][]string) {
	teardown = [][]string{
		{"tc", "qdisc", "del", "dev", iface, "root"},
		{"tc", "qdisc", "del", "dev", iface, "ingress"},
	}
	// Always attempt to remove a stale mirror device, even when this pass uses
	// policing (useIFB=false) - otherwise switching an interface from IFB back to
	// policing (or to no-limits) would orphan the ifb device and its qdisc.
	if ifbDev != "" {
		teardown = append(teardown,
			[]string{"tc", "qdisc", "del", "dev", ifbDev, "root"},
			[]string{"ip", "link", "del", ifbDev},
		)
	}
	if len(shaped) == 0 {
		return teardown, nil
	}

	// Download (egress on wg0) - HTB tree, identical in both modes.
	setup = [][]string{
		{"tc", "qdisc", "add", "dev", iface, "root", "handle", "1:", "htb", "default", "0"},
	}

	if useIFB {
		setup = append(setup,
			[]string{"ip", "link", "add", ifbDev, "type", "ifb"},
			[]string{"ip", "link", "set", ifbDev, "up"},
			[]string{"tc", "qdisc", "add", "dev", iface, "handle", "ffff:", "ingress"},
			// Mirror every wg0 ingress packet (client uploads) onto ifbDev's egress,
			// where the HTB tree below actually shapes it.
			[]string{"tc", "filter", "add", "dev", iface, "parent", "ffff:", "protocol", "ip", "prio", "1",
				"u32", "match", "u32", "0", "0", "action", "mirred", "egress", "redirect", "dev", ifbDev},
			[]string{"tc", "qdisc", "add", "dev", ifbDev, "root", "handle", "1:", "htb", "default", "0"},
		)
	} else {
		setup = append(setup,
			[]string{"tc", "qdisc", "add", "dev", iface, "handle", "ffff:", "ingress"},
		)
	}

	for i, p := range shaped {
		classID := fmt.Sprintf("1:%x", i+10) // minor ids from 0xa up; 16-bit space is far beyond any node's peer capacity
		rate := fmt.Sprintf("%dmbit", p.mbps)

		// Download: shape on wg0 egress, matched on destination (packets TO the client).
		setup = append(setup,
			[]string{"tc", "class", "add", "dev", iface, "parent", "1:", "classid", classID, "htb", "rate", rate, "ceil", rate},
			[]string{"tc", "filter", "add", "dev", iface, "parent", "1:", "protocol", "ip", "prio", "1",
				"u32", "match", "ip", "dst", p.ip + "/32", "flowid", classID},
		)

		if useIFB {
			// Upload: shape on ifbDev (the mirrored ingress), matched on source
			// (packets FROM the client). Real queuing, not dropping.
			setup = append(setup,
				[]string{"tc", "class", "add", "dev", ifbDev, "parent", "1:", "classid", classID, "htb", "rate", rate, "ceil", rate},
				[]string{"tc", "filter", "add", "dev", ifbDev, "parent", "1:", "protocol", "ip", "prio", "1",
					"u32", "match", "ip", "src", p.ip + "/32", "flowid", classID},
			)
		} else {
			// Upload fallback: police (drop above rate) on wg0 ingress.
			setup = append(setup,
				[]string{"tc", "filter", "add", "dev", iface, "parent", "ffff:", "protocol", "ip", "prio", "1",
					"u32", "match", "ip", "src", p.ip + "/32",
					"police", "rate", rate, "burst", fmt.Sprintf("%dk", policeBurstKB(p.mbps)), "drop"},
			)
		}
	}
	return teardown, setup
}

// ifbSupported reports whether the ifb module is usable on this host. Best-effort
// modprobe (kernels with ifb built in succeed without it), then a create/delete
// probe - the surest test short of actually shaping. Called only when a limited peer
// set exists and the desired set has changed, so its cost is negligible.
func ifbSupported(dev string) bool {
	if dev == "" {
		return false
	}
	_ = exec.Command("modprobe", "ifb").Run()
	if err := exec.Command("ip", "link", "add", dev, "type", "ifb").Run(); err != nil {
		return false
	}
	_ = exec.Command("ip", "link", "del", dev).Run()
	return true
}

func ingressMode(useIFB bool) string {
	if useIFB {
		return "ifb-shaped"
	}
	return "policed"
}

// applyShaping reconciles the interface's tc state to the desired peer list.
// Teardown errors are expected noise ("RTNETLINK answers: No such file or directory"
// when nothing was installed); any setup failure aborts and reports, since a
// half-built qdisc tree is worse than none.
func applyShaping(iface string, peers []heartbeatPeer, logger *slog.Logger) error {
	shaped := shapedPeersFrom(peers)
	ifbDev := ifbDeviceName(iface)
	useIFB := len(shaped) > 0 && ifbSupported(ifbDev)

	teardown, setup := buildShapingCommands(iface, ifbDev, shaped, useIFB)

	for _, cmd := range teardown {
		_ = exec.Command(cmd[0], cmd[1:]...).Run() // nothing installed yet is fine
	}
	for _, cmd := range setup {
		if out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput(); err != nil {
			// A half-built qdisc tree is worse than none: tear the partial state back
			// down so the interface is left cleanly unshaped (correct, just unlimited)
			// rather than in a broken in-between state the signature gate may not revisit.
			for _, td := range teardown {
				_ = exec.Command(td[0], td[1:]...).Run()
			}
			return fmt.Errorf("%s: %w (%s)", strings.Join(cmd, " "), err, strings.TrimSpace(string(out)))
		}
	}
	if len(shaped) > 0 {
		logger.Info("shaping_applied", "limited_peers", len(shaped), "ingress_mode", ingressMode(useIFB))
	}
	return nil
}
