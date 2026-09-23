package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

type auditLogEntryResponse struct {
	ID        int64          `json:"id"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Target    *string        `json:"target"`
	Detail    map[string]any `json:"detail,omitempty"`
	IPAddress *string        `json:"ip_address"`
	CreatedAt string         `json:"created_at"`
}

// handleListAuditLog is super-admin-only (docs/PRD-admin-panel-ux.md §3.7) - it's a
// global view across every admin and API key, not scoped to the caller.
func (s *Server) handleListAuditLog(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	entries, err := s.Store.ListAuditLog(r.Context(), limit)
	if err != nil {
		s.Logger.Error("list_audit_log_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list audit log")
		return
	}

	out := make([]auditLogEntryResponse, 0, len(entries))
	for _, e := range entries {
		resp := auditLogEntryResponse{
			ID:        e.ID,
			Actor:     e.Actor,
			Action:    e.Action,
			Target:    e.Target,
			IPAddress: e.IPAddress,
			CreatedAt: e.CreatedAt.Format(time.RFC3339),
		}
		if len(e.Detail) > 0 {
			_ = json.Unmarshal(e.Detail, &resp.Detail) // best-effort - malformed detail just renders as absent, never fatal
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}
