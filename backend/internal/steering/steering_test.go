package steering

import "testing"

func fptr(f float64) *float64 { return &f }

func TestRankPrefersOnlineNodes(t *testing.T) {
	ranked := Rank([]Candidate{
		{NodeID: "a", Name: "a", Online: false, ActivePeers: 0, Capacity: 100},
		{NodeID: "b", Name: "b", Online: true, ActivePeers: 99, Capacity: 100},
	}, "")
	if ranked[0].NodeID != "b" {
		t.Fatalf("expected the online node first even at high load, got %q", ranked[0].NodeID)
	}
	if !ranked[0].Recommended || ranked[1].Recommended {
		t.Fatalf("exactly the top entry should be recommended")
	}
}

func TestRankScoresByLoad(t *testing.T) {
	ranked := Rank([]Candidate{
		{NodeID: "full", Name: "full", Online: true, ActivePeers: 90, Capacity: 100},
		{NodeID: "empty", Name: "empty", Online: true, ActivePeers: 5, Capacity: 100},
	}, "")
	if ranked[0].NodeID != "empty" {
		t.Fatalf("expected the less-loaded node first, got %q", ranked[0].NodeID)
	}
}

func TestRankCPUBreaksLoadTies(t *testing.T) {
	ranked := Rank([]Candidate{
		{NodeID: "hot", Name: "hot", Online: true, ActivePeers: 10, Capacity: 100, CPUPercent: fptr(95)},
		{NodeID: "cool", Name: "cool", Online: true, ActivePeers: 10, Capacity: 100, CPUPercent: fptr(5)},
	}, "")
	if ranked[0].NodeID != "cool" {
		t.Fatalf("expected the cooler node first at equal load, got %q", ranked[0].NodeID)
	}
}

func TestRankMissingCPUIsNotPenalized(t *testing.T) {
	ranked := Rank([]Candidate{
		{NodeID: "quiet", Name: "quiet", Online: true, ActivePeers: 10, Capacity: 100, CPUPercent: nil},
		{NodeID: "busy", Name: "busy", Online: true, ActivePeers: 10, Capacity: 100, CPUPercent: fptr(50)},
	}, "")
	if ranked[0].NodeID != "quiet" {
		t.Fatalf("a node without CPU samples should not rank below one with real CPU load")
	}
}

func TestRankRegionAffinity(t *testing.T) {
	ranked := Rank([]Candidate{
		{NodeID: "us", Name: "us", Region: "us-east", Online: true, ActivePeers: 1, Capacity: 100},
		{NodeID: "eu", Name: "eu", Region: "EU", Online: true, ActivePeers: 80, Capacity: 100},
	}, "eu")
	if ranked[0].NodeID != "eu" {
		t.Fatalf("region match (case-insensitive) should outrank a better load score, got %q", ranked[0].NodeID)
	}
	if !ranked[0].RegionMatch || ranked[1].RegionMatch {
		t.Fatalf("region_match flags wrong: %+v", ranked)
	}

	// ...but never outrank being online at all.
	ranked = Rank([]Candidate{
		{NodeID: "eu-down", Name: "eu-down", Region: "eu", Online: false},
		{NodeID: "us-up", Name: "us-up", Region: "us", Online: true, Capacity: 100},
	}, "eu")
	if ranked[0].NodeID != "us-up" {
		t.Fatalf("an offline region match must not beat an online node, got %q", ranked[0].NodeID)
	}
}

func TestRankNoRegionRequestedDisablesAffinity(t *testing.T) {
	ranked := Rank([]Candidate{
		{NodeID: "a", Name: "a", Region: "eu", Online: true, ActivePeers: 50, Capacity: 100},
		{NodeID: "b", Name: "b", Region: "", Online: true, ActivePeers: 1, Capacity: 100},
	}, "")
	if ranked[0].NodeID != "b" {
		t.Fatalf("with no preferred region, load alone should decide, got %q", ranked[0].NodeID)
	}
	if ranked[0].RegionMatch || ranked[1].RegionMatch {
		t.Fatalf("no candidate should be a region match when none was requested")
	}
}

func TestRankZeroCapacityTreatedAsFull(t *testing.T) {
	ranked := Rank([]Candidate{
		{NodeID: "misconfigured", Name: "m", Online: true, ActivePeers: 0, Capacity: 0},
		{NodeID: "normal", Name: "n", Online: true, ActivePeers: 200, Capacity: 250},
	}, "")
	if ranked[0].NodeID != "normal" {
		t.Fatalf("a zero-capacity node should score as fully loaded, got %q first", ranked[0].NodeID)
	}
}

func TestRankEmpty(t *testing.T) {
	if got := Rank(nil, "eu"); len(got) != 0 {
		t.Fatalf("expected empty result for no candidates, got %d", len(got))
	}
}
