package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// rotationGracePeriod matches PRD-telegram-bot-api.md §5.2: "the old secret remains
// valid for 24 hours."
const rotationGracePeriod = 24 * time.Hour

var validPermissions = map[string]bool{
	"read": true, "create": true, "update": true, "suspend": true, "delete": true,
}

type createAPIKeyRequest struct {
	Label       string   `json:"label"`
	NodeGroups  []string `json:"node_groups"`
	Permissions []string `json:"permissions"`
}

type apiKeyResponse struct {
	ID          string   `json:"id"`
	KeyID       string   `json:"key_id"`
	Label       string   `json:"label"`
	NodeGroups  []string `json:"node_groups"`
	Permissions []string `json:"permissions"`
	Revoked     bool     `json:"revoked"`
	CreatedAt   string   `json:"created_at"`
}

// apiKeyCreatedResponse additionally carries the raw secret - only ever returned
// once, from creation and rotation, never from list/get.
type apiKeyCreatedResponse struct {
	apiKeyResponse
	Secret string `json:"secret"`
}

func toAPIKeyResponse(k store.APIKey) apiKeyResponse {
	return apiKeyResponse{
		ID:          k.ID,
		KeyID:       k.KeyID,
		Label:       k.Label,
		NodeGroups:  k.NodeGroups,
		Permissions: k.Permissions,
		Revoked:     k.RevokedAt != nil,
		CreatedAt:   k.CreatedAt.Format(time.RFC3339),
	}
}

func validatePermissions(perms []string) bool {
	for _, p := range perms {
		if !validPermissions[p] {
			return false
		}
	}
	return true
}

func generateHexSecret(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// handleCreateAPIKey is admin-only. Returns the raw secret exactly once - only its
// encrypted form is ever stored (docs/STORY-05-bot-api-key-auth.md).
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Label == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "label is required")
		return
	}
	if !validatePermissions(req.Permissions) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "unknown permission - allowed: read, create, update, suspend, delete")
		return
	}

	keyID, err := generateHexSecret(16)
	if err != nil {
		s.Logger.Error("generate_key_id_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not generate API key")
		return
	}
	secret, err := generateHexSecret(32)
	if err != nil {
		s.Logger.Error("generate_secret_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not generate API key")
		return
	}
	secretEncrypted, err := wgkeys.Encrypt(s.APIHMACMasterKey, secret)
	if err != nil {
		s.Logger.Error("encrypt_secret_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not secure API key secret")
		return
	}

	ctx := r.Context()
	key, err := s.Store.CreateAPIKey(ctx, keyID, secretEncrypted, req.Label, req.NodeGroups, req.Permissions)
	if errors.Is(err, store.ErrKeyIDTaken) {
		// Astronomically unlikely (16 random bytes) but handled rather than ignored.
		writeJSONError(w, http.StatusConflict, "key_id_collision", "please retry")
		return
	}
	if err != nil {
		s.Logger.Error("create_api_key_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not create API key")
		return
	}

	if admin, ok := adminFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, admin.Username, "api_key.created", key.KeyID, nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	writeJSON(w, http.StatusCreated, apiKeyCreatedResponse{apiKeyResponse: toAPIKeyResponse(key), Secret: secret})
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.Store.ListAPIKeys(r.Context())
	if err != nil {
		s.Logger.Error("list_api_keys_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list API keys")
		return
	}
	out := make([]apiKeyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAPIKeyResponse(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": out})
}

func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if err := s.Store.RevokeAPIKey(ctx, id); errors.Is(err, store.ErrAPIKeyNotFound) {
		writeJSONError(w, http.StatusNotFound, "api_key_not_found", "no API key with that id")
		return
	} else if err != nil {
		s.Logger.Error("revoke_api_key_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not revoke API key")
		return
	}

	if admin, ok := adminFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, admin.Username, "api_key.revoked", id, nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// handleRotateAPIKey issues a new secret while keeping the old one valid for
// rotationGracePeriod (PRD §5.2). Returns the new raw secret exactly once.
func (s *Server) handleRotateAPIKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	newSecret, err := generateHexSecret(32)
	if err != nil {
		s.Logger.Error("generate_secret_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not rotate API key")
		return
	}
	newSecretEncrypted, err := wgkeys.Encrypt(s.APIHMACMasterKey, newSecret)
	if err != nil {
		s.Logger.Error("encrypt_secret_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not rotate API key")
		return
	}

	key, err := s.Store.RotateAPIKeySecret(ctx, id, newSecretEncrypted, time.Now().Add(rotationGracePeriod))
	if errors.Is(err, store.ErrAPIKeyNotFound) {
		writeJSONError(w, http.StatusNotFound, "api_key_not_found", "no API key with that id")
		return
	}
	if err != nil {
		s.Logger.Error("rotate_api_key_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not rotate API key")
		return
	}

	if admin, ok := adminFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, admin.Username, "api_key.rotated", key.KeyID, nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	writeJSON(w, http.StatusOK, apiKeyCreatedResponse{apiKeyResponse: toAPIKeyResponse(key), Secret: newSecret})
}
