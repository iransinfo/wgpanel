// Package backupcrypto encrypts panel backups under an admin-chosen password so a
// backup file is fully self-contained: it can safely embed the deployment's
// ACCOUNT_KEY_ENCRYPTION_KEY and API_HMAC_MASTER_KEY (without which the accounts
// and api_keys tables are ciphertext) and still be handed to a fresh server whose
// deploy/.env no longer exists - the disaster-recovery case where the original
// server is simply gone. The password is the only thing the admin must retain.
//
// Construction: argon2id (same cost posture as authcrypto's password hashing)
// derives a 32-byte key from the password; AES-256-GCM seals the whole backup
// JSON. KDF parameters travel inside the envelope so they can be tuned later
// without breaking old files.
package backupcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// Format identifies the envelope; bump the suffix on any incompatible change.
const Format = "wgpanel-backup-enc/1"

const (
	kdfTimeCost = 1
	kdfMemoryKB = 64 * 1024
	kdfThreads  = 4
	keyLenByte  = 32
	saltLenByte = 16
)

// ErrWrongPassword covers both a wrong password and a corrupted/tampered file -
// AES-GCM authentication cannot distinguish the two, and callers shouldn't try.
var ErrWrongPassword = errors.New("wrong password or corrupted backup file")

type KDFParams struct {
	Salt     string `json:"salt"` // base64 (raw, unpadded)
	TimeCost uint32 `json:"t"`
	MemoryKB uint32 `json:"m"`
	Threads  uint8  `json:"p"`
}

// Envelope is the outer, plaintext-JSON shape of an encrypted backup file. Only
// format/created_at are readable without the password.
type Envelope struct {
	Format    string    `json:"format"`
	CreatedAt string    `json:"created_at"`
	KDF       KDFParams `json:"kdf"`
	Nonce     string    `json:"nonce"` // base64 (raw, unpadded)
	Data      string    `json:"data"`  // base64 (raw, unpadded) AES-256-GCM ciphertext
}

// Seal encrypts plaintext under password into a ready-to-serialize Envelope.
func Seal(password string, plaintext []byte, createdAt string) (Envelope, error) {
	salt := make([]byte, saltLenByte)
	if _, err := rand.Read(salt); err != nil {
		return Envelope{}, fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, kdfTimeCost, kdfMemoryKB, kdfThreads, keyLenByte)

	block, err := aes.NewCipher(key)
	if err != nil {
		return Envelope{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, fmt.Errorf("generate nonce: %w", err)
	}

	enc := base64.RawStdEncoding
	return Envelope{
		Format:    Format,
		CreatedAt: createdAt,
		KDF: KDFParams{
			Salt:     enc.EncodeToString(salt),
			TimeCost: kdfTimeCost,
			MemoryKB: kdfMemoryKB,
			Threads:  kdfThreads,
		},
		Nonce: enc.EncodeToString(nonce),
		Data:  enc.EncodeToString(gcm.Seal(nil, nonce, plaintext, nil)),
	}, nil
}

// Open decrypts an Envelope with password, using the KDF parameters the file was
// sealed with. Returns ErrWrongPassword on authentication failure.
func Open(password string, env Envelope) ([]byte, error) {
	if env.Format != Format {
		return nil, fmt.Errorf("unrecognized backup format %q (expected %q)", env.Format, Format)
	}
	dec := base64.RawStdEncoding
	salt, err := dec.DecodeString(env.KDF.Salt)
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	nonce, err := dec.DecodeString(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	data, err := dec.DecodeString(env.Data)
	if err != nil {
		return nil, fmt.Errorf("decode data: %w", err)
	}
	// Guard the KDF cost against a hostile file that would have us allocate
	// unbounded memory before authentication can reject it.
	if env.KDF.MemoryKB > 1024*1024 || env.KDF.TimeCost > 16 || env.KDF.Threads == 0 {
		return nil, fmt.Errorf("unreasonable KDF parameters in backup file")
	}

	key := argon2.IDKey([]byte(password), salt, env.KDF.TimeCost, env.KDF.MemoryKB, env.KDF.Threads, keyLenByte)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, ErrWrongPassword
	}
	return plaintext, nil
}
