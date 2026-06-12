package backends

import (
	"context"
	"strconv"
)

const (
	defaultRedisConfigHashKey    = "config:user"
	defaultRedisConfigVersionKey = "config:version"
)

type RedisClient interface {
	HGetAll(ctx context.Context, key string) (map[string]string, error)
	Get(ctx context.Context, key string) (string, error)
	Pipeline(transaction bool) RedisPipeline
	AClose(ctx context.Context) error
}

type RedisPipeline interface {
	Del(ctx context.Context, key string)
	HSet(ctx context.Context, key string, mapping map[string]string)
	Incr(ctx context.Context, key string)
	Execute(ctx context.Context) error
}

type RedisConfigOptions struct {
	HashKey    string
	VersionKey string
}

type RedisConfigBackend struct {
	client     RedisClient
	hashKey    string
	versionKey string
}

func NewRedisConfigBackend(client RedisClient, options RedisConfigOptions) *RedisConfigBackend {
	hashKey := options.HashKey
	if hashKey == "" {
		hashKey = defaultRedisConfigHashKey
	}
	versionKey := options.VersionKey
	if versionKey == "" {
		versionKey = defaultRedisConfigVersionKey
	}
	return &RedisConfigBackend{client: client, hashKey: hashKey, versionKey: versionKey}
}

func (b *RedisConfigBackend) Load(ctx context.Context) (map[string]any, error) {
	raw, err := b.client.HGetAll(ctx, b.hashKey)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	return Unflatten(raw), nil
}

func (b *RedisConfigBackend) ApplyPatch(ctx context.Context, patch map[string]any) error {
	flat, err := Flatten(patch)
	if err != nil {
		return err
	}
	if len(flat) == 0 {
		return nil
	}
	pipe := b.client.Pipeline(true)
	pipe.HSet(ctx, b.hashKey, flat)
	pipe.Incr(ctx, b.versionKey)
	return pipe.Execute(ctx)
}

func (b *RedisConfigBackend) Clear(ctx context.Context) error {
	pipe := b.client.Pipeline(true)
	pipe.Del(ctx, b.hashKey)
	pipe.Incr(ctx, b.versionKey)
	return pipe.Execute(ctx)
}

func (b *RedisConfigBackend) Version(ctx context.Context) (any, error) {
	raw, err := b.client.Get(ctx, b.versionKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return int64(0), nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return int64(0), err
	}
	return value, nil
}

func (b *RedisConfigBackend) Close(ctx context.Context) error {
	return b.client.AClose(ctx)
}
