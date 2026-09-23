package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"wgpanel-api/internal/authcrypto"
)

func TestRequireAdminRejectsMissingMalformedAndExpiredTokens(t *testing.T) {
	s := &Server{JWTSecret: "test-secret"}
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	handler := s.requireAdmin(inner)

	validToken, err := authcrypto.IssueAccessToken("test-secret", "admin-id", "admin", "super_admin")
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	wrongSecretToken, err := authcrypto.IssueAccessToken("other-secret", "admin-id", "admin", "super_admin")
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}

	cases := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"no bearer prefix", validToken},
		{"malformed token", "Bearer not-a-jwt"},
		{"signed with wrong secret", "Bearer " + wrongSecretToken},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rr.Code)
			}
			if called {
				t.Fatal("inner handler should not have been called")
			}
		})
	}
}

func TestRequireAdminAllowsValidToken(t *testing.T) {
	s := &Server{JWTSecret: "test-secret"}
	var gotClaims *authcrypto.Claims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims, _ = adminFromContext(r.Context())
	})
	handler := s.requireAdmin(inner)

	validToken, err := authcrypto.IssueAccessToken("test-secret", "admin-id", "admin", "super_admin")
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+validToken)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if gotClaims == nil || gotClaims.AdminID != "admin-id" {
		t.Fatalf("expected claims to be attached to context, got %+v", gotClaims)
	}
}
