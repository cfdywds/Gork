package backends

import (
	"context"
	"sort"

	redis "github.com/redis/go-redis/v9"
)

type goRedisConfigClient struct {
	client *redis.Client
}

type goRedisConfigPipeline struct {
	pipe redis.Pipeliner
}

func newGoRedisConfigBackend(rawURL string) (ConfigBackend, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	return NewRedisConfigBackend(
		&goRedisConfigClient{client: redis.NewClient(options)},
		RedisConfigOptions{},
	), nil
}

func (c *goRedisConfigClient) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return c.client.HGetAll(ctx, key).Result()
}

func (c *goRedisConfigClient) Get(ctx context.Context, key string) (string, error) {
	value, err := c.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return value, err
}

func (c *goRedisConfigClient) Pipeline(transaction bool) RedisPipeline {
	if transaction {
		return &goRedisConfigPipeline{pipe: c.client.TxPipeline()}
	}
	return &goRedisConfigPipeline{pipe: c.client.Pipeline()}
}

func (c *goRedisConfigClient) AClose(context.Context) error {
	return c.client.Close()
}

func (p *goRedisConfigPipeline) Del(ctx context.Context, key string) {
	p.pipe.Del(ctx, key)
}

func (p *goRedisConfigPipeline) HSet(ctx context.Context, key string, mapping map[string]string) {
	p.pipe.HSet(ctx, key, redisConfigHashArgs(mapping)...)
}

func (p *goRedisConfigPipeline) Incr(ctx context.Context, key string) {
	p.pipe.Incr(ctx, key)
}

func (p *goRedisConfigPipeline) Execute(ctx context.Context) error {
	_, err := p.pipe.Exec(ctx)
	return err
}

func redisConfigHashArgs(mapping map[string]string) []any {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := make([]any, 0, len(keys)*2)
	for _, key := range keys {
		args = append(args, key, mapping[key])
	}
	return args
}
