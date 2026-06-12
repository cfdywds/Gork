package config

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dslzl/gork/app/platform/config/backends"
)

type ConfigSnapshotOptions struct {
	Env map[string]string
}

type ConfigSnapshot struct {
	mu            sync.Mutex
	data          map[string]any
	loaded        bool
	mtimeDefaults time.Time
	version       any
	backend       backends.ConfigBackend
	env           map[string]string
}

func NewConfigSnapshot(backend backends.ConfigBackend, options ConfigSnapshotOptions) *ConfigSnapshot {
	return &ConfigSnapshot{
		data:    map[string]any{},
		backend: backend,
		env:     options.Env,
	}
}

func (s *ConfigSnapshot) Load(ctx context.Context, defaultsPath string) error {
	if defaultsPath == "" {
		defaultsPath = ResolveDefaultsPath()
	}
	backend, err := s.getBackend()
	if err != nil {
		return err
	}
	mt, err := configFileModTime(defaultsPath)
	if err != nil {
		return fmt.Errorf("Missing required defaults config: %s", defaultsPath)
	}
	version, err := backend.Version(ctx)
	if err != nil {
		return err
	}
	if s.loaded && mt.Equal(s.mtimeDefaults) && reflect.DeepEqual(version, s.version) {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	mt, err = configFileModTime(defaultsPath)
	if err != nil {
		return fmt.Errorf("Missing required defaults config: %s", defaultsPath)
	}
	version, err = backend.Version(ctx)
	if err != nil {
		return err
	}
	if s.loaded && mt.Equal(s.mtimeDefaults) && reflect.DeepEqual(version, s.version) {
		return nil
	}

	defaults, err := LoadTOML(defaultsPath)
	if err != nil {
		return err
	}
	overrides, err := backend.Load(ctx)
	if err != nil {
		return err
	}
	s.data = ApplyEnvConfig(DeepMergeConfig(defaults, overrides), "GROK_", s.env)
	s.loaded = true
	s.mtimeDefaults = mt
	s.version = version
	return nil
}

func (s *ConfigSnapshot) EnsureLoaded(ctx context.Context, defaultsPath string) error {
	if s.loaded {
		return nil
	}
	return s.Load(ctx, defaultsPath)
}

func (s *ConfigSnapshot) getBackend() (backends.ConfigBackend, error) {
	if s.backend != nil {
		return s.backend, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend != nil {
		return s.backend, nil
	}
	backend, err := backends.CreateConfigBackend(backends.FactoryOptions{
		Env: s.env,
		NewToml: func(path string) (backends.ConfigBackend, error) {
			return backends.NewTomlConfigBackend(path), nil
		},
	})
	if err != nil {
		return nil, err
	}
	s.backend = backend
	return s.backend, nil
}

func (s *ConfigSnapshot) Get(key string, defaultValue any) any {
	return GetNested(s.data, key, defaultValue)
}

func (s *ConfigSnapshot) GetInt(key string, defaultValue int) int {
	value := s.Get(key, defaultValue)
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(typed); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func (s *ConfigSnapshot) GetFloat(key string, defaultValue float64) float64 {
	value := s.Get(key, defaultValue)
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		if parsed, err := strconv.ParseFloat(typed, 64); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func (s *ConfigSnapshot) GetBool(key string, defaultValue bool) bool {
	value := s.Get(key, defaultValue)
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case nil:
		return defaultValue
	default:
		return true
	}
}

func (s *ConfigSnapshot) GetStr(key string, defaultValue string) string {
	value := s.Get(key, defaultValue)
	if value == nil {
		return defaultValue
	}
	return fmt.Sprint(value)
}

func (s *ConfigSnapshot) GetList(key string, defaultValue []any) []any {
	value := s.Get(key, defaultValue)
	if value == nil {
		if defaultValue == nil {
			return []any{}
		}
		return defaultValue
	}
	switch typed := value.(type) {
	case []any:
		return typed
	case []string:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	case string:
		parts := []any{}
		for _, part := range strings.Split(typed, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				parts = append(parts, part)
			}
		}
		return parts
	default:
		return []any{typed}
	}
}

func (s *ConfigSnapshot) Update(ctx context.Context, patch map[string]any) error {
	backend, err := s.getBackend()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := backend.ApplyPatch(ctx, patch); err != nil {
		return err
	}
	s.version = nil
	return nil
}

func (s *ConfigSnapshot) Reset(ctx context.Context) error {
	backend, err := s.getBackend()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := backend.Clear(ctx); err != nil {
		return err
	}
	s.data = map[string]any{}
	s.loaded = false
	s.version = nil
	return nil
}

func (s *ConfigSnapshot) Raw() map[string]any {
	out := map[string]any{}
	for key, value := range s.data {
		out[key] = value
	}
	return out
}

func ApplyEnvConfig(data map[string]any, prefix string, env map[string]string) map[string]any {
	if prefix == "" {
		prefix = "GROK_"
	}
	for key, value := range loadConfigEnv(env) {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		parts := strings.SplitN(strings.ToLower(key[len(prefix):]), "_", 2)
		if len(parts) != 2 {
			continue
		}
		section, item := parts[0], parts[1]
		nested, ok := data[section].(map[string]any)
		if !ok {
			nested = map[string]any{}
			data[section] = nested
		}
		nested[item] = value
	}
	return data
}

func ResolveDefaultsPath() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		file = ""
	}
	wd, _ := os.Getwd()
	return resolveDefaultsPathFromLocations(file, wd, "config.defaults.toml")
}

func configFileModTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

var GlobalConfig = NewConfigSnapshot(nil, ConfigSnapshotOptions{})

func GetConfig(key string, defaultValue any) any {
	return GlobalConfig.Get(key, defaultValue)
}

type noopConfigBackend struct{}

func (noopConfigBackend) Load(context.Context) (map[string]any, error) {
	return map[string]any{}, nil
}

func (noopConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return nil
}

func (noopConfigBackend) Clear(context.Context) error {
	return nil
}

func (noopConfigBackend) Version(context.Context) (any, error) {
	return nil, nil
}

func (noopConfigBackend) Close(context.Context) error {
	return nil
}
