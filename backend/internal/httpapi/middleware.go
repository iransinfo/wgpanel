package httpapi

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"wgpanel-api/internal/authcrypto"
)

type contextKey int

const adminClaimsKey contextKey = iota

// requireAdmin protects admin-facing routes (e.g. node management) with a valid,
// unexpired access token issued by handleLogin. docs/STORY-02-node-directory-join-tokens.md:
// Story 1 issued JWTs but nothing verified one until this middleware existed.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		tokenString, ok := strings.CutPrefix(authHeader, "Bearer ")
		if !ok || tokenString == "" {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}

		claims, err := authcrypto.ParseAccessToken(s.JWTSecret, tokenString)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
			return
		}

		// Also attach the shared CallerIdentity (docs/AUDIT-prd-implementation-status.md:
		// requireAdminOrAPIKey's CallerIdentity and this middleware's adminClaimsKey used
		// to be two disconnected mechanisms, which is how role enforcement went missing
		// entirely - requireRole needs one consistent place to read the role from
		// regardless of which of the two admin-auth paths a route uses).
		ctx := context.WithValue(r.Context(), adminClaimsKey, claims)
		ctx = context.WithValue(ctx, callerIdentityKey, &CallerIdentity{
			IsAdmin:       true,
			AdminUsername: claims.Username,
			AdminRole:     claims.Role,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireRole gates a route (mounted behind requireAdmin or requireAdminOrAPIKey) to
// admins whose role is at least minRole in the super_admin > operator > support
// hierarchy. docs/PRD-security-access-control.md §7: join-token generation and
// API-key issuance are super-admin-only "mint new trust" actions; general node/account
// mutation is operator-or-above; everything else (reads) needs no role check at all.
// Added during the implementation audit - this hierarchy existed only in the PRD and
// the `admins.role` column, never actually enforced anywhere until now.
func (s *Server) requireRole(minRole string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := callerIdentityFromContext(r.Context())
		if !ok || !identity.IsAdmin {
			writeJSONError(w, http.StatusForbidden, "forbidden", "this action requires an admin session")
			return
		}
		if roleRank(identity.AdminRole) < roleRank(minRole) {
			writeJSONError(w, http.StatusForbidden, "forbidden", "your role does not permit this action")
			return
		}
		next.ServeHTTP(w, r)
	})
}

var roleRanks = map[string]int{
	"support":     1,
	"operator":    2,
	"super_admin": 3,
}

func roleRank(role string) int {
	return roleRanks[role] // unknown/empty role ranks 0, below every real role
}

// adminFromContext retrieves the claims requireAdmin attached. Only ever called from
// handlers mounted behind requireAdmin, so a missing value would be a wiring bug -
// callers can safely assume ok is true.
func adminFromContext(ctx context.Context) (*authcrypto.Claims, bool) {
	claims, ok := ctx.Value(adminClaimsKey).(*authcrypto.Claims)
	return claims, ok
}

// internalOnly gates the /internal/* route group with a shared-secret header. This is
// the real boundary, not an IP/loopback check: Docker's bridge network rewrites source
// IPs and lets sibling containers (frontend, caddy) reach api:8080 directly regardless
// of what's published to the host or proxied by Caddy. See
// docs/PRD-security-access-control.md §5.
func (s *Server) internalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Internal-Token")
		valid := token != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(s.InternalAPIToken)) == 1
		if !valid {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid X-Internal-Token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs method, path, status, and duration for every request.
// It deliberately never logs request or response bodies - passwords, JWT secrets,
// and the HMAC/internal tokens only ever pass through as opaque values the logger
// never sees, which is a stronger guarantee than trying to redact them after the
// fact (see docs/STORY-01-control-plane-walking-skeleton.md task C12).
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(sw, r)

		s.Logger.Info("http_request",
			"method", r.Method,
			"path", redactPath(r.URL.Path),
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// redactPath masks capability tokens that are part of the URL path itself - the
// subscription endpoints are the one route family where the credential IS a path
// segment, so the body-never-logged guarantee above isn't enough on its own.
func redactPath(path string) string {
	const subPrefix = "/api/v1/sub/"
	rest, ok := strings.CutPrefix(path, subPrefix)
	if !ok || rest == "" {
		return path
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return subPrefix + "[redacted]" + rest[i:]
	}
	return subPrefix + "[redacted]"
}

type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
