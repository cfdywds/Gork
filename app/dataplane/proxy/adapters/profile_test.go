package adapters

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	controlproxy "github.com/dslzl/gork/app/control/proxy"
	platformconfig "github.com/dslzl/gork/app/platform/config"
)

type fakeProfileConfigBackend struct {
	data map[string]any
}

func (f fakeProfileConfigBackend) Load(context.Context) (map[string]any, error) {
	return f.data, nil
}

func (f fakeProfileConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return nil
}

func (f fakeProfileConfigBackend) Clear(context.Context) error {
	return nil
}

func (f fakeProfileConfigBackend) Version(context.Context) (any, error) {
	return "test", nil
}

func (f fakeProfileConfigBackend) Close(context.Context) error {
	return nil
}

func TestExtractCookieValueMatchesPython(t *testing.T) {
	if got := ExtractCookieValue("", "cf_clearance"); got != "" {
		t.Fatalf("empty cookie value = %q", got)
	}
	if got := ExtractCookieValue("foo=1; cf_clearance=abc; bar=2", "cf_clearance"); got != "abc" {
		t.Fatalf("cf_clearance = %q, want abc", got)
	}
	if got := ExtractCookieValue("foo=1; cf_clearance=abc", "missing"); got != "" {
		t.Fatalf("missing cookie = %q, want empty", got)
	}
}

func TestBrowserFromUserAgentMatchesPython(t *testing.T) {
	cases := []struct {
		ua   string
		want string
	}{
		{"Mozilla/5.0 Firefox/123.0", "firefox123"},
		{"Mozilla/5.0 Edg/120.0", "edge120"},
		{"Mozilla/5.0 Chrome/120.0.0.0", "chrome120"},
		{"Mozilla/5.0 Linux; Android 14 Chrome/120.0.0.0", "chrome120_android"},
		{"Mozilla/5.0 iPhone Version/17.0 Mobile Safari/604.1", "safari_ios"},
		{"Mozilla/5.0 Macintosh Version/17.0 Safari/605.1", "safari"},
		{"unknown", ""},
	}
	for _, tt := range cases {
		if got := BrowserFromUserAgent(tt.ua); got != tt.want {
			t.Fatalf("BrowserFromUserAgent(%q) = %q, want %q", tt.ua, got, tt.want)
		}
	}
}

func TestResolveProxyProfileUsesLeaseBeforeConfig(t *testing.T) {
	lease := controlproxy.NewProxyLease("lease-1")
	lease.CFCookies = "foo=1; cf_clearance=lease-clearance"
	lease.UserAgent = "Mozilla/5.0 Chrome/120.0.0.0"
	cfg := controlproxy.ClearanceConfig{
		CFCookies:   "cfg-cookies",
		UserAgent:   "cfg-ua",
		CFClearance: "cfg-clearance",
		Browser:     "firefox",
	}

	profile := ResolveProxyProfile(&lease, cfg)
	if profile.CFCookies != lease.CFCookies ||
		profile.UserAgent != lease.UserAgent ||
		profile.CFClearance != "lease-clearance" ||
		profile.Browser != "chrome120" {
		t.Fatalf("ResolveProxyProfile lease = %#v", profile)
	}
}

func TestResolveProxyProfileUsesConfigFallbacks(t *testing.T) {
	cfg := controlproxy.ClearanceConfig{
		CFCookies:   "cfg-cookies",
		UserAgent:   "",
		CFClearance: "cfg-clearance",
		Browser:     "firefox",
	}

	profile := ResolveProxyProfile(nil, cfg)
	if profile.CFCookies != "cfg-cookies" ||
		profile.UserAgent != "" ||
		profile.CFClearance != "cfg-clearance" ||
		profile.Browser != "firefox" {
		t.Fatalf("ResolveProxyProfile config = %#v", profile)
	}

	empty := ResolveProxyProfile(nil)
	if empty.Browser != "chrome120" {
		t.Fatalf("empty config browser = %q, want chrome120", empty.Browser)
	}
}

func TestResolveProxyProfileNilConfigUsesGlobalConfig(t *testing.T) {
	defaultsPath := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaultsPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	previous := platformconfig.GlobalConfig
	t.Cleanup(func() {
		platformconfig.GlobalConfig = previous
	})
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(fakeProfileConfigBackend{
		data: map[string]any{
			"proxy": map[string]any{
				"cf_cookies":   "global-cookies",
				"user_agent":   "Mozilla/5.0 Firefox/123.0",
				"cf_clearance": "global-clearance",
				"browser":      "chrome120",
			},
		},
	}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaultsPath); err != nil {
		t.Fatalf("load global config: %v", err)
	}

	profile := ResolveProxyProfile(nil)
	if profile.CFCookies != "global-cookies" ||
		profile.UserAgent != "Mozilla/5.0 Firefox/123.0" ||
		profile.CFClearance != "global-clearance" ||
		profile.Browser != "firefox123" {
		t.Fatalf("ResolveProxyProfile global config = %#v", profile)
	}
}
