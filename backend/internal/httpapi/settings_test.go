package httpapi

import (
	"testing"

	"wgpanel-api/internal/store"
)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

func TestSubscriptionBaseURL(t *testing.T) {
	cases := map[string]struct {
		settings store.PanelSettings
		want     *string
	}{
		"feature off":       {store.PanelSettings{}, nil},
		"cleared to empty":  {store.PanelSettings{SubDomain: strPtr("")}, nil},
		"domain only":       {store.PanelSettings{SubDomain: strPtr("sub.example.com")}, strPtr("https://sub.example.com")},
		"explicit 443":      {store.PanelSettings{SubDomain: strPtr("sub.example.com"), SubPort: intPtr(443)}, strPtr("https://sub.example.com")},
		"non-default port":  {store.PanelSettings{SubDomain: strPtr("sub.example.com"), SubPort: intPtr(8443)}, strPtr("https://sub.example.com:8443")},
		"port without host": {store.PanelSettings{SubPort: intPtr(8443)}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := subscriptionBaseURL(tc.settings)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("subscriptionBaseURL(%+v) = %v, want %v", tc.settings, deref(got), deref(tc.want))
			}
		})
	}
}

func TestDomainConfigFromSettings(t *testing.T) {
	s := &Server{AdminACLEmail: "ops@example.com", BootPanelDomain: "boot.example.com"}

	t.Run("falls back to boot domain", func(t *testing.T) {
		cfg, err := s.domainConfigFromSettings(store.PanelSettings{SubDomain: strPtr("sub.example.com")})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Domain != "boot.example.com" || cfg.SubDomain != "sub.example.com" {
			t.Errorf("got %+v", cfg)
		}
	})

	t.Run("db panel domain wins over boot", func(t *testing.T) {
		cfg, err := s.domainConfigFromSettings(store.PanelSettings{PanelDomain: strPtr("panel.example.com")})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Domain != "panel.example.com" {
			t.Errorf("got domain %q", cfg.Domain)
		}
	})

	t.Run("rejects sub domain shadowing the panel on 443", func(t *testing.T) {
		_, err := s.domainConfigFromSettings(store.PanelSettings{
			PanelDomain: strPtr("panel.example.com"),
			SubDomain:   strPtr("panel.example.com"),
		})
		if err == nil {
			t.Fatal("expected an error for a duplicate site address")
		}
	})

	t.Run("same domain on a different port is fine", func(t *testing.T) {
		cfg, err := s.domainConfigFromSettings(store.PanelSettings{
			PanelDomain: strPtr("panel.example.com"),
			SubDomain:   strPtr("panel.example.com"),
			SubPort:     intPtr(8443),
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SubPort != 8443 {
			t.Errorf("got %+v", cfg)
		}
	})

	t.Run("no domain anywhere", func(t *testing.T) {
		bare := &Server{AdminACLEmail: "ops@example.com"}
		if _, err := bare.domainConfigFromSettings(store.PanelSettings{SubDomain: strPtr("sub.example.com")}); err == nil {
			t.Fatal("expected an error when no panel domain is known")
		}
	})
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
