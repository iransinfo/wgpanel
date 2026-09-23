package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/store"
)

type createAdminRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type createAdminResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// handleCreateAdmin backs `wgpanel create-admin` and install.sh's first-boot flow
// (docs/PRD-security-access-control.md §5). It always creates a new admin row - it
// never resets an existing admin's password. The plaintext password is hashed
// immediately and never logged or echoed back in the response.
func (s *Server) handleCreateAdmin(w http.ResponseWriter, r *http.Request) {
	var req createAdminRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "username and password are required")
		return
	}
	role := req.Role
	if role == "" {
		role = "super_admin"
	}

	hash, err := authcrypto.HashPassword(req.Password)
	if err != nil {
		s.Logger.Error("hash_password_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not hash password")
		return
	}

	ctx := r.Context()
	admin, err := s.Store.CreateAdmin(ctx, req.Username, hash, role)
	if errors.Is(err, store.ErrUsernameTaken) {
		writeJSONError(w, http.StatusConflict, "username_taken", "an admin with this username already exists")
		return
	}
	if err != nil {
		s.Logger.Error("create_admin_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create admin")
		return
	}

	if err := s.Store.InsertAuditLog(ctx, "cli", "admin.created", admin.Username, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}

	writeJSON(w, http.StatusCreated, createAdminResponse{
		ID:       admin.ID,
		Username: admin.Username,
		Role:     admin.Role,
	})
}

var validAdminRoles = map[string]bool{"super_admin": true, "operator": true, "support": true}

// handleCreateAdminUser is the admin-panel-facing equivalent of handleCreateAdmin
// above - gated by requireRole("super_admin") rather than the loopback-only
// /internal/* token, so a logged-in super admin can add Operator/Support admins
// from the UI (docs/PRD-admin-panel-ux.md §3.6) instead of only being able to
// bootstrap more super admins via the CLI. Deliberately does not support deleting or
// deactivating an admin yet - that needs "can't remove the last super admin"
// safeguards this story doesn't attempt to design under time pressure.
func (s *Server) handleCreateAdminUser(w http.ResponseWriter, r *http.Request) {
	var req createAdminRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "username and password are required")
		return
	}
	if req.Role == "" {
		req.Role = "support"
	}
	if !validAdminRoles[req.Role] {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "role must be one of: super_admin, operator, support")
		return
	}

	hash, err := authcrypto.HashPassword(req.Password)
	if err != nil {
		s.Logger.Error("hash_password_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not hash password")
		return
	}

	ctx := r.Context()
	admin, err := s.Store.CreateAdmin(ctx, req.Username, hash, req.Role)
	if errors.Is(err, store.ErrUsernameTaken) {
		writeJSONError(w, http.StatusConflict, "username_taken", "an admin with this username already exists")
		return
	}
	if err != nil {
		s.Logger.Error("create_admin_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create admin")
		return
	}

	if identity, ok := callerIdentityFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "admin.created", admin.Username, map[string]string{"role": admin.Role}, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	writeJSON(w, http.StatusCreated, createAdminResponse{ID: admin.ID, Username: admin.Username, Role: admin.Role})
}

type adminUserResponse struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

func (s *Server) handleListAdminUsers(w http.ResponseWriter, r *http.Request) {
	admins, err := s.Store.ListAdmins(r.Context())
	if err != nil {
		s.Logger.Error("list_admins_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list admins")
		return
	}
	out := make([]adminUserResponse, 0, len(admins))
	for _, a := range admins {
		out = append(out, adminUserResponse{ID: a.ID, Username: a.Username, Role: a.Role, CreatedAt: a.CreatedAt.Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"admins": out})
}

type updateAdminUserRequest struct {
	Role     *string `json:"role"`
	Password *string `json:"password"`
}

// handleUpdateAdminUser fills the gap handleCreateAdminUser's doc comment flagged as
// deliberately deferred: role changes and password resets for an existing admin,
// each guarded against leaving the panel with zero super admins (checked by
// counting immediately before the change, not left to the UI to prevent).
func (s *Server) handleUpdateAdminUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateAdminUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Role == nil && req.Password == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "role and/or password must be provided")
		return
	}
	if req.Role != nil && !validAdminRoles[*req.Role] {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "role must be one of: super_admin, operator, support")
		return
	}
	if req.Password != nil && len(*req.Password) < 8 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "password must be at least 8 characters")
		return
	}

	ctx := r.Context()
	target, err := s.Store.GetAdminByID(ctx, id)
	if errors.Is(err, store.ErrAdminNotFound) {
		writeJSONError(w, http.StatusNotFound, "admin_not_found", "no admin with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_admin_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch admin")
		return
	}

	if req.Role != nil && *req.Role != target.Role && target.Role == "super_admin" {
		count, err := s.Store.CountAdminsByRole(ctx, "super_admin")
		if err != nil {
			s.Logger.Error("count_super_admins_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not verify super admin count")
			return
		}
		if count <= 1 {
			writeJSONError(w, http.StatusConflict, "last_super_admin", "cannot demote the last remaining super admin")
			return
		}
	}

	admin := target
	if req.Role != nil {
		admin, err = s.Store.UpdateAdminRole(ctx, id, *req.Role)
		if err != nil {
			s.Logger.Error("update_admin_role_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not update admin role")
			return
		}
	}
	if req.Password != nil {
		hash, err := authcrypto.HashPassword(*req.Password)
		if err != nil {
			s.Logger.Error("hash_password_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not hash password")
			return
		}
		if err := s.Store.ResetAdminPassword(ctx, id, hash); err != nil {
			s.Logger.Error("reset_admin_password_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not reset password")
			return
		}
	}

	if identity, ok := callerIdentityFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "admin.updated", admin.Username, nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	writeJSON(w, http.StatusOK, adminUserResponse{ID: admin.ID, Username: admin.Username, Role: admin.Role, CreatedAt: admin.CreatedAt.Format(time.RFC3339)})
}

// handleDeleteAdminUser guards against the two ways this could lock an operator out
// of the panel: deleting your own logged-in account, and deleting the last
// remaining super_admin (even if it isn't the caller).
func (s *Server) handleDeleteAdminUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	target, err := s.Store.GetAdminByID(ctx, id)
	if errors.Is(err, store.ErrAdminNotFound) {
		writeJSONError(w, http.StatusNotFound, "admin_not_found", "no admin with that id")
		return
	}
	if err != nil {
		s.Logger.Error("get_admin_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch admin")
		return
	}

	identity, ok := callerIdentityFromContext(ctx)
	if ok && identity.AdminUsername == target.Username {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "cannot delete your own admin account")
		return
	}

	if target.Role == "super_admin" {
		count, err := s.Store.CountAdminsByRole(ctx, "super_admin")
		if err != nil {
			s.Logger.Error("count_super_admins_failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not verify super admin count")
			return
		}
		if count <= 1 {
			writeJSONError(w, http.StatusConflict, "last_super_admin", "cannot delete the last remaining super admin")
			return
		}
	}

	if err := s.Store.DeleteAdmin(ctx, id); err != nil {
		s.Logger.Error("delete_admin_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not delete admin")
		return
	}

	if ok {
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "admin.deleted", target.Username, nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
