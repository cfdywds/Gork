package backends

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTomlConfigBackendLoadMissingAndVersionMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "config.toml")
	backend := NewTomlConfigBackend(path)

	loaded, err := backend.Load(context.Background())
	if err != nil {
		t.Fatalf("Load missing returned error: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("missing load = %#v", loaded)
	}

	version, err := backend.Version(context.Background())
	if err != nil {
		t.Fatalf("Version missing returned error: %v", err)
	}
	if version != float64(0) {
		t.Fatalf("missing version = %#v", version)
	}
}

func TestTomlConfigBackendApplyPatchDeepMergesAndCreatesParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("[model]\nname = \"old\"\nkeep = true\n[limits]\nrequests = 2\n"), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	backend := NewTomlConfigBackend(path)
	err := backend.ApplyPatch(context.Background(), map[string]any{
		"model": map[string]any{"name": "grok-2"},
		"flags": map[string]any{"stream": true},
	})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}

	loaded, err := backend.Load(context.Background())
	if err != nil {
		t.Fatalf("Load after patch returned error: %v", err)
	}
	want := map[string]any{
		"model": map[string]any{
			"name": "grok-2",
			"keep": true,
		},
		"limits": map[string]any{"requests": int64(2)},
		"flags":  map[string]any{"stream": true},
	}
	if !reflect.DeepEqual(want, loaded) {
		t.Fatalf("loaded=%#v want=%#v", loaded, want)
	}

	version, err := backend.Version(context.Background())
	if err != nil {
		t.Fatalf("Version returned error: %v", err)
	}
	if _, ok := version.(float64); !ok || version.(float64) <= 0 {
		t.Fatalf("version=%#v", version)
	}
}

func TestTomlConfigBackendClearRemovesStoredOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("[model]\nname = \"old\"\n"), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	backend := NewTomlConfigBackend(path)
	if err := backend.Clear(context.Background()); err != nil {
		t.Fatalf("Clear returned error: %v", err)
	}
	loaded, err := backend.Load(context.Background())
	if err != nil {
		t.Fatalf("Load after Clear returned error: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("cleared load = %#v", loaded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("cleared file should exist: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("cleared file size = %d", info.Size())
	}
}

func TestTomlConfigBackendRoundTripsArraysLikePythonTomllibTomliW(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[proxy.egress]\nproxy_pool = [\"http://a\", \"http://b\"]\nreset_session_status_codes = [403]\nflags = [true, false]\n"), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	backend := NewTomlConfigBackend(path)
	loaded, err := backend.Load(context.Background())
	if err != nil {
		t.Fatalf("Load array TOML returned error: %v", err)
	}
	wantLoaded := map[string]any{
		"proxy": map[string]any{
			"egress": map[string]any{
				"proxy_pool":                 []any{"http://a", "http://b"},
				"reset_session_status_codes": []any{int64(403)},
				"flags":                      []any{true, false},
			},
		},
	}
	if !reflect.DeepEqual(wantLoaded, loaded) {
		t.Fatalf("loaded=%#v want=%#v", loaded, wantLoaded)
	}

	if err := backend.ApplyPatch(context.Background(), map[string]any{
		"proxy": map[string]any{
			"egress": map[string]any{
				"proxy_pool":                 []any{"http://c"},
				"reset_session_status_codes": []int{401, 403},
			},
		},
	}); err != nil {
		t.Fatalf("ApplyPatch array values returned error: %v", err)
	}

	reloaded, err := backend.Load(context.Background())
	if err != nil {
		t.Fatalf("Load rewritten array TOML returned error: %v", err)
	}
	wantReloaded := map[string]any{
		"proxy": map[string]any{
			"egress": map[string]any{
				"proxy_pool":                 []any{"http://c"},
				"reset_session_status_codes": []any{int64(401), int64(403)},
				"flags":                      []any{true, false},
			},
		},
	}
	if !reflect.DeepEqual(wantReloaded, reloaded) {
		t.Fatalf("reloaded=%#v want=%#v", reloaded, wantReloaded)
	}
}

func TestTomlConfigBackendEmptyPatchStillWritesAndCloseIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	backend := NewTomlConfigBackend(path)

	if err := backend.ApplyPatch(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("empty ApplyPatch returned error: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("empty patch should create file: %v", err)
	}
	loaded, err := backend.Load(context.Background())
	if err != nil {
		t.Fatalf("Load empty file returned error: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("empty file load = %#v", loaded)
	}
	if err := backend.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}
