package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	platformconfig "github.com/dslzl/gork/app/platform/config"
)

type fakeFlareSolverrConfig struct {
	strings map[string]string
	ints    map[string]int
}

func (f fakeFlareSolverrConfig) GetString(key, defaultValue string) string {
	if value, ok := f.strings[key]; ok {
		return value
	}
	return defaultValue
}

func (f fakeFlareSolverrConfig) GetInt(key string, defaultValue int) int {
	if value, ok := f.ints[key]; ok {
		return value
	}
	return defaultValue
}

type fakeFlareSolverrGlobalConfigBackend struct {
	data map[string]any
}

func (f fakeFlareSolverrGlobalConfigBackend) Load(context.Context) (map[string]any, error) {
	return f.data, nil
}

func (f fakeFlareSolverrGlobalConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return nil
}

func (f fakeFlareSolverrGlobalConfigBackend) Clear(context.Context) error {
	return nil
}

func (f fakeFlareSolverrGlobalConfigBackend) Version(context.Context) (any, error) {
	return "test", nil
}

func (f fakeFlareSolverrGlobalConfigBackend) Close(context.Context) error {
	return nil
}

type failingFlareSolverrClient struct{}

func (failingFlareSolverrClient) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection failed")
}

func TestFlareSolverrProviderReturnsFalseUnlessEnabled(t *testing.T) {
	provider := FlareSolverrClearanceProvider{
		Config: fakeFlareSolverrConfig{
			strings: map[string]string{"proxy.clearance.mode": "manual"},
		},
	}

	if _, ok, err := provider.RefreshBundle(context.Background(), "node-1", "http://proxy:8080"); err != nil || ok {
		t.Fatalf("RefreshBundle should return false without flaresolverr mode")
	}
}

func TestFlareSolverrProviderSuppressesRequestAndDecodeFailures(t *testing.T) {
	config := fakeFlareSolverrConfig{
		strings: map[string]string{
			"proxy.clearance.mode":             "flaresolverr",
			"proxy.clearance.flaresolverr_url": "https://flaresolverr.local",
		},
	}
	provider := FlareSolverrClearanceProvider{
		Config: config,
		Client: failingFlareSolverrClient{},
	}
	if _, ok, err := provider.RefreshBundle(context.Background(), "node-1", "", "https://grok.com"); err != nil || ok {
		t.Fatalf("RefreshBundle client failure ok=%v err=%v, want false nil", ok, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()
	provider = FlareSolverrClearanceProvider{
		Config: fakeFlareSolverrConfig{
			strings: map[string]string{
				"proxy.clearance.mode":             "flaresolverr",
				"proxy.clearance.flaresolverr_url": server.URL,
			},
		},
		Client: server.Client(),
	}
	if _, ok, err := provider.RefreshBundle(context.Background(), "node-1", "", "https://grok.com"); err != nil || ok {
		t.Fatalf("RefreshBundle decode failure ok=%v err=%v, want false nil", ok, err)
	}
}

func TestFlareSolverrProviderNilConfigUsesGlobalConfig(t *testing.T) {
	var requestSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen = true
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if payload["cmd"] != "request.get" || payload["url"] != "https://grok.com/global" || payload["maxTimeout"] != float64(5000) {
			t.Fatalf("payload = %#v", payload)
		}
		if _, ok := payload["proxy"]; ok {
			t.Fatalf("payload unexpectedly included proxy: %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "ok",
			"solution": {
				"cookies": [{"name": "cf_clearance", "value": "global", "domain": ".grok.com"}],
				"userAgent": "Global-UA"
			}
		}`))
	}))
	defer server.Close()

	defaultsPath := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaultsPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	previous := platformconfig.GlobalConfig
	t.Cleanup(func() {
		platformconfig.GlobalConfig = previous
	})
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(fakeFlareSolverrGlobalConfigBackend{
		data: map[string]any{
			"proxy": map[string]any{
				"clearance": map[string]any{
					"mode":             "flaresolverr",
					"flaresolverr_url": server.URL,
					"timeout_sec":      5,
				},
			},
		},
	}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaultsPath); err != nil {
		t.Fatalf("load global config: %v", err)
	}

	provider := FlareSolverrClearanceProvider{Client: server.Client()}
	bundle, ok, err := provider.RefreshBundle(context.Background(), "node-global", "", "https://grok.com/global")
	if err != nil {
		t.Fatalf("RefreshBundle returned error: %v", err)
	}
	if !ok || !requestSeen {
		t.Fatalf("RefreshBundle ok=%v requestSeen=%v", ok, requestSeen)
	}
	if bundle.BundleID != "flaresolverr:node-global@grok.com" ||
		bundle.CFCookies != "cf_clearance=global" ||
		bundle.UserAgent != "Global-UA" ||
		bundle.AffinityKey != "node-global" ||
		bundle.ClearanceHost != "grok.com" {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestFlareSolverrProviderPostsRequestAndBuildsBundle(t *testing.T) {
	var requestSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1" {
			t.Fatalf("request = %s %s, want POST /v1", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if payload["cmd"] != "request.get" || payload["url"] != "https://grok.com/path" || payload["maxTimeout"] != float64(5000) {
			t.Fatalf("payload = %#v", payload)
		}
		proxyPayload, ok := payload["proxy"].(map[string]any)
		if !ok || proxyPayload["url"] != "http://proxy:8080" {
			t.Fatalf("proxy payload = %#v", payload["proxy"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "ok",
			"solution": {
				"cookies": [
					{"name": "cf_clearance", "value": "abc", "domain": ".grok.com"},
					{"name": "session", "value": "skip", "domain": "example.com"}
				],
				"userAgent": "Mozilla/5.0"
			}
		}`))
	}))
	defer server.Close()

	provider := FlareSolverrClearanceProvider{
		Config: fakeFlareSolverrConfig{
			strings: map[string]string{
				"proxy.clearance.mode":             "flaresolverr",
				"proxy.clearance.flaresolverr_url": server.URL,
			},
			ints: map[string]int{"proxy.clearance.timeout_sec": 5},
		},
		Client: server.Client(),
	}

	bundle, ok, err := provider.RefreshBundle(context.Background(), "node-1", "http://proxy:8080", "https://grok.com/path")
	if err != nil {
		t.Fatalf("RefreshBundle returned error: %v", err)
	}
	if !ok || !requestSeen {
		t.Fatalf("RefreshBundle ok=%v requestSeen=%v", ok, requestSeen)
	}
	if bundle.BundleID != "flaresolverr:node-1@grok.com" ||
		bundle.CFCookies != "cf_clearance=abc" ||
		bundle.UserAgent != "Mozilla/5.0" ||
		bundle.AffinityKey != "node-1" ||
		bundle.ClearanceHost != "grok.com" {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestFlareSolverrProviderReturnsFalseForNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","message":"bad"}`))
	}))
	defer server.Close()

	provider := FlareSolverrClearanceProvider{
		Config: fakeFlareSolverrConfig{
			strings: map[string]string{
				"proxy.clearance.mode":             "flaresolverr",
				"proxy.clearance.flaresolverr_url": server.URL,
			},
		},
		Client: server.Client(),
	}

	if _, ok, err := provider.RefreshBundle(context.Background(), "node-1", "", "https://grok.com"); err != nil || ok {
		t.Fatalf("RefreshBundle non-ok status ok=%v err=%v, want false nil", ok, err)
	}
}
