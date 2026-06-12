package adapters

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	controlproxy "github.com/dslzl/gork/app/control/proxy"
	platformconfig "github.com/dslzl/gork/app/platform/config"
)

type fakeHeadersConfigBackend struct {
	data map[string]any
}

func (f fakeHeadersConfigBackend) Load(context.Context) (map[string]any, error) {
	return f.data, nil
}

func (f fakeHeadersConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return nil
}

func (f fakeHeadersConfigBackend) Clear(context.Context) error {
	return nil
}

func (f fakeHeadersConfigBackend) Version(context.Context) (any, error) {
	return "test", nil
}

func (f fakeHeadersConfigBackend) Close(context.Context) error {
	return nil
}

func TestBuildSSOCookieMatchesPython(t *testing.T) {
	lease := controlproxy.NewProxyLease("lease-1")
	lease.CFCookies = "foo=1; cf_clearance=old; bar=2"
	cookies := "baz=3; cf_clearance=old2"
	clearance := " new token "

	got := BuildSSOCookie("sso= a b c ", CookieOptions{
		Lease:       &lease,
		CFCookies:   &cookies,
		CFClearance: &clearance,
	})
	want := "sso=abc; sso-rw=abc; baz=3; cf_clearance=newtoken"
	if got != want {
		t.Fatalf("BuildSSOCookie = %q, want %q", got, want)
	}

	withoutExisting := "foo=1"
	got = BuildSSOCookie("token", CookieOptions{
		CFCookies:   &withoutExisting,
		CFClearance: &clearance,
	})
	if got != "sso=token; sso-rw=token; foo=1; cf_clearance=newtoken" {
		t.Fatalf("BuildSSOCookie append clearance = %q", got)
	}
}

func TestSanitizeMatchesPythonNormalization(t *testing.T) {
	value := "\t\u201chello\u201d\u2014x😀\n"
	if got := sanitize(&value, "field", false); got != "\"hello\"-x" {
		t.Fatalf("sanitize trim/latin1 = %q", got)
	}

	spaced := " a\u00a0b\u200bc\t😀 "
	if got := sanitize(&spaced, "field", true); got != "abc" {
		t.Fatalf("sanitize strip spaces = %q", got)
	}
}

func TestBuildHTTPHeadersMatchesPythonForJSON(t *testing.T) {
	lease := controlproxy.NewProxyLease("lease-1")
	lease.CFCookies = "cf_clearance=abc"
	lease.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0 Safari/537.36"

	headers := BuildHTTPHeaders("sso-token", HTTPHeaderOptions{Lease: &lease})
	if headers["Accept"] != "*/*" ||
		headers["Content-Type"] != "application/json" ||
		headers["Origin"] != "https://grok.com" ||
		headers["Referer"] != "https://grok.com/" ||
		headers["Sec-Fetch-Dest"] != "empty" ||
		headers["Sec-Fetch-Mode"] != "cors" ||
		headers["Sec-Fetch-Site"] != "same-origin" {
		t.Fatalf("HTTP JSON headers = %#v", headers)
	}
	if headers["Cookie"] != "sso=sso-token; sso-rw=sso-token; cf_clearance=abc" {
		t.Fatalf("Cookie = %q", headers["Cookie"])
	}
	if headers["Sec-Ch-Ua-Mobile"] != "?0" ||
		headers["Sec-Ch-Ua-Platform"] != "\"Windows\"" ||
		headers["Sec-Ch-Ua-Arch"] != "x86" ||
		headers["Sec-Ch-Ua-Bitness"] != "64" ||
		!strings.Contains(headers["Sec-Ch-Ua"], "\"Google Chrome\";v=\"120\"") {
		t.Fatalf("client hints = %#v", headers)
	}
	if headers["x-statsig-id"] == "" || headers["x-xai-request-id"] == "" {
		t.Fatalf("dynamic ids missing: %#v", headers)
	}
}

func TestBuildHTTPHeadersUsesUUIDStatsigWhenDynamicDisabled(t *testing.T) {
	defaultsPath := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaultsPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	previous := platformconfig.GlobalConfig
	t.Cleanup(func() {
		platformconfig.GlobalConfig = previous
	})
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(fakeHeadersConfigBackend{
		data: map[string]any{
			"features": map[string]any{
				"dynamic_statsig": false,
			},
		},
	}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaultsPath); err != nil {
		t.Fatalf("load global config: %v", err)
	}

	headers := BuildHTTPHeaders("token")
	got := headers["x-statsig-id"]
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(got) {
		t.Fatalf("x-statsig-id = %q, want UUID when dynamic statsig is disabled", got)
	}
}

func TestStatsigIDUsesDynamicX1TypeErrorPrefix(t *testing.T) {
	defaultsPath := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaultsPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	previous := platformconfig.GlobalConfig
	t.Cleanup(func() {
		platformconfig.GlobalConfig = previous
	})
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(fakeHeadersConfigBackend{
		data: map[string]any{
			"features": map[string]any{
				"dynamic_statsig": true,
			},
		},
	}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaultsPath); err != nil {
		t.Fatalf("load global config: %v", err)
	}

	value := statsigID()
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("statsigID() = %q is not base64: %v", value, err)
	}
	if !strings.HasPrefix(string(decoded), "x1:TypeError:") {
		t.Fatalf("decoded statsigID = %q, want x1 TypeError prefix", string(decoded))
	}
}

func TestBuildHTTPHeadersMatchesPythonForDocumentContent(t *testing.T) {
	contentType := "image/png"
	headers := BuildHTTPHeaders("token", HTTPHeaderOptions{
		ContentType: &contentType,
		Origin:      "https://grok.com",
		Referer:     "https://assets.grok.com/file",
	})
	if headers["Sec-Fetch-Dest"] != "document" ||
		headers["Sec-Fetch-Site"] != "same-site" ||
		headers["Accept"] == "*/*" {
		t.Fatalf("document headers = %#v", headers)
	}
}

func TestClientHintsMatchPythonChromiumFiltering(t *testing.T) {
	safariUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
	if got := clientHints("", safariUA); len(got) != 0 {
		t.Fatalf("Safari UA should not emit Chromium client hints: %#v", got)
	}

	edgeMobileUA := "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36 EdgA/120.0.0.0"
	hints := clientHints("edge 120", edgeMobileUA)
	if hints["Sec-Ch-Ua-Mobile"] != "?1" ||
		hints["Sec-Ch-Ua-Platform"] != "\"Android\"" ||
		!strings.Contains(hints["Sec-Ch-Ua"], "\"Microsoft Edge\";v=\"120\"") {
		t.Fatalf("mobile Edge client hints = %#v", hints)
	}
}

func TestBuildWSHeadersMatchesPython(t *testing.T) {
	lease := controlproxy.NewProxyLease("lease-1")
	lease.CFCookies = "cf_clearance=abc"
	lease.UserAgent = "Mozilla/5.0 Firefox/123.0"
	headers := BuildWSHeaders("token", WSHeaderOptions{
		Lease:  &lease,
		Origin: "https://grok.com",
		Extra:  map[string]string{"X-Test": "1", "Origin": "https://override.example"},
	})
	if headers["Accept-Language"] != "zh-CN,zh;q=0.9,en;q=0.8" ||
		headers["Cache-Control"] != "no-cache" ||
		headers["Pragma"] != "no-cache" ||
		headers["Cookie"] != "sso=token; sso-rw=token; cf_clearance=abc" ||
		headers["X-Test"] != "1" ||
		headers["Origin"] != "https://override.example" {
		t.Fatalf("WS headers = %#v", headers)
	}
	if _, ok := headers["Sec-Ch-Ua"]; ok {
		t.Fatalf("Firefox UA should not emit Chromium client hints: %#v", headers)
	}
}

func TestBuildWSHeadersOmitsCookieWithoutToken(t *testing.T) {
	lease := controlproxy.NewProxyLease("lease-1")
	lease.CFCookies = "cf_clearance=abc"

	headers := BuildWSHeaders("", WSHeaderOptions{Lease: &lease})
	if _, ok := headers["Cookie"]; ok {
		t.Fatalf("WS headers should omit Cookie without token: %#v", headers)
	}
}

func TestBuildConsoleHeadersMatchesPython(t *testing.T) {
	lease := controlproxy.NewProxyLease("lease-1")
	lease.CFCookies = "cf_clearance=abc"
	headers := BuildConsoleHeaders("sso= console token ", ConsoleHeaderOptions{Lease: &lease})
	if headers["Authorization"] != "Bearer anonymous" ||
		headers["Origin"] != "https://console.x.ai" ||
		headers["Referer"] != "https://console.x.ai/" ||
		headers["x-cluster"] != "https://us-east-1.api.x.ai" ||
		headers["Cookie"] != "sso=consoletoken; sso-rw=consoletoken; cf_clearance=abc" {
		t.Fatalf("console headers = %#v", headers)
	}
	if !strings.Contains(headers["User-Agent"], "Chrome/136.0.0.0") {
		t.Fatalf("console default UA = %q", headers["User-Agent"])
	}
}
