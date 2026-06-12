package backends

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetConfigBackendNameMatchesAccountStorage(t *testing.T) {
	if got := GetConfigBackendName(map[string]string{}); got != "local" {
		t.Fatalf("default backend = %q, want local", got)
	}
	if got := GetConfigBackendName(map[string]string{"ACCOUNT_STORAGE": " ReDiS "}); got != "redis" {
		t.Fatalf("normalized backend = %q, want redis", got)
	}
}

func TestCreateConfigBackendBuildsLocalPath(t *testing.T) {
	calls := &factoryCalls{}
	backend, err := CreateConfigBackend(FactoryOptions{
		Env:         map[string]string{"ACCOUNT_STORAGE": "local"},
		DataDir:     filepath.Join("data"),
		ProjectRoot: filepath.Join("repo"),
		NewToml:     calls.newToml,
	})
	if err != nil {
		t.Fatalf("CreateConfigBackend local returned error: %v", err)
	}
	if backend != calls.backend {
		t.Fatalf("local backend = %#v, want injected backend", backend)
	}
	if calls.tomlPath != filepath.Join("repo", "data", "config.toml") {
		t.Fatalf("default local path = %q", calls.tomlPath)
	}

	calls = &factoryCalls{}
	_, err = CreateConfigBackend(FactoryOptions{
		Env:         map[string]string{"ACCOUNT_STORAGE": "local", "CONFIG_LOCAL_PATH": "relative/config.toml"},
		ProjectRoot: filepath.Join("repo"),
		NewToml:     calls.newToml,
	})
	if err != nil {
		t.Fatalf("CreateConfigBackend relative local returned error: %v", err)
	}
	if calls.tomlPath != filepath.Join("repo", "relative", "config.toml") {
		t.Fatalf("relative local path = %q", calls.tomlPath)
	}
}

func TestCreateConfigBackendDefaultLocalUsesTomlBackend(t *testing.T) {
	dataDir := t.TempDir()
	backend, err := CreateConfigBackend(FactoryOptions{
		Env:     map[string]string{"ACCOUNT_STORAGE": "local"},
		DataDir: dataDir,
	})
	if err != nil {
		t.Fatalf("CreateConfigBackend default local returned error: %v", err)
	}
	if _, ok := backend.(*TomlConfigBackend); !ok {
		t.Fatalf("default local backend = %T, want *TomlConfigBackend", backend)
	}
	if err := backend.ApplyPatch(context.Background(), map[string]any{
		"model": map[string]any{"name": "grok-2"},
	}); err != nil {
		t.Fatalf("default local ApplyPatch returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "config.toml")); err != nil {
		t.Fatalf("default local backend did not write config.toml: %v", err)
	}
}

func TestCreateConfigBackendDefaultRemoteBackendsUseRealConstructors(t *testing.T) {
	redisBackend, err := CreateConfigBackend(FactoryOptions{
		Env: map[string]string{
			"ACCOUNT_STORAGE":   "redis",
			"ACCOUNT_REDIS_URL": "redis://localhost:6379/0",
		},
	})
	if err != nil {
		t.Fatalf("CreateConfigBackend default redis returned error: %v", err)
	}
	if _, ok := redisBackend.(*RedisConfigBackend); !ok {
		t.Fatalf("default redis backend = %T, want *RedisConfigBackend", redisBackend)
	}
	if err := redisBackend.Close(context.Background()); err != nil {
		t.Fatalf("default redis backend Close returned error: %v", err)
	}

	mysqlBackend, err := CreateConfigBackend(FactoryOptions{
		Env: map[string]string{
			"ACCOUNT_STORAGE":   "mysql",
			"ACCOUNT_MYSQL_URL": "user:pass@tcp(localhost:3306)/gork",
		},
	})
	if err != nil {
		t.Fatalf("CreateConfigBackend default mysql returned error: %v", err)
	}
	if _, ok := mysqlBackend.(*SQLConfigBackend); !ok {
		t.Fatalf("default mysql backend = %T, want *SQLConfigBackend", mysqlBackend)
	}
	if err := mysqlBackend.Close(context.Background()); err != nil {
		t.Fatalf("default mysql backend Close returned error: %v", err)
	}

	postgresBackend, err := CreateConfigBackend(FactoryOptions{
		Env: map[string]string{
			"ACCOUNT_STORAGE":        "postgresql",
			"ACCOUNT_POSTGRESQL_URL": "postgres://user:pass@localhost/gork?sslmode=disable",
		},
	})
	if err != nil {
		t.Fatalf("CreateConfigBackend default postgresql returned error: %v", err)
	}
	if _, ok := postgresBackend.(*SQLConfigBackend); !ok {
		t.Fatalf("default postgresql backend = %T, want *SQLConfigBackend", postgresBackend)
	}
	if err := postgresBackend.Close(context.Background()); err != nil {
		t.Fatalf("default postgresql backend Close returned error: %v", err)
	}
}

func TestCreateConfigBackendRedisAndSQLValidateURLs(t *testing.T) {
	calls := &factoryCalls{}
	backend, err := CreateConfigBackend(FactoryOptions{
		Env:      map[string]string{"ACCOUNT_STORAGE": "redis", "ACCOUNT_REDIS_URL": " redis://localhost "},
		NewRedis: calls.newRedis,
	})
	if err != nil {
		t.Fatalf("CreateConfigBackend redis returned error: %v", err)
	}
	if backend != calls.backend || calls.redisURL != "redis://localhost" {
		t.Fatalf("redis backend/url = %#v %q", backend, calls.redisURL)
	}

	_, err = CreateConfigBackend(FactoryOptions{Env: map[string]string{"ACCOUNT_STORAGE": "redis"}})
	if err == nil || err.Error() != "Redis config backend requires ACCOUNT_REDIS_URL" {
		t.Fatalf("missing redis URL error = %v", err)
	}

	calls = &factoryCalls{}
	backend, err = CreateConfigBackend(FactoryOptions{
		Env:    map[string]string{"ACCOUNT_STORAGE": "mysql", "ACCOUNT_MYSQL_URL": " mysql://dsn "},
		NewSQL: calls.newSQL,
	})
	if err != nil {
		t.Fatalf("CreateConfigBackend mysql returned error: %v", err)
	}
	if backend != calls.backend || calls.sqlDialect != "mysql" || calls.sqlURL != "mysql://dsn" {
		t.Fatalf("mysql call = dialect %q url %q backend %#v", calls.sqlDialect, calls.sqlURL, backend)
	}

	calls = &factoryCalls{}
	backend, err = CreateConfigBackend(FactoryOptions{
		Env:    map[string]string{"ACCOUNT_STORAGE": "postgresql", "ACCOUNT_POSTGRESQL_URL": " postgres://dsn "},
		NewSQL: calls.newSQL,
	})
	if err != nil {
		t.Fatalf("CreateConfigBackend postgresql returned error: %v", err)
	}
	if backend != calls.backend || calls.sqlDialect != "postgresql" || calls.sqlURL != "postgres://dsn" {
		t.Fatalf("postgresql call = dialect %q url %q backend %#v", calls.sqlDialect, calls.sqlURL, backend)
	}

	_, err = CreateConfigBackend(FactoryOptions{Env: map[string]string{"ACCOUNT_STORAGE": "mysql"}})
	if err == nil || err.Error() != "MySQL config backend requires ACCOUNT_MYSQL_URL" {
		t.Fatalf("missing mysql URL error = %v", err)
	}
	_, err = CreateConfigBackend(FactoryOptions{Env: map[string]string{"ACCOUNT_STORAGE": "postgresql"}})
	if err == nil || err.Error() != "PostgreSQL config backend requires ACCOUNT_POSTGRESQL_URL" {
		t.Fatalf("missing postgresql URL error = %v", err)
	}
}

func TestCreateConfigBackendUnknownAndConstructorErrors(t *testing.T) {
	_, err := CreateConfigBackend(FactoryOptions{Env: map[string]string{"ACCOUNT_STORAGE": "sqlite"}})
	if err == nil || err.Error() != "Unknown account storage backend: 'sqlite'" {
		t.Fatalf("unknown backend error = %v", err)
	}

	wantErr := errors.New("constructor failed")
	_, err = CreateConfigBackend(FactoryOptions{
		Env:     map[string]string{"ACCOUNT_STORAGE": "local"},
		NewToml: func(string) (ConfigBackend, error) { return nil, wantErr },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("constructor error = %v", err)
	}
}

type factoryCalls struct {
	backend    *fakeFactoryBackend
	tomlPath   string
	redisURL   string
	sqlDialect string
	sqlURL     string
}

func (c *factoryCalls) ensureBackend() *fakeFactoryBackend {
	if c.backend == nil {
		c.backend = &fakeFactoryBackend{}
	}
	return c.backend
}

func (c *factoryCalls) newToml(path string) (ConfigBackend, error) {
	c.tomlPath = path
	return c.ensureBackend(), nil
}

func (c *factoryCalls) newRedis(rawURL string) (ConfigBackend, error) {
	c.redisURL = rawURL
	return c.ensureBackend(), nil
}

func (c *factoryCalls) newSQL(dialect, rawURL string) (ConfigBackend, error) {
	c.sqlDialect = dialect
	c.sqlURL = rawURL
	return c.ensureBackend(), nil
}

type fakeFactoryBackend struct{}

func (fakeFactoryBackend) Load(context.Context) (map[string]any, error)     { return map[string]any{}, nil }
func (fakeFactoryBackend) ApplyPatch(context.Context, map[string]any) error { return nil }
func (fakeFactoryBackend) Clear(context.Context) error                      { return nil }
func (fakeFactoryBackend) Version(context.Context) (any, error)             { return nil, nil }
func (fakeFactoryBackend) Close(context.Context) error                      { return nil }

func TestFactoryUnknownErrorQuotesLikePythonRepr(t *testing.T) {
	_, err := CreateConfigBackend(FactoryOptions{Env: map[string]string{"ACCOUNT_STORAGE": "bad"}})
	if err == nil || !strings.Contains(err.Error(), `'bad'`) {
		t.Fatalf("quoted error = %v", err)
	}
}
