package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"testing"

	"wgpanel-api/internal/wgkeys"
)

func testHexKey(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(buf)
}

func TestReencryptRows(t *testing.T) {
	oldKey, newKey := testHexKey(t), testHexKey(t)

	encrypted, err := wgkeys.Encrypt(oldKey, "wg-private-key-1")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := json.Marshal([]map[string]any{
		{"id": "a", "label": "one", "private_key_encrypted": encrypted},
		// Nullable/absent encrypted columns must pass through untouched
		// (api_keys.previous_secret_encrypted is the real-world case).
		{"id": "b", "label": "two", "private_key_encrypted": nil},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := reencryptRows(rows, []string{"private_key_encrypted"}, oldKey, newKey)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}

	plain, err := wgkeys.Decrypt(newKey, decoded[0]["private_key_encrypted"].(string))
	if err != nil {
		t.Fatalf("decrypt with new key: %v", err)
	}
	if plain != "wg-private-key-1" {
		t.Errorf("round trip = %q, want wg-private-key-1", plain)
	}
	if decoded[1]["private_key_encrypted"] != nil {
		t.Errorf("null column should stay null, got %v", decoded[1]["private_key_encrypted"])
	}
	if decoded[0]["label"] != "one" {
		t.Errorf("unrelated column mangled: %v", decoded[0]["label"])
	}

	// A blob encrypted under a different key than claimed must fail loudly - it
	// would otherwise restore undecryptable account keys.
	if _, err := reencryptRows(rows, []string{"private_key_encrypted"}, newKey, oldKey); err == nil {
		t.Error("expected an error re-encrypting with the wrong source key")
	}

	// Empty/missing table blobs (nothing dumped) pass through.
	if out, err := reencryptRows(nil, []string{"x"}, oldKey, newKey); err != nil || out != nil {
		t.Errorf("nil rows: out=%v err=%v", out, err)
	}
}
