// Backup & restore via the panel (Settings -> Backup & restore): one
// password-encrypted file (see internal/backupcrypto) containing every config
// table, the node-mTLS CA keypair, AND the two .env keys the data is useless
// without (ACCOUNT_KEY_ENCRYPTION_KEY, API_HMAC_MASTER_KEY). Embedding the keys
// is what makes the backup survive total server loss - the original deploy/.env
// no longer needs to exist. Restore onto a server with different keys transparently
// re-encrypts every account private key and API-key secret to the new keys, so the
// restored panel works with the new .env as-is.
//
// The decrypted contents are as sensitive as the database itself (admin password
// hashes, the CA private key, subscription tokens, the encryption keys) - the file
// is only ever produced for, and accepted from, a super_admin, is never readable
// without the admin-chosen password, and both directions are audit-logged.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"wgpanel-api/internal/backupcrypto"
	"wgpanel-api/internal/wgkeys"
)

// backupPasswordMinLen guards against trivially brute-forceable backups; the
// argon2id KDF does the heavy lifting beyond that.
const backupPasswordMinLen = 8

type backupCA struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

type backupSecrets struct {
	AccountKeyEncryptionKey string `json:"account_key_encryption_key"`
	APIHMACMasterKey        string `json:"api_hmac_master_key"`
}

// backupPayload is the plaintext sealed inside the backupcrypto envelope.
type backupPayload struct {
	Migrations []string                   `json:"migrations"`
	Secrets    backupSecrets              `json:"secrets"`
	CA         *backupCA                  `json:"ca,omitempty"`
	Tables     map[string]json.RawMessage `json:"tables"`
}

type downloadBackupRequest struct {
	Password string `json:"password"`
}

// handleDownloadBackup streams the encrypted panel backup as an attachment. POST,
// not GET, because the encryption password rides in the body. super_admin-only
// (wired via requireRole in server.go).
func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req downloadBackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if len(req.Password) < backupPasswordMinLen {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("password must be at least %d characters - it is the ONLY way to open the backup", backupPasswordMinLen))
		return
	}

	migrations, err := s.Store.AppliedMigrations(ctx)
	if err != nil {
		s.Logger.Error("backup_migrations_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not read schema version")
		return
	}
	tables, err := s.Store.DumpTables(ctx)
	if err != nil {
		s.Logger.Error("backup_dump_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not dump tables")
		return
	}

	payload, err := json.Marshal(backupPayload{
		Migrations: migrations,
		Secrets: backupSecrets{
			AccountKeyEncryptionKey: s.AccountKeyEncryptionKey,
			APIHMACMasterKey:        s.APIHMACMasterKey,
		},
		CA:     s.readCAForBackup(),
		Tables: tables,
	})
	if err != nil {
		s.Logger.Error("backup_marshal_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not build backup")
		return
	}

	createdAt := time.Now().UTC()
	envelope, err := backupcrypto.Seal(req.Password, payload, createdAt.Format(time.RFC3339))
	if err != nil {
		s.Logger.Error("backup_seal_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not encrypt backup")
		return
	}

	if identity, ok := callerIdentityFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "backup.downloaded", "panel", nil, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	filename := "wgpanel-backup-" + createdAt.Format("20060102T150405Z") + ".json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(envelope); err != nil {
		s.Logger.Error("backup_write_failed", "error", err)
	}
}

// readCAForBackup best-effort reads the CA keypair off disk. A deployment without
// one (files missing, no volume) just produces a backup without a ca section -
// restoring such a backup simply leaves the current CA in place.
func (s *Server) readCAForBackup() *backupCA {
	if s.CADataDir == "" {
		return nil
	}
	cert, err := os.ReadFile(filepath.Join(s.CADataDir, "ca-cert.pem"))
	if err != nil {
		s.Logger.Warn("backup_ca_cert_read_failed", "error", err)
		return nil
	}
	key, err := os.ReadFile(filepath.Join(s.CADataDir, "ca-key.pem"))
	if err != nil {
		s.Logger.Warn("backup_ca_key_read_failed", "error", err)
		return nil
	}
	return &backupCA{CertPEM: string(cert), KeyPEM: string(key)}
}

type restoreBackupRequest struct {
	Password string                `json:"password"`
	Backup   backupcrypto.Envelope `json:"backup"`
}

type restoreBackupResponse struct {
	Restored map[string]int `json:"restored"`
	// CARestored is true when the backup carried a CA keypair and it was written
	// to disk. RestartRequired additionally means that keypair differs from the
	// one this API loaded at startup - node-agent mTLS keeps using the old CA
	// until the api container is restarted (`wgpanel restart`).
	CARestored      bool `json:"ca_restored"`
	RestartRequired bool `json:"restart_required"`
}

// handleRestoreBackup replaces ALL panel state with an uploaded backup file.
// super_admin-only. Everything is validated - password, schema version, key
// re-encryption - before any state is touched.
func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Config tables without metrics history stay small; 256MB is far above any
	// realistic backup while still bounding a hostile upload.
	r.Body = http.MaxBytesReader(w, r.Body, 256<<20)

	var req restoreBackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "invalid_request", "backup file exceeds the 256MB limit")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "not a valid backup upload")
		return
	}

	plaintext, err := backupcrypto.Open(req.Password, req.Backup)
	if errors.Is(err, backupcrypto.ErrWrongPassword) {
		writeJSONError(w, http.StatusBadRequest, "wrong_password", "wrong password or corrupted backup file")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var backup backupPayload
	if err := json.Unmarshal(plaintext, &backup); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "backup contents are not valid JSON")
		return
	}
	if backup.Secrets.AccountKeyEncryptionKey == "" || backup.Secrets.APIHMACMasterKey == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "backup is missing its embedded encryption keys")
		return
	}

	migrations, err := s.Store.AppliedMigrations(ctx)
	if err != nil {
		s.Logger.Error("restore_migrations_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not read schema version")
		return
	}
	if !slices.Equal(backup.Migrations, migrations) {
		// Exact match, not prefix-compatibility: json_populate_recordset against a
		// drifted schema fails in data-dependent ways (or silently NULLs columns),
		// so "same panel version as the backup" is the only safe contract.
		writeJSONError(w, http.StatusConflict, "schema_mismatch",
			"this backup was taken on a different panel version - run the same version the backup came from, then restore")
		return
	}

	// The backup carries the keys its ciphertext columns were encrypted with; if
	// this deployment's keys differ (fresh .env after losing the old server),
	// re-encrypt those columns to the current keys BEFORE restoring, so the
	// restored panel works with the new .env as-is. Failures here abort before
	// anything is touched.
	if backup.Secrets.AccountKeyEncryptionKey != s.AccountKeyEncryptionKey {
		reencrypted, err := reencryptRows(backup.Tables["accounts"], []string{"private_key_encrypted"},
			backup.Secrets.AccountKeyEncryptionKey, s.AccountKeyEncryptionKey)
		if err != nil {
			s.Logger.Error("restore_reencrypt_accounts_failed", "error", err)
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "could not re-encrypt account keys from this backup")
			return
		}
		backup.Tables["accounts"] = reencrypted
	}
	if backup.Secrets.APIHMACMasterKey != s.APIHMACMasterKey {
		reencrypted, err := reencryptRows(backup.Tables["api_keys"], []string{"secret_encrypted", "previous_secret_encrypted"},
			backup.Secrets.APIHMACMasterKey, s.APIHMACMasterKey)
		if err != nil {
			s.Logger.Error("restore_reencrypt_api_keys_failed", "error", err)
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "could not re-encrypt API key secrets from this backup")
			return
		}
		backup.Tables["api_keys"] = reencrypted
	}

	counts, err := s.Store.RestoreTables(ctx, backup.Tables)
	if err != nil {
		s.Logger.Error("restore_tables_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "restore failed - no changes were applied: "+err.Error())
		return
	}

	resp := restoreBackupResponse{Restored: counts}
	if backup.CA != nil && s.CADataDir != "" {
		if err := s.writeRestoredCA(*backup.CA); err != nil {
			// The database restore already committed - report the partial outcome
			// honestly rather than failing the whole request after the point of no
			// return. The old CA keeps working meanwhile.
			s.Logger.Error("restore_ca_write_failed", "error", err)
		} else {
			resp.CARestored = true
			resp.RestartRequired = !bytes.Equal(bytes.TrimSpace(s.CA.CertPEM), bytes.TrimSpace([]byte(backup.CA.CertPEM)))
		}
	}

	// Written after RestoreTables so this lands in the restored audit_log - the
	// restore event must be visible in the timeline the panel now shows.
	if identity, ok := callerIdentityFromContext(ctx); ok {
		if err := s.Store.InsertAuditLog(ctx, identity.AdminUsername, "backup.restored", "panel", counts, r.RemoteAddr); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// reencryptRows rewrites the named wgkeys-encrypted columns in a json_agg rows
// blob from oldKey to newKey. Null/absent/empty values pass through untouched
// (previous_secret_encrypted is nullable). Missing/empty rows blobs (nothing to
// re-encrypt) pass through as-is.
func reencryptRows(rows json.RawMessage, columns []string, oldKey, newKey string) (json.RawMessage, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	var list []map[string]any
	if err := json.Unmarshal(rows, &list); err != nil {
		return nil, err
	}
	for i, row := range list {
		for _, col := range columns {
			v, ok := row[col].(string)
			if !ok || v == "" {
				continue
			}
			plain, err := wgkeys.Decrypt(oldKey, v)
			if err != nil {
				return nil, fmt.Errorf("row %d, column %s: %w", i, col, err)
			}
			enc, err := wgkeys.Encrypt(newKey, plain)
			if err != nil {
				return nil, fmt.Errorf("row %d, column %s: %w", i, col, err)
			}
			row[col] = enc
		}
	}
	return json.Marshal(list)
}

func (s *Server) writeRestoredCA(ca backupCA) error {
	if err := os.MkdirAll(s.CADataDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.CADataDir, "ca-cert.pem"), []byte(ca.CertPEM), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.CADataDir, "ca-key.pem"), []byte(ca.KeyPEM), 0o600)
}
