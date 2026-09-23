package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireRoleHierarchy(t *testing.T) {
	s := &Server{}

	cases := []struct {
		name     string
		role     string
		minRole  string
		wantPass bool
	}{
		{"support cannot reach operator-gated route", "support", "operator", false},
		{"support cannot reach super_admin-gated route", "support", "super_admin", false},
		{"operator can reach operator-gated route", "operator", "operator", true},
		{"operator cannot reach super_admin-gated route", "operator", "super_admin", false},
		{"super_admin can reach operator-gated route", "super_admin", "operator", true},
		{"super_admin can reach super_admin-gated route", "super_admin", "super_admin", true},
		{"unknown/empty role is rejected everywhere", "", "operator", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
			handler := s.requireRole(c.minRole, inner)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes", nil)
			ctx := context.WithValue(req.Context(), callerIdentityKey, &CallerIdentity{
				IsAdmin: true, AdminUsername: "x", AdminRole: c.role,
			})
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req.WithContext(ctx))

			if called != c.wantPass {
				t.Fatalf("role %q against minRole %q: expected pass=%v, got called=%v (status %d)", c.role, c.minRole, c.wantPass, called, rr.Code)
			}
			if !c.wantPass && rr.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for a rejected role, got %d", rr.Code)
			}
		})
	}
}

func TestRequirePermissionSupportRoleIsReadOnly(t *testing.T) {
	s := &Server{}

	cases := []struct {
		perm     string
		wantPass bool
	}{
		{"read", true},
		{"create", false},
		{"update", false},
		{"suspend", false},
		{"delete", false},
	}

	for _, c := range cases {
		t.Run(c.perm, func(t *testing.T) {
			called := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
			handler := s.requirePermission(c.perm, inner)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
			ctx := context.WithValue(req.Context(), callerIdentityKey, &CallerIdentity{
				IsAdmin: true, AdminUsername: "support-user", AdminRole: "support",
			})
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req.WithContext(ctx))

			if called != c.wantPass {
				t.Fatalf("perm %q for support role: expected pass=%v, got called=%v (status %d)", c.perm, c.wantPass, called, rr.Code)
			}
		})
	}
}

func TestRequirePermissionOperatorAndSuperAdminBypassRoleCheck(t *testing.T) {
	s := &Server{}

	for _, role := range []string{"operator", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			called := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
			handler := s.requirePermission("delete", inner)

			req := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/x", nil)
			ctx := context.WithValue(req.Context(), callerIdentityKey, &CallerIdentity{
				IsAdmin: true, AdminUsername: "x", AdminRole: role,
			})
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req.WithContext(ctx))

			if !called {
				t.Fatalf("expected role %q to be allowed to delete, got status %d", role, rr.Code)
			}
		})
	}
}
