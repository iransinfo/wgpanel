// Package steering ranks the nodes an account has a peer on, so subscription
// fetches and the /steer endpoint can recommend the best one to connect to right
// now. Pure scoring logic only - candidate data comes from store.SteerCandidatesForAccount,
// keeping this package trivially unit-testable.
package steering

import "sort"

type Candidate struct {
	NodeID         string
	Name           string
	Region         string
	PublicEndpoint string
	Online         bool
	ActivePeers    int
	Capacity       int
	CPUPercent     *float64 // avg over the recent metrics window; nil = no samples (fresh/quiet node)
}

type Ranked struct {
	Candidate
	Score       float64 // lower is better
	RegionMatch bool
	Recommended bool // exactly one entry per Rank call, the overall winner
}

// Weights for the composite score. Load (how full the node is) dominates because it
// is always available and directly reflects contention for this workload; CPU refines
// the ordering when metrics exist. A node without CPU samples just scores on load
// alone rather than being penalized for missing telemetry.
const (
	loadWeight = 0.7
	cpuWeight  = 0.3
)

func score(c Candidate) float64 {
	loadRatio := 1.0
	if c.Capacity > 0 {
		loadRatio = float64(c.ActivePeers) / float64(c.Capacity)
		if loadRatio > 1 {
			loadRatio = 1
		}
	}
	s := loadWeight * loadRatio
	if c.CPUPercent != nil {
		cpu := *c.CPUPercent / 100
		if cpu > 1 {
			cpu = 1
		}
		if cpu < 0 {
			cpu = 0
		}
		s += cpuWeight * cpu
	}
	return s
}

// Rank orders candidates best-first: online before offline, preferred-region matches
// before the rest, then by composite load/CPU score. preferredRegion == "" disables
// region affinity entirely. The top entry is marked Recommended - even if offline
// (when every node is down there is still a "least bad" answer, and callers that
// must distinguish can read Online themselves).
func Rank(candidates []Candidate, preferredRegion string) []Ranked {
	ranked := make([]Ranked, 0, len(candidates))
	for _, c := range candidates {
		ranked = append(ranked, Ranked{
			Candidate:   c,
			Score:       score(c),
			RegionMatch: preferredRegion != "" && c.Region != "" && equalFold(c.Region, preferredRegion),
		})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.Online != b.Online {
			return a.Online
		}
		if a.RegionMatch != b.RegionMatch {
			return a.RegionMatch
		}
		if a.Score != b.Score {
			return a.Score < b.Score
		}
		return a.Name < b.Name // deterministic tie-break
	})

	if len(ranked) > 0 {
		ranked[0].Recommended = true
	}
	return ranked
}

// equalFold is ASCII-only case-insensitive equality - region labels are operator-
// entered short ASCII slugs ("eu", "US-East"), not arbitrary Unicode.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
