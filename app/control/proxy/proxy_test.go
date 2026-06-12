package proxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	platformconfig "github.com/dslzl/gork/app/platform/config"
)

type fakeDirectoryConfig struct {
	strings map[string]string
	lists   map[string][]string
	ints    map[string]int
}

func (f fakeDirectoryConfig) GetString(key, defaultValue string) string {
	if f.strings == nil {
		return defaultValue
	}
	if value, ok := f.strings[key]; ok {
		return value
	}
	return defaultValue
}

func (f fakeDirectoryConfig) GetList(key string, defaultValue []string) []string {
	if f.lists == nil {
		return defaultValue
	}
	if value, ok := f.lists[key]; ok {
		return append([]string(nil), value...)
	}
	return defaultValue
}

func (f fakeDirectoryConfig) GetInt(key string, defaultValue int) int {
	if f.ints == nil {
		return defaultValue
	}
	if value, ok := f.ints[key]; ok {
		return value
	}
	return defaultValue
}

type fakeManualProvider struct {
	cookies string
	ua      string
	calls   []string
	err     error
}

func (f *fakeManualProvider) BuildBundle(affinityKey string, clearanceHost ...string) (ClearanceBundle, bool, error) {
	if f.err != nil {
		return ClearanceBundle{}, false, f.err
	}
	host := "grok.com"
	if len(clearanceHost) > 0 && clearanceHost[0] != "" {
		host = clearanceHost[0]
	}
	f.calls = append(f.calls, affinityKey+"@"+host)
	bundle := NewClearanceBundle("manual:" + affinityKey + "@" + host)
	bundle.AffinityKey = affinityKey
	bundle.ClearanceHost = host
	bundle.CFCookies = f.cookies
	bundle.UserAgent = f.ua
	return bundle, true, nil
}

type fakeFlareProvider struct {
	bundles []ClearanceBundle
	calls   []string
	err     error
}

func (f *fakeFlareProvider) RefreshBundle(_ context.Context, affinityKey, proxyURL string, targetURL ...string) (ClearanceBundle, bool, error) {
	if f.err != nil {
		return ClearanceBundle{}, false, f.err
	}
	target := "https://grok.com"
	if len(targetURL) > 0 && targetURL[0] != "" {
		target = targetURL[0]
	}
	f.calls = append(f.calls, affinityKey+"|"+proxyURL+"|"+target)
	if len(f.bundles) == 0 {
		return ClearanceBundle{}, false, nil
	}
	bundle := f.bundles[0]
	f.bundles = f.bundles[1:]
	return bundle, true, nil
}

func TestProxyDirectoryLoadBuildsNodesAndPreservesValidAffinities(t *testing.T) {
	cfg := fakeDirectoryConfig{
		strings: map[string]string{
			"proxy.egress.mode":          "single_proxy",
			"proxy.egress.proxy_url":     "http://base",
			"proxy.clearance.mode":       "manual",
			"proxy.clearance.cf_cookies": "cf=one",
			"proxy.clearance.user_agent": "ua-one",
		},
	}
	directory := NewProxyDirectory(DirectoryOptions{
		Config:         cfg,
		ManualProvider: &fakeManualProvider{cookies: "cf=one", ua: "ua-one"},
		IDGenerator:    func() string { return "lease-1" },
		Clock:          func() int64 { return 1234 },
	})

	if err := directory.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if directory.EgressMode() != EgressModeSingleProxy {
		t.Fatalf("egress mode = %q, want %q", directory.EgressMode(), EgressModeSingleProxy)
	}
	if directory.ClearanceMode() != ClearanceModeManual {
		t.Fatalf("clearance mode = %q, want %q", directory.ClearanceMode(), ClearanceModeManual)
	}
	if directory.NodeCount() != 1 {
		t.Fatalf("node count = %d, want 1", directory.NodeCount())
	}
	nodes := directory.Nodes()
	if len(nodes) != 1 || nodes[0].NodeID != "single" || nodes[0].ProxyURL == nil || *nodes[0].ProxyURL != "http://base" {
		t.Fatalf("nodes = %#v", nodes)
	}
}

func TestGetProxyDirectoryUsesGlobalConfigWhenNoOptions(t *testing.T) {
	defaultsPath := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaultsPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	previousConfig := platformconfig.GlobalConfig
	globalDirectoryMu.Lock()
	previousDirectory := globalDirectory
	globalDirectory = nil
	globalDirectoryMu.Unlock()
	t.Cleanup(func() {
		platformconfig.GlobalConfig = previousConfig
		globalDirectoryMu.Lock()
		globalDirectory = previousDirectory
		globalDirectoryMu.Unlock()
	})

	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(fakeGlobalConfigBackend{
		data: map[string]any{
			"proxy": map[string]any{
				"egress": map[string]any{
					"mode":      "single_proxy",
					"proxy_url": "http://proxy.test:8080",
				},
			},
		},
	}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaultsPath); err != nil {
		t.Fatalf("load global config: %v", err)
	}

	directory, err := GetProxyDirectory(context.Background())
	if err != nil {
		t.Fatalf("GetProxyDirectory returned error: %v", err)
	}
	lease, err := directory.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	if lease.ProxyURL == nil || *lease.ProxyURL != "http://proxy.test:8080" {
		t.Fatalf("lease proxy URL = %#v", lease.ProxyURL)
	}
}

func TestProxyDirectoryAcquireUsesResourceProxyWhenConfigured(t *testing.T) {
	directory := NewProxyDirectory(DirectoryOptions{
		Config: fakeDirectoryConfig{
			strings: map[string]string{
				"proxy.egress.mode":               "single_proxy",
				"proxy.egress.proxy_url":          "http://base",
				"proxy.egress.resource_proxy_url": "http://resource",
				"proxy.clearance.mode":            "none",
			},
		},
		IDGenerator: func() string { return "lease-resource" },
		Clock:       func() int64 { return 1000 },
	})
	if err := directory.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	baseLease, err := directory.Acquire(context.Background())
	if err != nil {
		t.Fatalf("base Acquire() error = %v", err)
	}
	if baseLease.ProxyURL == nil || *baseLease.ProxyURL != "http://base" {
		t.Fatalf("base lease proxy = %#v, want http://base", baseLease.ProxyURL)
	}

	resourceLease, err := directory.Acquire(context.Background(), AcquireOptions{Resource: true})
	if err != nil {
		t.Fatalf("resource Acquire() error = %v", err)
	}
	if resourceLease.ProxyURL == nil || *resourceLease.ProxyURL != "http://resource" {
		t.Fatalf("resource lease proxy = %#v, want http://resource", resourceLease.ProxyURL)
	}
}

func TestProxyDirectoryAcquireBuildsManualLeaseAndCachesBundle(t *testing.T) {
	manual := &fakeManualProvider{cookies: "cf=manual", ua: "ua-manual"}
	directory := NewProxyDirectory(DirectoryOptions{
		Config: fakeDirectoryConfig{
			strings: map[string]string{
				"proxy.egress.mode":    "direct",
				"proxy.clearance.mode": "manual",
			},
		},
		ManualProvider: manual,
		IDGenerator:    func() string { return "lease-manual" },
		Clock:          func() int64 { return 999 },
	})
	if err := directory.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	lease, err := directory.Acquire(context.Background(), AcquireOptions{
		Scope:           ProxyScopeAsset,
		Kind:            RequestKindWebSocket,
		ClearanceOrigin: "https://console.x.ai/path",
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	if lease.ProxyURL != nil {
		t.Fatalf("direct lease proxy = %v, want nil", *lease.ProxyURL)
	}
	if lease.LeaseID != "lease-manual" || lease.AcquiredAt != 999 {
		t.Fatalf("lease id/time = %s/%d", lease.LeaseID, lease.AcquiredAt)
	}
	if lease.CFCookies != "cf=manual" || lease.UserAgent != "ua-manual" || lease.ClearanceHost != "console.x.ai" {
		t.Fatalf("lease clearance fields = %#v", lease)
	}
	if lease.Scope != ProxyScopeAsset || lease.Kind != RequestKindWebSocket {
		t.Fatalf("lease routing fields = %#v", lease)
	}
	if len(manual.calls) != 1 || manual.calls[0] != "direct@console.x.ai" {
		t.Fatalf("manual calls = %#v", manual.calls)
	}

	_, err = directory.Acquire(context.Background(), AcquireOptions{
		ClearanceOrigin: "https://console.x.ai/again",
	})
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if len(manual.calls) != 1 {
		t.Fatalf("manual provider called %d times, want cached bundle", len(manual.calls))
	}
}

func TestProxyDirectoryFeedbackInvalidatesClearanceAndRotatesPool(t *testing.T) {
	manual := &fakeManualProvider{cookies: "cf=pool", ua: "ua-pool"}
	directory := NewProxyDirectory(DirectoryOptions{
		Config: fakeDirectoryConfig{
			strings: map[string]string{
				"proxy.egress.mode":    "proxy_pool",
				"proxy.clearance.mode": "manual",
			},
			lists: map[string][]string{
				"proxy.egress.proxy_pool": []string{"http://p1", "http://p2"},
			},
		},
		ManualProvider: manual,
		IDGenerator:    func() string { return "lease-pool" },
		Clock:          func() int64 { return 1 },
	})
	if err := directory.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	first, err := directory.Acquire(context.Background())
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if first.ProxyURL == nil || *first.ProxyURL != "http://p1" {
		t.Fatalf("first proxy = %#v, want http://p1", first.ProxyURL)
	}
	if err := directory.Feedback(context.Background(), first, NewProxyFeedback(ProxyFeedbackChallenge)); err != nil {
		t.Fatalf("Feedback() error = %v", err)
	}

	second, err := directory.Acquire(context.Background())
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if second.ProxyURL == nil || *second.ProxyURL != "http://p2" {
		t.Fatalf("second proxy = %#v, want http://p2", second.ProxyURL)
	}
	if len(manual.calls) != 2 {
		t.Fatalf("manual calls = %#v, want rebuild after invalidation", manual.calls)
	}
}

func TestProxyDirectoryLoadReloadInvalidatesMatchingBundlesAndDropsStale(t *testing.T) {
	cfg := fakeDirectoryConfig{
		strings: map[string]string{
			"proxy.egress.mode":    "proxy_pool",
			"proxy.clearance.mode": "manual",
		},
		lists: map[string][]string{
			"proxy.egress.proxy_pool": {"http://p1", "http://p2"},
		},
	}
	manual := &fakeManualProvider{cookies: "cf=reload", ua: "ua-reload"}
	directory := NewProxyDirectory(DirectoryOptions{
		Config:         cfg,
		ManualProvider: manual,
	})
	if err := directory.Load(context.Background()); err != nil {
		t.Fatalf("initial Load() error = %v", err)
	}
	if err := directory.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp() error = %v", err)
	}
	if len(directory.Bundles()) != 2 {
		t.Fatalf("initial bundle count = %d, want 2", len(directory.Bundles()))
	}

	cfg.lists["proxy.egress.proxy_pool"] = []string{"http://p1"}
	if err := directory.Load(context.Background()); err != nil {
		t.Fatalf("reload Load() error = %v", err)
	}
	bundles := directory.Bundles()
	key := BundleKey{Affinity: "http://p1", ClearanceHost: "grok.com"}
	if len(bundles) != 1 {
		t.Fatalf("bundle count after reload = %d, want 1: %#v", len(bundles), bundles)
	}
	if bundle, ok := bundles[key]; !ok || bundle.State != ClearanceBundleInvalid {
		t.Fatalf("reloaded bundle = %#v ok=%v, want invalid preserved p1 bundle", bundle, ok)
	}
	if _, ok := bundles[BundleKey{Affinity: "http://p2", ClearanceHost: "grok.com"}]; ok {
		t.Fatalf("stale p2 bundle should be dropped: %#v", bundles)
	}

	if err := directory.RefreshClearanceSafe(context.Background()); err != nil {
		t.Fatalf("RefreshClearanceSafe() error = %v", err)
	}
	if bundle := directory.Bundles()[key]; bundle.State != ClearanceBundleValid {
		t.Fatalf("refreshed bundle state = %v, want valid", bundle.State)
	}
	if err := directory.Load(context.Background()); err != nil {
		t.Fatalf("unchanged Load() error = %v", err)
	}
	if bundle := directory.Bundles()[key]; bundle.State != ClearanceBundleValid {
		t.Fatalf("unchanged Load should keep valid bundle, got %v", bundle.State)
	}
}

func TestProxyDirectoryWarmUpAndRefreshSafeMatchPythonLifecycle(t *testing.T) {
	manual := &fakeManualProvider{cookies: "cf=warm", ua: "ua-warm"}
	directory := NewProxyDirectory(DirectoryOptions{
		Config: fakeDirectoryConfig{
			strings: map[string]string{
				"proxy.egress.mode":    "proxy_pool",
				"proxy.clearance.mode": "manual",
			},
			lists: map[string][]string{
				"proxy.egress.proxy_pool": []string{"http://p1", "http://p2"},
			},
		},
		ManualProvider: manual,
	})
	if err := directory.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if err := directory.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp() error = %v", err)
	}
	if len(directory.Bundles()) != 2 {
		t.Fatalf("bundle count after warm up = %d, want 2", len(directory.Bundles()))
	}
	if err := directory.InvalidateClearance(context.Background()); err != nil {
		t.Fatalf("InvalidateClearance() error = %v", err)
	}
	for key, bundle := range directory.Bundles() {
		if bundle.State != ClearanceBundleInvalid {
			t.Fatalf("bundle %v state = %v, want invalid", key, bundle.State)
		}
	}
	if err := directory.RefreshClearanceSafe(context.Background()); err != nil {
		t.Fatalf("RefreshClearanceSafe() error = %v", err)
	}
	for key, bundle := range directory.Bundles() {
		if bundle.State != ClearanceBundleValid {
			t.Fatalf("bundle %v state = %v, want valid", key, bundle.State)
		}
	}
}

func TestProxyDirectoryFlareRefreshKeepsOldBundleOnFailure(t *testing.T) {
	oldBundle := NewClearanceBundle("old")
	oldBundle.AffinityKey = "direct"
	oldBundle.ClearanceHost = "grok.com"
	oldBundle.CFCookies = "cf=old"
	flare := &fakeFlareProvider{
		bundles: []ClearanceBundle{oldBundle},
	}
	directory := NewProxyDirectory(DirectoryOptions{
		Config: fakeDirectoryConfig{
			strings: map[string]string{
				"proxy.egress.mode":    "direct",
				"proxy.clearance.mode": "flaresolverr",
			},
		},
		FlareProvider: flare,
	})
	if err := directory.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := directory.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp() error = %v", err)
	}

	flare.err = errors.New("temporarily unavailable")
	if err := directory.RefreshClearanceSafe(context.Background()); err == nil {
		t.Fatalf("RefreshClearanceSafe() error = nil, want provider error")
	}
	bundle := directory.Bundles()[BundleKey{Affinity: "direct", ClearanceHost: "grok.com"}]
	if bundle.BundleID != "old" || bundle.CFCookies != "cf=old" {
		t.Fatalf("bundle after failed refresh = %#v, want old bundle kept", bundle)
	}
}
