package providers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dslzl/gork/app/control/proxy"
	platformconfig "github.com/dslzl/gork/app/platform/config"
)

type fakeStringConfig map[string]string

func (f fakeStringConfig) GetString(key, defaultValue string) string {
	if value, ok := f[key]; ok {
		return value
	}
	return defaultValue
}

type fakeManualGlobalConfigBackend struct {
	data map[string]any
}

func (f fakeManualGlobalConfigBackend) Load(context.Context) (map[string]any, error) {
	return f.data, nil
}

func (f fakeManualGlobalConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return nil
}

func (f fakeManualGlobalConfigBackend) Clear(context.Context) error {
	return nil
}

func (f fakeManualGlobalConfigBackend) Version(context.Context) (any, error) {
	return "test", nil
}

func (f fakeManualGlobalConfigBackend) Close(context.Context) error {
	return nil
}

func TestManualClearanceProviderReturnsFalseUnlessManualMode(t *testing.T) {
	provider := ManualClearanceProvider{
		Config: fakeStringConfig{"proxy.clearance.mode": "none"},
	}

	if _, ok, err := provider.BuildBundle("node-1"); err != nil || ok {
		t.Fatalf("BuildBundle should return false when clearance mode is not manual")
	}
}

func TestManualClearanceProviderNilConfigUsesGlobalConfig(t *testing.T) {
	defaultsPath := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaultsPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	previous := platformconfig.GlobalConfig
	t.Cleanup(func() {
		platformconfig.GlobalConfig = previous
	})
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(fakeManualGlobalConfigBackend{
		data: map[string]any{
			"proxy": map[string]any{
				"clearance": map[string]any{
					"mode":       "manual",
					"cf_cookies": "global-cookies",
					"user_agent": "global-ua",
				},
			},
		},
	}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaultsPath); err != nil {
		t.Fatalf("load global config: %v", err)
	}

	provider := ManualClearanceProvider{}
	bundle, ok, err := provider.BuildBundle("node-global")
	if err != nil {
		t.Fatalf("BuildBundle returned error: %v", err)
	}
	if !ok {
		t.Fatalf("BuildBundle should return a bundle in manual mode")
	}
	if bundle.BundleID != "manual:node-global@grok.com" ||
		bundle.CFCookies != "global-cookies" ||
		bundle.UserAgent != "global-ua" ||
		bundle.AffinityKey != "node-global" ||
		bundle.ClearanceHost != "grok.com" {
		t.Fatalf("manual global bundle = %#v", bundle)
	}
}

func TestManualClearanceProviderBuildsBundleFromConfig(t *testing.T) {
	provider := ManualClearanceProvider{
		Config: fakeStringConfig{
			"proxy.clearance.mode":       "  MANUAL  ",
			"proxy.cf_cookies":           "cf_clearance=abc",
			"proxy.clearance.user_agent": "Mozilla/5.0",
		},
	}

	bundle, ok, err := provider.BuildBundle("node-1")
	if err != nil {
		t.Fatalf("BuildBundle returned error: %v", err)
	}
	if !ok {
		t.Fatalf("BuildBundle should return a bundle in manual mode")
	}
	if bundle.BundleID != "manual:node-1@grok.com" ||
		bundle.CFCookies != "cf_clearance=abc" ||
		bundle.UserAgent != "Mozilla/5.0" ||
		bundle.State != proxy.ClearanceBundleValid ||
		bundle.AffinityKey != "node-1" ||
		bundle.ClearanceHost != "grok.com" ||
		bundle.LastRefreshAt != nil {
		t.Fatalf("manual bundle = %#v", bundle)
	}
}

func TestManualClearanceProviderSupportsCustomHost(t *testing.T) {
	provider := ManualClearanceProvider{
		Config: fakeStringConfig{"proxy.clearance.mode": "manual"},
	}

	bundle, ok, err := provider.BuildBundle("node-1", "console.x.ai")
	if err != nil {
		t.Fatalf("BuildBundle returned error: %v", err)
	}
	if !ok {
		t.Fatalf("BuildBundle should return a bundle in manual mode")
	}
	if bundle.BundleID != "manual:node-1@console.x.ai" || bundle.ClearanceHost != "console.x.ai" {
		t.Fatalf("custom host bundle = %#v", bundle)
	}
}

func TestManualClearanceProviderPreservesExplicitEmptyHost(t *testing.T) {
	provider := ManualClearanceProvider{
		Config: fakeStringConfig{"proxy.clearance.mode": "manual"},
	}

	bundle, ok, err := provider.BuildBundle("node-1", "")
	if err != nil {
		t.Fatalf("BuildBundle returned error: %v", err)
	}
	if !ok {
		t.Fatalf("BuildBundle should return a bundle in manual mode")
	}
	if bundle.BundleID != "manual:node-1@" || bundle.ClearanceHost != "" {
		t.Fatalf("empty host bundle = %#v", bundle)
	}
}

func TestManualClearanceProviderReturnsParseErrors(t *testing.T) {
	provider := ManualClearanceProvider{
		Config: fakeStringConfig{"proxy.clearance.mode": "bad-mode"},
	}

	if _, _, err := provider.BuildBundle("node-1"); err == nil {
		t.Fatalf("BuildBundle should return parse errors for invalid clearance mode")
	}
}
