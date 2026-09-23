package httpapi

import (
	"errors"
	"net/http"

	"wgpanel-api/internal/steering"
	"wgpanel-api/internal/store"
)

type steerNodeResponse struct {
	NodeID         string   `json:"node_id"`
	Name           string   `json:"name"`
	Region         string   `json:"region"`
	PublicEndpoint string   `json:"public_endpoint"`
	Online         bool     `json:"online"`
	ActivePeers    int      `json:"active_peers"`
	Capacity       int      `json:"capacity"`
	CPUPercent     *float64 `json:"cpu_percent"` // recent average; null when the node has no samples in the window
	Score          float64  `json:"score"`       // lower is better
	RegionMatch    bool     `json:"region_match"`
	Recommended    bool     `json:"recommended"`
}

// handleSteerAccount ranks the nodes this account could connect to right now, best
// first - the authenticated (admin/bot) view of the same engine the subscription
// endpoint uses for its default node choice. ?region= biases toward nodes whose
// region label matches.
func (s *Server) handleSteerAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)
	id := r.PathValue("id")

	if _, err := s.Store.GetAccount(ctx, id, ns); errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	} else if err != nil {
		s.Logger.Error("get_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch account")
		return
	}

	candidates, err := s.Store.SteerCandidatesForAccount(ctx, id, ns)
	if err != nil {
		s.Logger.Error("steer_candidates_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not compute steering")
		return
	}
	ranked := steering.Rank(candidates, r.URL.Query().Get("region"))

	out := make([]steerNodeResponse, 0, len(ranked))
	for _, c := range ranked {
		out = append(out, steerNodeResponse{
			NodeID:         c.NodeID,
			Name:           c.Name,
			Region:         c.Region,
			PublicEndpoint: c.PublicEndpoint,
			Online:         c.Online,
			ActivePeers:    c.ActivePeers,
			Capacity:       c.Capacity,
			CPUPercent:     c.CPUPercent,
			Score:          c.Score,
			RegionMatch:    c.RegionMatch,
			Recommended:    c.Recommended,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}
