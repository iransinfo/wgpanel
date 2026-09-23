// Package authcrypto holds password hashing (argon2id) and token issuance (JWT +
// Redis-backed refresh tokens) for admin authentication (docs/PRD-security-access-control.md §6).
package authcrypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTimeCost   = 1
	argonMemoryKB   = 64 * 1024 // 64 MB
	argonThreads    = 4
	argonKeyLenByte = 32
	saltLenByte     = 16
)

// HashPassword returns a self-describing encoded string:
// argon2id$m=<memoryKB>,t=<timeCost>,p=<threads>$<base64 salt>$<base64 hash>
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLenByte)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTimeCost, argonMemoryKB, argonThreads, argonKeyLenByte)

	return fmt.Sprintf("argon2id$m=%d,t=%d,p=%d$%s$%s",
		argonMemoryKB, argonTimeCost, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword checks a plaintext password against a hash produced by HashPassword.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "argon2id" {
		return false, fmt.Errorf("unrecognized hash format")
	}

	var memoryKB, timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[1], "m=%d,t=%d,p=%d", &memoryKB, &timeCost, &threads); err != nil {
		return false, fmt.Errorf("parse hash params: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}
	wantHash, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}

	gotHash := argon2.IDKey([]byte(password), salt, timeCost, memoryKB, threads, uint32(len(wantHash)))
	return subtle.ConstantTimeCompare(gotHash, wantHash) == 1, nil
}
