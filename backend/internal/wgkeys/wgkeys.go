// Package wgkeys generates real WireGuard (Curve25519) keypairs and encrypts the
// private key at rest with a dedicated AES-256-GCM key (docs/PRD-account-management.md
// §4, §7). This is pure cryptography - no kernel/netlink access, so it works
// identically whether or not any real WireGuard interface exists yet.
package wgkeys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type KeyPair struct {
	PublicKey  string // base64, safe to store/display in plaintext
	PrivateKey string // base64, must be encrypted before storage - see Encrypt
}

func GenerateKeyPair() (KeyPair, error) {
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate wireguard keypair: %w", err)
	}
	return KeyPair{
		PublicKey:  priv.PublicKey().String(),
		PrivateKey: priv.String(),
	}, nil
}

// Encrypt AES-256-GCM encrypts plaintext (a private key) with hexKey (32 raw bytes,
// hex-encoded - i.e. ACCOUNT_KEY_ENCRYPTION_KEY) and returns a base64 string safe to
// store in a TEXT column. The nonce is prepended to the ciphertext.
func Encrypt(hexKey, plaintext string) (string, error) {
	gcm, err := newGCM(hexKey)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt reverses Encrypt.
func Decrypt(hexKey, encoded string) (string, error) {
	gcm, err := newGCM(hexKey)
	if err != nil {
		return "", err
	}

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}

func newGCM(hexKey string) (cipher.AEAD, error) {
	key, err := decodeHexKey(hexKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

func decodeHexKey(hexKey string) ([]byte, error) {
	key := make([]byte, hex.DecodedLen(len(hexKey)))
	n, err := hex.Decode(key, []byte(hexKey))
	if err != nil {
		return nil, fmt.Errorf("decode ACCOUNT_KEY_ENCRYPTION_KEY as hex: %w", err)
	}
	key = key[:n]
	if len(key) != 32 {
		return nil, fmt.Errorf("ACCOUNT_KEY_ENCRYPTION_KEY must decode to exactly 32 bytes, got %d", len(key))
	}
	return key, nil
}
