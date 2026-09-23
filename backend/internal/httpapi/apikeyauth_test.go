package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

// hexHMAC computes HMAC-SHA256 directly via stdlib primitives, independent of
// hmacSignatureMatches's own internals - so these tests check the production code
// against a known-correct reference rather than against itself.
func hexHMAC(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestHMACSignatureMatches(t *testing.T) {
	secret := "test-secret"
	payload := "POST\n/api/v1/accounts\n1700000000\n{\"label\":\"x\"}"

	valid := hmacSignatureMatches(secret, payload, computeTestSignature(secret, payload))
	if !valid {
		t.Fatal("expected a correctly computed signature to match")
	}

	if hmacSignatureMatches("wrong-secret", payload, computeTestSignature(secret, payload)) {
		t.Fatal("expected signature computed with a different secret to fail verification")
	}

	tamperedPayload := payload + "tampered"
	if hmacSignatureMatches(secret, tamperedPayload, computeTestSignature(secret, payload)) {
		t.Fatal("expected a signature to fail once the payload it covers is altered")
	}
}

func computeTestSignature(secret, payload string) string {
	// Deliberately reimplemented independently of hmacSignatureMatches's own HMAC
	// call, so this test doesn't just check the function agrees with itself.
	return hexHMAC(secret, payload)
}

func TestRequirePermissionAdminBypassesEverything(t *testing.T) {
	s := &Server{}
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	handler := s.requirePermission("delete", inner)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/x", nil)
	ctx := context.WithValue(req.Context(), callerIdentityKey, &CallerIdentity{IsAdmin: true, AdminUsername: "admin"})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req.WithContext(ctx))

	if !called || rr.Code != http.StatusOK {
		t.Fatalf("expected admin to bypass permission check, called=%v code=%d", called, rr.Code)
	}
}

func TestRequirePermissionRejectsAPIKeyWithoutIt(t *testing.T) {
	s := &Server{}
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	handler := s.requirePermission("delete", inner)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/x", nil)
	ctx := context.WithValue(req.Context(), callerIdentityKey, &CallerIdentity{
		KeyNamespace: "abc123", Permissions: []string{"read", "create"},
	})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req.WithContext(ctx))

	if called {
		t.Fatal("inner handler should not have been called")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestRequirePermissionAllowsAPIKeyWithIt(t *testing.T) {
	s := &Server{}
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	handler := s.requirePermission("read", inner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
	ctx := context.WithValue(req.Context(), callerIdentityKey, &CallerIdentity{
		KeyNamespace: "abc123", Permissions: []string{"read", "create"},
	})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req.WithContext(ctx))

	if !called {
		t.Fatal("expected inner handler to be called")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestCallerNamespaceArg(t *testing.T) {
	admin := &CallerIdentity{IsAdmin: true, AdminUsername: "admin"}
	if ns := callerNamespaceArg(admin); ns != nil {
		t.Fatalf("expected nil namespace for admin, got %v", *ns)
	}

	apiKey := &CallerIdentity{KeyNamespace: "abc123"}
	ns := callerNamespaceArg(apiKey)
	if ns == nil || *ns != "abc123" {
		t.Fatalf("expected namespace 'abc123', got %v", ns)
	}
}
