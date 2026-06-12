package backends

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

type fakeConfigBackend struct {
	data    map[string]any
	version any
	closed  bool
}

func (b *fakeConfigBackend) Load(_ context.Context) (map[string]any, error) {
	return b.data, nil
}

func (b *fakeConfigBackend) ApplyPatch(_ context.Context, patch map[string]any) error {
	b.data = patch
	return nil
}

func (b *fakeConfigBackend) Clear(context.Context) error {
	b.data = map[string]any{}
	return nil
}

func (b *fakeConfigBackend) Version(_ context.Context) (any, error) {
	return b.version, nil
}

func (b *fakeConfigBackend) Close(_ context.Context) error {
	b.closed = true
	return nil
}

func TestConfigBackendContract(t *testing.T) {
	var backend ConfigBackend = &fakeConfigBackend{
		data:    map[string]any{"server": map[string]any{"port": int64(8000)}},
		version: int64(7),
	}

	loaded, err := backend.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !reflect.DeepEqual(loaded, map[string]any{"server": map[string]any{"port": int64(8000)}}) {
		t.Fatalf("Load = %#v", loaded)
	}

	patch := map[string]any{"auth": map[string]any{"api_key": "secret"}}
	if err := backend.ApplyPatch(context.Background(), patch); err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	loaded, _ = backend.Load(context.Background())
	if !reflect.DeepEqual(loaded, patch) {
		t.Fatalf("Load after ApplyPatch = %#v, want %#v", loaded, patch)
	}

	version, err := backend.Version(context.Background())
	if err != nil {
		t.Fatalf("Version returned error: %v", err)
	}
	if version != int64(7) {
		t.Fatalf("Version = %#v, want 7", version)
	}

	if err := backend.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !backend.(*fakeConfigBackend).closed {
		t.Fatalf("Close did not mark backend closed")
	}
}

func TestConfigBackendDocMatchesPythonProtocolSemantics(t *testing.T) {
	content, err := os.ReadFile("base.go")
	if err != nil {
		t.Fatalf("read base.go: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"flat key-value pairs",
		"dotted keys",
		"JSON-serialized values",
		"rebuilds the full nested map",
		"persists only the keys present in patch",
		"cheap to call on every request",
		"optional Python close",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("base.go missing %q in:\n%s", want, text)
		}
	}
}
