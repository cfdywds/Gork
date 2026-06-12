package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	controlproxy "github.com/dslzl/gork/app/control/proxy"
	platform "github.com/dslzl/gork/app/platform"
	platformconfig "github.com/dslzl/gork/app/platform/config"
)

type fakeSessionGlobalConfigBackend struct {
	data map[string]any
}

func (f fakeSessionGlobalConfigBackend) Load(context.Context) (map[string]any, error) {
	return f.data, nil
}

func (f fakeSessionGlobalConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return nil
}

func (f fakeSessionGlobalConfigBackend) Clear(context.Context) error {
	return nil
}

func (f fakeSessionGlobalConfigBackend) Version(context.Context) (any, error) {
	return "test", nil
}

func (f fakeSessionGlobalConfigBackend) Close(context.Context) error {
	return nil
}

type fakeSessionConfig struct {
	skipProxySSL bool
	resetCodes   []int
}

func (f fakeSessionConfig) GetBool(key string, defaultValue bool) bool {
	if key == "proxy.egress.skip_ssl_verify" {
		return f.skipProxySSL
	}
	return defaultValue
}

func (f fakeSessionConfig) GetList(key string, defaultValue []int) []int {
	if key == "retry.reset_session_status_codes" && f.resetCodes != nil {
		return f.resetCodes
	}
	return defaultValue
}

func TestNormalizeProxyURLMatchesPython(t *testing.T) {
	cases := map[string]string{
		"":                         "",
		"http://proxy.local:8080":  "http://proxy.local:8080",
		"socks://proxy.local:1080": "socks5h://proxy.local:1080",
		"socks5://proxy.local":     "socks5h://proxy.local",
		"socks4://proxy.local":     "socks4a://proxy.local",
	}
	for input, want := range cases {
		if got := NormalizeProxyURL(input); got != want {
			t.Fatalf("NormalizeProxyURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildSessionKwargsMatchesPython(t *testing.T) {
	httpProxy := "http://proxy.local:8080"
	lease := controlproxy.NewProxyLease("lease-1")
	lease.ProxyURL = &httpProxy
	lease.UserAgent = "Mozilla/5.0 Chrome/120.0.0.0"
	extra := map[string]any{"timeout": 30}

	kwargs := BuildSessionKwargs(BuildSessionOptions{
		Lease: &lease,
		Extra: extra,
	})
	if kwargs["timeout"] != 30 {
		t.Fatalf("extra timeout not preserved: %#v", kwargs)
	}
	if kwargs["impersonate"] != "chrome120" {
		t.Fatalf("impersonate = %#v, want chrome120", kwargs["impersonate"])
	}
	proxies, ok := kwargs["proxies"].(map[string]string)
	if !ok || proxies["http"] != httpProxy || proxies["https"] != httpProxy {
		t.Fatalf("proxies = %#v", kwargs["proxies"])
	}
	if _, ok := kwargs["proxy"]; ok {
		t.Fatalf("http proxy should use proxies, got proxy=%#v", kwargs["proxy"])
	}
	if _, exists := extra["impersonate"]; exists {
		t.Fatalf("BuildSessionKwargs should not mutate extra")
	}
}

func TestBuildSessionKwargsTreatsEmptyImpersonateAsMissing(t *testing.T) {
	lease := controlproxy.NewProxyLease("lease-1")
	lease.UserAgent = "Mozilla/5.0 Chrome/120.0.0.0"

	kwargs := BuildSessionKwargs(BuildSessionOptions{
		Lease: &lease,
		Extra: map[string]any{
			"impersonate": "",
		},
	})
	if kwargs["impersonate"] != "chrome120" {
		t.Fatalf("empty impersonate = %#v, want chrome120", kwargs["impersonate"])
	}
}

func TestBuildSessionKwargsSetDefaultSemanticsAndSocksProxy(t *testing.T) {
	socksProxy := "socks5://proxy.local:1080"
	lease := controlproxy.NewProxyLease("lease-1")
	lease.ProxyURL = &socksProxy
	extra := map[string]any{
		"impersonate": "chrome99",
		"proxy":       "existing-proxy",
	}

	kwargs := BuildSessionKwargs(BuildSessionOptions{
		Lease:           &lease,
		BrowserOverride: "firefox123",
		Extra:           extra,
		Config:          fakeSessionConfig{skipProxySSL: true},
	})
	if kwargs["impersonate"] != "chrome99" {
		t.Fatalf("existing impersonate overwritten: %#v", kwargs["impersonate"])
	}
	if kwargs["proxy"] != "existing-proxy" {
		t.Fatalf("existing proxy overwritten: %#v", kwargs["proxy"])
	}
	curlOptions, ok := kwargs["curl_options"].(map[CurlOption]int)
	if !ok || curlOptions[CurlOptProxySSLVerifyPeer] != 0 || curlOptions[CurlOptProxySSLVerifyHost] != 0 {
		t.Fatalf("curl options = %#v", kwargs["curl_options"])
	}
}

func TestBuildSessionKwargsNilConfigUsesGlobalSkipProxySSL(t *testing.T) {
	defaultsPath := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaultsPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	previous := platformconfig.GlobalConfig
	t.Cleanup(func() {
		platformconfig.GlobalConfig = previous
	})
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(fakeSessionGlobalConfigBackend{
		data: map[string]any{
			"proxy": map[string]any{
				"egress": map[string]any{
					"skip_ssl_verify": true,
				},
			},
		},
	}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaultsPath); err != nil {
		t.Fatalf("load global config: %v", err)
	}

	proxyURL := "https://proxy.local:8443"
	lease := controlproxy.NewProxyLease("lease-1")
	lease.ProxyURL = &proxyURL
	kwargs := BuildSessionKwargs(BuildSessionOptions{Lease: &lease})
	curlOptions, ok := kwargs["curl_options"].(map[CurlOption]int)
	if !ok || curlOptions[CurlOptProxySSLVerifyPeer] != 0 || curlOptions[CurlOptProxySSLVerifyHost] != 0 {
		t.Fatalf("global skip proxy SSL curl options = %#v", kwargs["curl_options"])
	}
}

func TestWrapTransportErrorMatchesPython(t *testing.T) {
	upstream := platform.NewUpstreamError("already upstream", 503, "body")
	if got := WrapTransportError(upstream); got != upstream {
		t.Fatalf("WrapTransportError should return existing UpstreamError")
	}

	err := WrapTransportError(errors.New("line1\nline2"))
	if err.Status != 502 || err.Body != "line1\\nline2" {
		t.Fatalf("wrapped error status=%d body=%q", err.Status, err.Body)
	}
}

type fakeSession struct {
	responses []SessionResponse
	errs      []error
	calls     int
	closed    bool
}

func (f *fakeSession) Request(_ context.Context, method string, _ ...any) (SessionResponse, error) {
	f.calls++
	if method == "" {
		return SessionResponse{}, errors.New("missing method")
	}
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return SessionResponse{}, err
		}
	}
	if len(f.responses) == 0 {
		return SessionResponse{StatusCode: 200}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *fakeSession) Close(_ context.Context) error {
	f.closed = true
	return nil
}

func TestResettableSessionResetsAfterConfiguredStatus(t *testing.T) {
	first := &fakeSession{responses: []SessionResponse{{StatusCode: 403}}}
	second := &fakeSession{responses: []SessionResponse{{StatusCode: 200}}}
	created := []*fakeSession{}
	resettable := NewResettableSession(ResettableSessionOptions{
		Factory: func(SessionKwargs) SessionClient {
			if len(created) == 0 {
				created = append(created, first)
				return first
			}
			created = append(created, second)
			return second
		},
		ResetOnStatus: []int{403},
	})

	if resp, err := resettable.Get(context.Background(), "https://example.com"); err != nil || resp.StatusCode != 403 {
		t.Fatalf("first get resp=%#v err=%v", resp, err)
	}
	if resp, err := resettable.Get(context.Background(), "https://example.com"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("second get resp=%#v err=%v", resp, err)
	}
	if len(created) != 2 || !first.closed {
		t.Fatalf("sessions created=%d first.closed=%v", len(created), first.closed)
	}
}

func TestResettableSessionNilConfigUsesGlobalResetStatuses(t *testing.T) {
	defaultsPath := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaultsPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	previous := platformconfig.GlobalConfig
	t.Cleanup(func() {
		platformconfig.GlobalConfig = previous
	})
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(fakeSessionGlobalConfigBackend{
		data: map[string]any{
			"retry": map[string]any{
				"reset_session_status_codes": []any{418},
			},
		},
	}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaultsPath); err != nil {
		t.Fatalf("load global config: %v", err)
	}

	first := &fakeSession{responses: []SessionResponse{{StatusCode: 418}, {StatusCode: 409}}}
	second := &fakeSession{responses: []SessionResponse{{StatusCode: 200}}}
	created := []*fakeSession{}
	resettable := NewResettableSession(ResettableSessionOptions{
		Factory: func(SessionKwargs) SessionClient {
			if len(created) == 0 {
				created = append(created, first)
				return first
			}
			created = append(created, second)
			return second
		},
	})

	if resp, err := resettable.Get(context.Background(), "https://example.com"); err != nil || resp.StatusCode != 418 {
		t.Fatalf("first get resp=%#v err=%v", resp, err)
	}
	if resp, err := resettable.Get(context.Background(), "https://example.com"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("second get resp=%#v err=%v", resp, err)
	}
	if len(created) != 2 || !first.closed {
		t.Fatalf("sessions created=%d first.closed=%v", len(created), first.closed)
	}
}

func TestResettableSessionWrapsTransportErrorsAndResets(t *testing.T) {
	first := &fakeSession{errs: []error{errors.New("boom")}}
	second := &fakeSession{responses: []SessionResponse{{StatusCode: 200}}}
	created := []*fakeSession{}
	resettable := NewResettableSession(ResettableSessionOptions{
		Factory: func(SessionKwargs) SessionClient {
			if len(created) == 0 {
				created = append(created, first)
				return first
			}
			created = append(created, second)
			return second
		},
	})

	if _, err := resettable.Post(context.Background(), "https://example.com"); err == nil {
		t.Fatalf("Post should wrap transport error")
	} else if upstream, ok := err.(*platform.UpstreamError); !ok || upstream.Status != 502 {
		t.Fatalf("wrapped error = %#v", err)
	}
	if resp, err := resettable.Delete(context.Background(), "https://example.com"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("delete after reset resp=%#v err=%v", resp, err)
	}
	if len(created) != 2 || !first.closed {
		t.Fatalf("sessions created=%d first.closed=%v", len(created), first.closed)
	}
}
