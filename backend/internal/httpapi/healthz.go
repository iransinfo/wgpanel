package httpapi

import "net/http"

// handleHealthz is the public, unauthenticated GET /healthz - deliberately minimal
// (no version, migration, or admin-count info). It's the target for Docker's own
// container healthcheck and any future uptime monitor / load balancer.
// docs/PRD-security-access-control.md §5.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := s.Store.Ping(ctx); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "not_ready", "database unreachable")
		return
	}
	if err := s.Redis.Ping(ctx).Err(); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "not_ready", "redis unreachable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleInternalHealthz is the richer, /internal/*-gated readiness check used by the
// wgpanel CLI and install.sh to know when it's safe to call POST /internal/admins.
func (s *Server) handleInternalHealthz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	dbOK := s.Store.Ping(ctx) == nil
	redisOK := s.Redis.Ping(ctx).Err() == nil

	migrationsApplied, err := s.Store.MigrationsApplied(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not check migration status")
		return
	}

	adminCount, err := s.Store.AdminCount(ctx)
	if err != nil {
		adminCount = -1 // table may not exist yet if migrations haven't applied
	}

	status := http.StatusOK
	if !dbOK || !redisOK || !migrationsApplied {
		status = http.StatusServiceUnavailable
	}

	writeJSON(w, status, map[string]any{
		"db_ok":              dbOK,
		"redis_ok":           redisOK,
		"migrations_applied": migrationsApplied,
		"admin_bootstrapped": adminCount > 0,
	})
}
