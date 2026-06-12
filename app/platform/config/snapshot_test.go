package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dslzl/gork/app/platform/config/backends"
)

func TestConfigSnapshotLoadMergesBackendEnvAndCachesByVersion(t *testing.T) {
	defaults := writeSnapshotDefaults(t)
	backend := &fakeConfigBackend{
		data:    map[string]any{"proxy": map[string]any{"timeout": int64(20), "mode": "pool"}},
		version: "v1",
	}
	snapshot := NewConfigSnapshot(backend, ConfigSnapshotOptions{
		Env: map[string]string{
			"GROK_PROXY_BASE_PROXY_URL": "env-proxy",
			"GROK_APP_WEBUI_ENABLED":    "true",
		},
	})

	if err := snapshot.Load(context.Background(), defaults); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if snapshot.Get("proxy.base_proxy_url", "") != "env-proxy" {
		t.Fatalf("env override missing: %#v", snapshot.Raw())
	}
	if snapshot.GetInt("proxy.timeout", 0) != 20 || snapshot.GetStr("proxy.mode", "") != "pool" {
		t.Fatalf("backend merge missing: %#v", snapshot.Raw())
	}
	if !snapshot.GetBool("app.webui_enabled", false) {
		t.Fatalf("env bool string should read true: %#v", snapshot.Raw())
	}

	if err := snapshot.Load(context.Background(), defaults); err != nil {
		t.Fatalf("second Load returned error: %v", err)
	}
	if backend.loadCalls != 1 || backend.versionCalls != 3 {
		t.Fatalf("cache calls load=%d version=%d", backend.loadCalls, backend.versionCalls)
	}
}

func TestConfigSnapshotNilBackendCreatesLocalBackendLikePython(t *testing.T) {
	defaults := writeSnapshotDefaults(t)
	userPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(userPath, []byte("[app]\nwebui_enabled = true\n[proxy]\ntimeout = 44\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	snapshot := NewConfigSnapshot(nil, ConfigSnapshotOptions{
		Env: map[string]string{
			"ACCOUNT_STORAGE":    "local",
			"CONFIG_LOCAL_PATH":  userPath,
			"GROK_PROXY_TIMEOUT": "55",
		},
	})

	if err := snapshot.Load(context.Background(), defaults); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !snapshot.GetBool("app.webui_enabled", false) {
		t.Fatalf("nil backend did not load local user config: %#v", snapshot.Raw())
	}
	if snapshot.GetInt("proxy.timeout", 0) != 55 {
		t.Fatalf("env should still win over local user config: %#v", snapshot.Raw())
	}
}

func TestConfigSnapshotUpdateInvalidatesVersionAndTypedGetters(t *testing.T) {
	defaults := writeSnapshotDefaults(t)
	backend := &fakeConfigBackend{data: map[string]any{}, version: "v1"}
	snapshot := NewConfigSnapshot(backend, ConfigSnapshotOptions{})
	if err := snapshot.Load(context.Background(), defaults); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if err := snapshot.Update(context.Background(), map[string]any{"app": map[string]any{"api_key": "a,b"}}); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if !reflect.DeepEqual(backend.patches[0], map[string]any{"app": map[string]any{"api_key": "a,b"}}) {
		t.Fatalf("patches = %#v", backend.patches)
	}
	backend.data = map[string]any{"app": map[string]any{"api_key": "a,b"}}
	backend.version = "v2"
	if err := snapshot.Load(context.Background(), defaults); err != nil {
		t.Fatalf("Load after update returned error: %v", err)
	}
	if !reflect.DeepEqual(snapshot.GetList("app.api_key", nil), []any{"a", "b"}) {
		t.Fatalf("api key list = %#v", snapshot.GetList("app.api_key", nil))
	}
	if snapshot.GetFloat("numbers.ratio", 0) != 1.5 || snapshot.GetInt("numbers.bad", 9) != 9 {
		t.Fatalf("typed numeric getters mismatch raw=%#v", snapshot.Raw())
	}
	if snapshot.GetStr("missing.key", "fallback") != "fallback" {
		t.Fatalf("string default mismatch")
	}
}

func TestConfigSnapshotResetClearsOverridesAndReloadsDefaults(t *testing.T) {
	defaults := writeSnapshotDefaults(t)
	backend := &fakeConfigBackend{
		data:    map[string]any{"proxy": map[string]any{"timeout": int64(20), "mode": "pool"}},
		version: "v1",
	}
	snapshot := NewConfigSnapshot(backend, ConfigSnapshotOptions{})
	if err := snapshot.Load(context.Background(), defaults); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if err := snapshot.Reset(context.Background()); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}
	if !backend.cleared {
		t.Fatalf("backend was not cleared")
	}
	backend.data = map[string]any{}
	backend.version = "v2"
	if err := snapshot.Load(context.Background(), defaults); err != nil {
		t.Fatalf("Load after Reset returned error: %v", err)
	}
	if snapshot.GetInt("proxy.timeout", 0) != 10 || snapshot.GetStr("proxy.mode", "") != "" {
		t.Fatalf("reset should expose defaults only: %#v", snapshot.Raw())
	}
}

func TestConfigSnapshotLoadMissingDefaultsReturnsErrorAndRawIsShallowCopy(t *testing.T) {
	snapshot := NewConfigSnapshot(&fakeConfigBackend{}, ConfigSnapshotOptions{})
	err := snapshot.Load(context.Background(), filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil {
		t.Fatalf("missing defaults should return error")
	}

	defaults := writeSnapshotDefaults(t)
	if err := snapshot.Load(context.Background(), defaults); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	raw := snapshot.Raw()
	raw["new"] = "value"
	if snapshot.Get("new", nil) != nil {
		t.Fatalf("Raw should return a shallow copy")
	}
}

func TestGetConfigUsesGlobalConfigSingleton(t *testing.T) {
	previous := GlobalConfig
	t.Cleanup(func() { GlobalConfig = previous })
	defaults := writeSnapshotDefaults(t)
	GlobalConfig = NewConfigSnapshot(&fakeConfigBackend{
		data:    map[string]any{"app": map[string]any{"name": "from-backend"}},
		version: "v1",
	}, ConfigSnapshotOptions{})
	if err := GlobalConfig.Load(context.Background(), defaults); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if GetConfig("app.name", "") != "from-backend" {
		t.Fatalf("GetConfig did not read GlobalConfig: %#v", GlobalConfig.Raw())
	}
}

func TestResolveDefaultsPathMatchesPythonSourceRootResolution(t *testing.T) {
	path := ResolveDefaultsPath()
	if !filepath.IsAbs(path) {
		t.Fatalf("ResolveDefaultsPath should return absolute project-root path, got %q", path)
	}
	if filepath.Base(path) != "config.defaults.toml" {
		t.Fatalf("ResolveDefaultsPath base = %q", filepath.Base(path))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("resolved defaults path should exist: %v", err)
	}
}

func TestResolveDefaultsPathHandlesTrimpathCallerFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.defaults.toml"), []byte("ok = true\n"), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}

	workDir := filepath.Join(root, "app", "platform", "config")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	path := resolveDefaultsPathFromLocations(
		"github.com/dslzl/gork/app/platform/config/snapshot.go",
		workDir,
		"",
	)
	want := filepath.Join(root, "config.defaults.toml")
	if path != want {
		t.Fatalf("trimpath defaults path = %q, want %q", path, want)
	}
}

func writeSnapshotDefaults(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.defaults.toml")
	if err := os.WriteFile(path, []byte(`
[proxy]
base_proxy_url = "default"
timeout = 10

[app]
webui_enabled = false

[numbers]
ratio = 1.5
bad = "oops"
`), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	return path
}

type fakeConfigBackend struct {
	backends.ConfigBackend
	data         map[string]any
	version      any
	loadCalls    int
	versionCalls int
	patches      []map[string]any
	cleared      bool
}

func (b *fakeConfigBackend) Load(context.Context) (map[string]any, error) {
	b.loadCalls++
	return b.data, nil
}

func (b *fakeConfigBackend) ApplyPatch(_ context.Context, patch map[string]any) error {
	b.patches = append(b.patches, patch)
	return nil
}

func (b *fakeConfigBackend) Clear(context.Context) error {
	b.cleared = true
	return nil
}

func (b *fakeConfigBackend) Version(context.Context) (any, error) {
	b.versionCalls++
	return b.version, nil
}

func (b *fakeConfigBackend) Close(context.Context) error {
	return nil
}
