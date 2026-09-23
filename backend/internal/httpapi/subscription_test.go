package httpapi

import "testing"

func TestConfFilename(t *testing.T) {
	cases := map[string]string{
		"alice-laptop":                  "alice-laptop.conf",
		"Alice's Laptop!":               "alice-s-laptop.conf",
		"":                              "wgpanel.conf",
		"!!!":                           "wgpanel.conf",
		"a-very-long-account-label-xyz": "a-very-long-acc.conf", // wg-quick tunnel names cap at 15 chars
		"abcdefghijklmn-xyz":            "abcdefghijklmn.conf",  // 15-char cut lands on a "-"; trimmed after truncation, no trailing separator
	}
	for label, want := range cases {
		if got := confFilename(label); got != want {
			t.Errorf("confFilename(%q) = %q, want %q", label, got, want)
		}
	}
}

func TestRedactPathMasksSubscriptionTokens(t *testing.T) {
	cases := map[string]string{
		"/api/v1/sub/aabbccdd":       "/api/v1/sub/[redacted]",
		"/api/v1/sub/aabbccdd/nodes": "/api/v1/sub/[redacted]/nodes",
		"/api/v1/sub/":               "/api/v1/sub/", // nothing to redact
		"/api/v1/accounts/123":       "/api/v1/accounts/123",
		"/healthz":                   "/healthz",
	}
	for in, want := range cases {
		if got := redactPath(in); got != want {
			t.Errorf("redactPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewSubscriptionTokenShape(t *testing.T) {
	a, err := newSubscriptionToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newSubscriptionToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 48 {
		t.Fatalf("expected 48 hex chars (192 bits), got %d", len(a))
	}
	if a == b {
		t.Fatalf("two tokens should never collide")
	}
}
