package backupcrypto

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	plaintext := []byte(`{"tables":{"accounts":[]},"secret":"hunter2"}`)

	env, err := Seal("correct horse battery staple", plaintext, "2026-07-18T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if env.Format != Format {
		t.Errorf("format = %q, want %q", env.Format, Format)
	}

	// The envelope must survive JSON serialization - that's the on-disk shape.
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("hunter2")) {
		t.Fatal("plaintext leaked into the serialized envelope")
	}
	var decoded Envelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}

	got, err := Open("correct horse battery staple", decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round trip mismatch: %q", got)
	}
}

func TestOpenWrongPassword(t *testing.T) {
	env, err := Seal("right-password", []byte("data"), "2026-07-18T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open("wrong-password", env); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("expected ErrWrongPassword, got %v", err)
	}
}

func TestOpenTamperedData(t *testing.T) {
	env, err := Seal("pw", []byte("data"), "2026-07-18T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	env.Data = env.Data[:len(env.Data)-2] + "AA"
	if _, err := Open("pw", env); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("expected ErrWrongPassword for tampered data, got %v", err)
	}
}

func TestOpenRejectsHostileKDFParams(t *testing.T) {
	env, err := Seal("pw", []byte("data"), "2026-07-18T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	env.KDF.MemoryKB = 64 * 1024 * 1024 // 64GB - a memory-exhaustion attempt
	if _, err := Open("pw", env); err == nil || errors.Is(err, ErrWrongPassword) {
		t.Fatalf("expected a KDF-parameter rejection, got %v", err)
	}
}
