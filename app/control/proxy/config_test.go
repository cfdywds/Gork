package proxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	platformconfig "github.com/dslzl/gork/app/platform/config"
)

type fakeStringConfig map[string]string

func (f fakeStringConfig) GetString(key, defaultValue string) string {
	if value, ok := f[key]; ok {
		return value
	}
	return defaultValue
}

type fakeGlobalConfigBackend struct {
	data map[string]any
}

func (f fakeGlobalConfigBackend) Load(context.Context) (map[string]any, error) {
	return f.data, nil
}

func (f fakeGlobalConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return nil
}

func (f fakeGlobalConfigBackend) Clear(context.Context) error {
	return nil
}

func (f fakeGlobalConfigBackend) Version(context.Context) (any, error) {
	return "test", nil
}

func (f fakeGlobalConfigBackend) Close(context.Context) error {
	return nil
}

func TestFirstConfigStringMatchesPython(t *testing.T) {
	cfg := fakeStringConfig{
		"empty":   "",
		"blank":   "   ",
		"primary": "  keep spacing  ",
		"legacy":  "legacy-value",
	}

	if got := FirstConfigString(cfg, "empty", "blank", "primary", "legacy"); got != "  keep spacing  " {
		t.Fatalf("FirstConfigString = %q, want value with original spacing", got)
	}
	if got := FirstConfigString(cfg, "missing", "legacy"); got != "legacy-value" {
		t.Fatalf("FirstConfigString fallback = %q, want legacy-value", got)
	}
	if got := FirstConfigString(cfg, "missing", "blank"); got != "" {
		t.Fatalf("FirstConfigString missing/blank = %q, want empty", got)
	}
}

func TestResolveClearanceConfigMatchesPythonKeyOrder(t *testing.T) {
	cfg := fakeStringConfig{
		"proxy.cf_cookies":             "cookies-primary",
		"proxy.clearance.cf_cookies":   "cookies-legacy",
		"proxy.clearance.user_agent":   "ua-legacy",
		"proxy.cf_clearance":           "   ",
		"proxy.clearance.cf_clearance": "clearance-legacy",
		"proxy.browser":                "firefox",
		"proxy.clearance.browser":      "chrome",
	}

	got := ResolveClearanceConfig(cfg)
	if got.CFCookies != "cookies-primary" || got.UserAgent != "ua-legacy" ||
		got.CFClearance != "clearance-legacy" || got.Browser != "firefox" {
		t.Fatalf("ResolveClearanceConfig = %#v", got)
	}
}

func TestResolveClearanceConfigNilSourceUsesGlobalConfig(t *testing.T) {
	defaultsPath := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaultsPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	previous := platformconfig.GlobalConfig
	t.Cleanup(func() {
		platformconfig.GlobalConfig = previous
	})
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(fakeGlobalConfigBackend{
		data: map[string]any{
			"proxy": map[string]any{
				"clearance": map[string]any{
					"cf_cookies":   "global-cookies",
					"user_agent":   "global-ua",
					"cf_clearance": "global-clearance",
					"browser":      "global-browser",
				},
			},
		},
	}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaultsPath); err != nil {
		t.Fatalf("load global config: %v", err)
	}

	got := ResolveClearanceConfig(nil)
	want := ClearanceConfig{
		CFCookies:   "global-cookies",
		UserAgent:   "global-ua",
		CFClearance: "global-clearance",
		Browser:     "global-browser",
	}
	if got != want {
		t.Fatalf("ResolveClearanceConfig(nil) = %#v, want %#v", got, want)
	}
}
