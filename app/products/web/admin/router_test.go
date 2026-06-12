package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dslzl/gork/app/platform/auth"
	"github.com/dslzl/gork/app/platform/storage"
)

func TestAdminRouterVerifyRequiresAdminKey(t *testing.T) {
	resetAdminRouterDepsForTest(t)
	adminRouterAuthSettings = func() auth.AuthSettings { return auth.AuthSettings{AdminKey: "secret"} }

	rec := adminRequest(http.MethodGet, "/admin/api/verify", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = adminRequest(http.MethodGet, "/admin/api/verify", "", "Bearer secret")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"success"`) {
		t.Fatalf("valid key status/body=%d/%s", rec.Code, rec.Body.String())
	}
	rec = adminRequest(http.MethodGet, "/admin/api/verify?app_key=secret", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"success"`) {
		t.Fatalf("query key status/body=%d/%s", rec.Code, rec.Body.String())
	}
}

func TestAdminConfigAndStorageEndpointsMatchPythonShape(t *testing.T) {
	resetAdminRouterDepsForTest(t)
	adminRouterConfig = &fakeAdminConfig{raw: map[string]any{
		"app": map[string]any{"admin_key": "gork"},
	}}
	adminStorageBackend = func() string { return "local" }

	rec := adminRequest(http.MethodGet, "/admin/api/config", "", "Bearer gork")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"admin_key":"gork"`) {
		t.Fatalf("config status/body=%d/%s", rec.Code, rec.Body.String())
	}

	rec = adminRequest(http.MethodGet, "/admin/api/storage", "", "Bearer gork")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"type":"local"`) {
		t.Fatalf("storage status/body=%d/%s", rec.Code, rec.Body.String())
	}
}

func TestAdminConfigUpdateSanitizesAndRejectsStartupOnly(t *testing.T) {
	resetAdminRouterDepsForTest(t)
	cfg := &fakeAdminConfig{strs: map[string]string{"logging.file_level": "debug"}, ints: map[string]int{"logging.max_files": 3}}
	adminRouterConfig = cfg
	adminReconcileRefreshRuntime = func() string { return "quota" }
	var reloadedLevel string
	var reloadedMax int
	adminReloadFileLogging = func(level string, maxFiles int) error {
		reloadedLevel, reloadedMax = level, maxFiles
		return nil
	}
	reconciled := false
	adminReconcileLocalMediaCache = func(context.Context) error { reconciled = true; return nil }

	body := `{"proxy":{"cf_clearance":" a b ","user_agent":" “UA” "},"cache":{"local":{"image_limit_mb":10}}}`
	rec := adminRequest(http.MethodPost, "/admin/api/config", body, "Bearer gork")
	if rec.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", rec.Code, rec.Body.String())
	}
	proxy := cfg.patch["proxy"].(map[string]any)
	if proxy["cf_clearance"] != "ab" || proxy["user_agent"] != `"UA"` {
		t.Fatalf("sanitized proxy=%#v", proxy)
	}
	if !cfg.loaded || !reconciled || reloadedLevel != "debug" || reloadedMax != 3 {
		t.Fatalf("side effects loaded=%v reconciled=%v reload=%s/%d", cfg.loaded, reconciled, reloadedLevel, reloadedMax)
	}

	rec = adminRequest(http.MethodPost, "/admin/api/config", `{"account":{"storage":"redis"}}`, "Bearer gork")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "startup_only_config") {
		t.Fatalf("startup-only status/body=%d/%s", rec.Code, rec.Body.String())
	}
}

func TestAdminConfigResetClearsOverridesAndRunsRuntimeSideEffects(t *testing.T) {
	resetAdminRouterDepsForTest(t)
	cfg := &fakeAdminConfig{strs: map[string]string{"logging.file_level": "info"}, ints: map[string]int{"logging.max_files": 5}}
	adminRouterConfig = cfg
	adminReconcileRefreshRuntime = func() string { return "quota" }
	var reloadedLevel string
	var reloadedMax int
	adminReloadFileLogging = func(level string, maxFiles int) error {
		reloadedLevel, reloadedMax = level, maxFiles
		return nil
	}
	reconciled := false
	adminReconcileLocalMediaCache = func(context.Context) error { reconciled = true; return nil }

	rec := adminRequest(http.MethodPost, "/admin/api/config/reset", "", "Bearer gork")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !cfg.reset || !cfg.loaded || !reconciled || reloadedLevel != "info" || reloadedMax != 5 {
		t.Fatalf("side effects reset=%v loaded=%v reconciled=%v reload=%s/%d", cfg.reset, cfg.loaded, reconciled, reloadedLevel, reloadedMax)
	}
	assertAdminGoldenJSON(t, rec, http.StatusOK, map[string]any{"status": "success", "selection_strategy": "quota"})
}

func TestAdminStatusAndSyncUseDirectory(t *testing.T) {
	resetAdminRouterDepsForTest(t)
	dir := &fakeAdminDirectory{size: 2, revision: 7, changed: true}
	adminAccountDirectory = func() adminDirectory { return dir }
	adminReconcileRefreshRuntime = func() string { return "quota" }

	rec := adminRequest(http.MethodGet, "/admin/api/status", "", "Bearer gork")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"size":2`) || !strings.Contains(rec.Body.String(), `"selection_strategy":"quota"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = adminRequest(http.MethodPost, "/admin/api/sync", "", "Bearer gork")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"changed":true`) || dir.syncs != 1 {
		t.Fatalf("sync=%d body=%s syncs=%d", rec.Code, rec.Body.String(), dir.syncs)
	}
}

func TestAdminRouterCoreRouteGoldenStatusHeadersAndShapes(t *testing.T) {
	resetAdminRouterDepsForTest(t)
	adminRouterConfig = &fakeAdminConfig{raw: map[string]any{
		"app": map[string]any{"admin_key": "gork"},
	}}
	adminStorageBackend = func() string { return "local" }
	dir := &fakeAdminDirectory{size: 2, revision: 7, changed: true}
	adminAccountDirectory = func() adminDirectory { return dir }
	adminReconcileRefreshRuntime = func() string { return "quota" }

	for _, tt := range []struct {
		name   string
		method string
		path   string
		body   string
		status int
		json   map[string]any
	}{
		{name: "verify", method: http.MethodGet, path: "/admin/api/verify", status: http.StatusOK, json: map[string]any{"status": "success"}},
		{name: "config get", method: http.MethodGet, path: "/admin/api/config", status: http.StatusOK, json: map[string]any{"app.admin_key": "gork"}},
		{name: "config post", method: http.MethodPost, path: "/admin/api/config", body: `{"cache":{"local":{"image_limit_mb":10}}}`, status: http.StatusOK, json: map[string]any{"status": "success", "selection_strategy": "quota"}},
		{name: "config reset", method: http.MethodPost, path: "/admin/api/config/reset", status: http.StatusOK, json: map[string]any{"status": "success", "selection_strategy": "quota"}},
		{name: "storage", method: http.MethodGet, path: "/admin/api/storage", status: http.StatusOK, json: map[string]any{"type": "local"}},
		{name: "status", method: http.MethodGet, path: "/admin/api/status", status: http.StatusOK, json: map[string]any{"status": "ok", "size": float64(2), "revision": float64(7), "selection_strategy": "quota"}},
		{name: "sync", method: http.MethodPost, path: "/admin/api/sync", status: http.StatusOK, json: map[string]any{"changed": true, "revision": float64(7)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := adminRequest(tt.method, tt.path, tt.body, "Bearer gork")
			assertAdminGoldenJSON(t, rec, tt.status, tt.json)
		})
	}

	rec := adminRequest(http.MethodDelete, "/admin/api/config", "", "Bearer gork")
	assertAdminGoldenJSON(t, rec, http.StatusMethodNotAllowed, map[string]any{"error.message": "Method not allowed"})
	if rec.Header().Get("Allow") != "" {
		t.Fatalf("unexpected allow header for multi-method route: %q", rec.Header().Get("Allow"))
	}
}

func adminRequest(method, path, body, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)
	return rec
}

func assertAdminGoldenJSON(t *testing.T, rec *httptest.ResponseRecorder, status int, want map[string]any) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, status, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type=%q", got)
	}
	body := decodeAdminBody(t, rec)
	for key, wantValue := range want {
		gotValue, ok := adminGoldenJSONValue(body, key)
		if !ok {
			t.Fatalf("json missing %q in %#v", key, body)
		}
		if gotValue != wantValue {
			t.Fatalf("json[%s]=%#v want %#v body=%s", key, gotValue, wantValue, rec.Body.String())
		}
	}
}

func adminGoldenJSONValue(body map[string]any, dotted string) (any, bool) {
	current := any(body)
	for _, part := range strings.Split(dotted, ".") {
		item, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = item[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func resetAdminRouterDepsForTest(t *testing.T) {
	t.Helper()
	oldAuth := adminRouterAuthSettings
	oldConfig := adminRouterConfig
	oldRuntime := adminReconcileRefreshRuntime
	oldReload := adminReloadFileLogging
	oldCache := adminReconcileLocalMediaCache
	oldDirectory := adminAccountDirectory
	oldAssetsRepo := adminAssetsRepoProvider
	oldListAssets := adminListAssets
	oldDeleteAsset := adminDeleteAsset
	oldMarkInvalid := adminMarkInvalidCredentials
	oldBatchRepo := adminBatchRepoProvider
	oldBatchRefresh := adminBatchRefreshServiceProvider
	oldBatchConfigInt := adminBatchConfigInt
	oldBatchRunner := adminBatchAsyncRunner
	oldBatchSequence := adminBatchNSFWSequence
	oldBatchSetNSFW := adminBatchSetNSFW
	oldCacheImageDir := adminCacheImageDir
	oldCacheVideoDir := adminCacheVideoDir
	oldCacheConfigInt := adminCacheConfigInt
	oldCacheStore := adminCacheStoreProvider
	oldTokensRepo := adminTokensRepoProvider
	oldTokensRefresh := adminTokensRefreshServiceProvider
	oldTokensRunner := adminTokensAsyncRunner
	oldTokensNow := adminTokensNowMS
	adminRouterAuthSettings = func() auth.AuthSettings { return auth.AuthSettings{} }
	adminRouterConfig = &fakeAdminConfig{}
	adminReconcileRefreshRuntime = func() string { return "" }
	adminReloadFileLogging = func(string, int) error { return nil }
	adminReconcileLocalMediaCache = func(context.Context) error { return nil }
	adminAccountDirectory = func() adminDirectory { return nil }
	adminAssetsRepoProvider = defaultAdminAssetsRepoProvider
	adminListAssets = defaultAdminListAssets
	adminDeleteAsset = defaultAdminDeleteAsset
	adminMarkInvalidCredentials = func(context.Context, adminAssetsRepository, string, error, string) bool { return false }
	adminBatchRepoProvider = defaultAdminBatchRepoProvider
	adminBatchRefreshServiceProvider = func() adminBatchRefreshService { return nil }
	adminBatchConfigInt = func(string, int) int { return 50 }
	adminBatchAsyncRunner = func(run func()) { go run() }
	adminBatchNSFWSequence = defaultAdminBatchNSFWSequence
	adminBatchSetNSFW = defaultAdminBatchSetNSFW
	adminCacheImageDir = storage.ImageFilesDir
	adminCacheVideoDir = storage.VideoFilesDir
	adminCacheConfigInt = func(string, int) int { return 0 }
	adminCacheStoreProvider = func() adminCacheStore {
		return storage.NewLocalMediaCacheStore(storage.LocalMediaCacheOptions{Config: adminCacheConfig{}})
	}
	adminTokensRepoProvider = defaultAdminTokensRepoProvider
	adminTokensRefreshServiceProvider = func() adminTokensRefreshService { return nil }
	adminTokensAsyncRunner = func(run func()) { go run() }
	adminTokensNowMS = defaultAdminTokensNowMS
	t.Cleanup(func() {
		adminRouterAuthSettings = oldAuth
		adminRouterConfig = oldConfig
		adminReconcileRefreshRuntime = oldRuntime
		adminReloadFileLogging = oldReload
		adminReconcileLocalMediaCache = oldCache
		adminAccountDirectory = oldDirectory
		adminAssetsRepoProvider = oldAssetsRepo
		adminListAssets = oldListAssets
		adminDeleteAsset = oldDeleteAsset
		adminMarkInvalidCredentials = oldMarkInvalid
		adminBatchRepoProvider = oldBatchRepo
		adminBatchRefreshServiceProvider = oldBatchRefresh
		adminBatchConfigInt = oldBatchConfigInt
		adminBatchAsyncRunner = oldBatchRunner
		adminBatchNSFWSequence = oldBatchSequence
		adminBatchSetNSFW = oldBatchSetNSFW
		adminCacheImageDir = oldCacheImageDir
		adminCacheVideoDir = oldCacheVideoDir
		adminCacheConfigInt = oldCacheConfigInt
		adminCacheStoreProvider = oldCacheStore
		adminTokensRepoProvider = oldTokensRepo
		adminTokensRefreshServiceProvider = oldTokensRefresh
		adminTokensAsyncRunner = oldTokensRunner
		adminTokensNowMS = oldTokensNow
	})
}

type fakeAdminConfig struct {
	raw    map[string]any
	patch  map[string]any
	reset  bool
	loaded bool
	strs   map[string]string
	ints   map[string]int
}

func (c *fakeAdminConfig) Raw() map[string]any { return c.raw }

func (c *fakeAdminConfig) Update(_ context.Context, patch map[string]any) error {
	c.patch = patch
	return nil
}

func (c *fakeAdminConfig) Reset(context.Context) error {
	c.reset = true
	return nil
}

func (c *fakeAdminConfig) Load(context.Context, string) error {
	c.loaded = true
	return nil
}

func (c *fakeAdminConfig) GetStr(key string, fallback string) string {
	if c.strs != nil && c.strs[key] != "" {
		return c.strs[key]
	}
	return fallback
}

func (c *fakeAdminConfig) GetInt(key string, fallback int) int {
	if c.ints != nil {
		if value, ok := c.ints[key]; ok {
			return value
		}
	}
	return fallback
}

type fakeAdminDirectory struct {
	size     int
	revision int
	changed  bool
	syncs    int
}

func (d *fakeAdminDirectory) Size() int { return d.size }

func (d *fakeAdminDirectory) Revision() int { return d.revision }

func (d *fakeAdminDirectory) SyncIfChanged(context.Context) (bool, error) {
	d.syncs++
	return d.changed, nil
}

func decodeAdminBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}
