package backends

import (
	"context"
	"reflect"
	"testing"
)

func TestRedisConfigBackendLoadUnflattensHash(t *testing.T) {
	client := &fakeRedisClient{hash: map[string]string{
		"proxy.timeout": "20",
		"app.keys":      `["a","b"]`,
		"bad.raw":       "not-json",
	}}
	backend := NewRedisConfigBackend(client, RedisConfigOptions{})

	got, err := backend.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	want := map[string]any{
		"proxy": map[string]any{"timeout": int64(20)},
		"app":   map[string]any{"keys": []any{"a", "b"}},
		"bad":   map[string]any{"raw": "not-json"},
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("loaded config mismatch\nwant: %#v\n got: %#v", want, got)
	}
	if client.hgetallKey != "config:user" {
		t.Fatalf("hgetall key = %q", client.hgetallKey)
	}
}

func TestRedisConfigBackendApplyPatchWritesOnlyChangedFieldsAndIncrementsVersion(t *testing.T) {
	client := &fakeRedisClient{}
	backend := NewRedisConfigBackend(client, RedisConfigOptions{HashKey: "hash", VersionKey: "ver"})

	err := backend.ApplyPatch(context.Background(), map[string]any{
		"proxy": map[string]any{"timeout": int64(30)},
		"app":   map[string]any{"enabled": true},
	})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if client.pipeline == nil || !client.pipeline.executed {
		t.Fatalf("pipeline was not executed")
	}
	if !client.pipelineTransaction {
		t.Fatalf("ApplyPatch should use a transactional Redis pipeline")
	}
	wantMapping := map[string]string{"proxy.timeout": "30", "app.enabled": "true"}
	if !reflect.DeepEqual(wantMapping, client.pipeline.mapping) || client.pipeline.hsetKey != "hash" || client.pipeline.incrKey != "ver" {
		t.Fatalf("pipeline state = %#v", client.pipeline)
	}

	client = &fakeRedisClient{}
	backend = NewRedisConfigBackend(client, RedisConfigOptions{})
	if err := backend.ApplyPatch(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("empty ApplyPatch returned error: %v", err)
	}
	if client.pipeline != nil {
		t.Fatalf("empty patch should not create pipeline")
	}
}

func TestRedisConfigBackendClearDeletesHashAndIncrementsVersion(t *testing.T) {
	client := &fakeRedisClient{}
	backend := NewRedisConfigBackend(client, RedisConfigOptions{HashKey: "hash", VersionKey: "ver"})

	if err := backend.Clear(context.Background()); err != nil {
		t.Fatalf("Clear returned error: %v", err)
	}
	if client.pipeline == nil || !client.pipeline.executed {
		t.Fatalf("pipeline was not executed")
	}
	if client.pipeline.delKey != "hash" || client.pipeline.incrKey != "ver" {
		t.Fatalf("pipeline state = %#v", client.pipeline)
	}
}

func TestRedisConfigBackendVersionAndClose(t *testing.T) {
	client := &fakeRedisClient{value: "42"}
	backend := NewRedisConfigBackend(client, RedisConfigOptions{})

	version, err := backend.Version(context.Background())
	if err != nil {
		t.Fatalf("Version returned error: %v", err)
	}
	if version != int64(42) || client.getKey != "config:version" {
		t.Fatalf("version=%#v getKey=%q", version, client.getKey)
	}
	client.value = ""
	version, err = backend.Version(context.Background())
	if err != nil {
		t.Fatalf("empty Version returned error: %v", err)
	}
	if version != int64(0) {
		t.Fatalf("empty version = %#v", version)
	}
	if err := backend.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !client.closed {
		t.Fatalf("client was not closed")
	}
}

type fakeRedisClient struct {
	hash                map[string]string
	value               string
	hgetallKey          string
	getKey              string
	pipeline            *fakeRedisPipeline
	pipelineTransaction bool
	closed              bool
}

func (c *fakeRedisClient) HGetAll(_ context.Context, key string) (map[string]string, error) {
	c.hgetallKey = key
	return c.hash, nil
}

func (c *fakeRedisClient) Get(_ context.Context, key string) (string, error) {
	c.getKey = key
	return c.value, nil
}

func (c *fakeRedisClient) Pipeline(transaction bool) RedisPipeline {
	c.pipelineTransaction = transaction
	c.pipeline = &fakeRedisPipeline{}
	return c.pipeline
}

func (c *fakeRedisClient) AClose(context.Context) error {
	c.closed = true
	return nil
}

type fakeRedisPipeline struct {
	delKey   string
	hsetKey  string
	mapping  map[string]string
	incrKey  string
	executed bool
}

func (p *fakeRedisPipeline) Del(_ context.Context, key string) {
	p.delKey = key
}

func (p *fakeRedisPipeline) HSet(_ context.Context, key string, mapping map[string]string) {
	p.hsetKey = key
	p.mapping = mapping
}

func (p *fakeRedisPipeline) Incr(_ context.Context, key string) {
	p.incrKey = key
}

func (p *fakeRedisPipeline) Execute(context.Context) error {
	p.executed = true
	return nil
}
