package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in_seconds"`
}

// dummyHash is verified against when the username doesn't exist, so a login attempt
// against an unknown username takes roughly the same time as one against a real
// username with a wrong password - it doesn't fully close the timing side-channel
// (real accounts still finish a real DB round-trip), but avoids ok-vs-not-found being
// resolved from a bare argon2 comparison alone.
const dummyHash = "argon2id$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// handleLogin verifies admin credentials and issues a short-lived JWT plus a
// Redis-backed refresh token (docs/PRD-security-access-control.md §6).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "username and password are required")
		return
	}

	ctx := r.Context()
	admin, err := s.Store.GetAdminByUsername(ctx, req.Username)
	if err != nil && !errors.Is(err, store.ErrAdminNotFound) {
		s.Logger.Error("login_lookup_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "login failed")
		return
	}

	hashToCheck := dummyHash
	if err == nil {
		hashToCheck = admin.PasswordHash
	}

	valid, verr := authcrypto.VerifyPassword(req.Password, hashToCheck)
	if verr != nil {
		s.Logger.Error("password_verify_failed", "error", verr)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "login failed")
		return
	}
	if errors.Is(err, store.ErrAdminNotFound) || !valid {
		writeJSONError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}

	accessToken, err := authcrypto.IssueAccessToken(s.JWTSecret, admin.ID, admin.Username, admin.Role)
	if err != nil {
		s.Logger.Error("issue_access_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "login failed")
		return
	}
	refreshToken, err := authcrypto.IssueRefreshToken(ctx, s.Redis, admin.ID)
	if err != nil {
		s.Logger.Error("issue_refresh_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "login failed")
		return
	}

	if err := s.Store.InsertAuditLog(ctx, admin.Username, "admin.login", admin.Username, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}

	writeJSON(w, http.StatusOK, loginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int(authcrypto.AccessTokenTTL.Seconds()),
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type refreshResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in_seconds"`
}

// handleRefresh exchanges a still-valid refresh token (issued at login) for a new
// access token, so a session can outlive the 15-minute access-token TTL without a
// full re-login. Added in STORY-06 - a real SPA can't work without it, and Story 1
// issued refresh tokens that nothing ever consumed. Does not rotate the refresh
// token itself (STORY-06 scope note).
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}

	ctx := r.Context()
	adminID, err := authcrypto.ValidateRefreshToken(ctx, s.Redis, req.RefreshToken)
	if errors.Is(err, authcrypto.ErrInvalidRefreshToken) {
		writeJSONError(w, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
		return
	}
	if err != nil {
		s.Logger.Error("validate_refresh_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not refresh session")
		return
	}

	admin, err := s.Store.GetAdminByID(ctx, adminID)
	if errors.Is(err, store.ErrAdminNotFound) {
		// The admin behind this refresh token no longer exists (e.g. deleted) -
		// treat identically to an invalid token rather than a distinct error.
		writeJSONError(w, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
		return
	}
	if err != nil {
		s.Logger.Error("get_admin_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not refresh session")
		return
	}

	accessToken, err := authcrypto.IssueAccessToken(s.JWTSecret, admin.ID, admin.Username, admin.Role)
	if err != nil {
		s.Logger.Error("issue_access_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not refresh session")
		return
	}

	writeJSON(w, http.StatusOK, refreshResponse{
		AccessToken: accessToken,
		ExpiresIn:   int(authcrypto.AccessTokenTTL.Seconds()),
	})
}
