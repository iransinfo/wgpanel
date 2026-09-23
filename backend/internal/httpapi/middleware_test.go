package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternalOnlyRejectsMissingOrWrongToken(t *testing.T) {
	s := &Server{InternalAPIToken: "correct-token"}
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	handler := s.internalOnly(inner)

	cases := []struct {
		name  string
		token string
	}{
		{"missing token", ""},
		{"wrong token", "wrong-token"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodGet, "/internal/healthz", nil)
			if c.token != "" {
				req.Header.Set("X-Internal-Token", c.token)
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

func TestInternalOnlyAllowsCorrectToken(t *testing.T) {
	s := &Server{InternalAPIToken: "correct-token"}
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	handler := s.internalOnly(inner)

	req := httptest.NewRequest(http.MethodGet, "/internal/healthz", nil)
	req.Header.Set("X-Internal-Token", "correct-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatal("inner handler should have been called")
	}
}
