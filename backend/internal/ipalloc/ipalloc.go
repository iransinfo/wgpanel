// Package ipalloc allocates the next free peer IP from a node's WireGuard subnet
// (docs/PRD-account-management.md §6.1). Pure function, no database access, so it's
// fully unit-testable and the store package (which knows the already-allocated IPs)
// is the only caller that needs a DB.
package ipalloc

import (
	"fmt"
	"net"
)

var ErrSubnetExhausted = fmt.Errorf("no free IP addresses remain in this subnet")

// NextFree returns the first host address in cidr that is neither already in
// allocated nor reserved: the network address, the broadcast address (IPv4), and
// .1 (reserved for the node's own WireGuard interface address).
func NextFree(cidr string, allocated []string) (string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("parse subnet %q: %w", cidr, err)
	}

	taken := make(map[string]bool, len(allocated)+2)
	for _, ip := range allocated {
		taken[ip] = true
	}

	network := ipNet.IP.Mask(ipNet.Mask)
	broadcast := lastAddress(ipNet)
	gateway := nextIP(network) // network + 1, reserved for the node itself

	taken[network.String()] = true
	taken[broadcast.String()] = true
	taken[gateway.String()] = true

	for ip := nextIP(gateway); ipNet.Contains(ip) && !ip.Equal(broadcast); ip = nextIP(ip) {
		if !taken[ip.String()] {
			return ip.String(), nil
		}
	}

	return "", ErrSubnetExhausted
}

func nextIP(ip net.IP) net.IP {
	next := make(net.IP, len(ip))
	copy(next, ip)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}

// lastAddress computes the broadcast address of an IPv4 network (all host bits set).
func lastAddress(ipNet *net.IPNet) net.IP {
	ip := ipNet.IP.To4()
	mask := ipNet.Mask
	broadcast := make(net.IP, len(ip))
	for i := range ip {
		broadcast[i] = ip[i] | ^mask[i]
	}
	return broadcast
}
