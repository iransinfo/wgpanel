package caddyadmin

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startFakeCaddy listens on a real Unix socket like Caddy's admin API would and
// captures the body POSTed to /load, so these tests exercise the actual dial path,
// not just the template.
func startFakeCaddy(t *testing.T) (socketPath string, lastBody *string) {
	t.Helper()
	// Not t.TempDir(): it embeds the (sub)test name, and Unix socket paths are
	// capped at ~104 chars on macOS - long test names make bind fail with EINVAL.
	dir, err := os.MkdirTemp("", "wgca")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socketPath = filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var body string
	lastBody = &body
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusOK)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return socketPath, lastBody
}

func TestApplyPanelDomainOnly(t *testing.T) {
	socketPath, body := startFakeCaddy(t)
	c := New(socketPath)

	if err := c.Apply(context.Background(), Config{Domain: "panel.example.com", Email: "ops@example.com"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*body, "panel.example.com {") {
		t.Errorf("expected panel site block, got:\n%s", *body)
	}
	if strings.Contains(*body, "/api/v1/sub/*") {
		t.Errorf("no sub domain configured, but a subscription block was rendered:\n%s", *body)
	}
}

func TestApplyWithSubDomain(t *testing.T) {
	cases := map[string]struct {
		port        int
		wantAddress string
	}{
		"default port":  {port: 0, wantAddress: "\nsub.example.com {"},
		"explicit 443":  {port: 443, wantAddress: "\nsub.example.com {"},
		"separate port": {port: 8443, wantAddress: "\nsub.example.com:8443 {"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			socketPath, body := startFakeCaddy(t)
			c := New(socketPath)

			err := c.Apply(context.Background(), Config{
				Domain:    "panel.example.com",
				Email:     "ops@example.com",
				SubDomain: "sub.example.com",
				SubPort:   tc.port,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(*body, tc.wantAddress) {
				t.Errorf("expected subscription site address %q, got:\n%s", tc.wantAddress, *body)
			}
			// The subscription block must serve only the capability endpoints - the
			// panel SPA/API must never be reachable through it.
			if !strings.Contains(*body, "handle /api/v1/sub/*") || !strings.Contains(*body, "respond 404") {
				t.Errorf("subscription block should proxy only /api/v1/sub/* and 404 the rest, got:\n%s", *body)
			}
			if strings.Count(*body, "reverse_proxy frontend:80") != 1 {
				t.Errorf("frontend should only be proxied from the panel block, got:\n%s", *body)
			}
		})
	}
}

func TestApplyRequiresDomain(t *testing.T) {
	c := New("/nonexistent.sock")
	if err := c.Apply(context.Background(), Config{Email: "ops@example.com"}); err == nil {
		t.Fatal("expected an error for an empty domain")
	}
}
