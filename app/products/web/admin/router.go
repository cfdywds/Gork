package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/dslzl/gork/app/platform"
	"github.com/dslzl/gork/app/platform/auth"
	"github.com/dslzl/gork/app/platform/config"
	"github.com/dslzl/gork/app/platform/logging"
	"github.com/dslzl/gork/app/platform/storage"
)

type adminConfigStore interface {
	Raw() map[string]any
	Update(context.Context, map[string]any) error
	Reset(context.Context) error
	Load(context.Context, string) error
	GetStr(string, string) string
	GetInt(string, int) int
}

type adminDirectory interface {
	Size() int
	Revision() int
	SyncIfChanged(context.Context) (bool, error)
}

var (
	adminRouterAuthSettings = func() auth.AuthSettings {
		return auth.AuthSettings{AdminKey: config.GetConfig("app.admin_key", nil)}
	}
	adminRouterConfig      = adminConfigStore(config.GlobalConfig)
	adminReloadFileLogging = func(level string, maxFiles int) error {
		return logging.ReloadFileLogging(logging.ReloadFileLoggingOptions{FileLevel: level, MaxFiles: maxFiles})
	}
	adminReconcileLocalMediaCache = func(context.Context) error {
		return storage.ReconcileLocalMediaCache()
	}
	adminReconcileRefreshRuntime = func() string { return "" }
	adminAccountDirectory        = func() adminDirectory { return nil }
	adminStorageBackend          = func() string {
		return fmt.Sprint(config.GetConfig("account.storage", "local"))
	}
)

// NewRouter returns the /admin/api HTTP surface.
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/api/verify", adminProtected(http.MethodGet, handleAdminVerify))
	mux.HandleFunc("/admin/api/config", adminProtectedAny(map[string]http.HandlerFunc{
		http.MethodGet: handleAdminGetConfig, http.MethodPost: handleAdminUpdateConfig,
	}))
	mux.HandleFunc("/admin/api/config/reset", adminProtected(http.MethodPost, handleAdminResetConfig))
	mux.HandleFunc("/admin/api/storage", adminProtected(http.MethodGet, handleAdminStorage))
	mux.HandleFunc("/admin/api/status", adminProtected(http.MethodGet, handleAdminStatus))
	mux.HandleFunc("/admin/api/sync", adminProtected(http.MethodPost, handleAdminSync))
	mux.HandleFunc("/admin/api/assets", adminProtected(http.MethodGet, handleAdminAssetsList))
	mux.HandleFunc("/admin/api/assets/delete-item", adminProtected(http.MethodPost, handleAdminAssetDeleteItem))
	mux.HandleFunc("/admin/api/assets/clear-token", adminProtected(http.MethodPost, handleAdminAssetClearToken))
	mux.HandleFunc("/admin/api/batch/nsfw", adminProtected(http.MethodPost, handleAdminBatchNSFW))
	mux.HandleFunc("/admin/api/batch/refresh", adminProtected(http.MethodPost, handleAdminBatchRefresh))
	mux.HandleFunc("/admin/api/batch/cache-clear", adminProtected(http.MethodPost, handleAdminBatchCacheClear))
	mux.HandleFunc("/admin/api/batch/", adminProtectedAny(map[string]http.HandlerFunc{
		http.MethodGet: handleAdminBatchStream, http.MethodPost: handleAdminBatchCancel,
	}))
	mux.HandleFunc("/admin/api/cache", adminProtected(http.MethodGet, handleAdminCacheStats))
	mux.HandleFunc("/admin/api/cache/list", adminProtected(http.MethodGet, handleAdminCacheList))
	mux.HandleFunc("/admin/api/cache/clear", adminProtected(http.MethodPost, handleAdminCacheClear))
	mux.HandleFunc("/admin/api/cache/item/delete", adminProtected(http.MethodPost, handleAdminCacheDeleteItem))
	mux.HandleFunc("/admin/api/cache/items/delete", adminProtected(http.MethodPost, handleAdminCacheDeleteItems))
	mux.HandleFunc("/admin/api/tokens", adminProtectedAny(map[string]http.HandlerFunc{
		http.MethodGet: handleAdminTokensList, http.MethodPost: handleAdminTokensSave, http.MethodDelete: handleAdminTokensDelete,
	}))
	mux.HandleFunc("/admin/api/tokens/import-async", adminProtected(http.MethodPost, handleAdminTokensImportAsync))
	mux.HandleFunc("/admin/api/tokens/add", adminProtected(http.MethodPost, handleAdminTokensAdd))
	mux.HandleFunc("/admin/api/tokens/edit", adminProtected(http.MethodPut, handleAdminTokensEdit))
	mux.HandleFunc("/admin/api/tokens/disabled", adminProtected(http.MethodPost, handleAdminTokensToggle))
	mux.HandleFunc("/admin/api/tokens/disabled/batch", adminProtected(http.MethodPost, handleAdminTokensToggleBatch))
	mux.HandleFunc("/admin/api/tokens/pool", adminProtected(http.MethodPut, handleAdminTokensPool))
	return mux
}

func adminProtected(method string, handler http.HandlerFunc) http.HandlerFunc {
	return adminProtectedAny(map[string]http.HandlerFunc{method: handler})
}

func adminProtectedAny(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handler, ok := handlers[r.Method]
		if !ok {
			writeAdminJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]any{"message": "Method not allowed"}})
			return
		}
		if err := auth.VerifyAdminKey(r.Header.Get("Authorization"), r.URL.Query().Get("app_key"), adminRouterAuthSettings()); err != nil {
			writeAdminError(w, err)
			return
		}
		handler(w, r)
	}
}

func handleAdminVerify(w http.ResponseWriter, _ *http.Request) {
	writeAdminJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func handleAdminGetConfig(w http.ResponseWriter, _ *http.Request) {
	writeAdminJSON(w, http.StatusOK, adminRouterConfig.Raw())
}

func handleAdminUpdateConfig(w http.ResponseWriter, r *http.Request) {
	patch, err := decodeAdminPatch(r)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	result, err := updateAdminConfig(r.Context(), patch)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, result)
}

func handleAdminResetConfig(w http.ResponseWriter, r *http.Request) {
	result, err := resetAdminConfig(r.Context())
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, result)
}

func handleAdminStorage(w http.ResponseWriter, _ *http.Request) {
	writeAdminJSON(w, http.StatusOK, map[string]any{"type": adminStorageBackend()})
}

func handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	directory := adminAccountDirectory()
	if directory == nil {
		writeAdminError(w, adminDirectoryError())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"size":               directory.Size(),
		"revision":           directory.Revision(),
		"selection_strategy": adminReconcileRefreshRuntime(),
	})
	_ = r
}

func handleAdminSync(w http.ResponseWriter, r *http.Request) {
	directory := adminAccountDirectory()
	if directory == nil {
		writeAdminError(w, adminDirectoryError())
		return
	}
	changed, err := directory.SyncIfChanged(r.Context())
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"changed": changed, "revision": directory.Revision()})
}

func decodeAdminPatch(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var patch map[string]any
	if err := decoder.Decode(&patch); err != nil {
		return nil, platform.NewValidationError("Invalid JSON body", "body", "invalid_json")
	}
	if patch == nil {
		patch = map[string]any{}
	}
	return patch, nil
}

func updateAdminConfig(ctx context.Context, patch map[string]any) (map[string]any, error) {
	patch = sanitizeProxyConfig(patch)
	if err := ensureRuntimePatchAllowed(patch); err != nil {
		return nil, err
	}
	cacheLocalChanged := patchTouchesPrefix(patch, "cache.local")
	if err := adminRouterConfig.Update(ctx, patch); err != nil {
		return nil, err
	}
	if err := adminRouterConfig.Load(ctx, ""); err != nil {
		return nil, err
	}
	if err := adminReloadFileLogging(adminRouterConfig.GetStr("logging.file_level", ""), adminRouterConfig.GetInt("logging.max_files", 7)); err != nil {
		return nil, err
	}
	if cacheLocalChanged {
		if err := adminReconcileLocalMediaCache(ctx); err != nil {
			return nil, err
		}
	}
	return map[string]any{"status": "success", "message": "配置已更新", "selection_strategy": adminReconcileRefreshRuntime()}, nil
}

func resetAdminConfig(ctx context.Context) (map[string]any, error) {
	if err := adminRouterConfig.Reset(ctx); err != nil {
		return nil, err
	}
	if err := adminRouterConfig.Load(ctx, ""); err != nil {
		return nil, err
	}
	if err := adminReloadFileLogging(adminRouterConfig.GetStr("logging.file_level", ""), adminRouterConfig.GetInt("logging.max_files", 7)); err != nil {
		return nil, err
	}
	if err := adminReconcileLocalMediaCache(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"status": "success", "message": "配置已还原为默认值", "selection_strategy": adminReconcileRefreshRuntime()}, nil
}

func adminDirectoryError() error {
	return platform.NewAppError("Account directory not initialised", platform.ErrorKindServer, "directory_not_initialised", 503, nil)
}

func writeAdminJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAdminError(w http.ResponseWriter, err error) {
	var validation *platform.ValidationError
	if errors.As(err, &validation) && validation.AppError != nil {
		writeAdminJSON(w, validation.Status, validation.ToDict())
		return
	}
	var upstream *platform.UpstreamError
	if errors.As(err, &upstream) && upstream.AppError != nil {
		writeAdminJSON(w, upstream.Status, upstream.ToDict())
		return
	}
	var appErr *platform.AppError
	if errors.As(err, &appErr) && appErr != nil {
		writeAdminJSON(w, appErr.Status, appErr.ToDict())
		return
	}
	writeAdminJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error()}})
}
