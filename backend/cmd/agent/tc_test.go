package main

import (
	"reflect"
	"strings"
	"testing"
)

func iptr(i int) *int { return &i }

func TestShapedPeersFromFiltersAndSorts(t *testing.T) {
	peers := []heartbeatPeer{
		{PublicKey: "unlimited", AllowedIPs: []string{"10.0.0.9/32"}},
		{PublicKey: "b", AllowedIPs: []string{"10.0.0.5/32"}, BandwidthLimitMbps: iptr(50)},
		{PublicKey: "zero", AllowedIPs: []string{"10.0.0.6/32"}, BandwidthLimitMbps: iptr(0)},
		{PublicKey: "no-ips", AllowedIPs: nil, BandwidthLimitMbps: iptr(10)},
		{PublicKey: "not-host-route", AllowedIPs: []string{"10.0.0.0/24"}, BandwidthLimitMbps: iptr(10)},
		{PublicKey: "a", AllowedIPs: []string{"10.0.0.2/32"}, BandwidthLimitMbps: iptr(20)},
	}
	got := shapedPeersFrom(peers)
	want := []shapedPeer{{ip: "10.0.0.2", mbps: 20}, {ip: "10.0.0.5", mbps: 50}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shapedPeersFrom = %+v, want %+v", got, want)
	}
}

func TestIFBDeviceName(t *testing.T) {
	if got := ifbDeviceName("wg0"); got != "ifb-wg0" {
		t.Fatalf("ifbDeviceName(wg0) = %q, want ifb-wg0", got)
	}
	// "ifb-" + a 12-char iface = 16 chars, over IFNAMSIZ (15) -> "" (fall back to policing).
	if got := ifbDeviceName("verylongwgif0"); got != "" {
		t.Fatalf("over-long device name must yield empty (policing fallback), got %q", got)
	}
}

func TestBuildShapingCommandsNoLimits(t *testing.T) {
	teardown, setup := buildShapingCommands("wg0", "ifb-wg0", nil, false)
	// wg0 root + wg0 ingress + ifb root + ifb link del = always torn down so switching
	// strategies never orphans the mirror device.
	if len(teardown) != 4 {
		t.Fatalf("expected 4 teardown commands (incl. ifb cleanup), got %d: %v", len(teardown), teardown)
	}
	if len(setup) != 0 {
		t.Fatalf("no limited peers must mean no setup commands, got %v", setup)
	}
}

func TestBuildShapingCommandsPolicingPlan(t *testing.T) {
	_, setup := buildShapingCommands("wg0", "ifb-wg0", []shapedPeer{
		{ip: "10.0.0.2", mbps: 20},
		{ip: "10.0.0.5", mbps: 50},
	}, false)

	// root htb + ingress qdisc + 3 commands (class, egress filter, ingress police) per peer.
	if len(setup) != 2+3*2 {
		t.Fatalf("expected 8 setup commands, got %d: %v", len(setup), setup)
	}

	joined := make([]string, len(setup))
	for i, cmd := range setup {
		if cmd[0] != "tc" {
			t.Fatalf("policing plan must only invoke tc, got %v", cmd)
		}
		joined[i] = strings.Join(cmd, " ")
	}

	if !strings.Contains(joined[0], "htb default 0") {
		t.Fatalf("root qdisc must send unclassified (unlimited) traffic direct: %s", joined[0])
	}
	all := strings.Join(joined, "\n")
	for _, want := range []string{
		"classid 1:a htb rate 20mbit ceil 20mbit",
		"match ip dst 10.0.0.2/32 flowid 1:a",
		"match ip src 10.0.0.2/32 police rate 20mbit",
		"classid 1:b htb rate 50mbit ceil 50mbit",
		"match ip dst 10.0.0.5/32 flowid 1:b",
		"match ip src 10.0.0.5/32 police rate 50mbit",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in policing plan:\n%s", want, all)
		}
	}
}

func TestBuildShapingCommandsIFBPlan(t *testing.T) {
	_, setup := buildShapingCommands("wg0", "ifb-wg0", []shapedPeer{
		{ip: "10.0.0.2", mbps: 20},
		{ip: "10.0.0.5", mbps: 50},
	}, true)

	all := ""
	for _, cmd := range setup {
		all += strings.Join(cmd, " ") + "\n"
	}

	for _, want := range []string{
		// mirror wg0 ingress -> ifb device
		"ip link add ifb-wg0 type ifb",
		"ip link set ifb-wg0 up",
		"tc qdisc add dev wg0 handle ffff: ingress",
		"mirred egress redirect dev ifb-wg0",
		"tc qdisc add dev ifb-wg0 root handle 1: htb default 0",
		// download shaped on wg0 by destination
		"tc class add dev wg0 parent 1: classid 1:a htb rate 20mbit",
		"match ip dst 10.0.0.2/32 flowid 1:a",
		// upload SHAPED (not policed) on the ifb device by source
		"tc class add dev ifb-wg0 parent 1: classid 1:a htb rate 20mbit",
		"match ip src 10.0.0.2/32 flowid 1:a",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in IFB plan:\n%s", want, all)
		}
	}
	if strings.Contains(all, "police") {
		t.Fatalf("IFB plan must SHAPE uploads, never police (drop):\n%s", all)
	}
}

func TestPoliceBurstKBFloor(t *testing.T) {
	if got := policeBurstKB(1); got != 32 {
		t.Fatalf("1 Mbps burst should hit the 32KB floor, got %d", got)
	}
	if got := policeBurstKB(100); got != 1250 {
		t.Fatalf("100 Mbps burst should be ~100ms of rate (1250KB), got %d", got)
	}
}

func TestPeerSignatureIncludesBandwidthLimit(t *testing.T) {
	base := []heartbeatPeer{{PublicKey: "k", AllowedIPs: []string{"10.0.0.2/32"}}}
	limited := []heartbeatPeer{{PublicKey: "k", AllowedIPs: []string{"10.0.0.2/32"}, BandwidthLimitMbps: iptr(10)}}
	if peerSignature(base) == peerSignature(limited) {
		t.Fatalf("changing only the bandwidth limit must change the signature, or limit changes would never be applied")
	}
}
