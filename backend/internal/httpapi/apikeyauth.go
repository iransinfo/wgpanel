package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

const callerIdentityKey contextKey = 1 // adminClaimsKey (middleware.go) is 0 - see contextKey there

// CallerIdentity is attached to the request context by requireAdmin and
// requireAdminOrAPIKey. Admins bypass namespace filtering entirely (they see every
// namespace, by design) but are NOT automatically exempt from permission checks -
// requirePermission still enforces AdminRole (support is read-only) per
// docs/PRD-security-access-control.md §4/§7. API keys carry the scoping
// docs/PRD-telegram-bot-api.md §5.1 requires instead of a role.
type CallerIdentity struct {
	IsAdmin       bool
	AdminUsername string
	AdminRole     string // super_admin | operator | support - only meaningful when IsAdmin
	KeyNamespace  string // == the api_keys.key_id, only meaningful when !IsAdmin
	Permissions   []string
	NodeGroups    []string
}

func callerIdentityFromContext(ctx context.Context) (*CallerIdentity, bool) {
	id, ok := ctx.Value(callerIdentityKey).(*CallerIdentity)
	return id, ok
}

// callerNamespaceArg is what handlers pass to the namespace-scoped store methods:
// nil for an admin (no filtering), a pointer to the key's namespace otherwise.
func callerNamespaceArg(identity *CallerIdentity) *string {
	if identity.IsAdmin {
		return nil
	}
	ns := identity.KeyNamespace
	return &ns
}

const apiKeyTimestampWindow = 5 * time.Minute

// requireAdminOrAPIKey accepts either a Story-1 admin JWT (Authorization: Bearer) or
// an HMAC-signed bot/reseller API key (X-API-Key/X-Timestamp/X-Signature), so the
// same account handlers serve both the admin panel and bot clients
// (docs/PRD-account-management.md §1: "one unambiguous account lifecycle").
func (s *Server) requireAdminOrAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authHeader := r.Header.Get("Authorization"); authHeader != "" {
			s.authenticateAdmin(w, r, next)
			return
		}
		s.authenticateAPIKey(w, r, next)
	})
}

func (s *Server) authenticateAdmin(w http.ResponseWriter, r *http.Request, next http.Handler) {
	tokenString, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tokenString == "" {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
		return
	}
	claims, err := authcrypto.ParseAccessToken(s.JWTSecret, tokenString)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
		return
	}

	identity := &CallerIdentity{IsAdmin: true, AdminUsername: claims.Username, AdminRole: claims.Role}
	ctx := context.WithValue(r.Context(), callerIdentityKey, identity)
	next.ServeHTTP(w, r.WithContext(ctx))
}

func (s *Server) authenticateAPIKey(w http.ResponseWriter, r *http.Request, next http.Handler) {
	apiKeyID := r.Header.Get("X-API-Key")
	timestampHeader := r.Header.Get("X-Timestamp")
	signature := r.Header.Get("X-Signature")
	if apiKeyID == "" || timestampHeader == "" || signature == "" {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing Authorization or X-API-Key/X-Timestamp/X-Signature")
		return
	}

	ts, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "invalid_timestamp", "X-Timestamp must be a unix second timestamp")
		return
	}
	age := time.Since(time.Unix(ts, 0))
	if age < 0 {
		age = -age
	}
	if age > apiKeyTimestampWindow {
		writeJSONError(w, http.StatusUnauthorized, "stale_timestamp", "timestamp outside the allowed +/-5 minute window")
		return
	}

	ctx := r.Context()
	key, err := s.Store.GetAPIKeyByKeyID(ctx, apiKeyID)
	if errors.Is(err, store.ErrAPIKeyNotFound) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid API key")
		return
	}
	if err != nil {
		s.Logger.Error("get_api_key_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "authentication failed")
		return
	}
	if key.RevokedAt != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "this API key has been revoked")
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "could not read request body")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes)) // handlers downstream still need to read it

	payload := r.Method + "\n" + r.URL.Path + "\n" + timestampHeader + "\n" + string(bodyBytes)
	valid, err := s.verifyAPIKeySignature(key, payload, signature)
	if err != nil {
		s.Logger.Error("verify_api_key_signature_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "authentication failed")
		return
	}
	if !valid {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid signature")
		return
	}

	identity := &CallerIdentity{
		KeyNamespace: key.KeyID,
		Permissions:  key.Permissions,
		NodeGroups:   key.NodeGroups,
	}
	next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, callerIdentityKey, identity)))
}

// verifyAPIKeySignature checks payload against the key's current secret, falling
// back to its previous secret if that's still within its rotation grace window
// (docs/PRD-telegram-bot-api.md §5.2: "the old secret remains valid for 24 hours").
func (s *Server) verifyAPIKeySignature(key store.APIKey, payload, providedSignature string) (bool, error) {
	currentSecret, err := wgkeys.Decrypt(s.APIHMACMasterKey, key.SecretEncrypted)
	if err != nil {
		return false, err
	}
	if hmacSignatureMatches(currentSecret, payload, providedSignature) {
		return true, nil
	}

	if key.PreviousSecretEncrypted != nil && key.PreviousSecretExpiresAt != nil && key.PreviousSecretExpiresAt.After(time.Now()) {
		prevSecret, err := wgkeys.Decrypt(s.APIHMACMasterKey, *key.PreviousSecretEncrypted)
		if err != nil {
			return false, err
		}
		if hmacSignatureMatches(prevSecret, payload, providedSignature) {
			return true, nil
		}
	}

	return false, nil
}

func hmacSignatureMatches(secret, payload, providedSignatureHex string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expectedHex := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(expectedHex), []byte(providedSignatureHex)) == 1
}

// requirePermission gates a handler to callers with perm. API keys are checked
// against their own Permissions list. Admins are NOT an automatic bypass for
// anything beyond "read" - a support-role admin is read-only by design
// (docs/PRD-security-access-control.md §4: "Support (read-only): ... cannot
// create/update/delete/suspend anything"). This distinction didn't exist before the
// implementation audit found it: every admin previously bypassed every permission
// check regardless of role.
func (s *Server) requirePermission(perm string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := callerIdentityFromContext(r.Context())
		if !ok {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "missing caller identity")
			return
		}
		if identity.IsAdmin {
			if perm != "read" && identity.AdminRole == "support" {
				writeJSONError(w, http.StatusForbidden, "forbidden", "your role is read-only")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !stringSliceContains(identity.Permissions, perm) {
			writeJSONError(w, http.StatusForbidden, "forbidden", "this API key lacks the '"+perm+"' permission")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func stringSliceContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
