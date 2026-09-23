package httpapi

import (
	"errors"
	"net/http"
	"time"

	"wgpanel-api/internal/store"
)

type accountDeviceResponse struct {
	ID             string `json:"id"`
	SourceEndpoint string `json:"source_endpoint"`
	NodeID         string `json:"node_id"`
	NodeName       string `json:"node_name"`
	FirstSeenAt    string `json:"first_seen_at"`
	LastSeenAt     string `json:"last_seen_at"`
	// Active mirrors the enforcement side's definition exactly (last handshake within
	// store.DeviceActiveWindow) - what this list shows as active is what counted
	// against the limit.
	Active bool `json:"active"`
}

// handleListAccountDevices serves the account detail's Devices section and the bot
// API's visibility into PRD §6.4 device tracking: every distinct source endpoint
// observed for this account, newest first, with the currently-active ones flagged.
func (s *Server) handleListAccountDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)
	id := r.PathValue("id")

	account, err := s.Store.GetAccount(ctx, id, ns)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch account")
		return
	}

	devices, err := s.Store.ListAccountDevices(ctx, id, ns)
	if err != nil {
		s.Logger.Error("list_account_devices_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list devices")
		return
	}

	activeCount := 0
	out := make([]accountDeviceResponse, 0, len(devices))
	for _, d := range devices {
		active := time.Since(d.LastSeenAt) <= store.DeviceActiveWindow()
		if active {
			activeCount++
		}
		out = append(out, accountDeviceResponse{
			ID:             d.ID,
			SourceEndpoint: d.SourceEndpoint,
			NodeID:         d.NodeID,
			NodeName:       d.NodeName,
			FirstSeenAt:    d.FirstSeenAt.Format(time.RFC3339),
			LastSeenAt:     d.LastSeenAt.Format(time.RFC3339),
			Active:         active,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"devices":               out,
		"active_devices":        activeCount,
		"device_limit":          account.DeviceLimit,
		"device_limit_exceeded": account.DeviceLimitExceededAt != nil,
	})
}
