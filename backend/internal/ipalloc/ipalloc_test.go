package ipalloc

import (
	"errors"
	"testing"
)

func TestNextFreeSkipsNetworkGatewayAndBroadcast(t *testing.T) {
	// 10.0.0.0/29: network .0, gateway .1 (reserved), usable .2-.6, broadcast .7
	ip, err := NextFree("10.0.0.0/29", nil)
	if err != nil {
		t.Fatalf("NextFree: %v", err)
	}
	if ip != "10.0.0.2" {
		t.Fatalf("expected first usable address 10.0.0.2, got %s", ip)
	}
}

func TestNextFreeSkipsAlreadyAllocated(t *testing.T) {
	ip, err := NextFree("10.0.0.0/29", []string{"10.0.0.2", "10.0.0.3"})
	if err != nil {
		t.Fatalf("NextFree: %v", err)
	}
	if ip != "10.0.0.4" {
		t.Fatalf("expected 10.0.0.4, got %s", ip)
	}
}

func TestNextFreeExhaustedSubnet(t *testing.T) {
	// 10.0.0.0/30 has exactly one usable host address (.2) once network/gateway/broadcast are reserved.
	ip, err := NextFree("10.0.0.0/30", nil)
	if err != nil {
		t.Fatalf("NextFree: %v", err)
	}
	if ip != "10.0.0.2" {
		t.Fatalf("expected 10.0.0.2, got %s", ip)
	}

	_, err = NextFree("10.0.0.0/30", []string{"10.0.0.2"})
	if !errors.Is(err, ErrSubnetExhausted) {
		t.Fatalf("expected ErrSubnetExhausted, got %v", err)
	}
}

func TestNextFreeInvalidCIDR(t *testing.T) {
	if _, err := NextFree("not-a-cidr", nil); err == nil {
		t.Fatal("expected an error for an invalid CIDR")
	}
}
