package wgkeys

import (
	"encoding/hex"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	return hex.EncodeToString(raw)
}

func TestGenerateKeyPairProducesDistinctValidKeys(t *testing.T) {
	kp1, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	kp2, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	if kp1.PrivateKey == kp2.PrivateKey {
		t.Fatal("expected two calls to produce different private keys")
	}
	if kp1.PublicKey == "" || kp1.PrivateKey == "" {
		t.Fatal("expected non-empty keys")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testKey(t)
	plaintext := "super-secret-wireguard-private-key"

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ciphertext == plaintext {
		t.Fatal("ciphertext should not equal plaintext")
	}

	decrypted, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestDecryptFailsWithWrongKey(t *testing.T) {
	rightKey := testKey(t)
	wrongRaw := make([]byte, 32)
	for i := range wrongRaw {
		wrongRaw[i] = byte(255 - i)
	}
	wrongKey := hex.EncodeToString(wrongRaw)

	ciphertext, err := Encrypt(rightKey, "secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if _, err := Decrypt(wrongKey, ciphertext); err == nil {
		t.Fatal("expected decryption with the wrong key to fail")
	}
}

func TestKeyMustBeExactly32Bytes(t *testing.T) {
	shortKey := hex.EncodeToString([]byte("too short"))
	if _, err := Encrypt(shortKey, "secret"); err == nil {
		t.Fatal("expected Encrypt to reject a non-32-byte key")
	}
}
