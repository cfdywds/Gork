package backends

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	platform "github.com/dslzl/gork/app/platform"
)

type BackendConstructor func(path string) (ConfigBackend, error)
type RedisBackendConstructor func(rawURL string) (ConfigBackend, error)
type SQLBackendConstructor func(dialect, rawURL string) (ConfigBackend, error)

type FactoryOptions struct {
	Env         map[string]string
	DataDir     string
	ProjectRoot string
	NewToml     BackendConstructor
	NewRedis    RedisBackendConstructor
	NewSQL      SQLBackendConstructor
}

func GetConfigBackendName(env map[string]string) string {
	return strings.ToLower(strings.TrimSpace(factoryEnv(env, "ACCOUNT_STORAGE", "local")))
}

func CreateConfigBackend(options FactoryOptions) (ConfigBackend, error) {
	backend := GetConfigBackendName(options.Env)
	switch backend {
	case "local":
		return makeTomlBackend(options)
	case "redis":
		return makeRedisBackend(options)
	case "mysql", "postgresql":
		return makeSQLBackend(backend, options)
	default:
		return nil, fmt.Errorf("Unknown account storage backend: '%s'", backend)
	}
}

func makeTomlBackend(options FactoryOptions) (ConfigBackend, error) {
	constructor := options.NewToml
	if constructor == nil {
		constructor = func(path string) (ConfigBackend, error) {
			return NewTomlConfigBackend(path), nil
		}
	}
	defaultPath := filepath.Join(factoryDataDir(options), "config.toml")
	path := strings.TrimSpace(factoryEnv(options.Env, "CONFIG_LOCAL_PATH", defaultPath))
	if !filepath.IsAbs(path) {
		path = filepath.Join(factoryProjectRoot(options), path)
	}
	return constructor(path)
}

func makeRedisBackend(options FactoryOptions) (ConfigBackend, error) {
	rawURL := strings.TrimSpace(factoryEnv(options.Env, "ACCOUNT_REDIS_URL", ""))
	if rawURL == "" {
		return nil, fmt.Errorf("Redis config backend requires ACCOUNT_REDIS_URL")
	}
	constructor := options.NewRedis
	if constructor == nil {
		constructor = newGoRedisConfigBackend
	}
	return constructor(rawURL)
}

func makeSQLBackend(dialect string, options FactoryOptions) (ConfigBackend, error) {
	envName := "ACCOUNT_POSTGRESQL_URL"
	missingMessage := "PostgreSQL config backend requires ACCOUNT_POSTGRESQL_URL"
	if dialect == "mysql" {
		envName = "ACCOUNT_MYSQL_URL"
		missingMessage = "MySQL config backend requires ACCOUNT_MYSQL_URL"
	}
	rawURL := strings.TrimSpace(factoryEnv(options.Env, envName, ""))
	if rawURL == "" {
		return nil, errors.New(missingMessage)
	}
	constructor := options.NewSQL
	if constructor == nil {
		constructor = newDatabaseSQLConfigBackend
	}
	return constructor(dialect, rawURL)
}

func factoryEnv(env map[string]string, key, defaultValue string) string {
	if env != nil {
		if value, ok := env[key]; ok {
			return value
		}
		return defaultValue
	}
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

func factoryDataDir(options FactoryOptions) string {
	if options.DataDir != "" {
		return options.DataDir
	}
	return platform.DataDir()
}

func factoryProjectRoot(options FactoryOptions) string {
	if options.ProjectRoot != "" {
		return options.ProjectRoot
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

type UnsupportedConfigBackend struct {
	Reason string
}

func (b UnsupportedConfigBackend) Load(context.Context) (map[string]any, error) {
	return nil, errors.New(b.Reason)
}

func (b UnsupportedConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return errors.New(b.Reason)
}

func (b UnsupportedConfigBackend) Clear(context.Context) error {
	return errors.New(b.Reason)
}

func (b UnsupportedConfigBackend) Version(context.Context) (any, error) {
	return nil, errors.New(b.Reason)
}

func (b UnsupportedConfigBackend) Close(context.Context) error {
	return nil
}
