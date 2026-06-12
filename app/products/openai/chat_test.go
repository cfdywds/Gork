package openai

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	controlaccount "github.com/dslzl/gork/app/control/account"
	"github.com/dslzl/gork/app/control/model"
	dataaccount "github.com/dslzl/gork/app/dataplane/account"
	"github.com/dslzl/gork/app/dataplane/reverse/protocol"
	"github.com/dslzl/gork/app/platform"
	platformconfig "github.com/dslzl/gork/app/platform/config"
)

func TestChatRetryCodeParsingMatchesPython(t *testing.T) {
	if got, want := parseRetryCodes("429, 401,abc,503, 401"), map[int]struct{}{429: {}, 401: {}, 503: {}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRetryCodes(csv)=%#v want %#v", got, want)
	}
	if got, want := parseRetryCodes([]any{"429", 503, "  ", "x"}), map[int]struct{}{429: {}, 503: {}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRetryCodes(list)=%#v want %#v", got, want)
	}
	if got := parseRetryCodes(123); len(got) != 0 {
		t.Fatalf("parseRetryCodes(invalid)=%#v want empty", got)
	}

	cfg := map[string]any{"retry.retry_status_codes": []any{"500", "502"}}
	if got, want := configuredRetryCodes(cfg), map[int]struct{}{500: {}, 502: {}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("configuredRetryCodes(legacy)=%#v want %#v", got, want)
	}
	cfg = map[string]any{"retry.on_codes": "418"}
	if got, want := configuredRetryCodes(cfg), map[int]struct{}{418: {}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("configuredRetryCodes(new)=%#v want %#v", got, want)
	}
}

func TestChatNormalizeImageFormatAndImagineURL(t *testing.T) {
	if got := normalizeImageFormat(""); got != "grok_url" {
		t.Fatalf("normalizeImageFormat(empty)=%q", got)
	}
	if got := normalizeImageFormat(" LOCAL_MD "); got != "local_md" {
		t.Fatalf("normalizeImageFormat(local)=%q", got)
	}
	if isImaginePublicURL("https://imagine-public.x.ai/image.png") != true {
		t.Fatalf("expected imagine-public URL")
	}
	if isImaginePublicURL("https://static.x.ai/image.png") != false {
		t.Fatalf("expected non imagine-public URL")
	}
}

func TestChatStripGeneratedArtifacts(t *testing.T) {
	input := " answer\n\nSources:\n- https://example.test\n"
	if got := stripGeneratedArtifacts(input, false); got != "answer\n\nSources:\n- https://example.test" {
		t.Fatalf("stripGeneratedArtifacts(no sources)=%q", got)
	}
	if got := stripGeneratedArtifacts(input, true); got != "answer" {
		t.Fatalf("stripGeneratedArtifacts(strip sources)=%q", got)
	}
}

func TestChatAnnotationsMatchPythonShape(t *testing.T) {
	got := toChatAnnotations([]map[string]any{{
		"url":         "https://example.test",
		"title":       "Example",
		"start_index": 2,
		"end_index":   9,
	}})
	want := []map[string]any{{
		"type": "url_citation",
		"url_citation": map[string]any{
			"url":         "https://example.test",
			"title":       "Example",
			"start_index": 2,
			"end_index":   9,
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("toChatAnnotations=%#v want %#v", got, want)
	}
}

func TestChatExtractMessageFlattensTextFilesToolResultsAndToolCalls(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "be concise"},
		{"role": "assistant", "content": "old answer\n\nSources:\n- https://example.test"},
		{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "look"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,abc"}},
			map[string]any{"type": "file", "file": map[string]any{"file_data": "file-bytes"}},
			"ignored",
		}},
		{"role": "tool", "tool_call_id": "call_1", "content": "tool output"},
		{"role": "assistant", "content": "", "tool_calls": []map[string]any{
			{"function": map[string]any{"name": "search", "arguments": `{"q":"go"}`}},
		}},
	}

	prompt, files := extractMessage(messages)
	for _, want := range []string{
		"[system]: be concise",
		"[assistant]: old answer",
		"[user]: look",
		"[tool result for call_1]:\ntool output",
		"[assistant]:\n<tool_calls>",
		"<tool_name>search</tool_name>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Sources:") {
		t.Fatalf("prompt retained generated sources:\n%s", prompt)
	}
	if !reflect.DeepEqual(files, []string{"data:image/png;base64,abc", "file-bytes"}) {
		t.Fatalf("files=%#v", files)
	}
}

func TestChatUpstreamErrorHelpersMatchPython(t *testing.T) {
	upstream := platform.NewUpstreamError("bad", 503, "line1\nline2")
	if got := upstreamBodyExcerpt(upstream, 8); got != `line1\nl` {
		t.Fatalf("upstreamBodyExcerpt=%q", got)
	}
	if got := upstreamBodyExcerpt(nil, 240); got != "-" {
		t.Fatalf("upstreamBodyExcerpt(nil)=%q", got)
	}
	if got := transportUpstreamError(upstream, "ctx"); got != upstream {
		t.Fatalf("transportUpstreamError(existing) returned %#v", got)
	}
	wrapped := transportUpstreamError(errors.New("dial\nfail"), "chat upstream")
	if wrapped.Status != 502 || wrapped.Body != `dial\nfail` || !strings.Contains(wrapped.Error(), "chat upstream: dial") {
		t.Fatalf("wrapped upstream=%#v body=%q err=%v", wrapped, wrapped.Body, wrapped)
	}
}

func TestChatResolveImageFastPathAndFallbacks(t *testing.T) {
	resetChatDepsForTest(t)

	imageFormatConfig = "grok_url"
	got, err := resolveImage(context.Background(), "tok", "https://cdn.x.ai/a.png", "img1")
	if err != nil || got != "https://cdn.x.ai/a.png" {
		t.Fatalf("grok_url got=%q err=%v", got, err)
	}

	imageFormatConfig = "grok_md"
	got, err = resolveImage(context.Background(), "tok", "https://cdn.x.ai/a.png", "img1")
	if err != nil || got != "![image](https://cdn.x.ai/a.png)" {
		t.Fatalf("grok_md got=%q err=%v", got, err)
	}

	imageFormatConfig = "base64"
	downloadImageBytes = func(context.Context, string, string) ([]byte, string, error) {
		return []byte("hi"), "image/png", nil
	}
	got, err = resolveImage(context.Background(), "tok", "https://cdn.x.ai/a.png", "img1")
	if err != nil || got != "![image](data:image/png;base64,aGk=)" {
		t.Fatalf("base64 got=%q err=%v", got, err)
	}

	imageFormatConfig = "local_url"
	appURLConfig = "https://api.example.test/"
	saveImage = func(raw []byte, mime string, imageID string) string {
		if string(raw) != "hi" || mime != "image/png" || imageID != "img2" {
			t.Fatalf("save args raw=%q mime=%q imageID=%q", raw, mime, imageID)
		}
		return "file_123"
	}
	got, err = resolveImage(context.Background(), "tok", "https://cdn.x.ai/b.png", "img2")
	if err != nil || got != "https://api.example.test/v1/files/image?id=file_123" {
		t.Fatalf("local_url got=%q err=%v", got, err)
	}

	imageFormatConfig = "local_md"
	appURLConfig = ""
	got, err = resolveImage(context.Background(), "tok", "https://cdn.x.ai/c.png", "img2")
	if err != nil || got != "![image](/v1/files/image?id=file_123)" {
		t.Fatalf("local_md got=%q err=%v", got, err)
	}

	imageFormatConfig = "local_url"
	downloadImageBytes = func(context.Context, string, string) ([]byte, string, error) {
		return nil, "", errors.New("download failed")
	}
	got, err = resolveImage(context.Background(), "tok", "https://cdn.x.ai/fallback.png", "img3")
	if err != nil || got != "https://cdn.x.ai/fallback.png" {
		t.Fatalf("download fallback got=%q err=%v", got, err)
	}
}

func TestChatResolveImageProxiesImaginePublicEvenForGrokURL(t *testing.T) {
	resetChatDepsForTest(t)
	imageFormatConfig = "grok_url"
	proxyImaginePublicConfig = true
	downloadImageBytes = func(context.Context, string, string) ([]byte, string, error) {
		return []byte("hi"), "image/jpeg", nil
	}
	saveImage = func([]byte, string, string) string { return "file_imagine" }

	got, err := resolveImage(context.Background(), "tok", "https://imagine-public.x.ai/a.jpg", "img")
	if err != nil || got != "/v1/files/image?id=file_imagine" {
		t.Fatalf("imagine proxy got=%q err=%v", got, err)
	}
}

func TestChatPrepareFileAttachmentsSkipsEmptyAndKeepsIDs(t *testing.T) {
	resetChatDepsForTest(t)
	uploaded := []string{}
	uploadInput = func(_ context.Context, token string, input string) (string, string, error) {
		uploaded = append(uploaded, token+":"+input)
		if input == "skip-id" {
			return "", "uri", nil
		}
		return "file_" + input, "uri", nil
	}

	got, err := prepareFileAttachments(context.Background(), "tok", []string{"a", "", "skip-id", "b"})
	if err != nil {
		t.Fatalf("prepareFileAttachments err=%v", err)
	}
	if !reflect.DeepEqual(uploaded, []string{"tok:a", "tok:skip-id", "tok:b"}) {
		t.Fatalf("uploaded=%#v", uploaded)
	}
	if !reflect.DeepEqual(got, []string{"file_a", "file_b"}) {
		t.Fatalf("attachments=%#v", got)
	}
}

func TestChatStreamChatPostsPayloadAndReturnsLines(t *testing.T) {
	resetChatDepsForTest(t)
	uploadInput = func(_ context.Context, _ string, input string) (string, string, error) {
		return "att_" + input, "", nil
	}
	streamPost = func(_ context.Context, req chatStreamRequest) (*chatStreamResponse, error) {
		if req.Token != "tok" || req.TimeoutSeconds != 9.5 {
			t.Fatalf("request token/timeout=%q/%v", req.Token, req.TimeoutSeconds)
		}
		if req.Headers["origin"] != "https://grok.com" || req.Headers["referer"] != "https://grok.com/" {
			t.Fatalf("headers=%#v", req.Headers)
		}
		var payload map[string]any
		if err := json.Unmarshal(req.PayloadBytes, &payload); err != nil {
			t.Fatalf("payload json: %v", err)
		}
		if payload["message"] != "hello" || payload["modeId"] != "auto" {
			t.Fatalf("payload=%#v", payload)
		}
		if !reflect.DeepEqual(payload["fileAttachments"], []any{"att_a", "att_b"}) {
			t.Fatalf("fileAttachments=%#v", payload["fileAttachments"])
		}
		return &chatStreamResponse{StatusCode: 200, Lines: []string{"data: 1", "data: 2"}}, nil
	}

	lines, err := streamChat(context.Background(), chatStreamOptions{
		Token:          "tok",
		ModeID:         model.ModeAuto,
		Message:        "hello",
		Files:          []string{"a", "", "b"},
		TimeoutSeconds: 9.5,
	})
	if err != nil {
		t.Fatalf("streamChat err=%v", err)
	}
	if !reflect.DeepEqual(lines, []string{"data: 1", "data: 2"}) {
		t.Fatalf("lines=%#v", lines)
	}
}

func TestChatStreamChatWrapsTransportAndNon200(t *testing.T) {
	resetChatDepsForTest(t)
	streamPost = func(context.Context, chatStreamRequest) (*chatStreamResponse, error) {
		return nil, errors.New("dial\nfail")
	}
	_, err := streamChat(context.Background(), chatStreamOptions{Token: "tok", ModeID: model.ModeAuto, Message: "m"})
	upstream, ok := err.(*platform.UpstreamError)
	if !ok || upstream.Status != 502 || upstream.Body != `dial\nfail` {
		t.Fatalf("transport err=%#v", err)
	}

	streamPost = func(context.Context, chatStreamRequest) (*chatStreamResponse, error) {
		return &chatStreamResponse{StatusCode: 503, Body: "bad body"}, nil
	}
	_, err = streamChat(context.Background(), chatStreamOptions{Token: "tok", ModeID: model.ModeAuto, Message: "m"})
	upstream, ok = err.(*platform.UpstreamError)
	if !ok || upstream.Status != 503 || upstream.Body != "bad body" || !strings.Contains(upstream.Error(), "Chat upstream returned 503") {
		t.Fatalf("non200 err=%#v", err)
	}
}

func TestChatRetryAndFeedbackKind(t *testing.T) {
	resetChatDepsForTest(t)
	codes := map[int]struct{}{429: {}, 503: {}}
	if !shouldRetryUpstream(platform.NewUpstreamError("rate", 429, ""), codes) {
		t.Fatalf("429 should retry")
	}
	if shouldRetryUpstream(platform.NewUpstreamError("bad request", 400, ""), codes) {
		t.Fatalf("400 should not retry")
	}
	isInvalidCredentials = func(error) bool { return true }
	if !shouldRetryUpstream(platform.NewUpstreamError("blocked", 403, ""), map[int]struct{}{}) {
		t.Fatalf("invalid credentials should retry")
	}

	isInvalidCredentials = func(error) bool { return false }
	if got := feedbackKind(nil); got != feedbackKindServerError {
		t.Fatalf("feedback nil=%q", got)
	}
	if got := feedbackKind(platform.NewUpstreamError("rate", 429, "")); got != feedbackKindRateLimited {
		t.Fatalf("feedback 429=%q", got)
	}
	if got := feedbackKind(platform.NewUpstreamError("unauth", 401, "")); got != feedbackKindUnauthorized {
		t.Fatalf("feedback 401=%q", got)
	}
	if got := feedbackKind(platform.NewUpstreamError("forbid", 403, "")); got != feedbackKindForbidden {
		t.Fatalf("feedback 403=%q", got)
	}
	isInvalidCredentials = func(error) bool { return true }
	if got := feedbackKind(platform.NewUpstreamError("blocked", 403, "")); got != feedbackKindUnauthorized {
		t.Fatalf("feedback invalid creds=%q", got)
	}
}

func TestChatQuotaSyncOnlyRunsForQuotaStrategy(t *testing.T) {
	resetChatDepsForTest(t)
	refresh := &fakeChatRefreshService{}
	currentAccountStrategy = func() string { return "random" }
	chatRefreshService = func() chatRefreshProvider { return refresh }
	quotaSync(context.Background(), "token-abcdef", 7)
	if refresh.refreshCalls != 0 {
		t.Fatalf("random quota refreshCalls=%d", refresh.refreshCalls)
	}

	currentAccountStrategy = func() string { return "quota" }
	quotaSync(context.Background(), "token-abcdef", 7)
	if refresh.refreshCalls != 1 || refresh.token != "token-abcdef" || refresh.modeID != 7 {
		t.Fatalf("quota refresh=%#v", refresh)
	}
}

func TestChatFailSyncRecordsFailureAndRefreshesQuota429(t *testing.T) {
	resetChatDepsForTest(t)
	refresh := &fakeChatRefreshService{}
	chatRefreshService = func() chatRefreshProvider { return refresh }
	currentAccountStrategy = func() string { return "random" }
	failSync(context.Background(), "tok", 3, platform.NewUpstreamError("rate", 429, ""))
	if refresh.recordCalls != 1 || refresh.onDemandCalls != 0 {
		t.Fatalf("random fail sync=%#v", refresh)
	}

	currentAccountStrategy = func() string { return "quota" }
	failSync(context.Background(), "tok", 3, platform.NewUpstreamError("rate", 429, ""))
	if refresh.recordCalls != 2 || refresh.onDemandCalls != 1 {
		t.Fatalf("quota 429 fail sync=%#v", refresh)
	}

	failSync(context.Background(), "tok", 3, platform.NewUpstreamError("server", 503, ""))
	if refresh.recordCalls != 3 || refresh.onDemandCalls != 1 {
		t.Fatalf("quota 503 fail sync=%#v", refresh)
	}
}

func TestChatBuildNonStreamResponseReturnsToolCallsWhenParsed(t *testing.T) {
	resetChatDepsForTest(t)
	state := chatCompletionState{
		Text:          `<tool_calls><tool_call><tool_name>search</tool_name><parameters>{"q":"go"}</parameters></tool_call></tool_calls>`,
		SearchSources: []map[string]any{{"url": "https://example.test", "title": "Example", "type": "web"}},
	}

	resp, err := buildNonStreamChatResponse(chatResponseBuildOptions{
		Model:      "grok-2",
		Message:    "prompt",
		ResponseID: "chatcmpl-fixed",
		ToolNames:  []string{"search"},
		State:      state,
	})
	if err != nil {
		t.Fatalf("build err=%v", err)
	}
	choices := resp["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != nil {
		t.Fatalf("tool response content=%#v", message["content"])
	}
	if got := len(message["tool_calls"].([]any)); got != 1 {
		t.Fatalf("tool call count=%d", got)
	}
	if !reflect.DeepEqual(resp["search_sources"], state.SearchSources) {
		t.Fatalf("search_sources=%#v", resp["search_sources"])
	}
}

func TestChatBuildNonStreamResponseAppendsImagesReferencesThinkingAndAnnotations(t *testing.T) {
	resetChatDepsForTest(t)
	state := chatCompletionState{
		Text:       "answer",
		Thinking:   "reason",
		ImageTexts: []string{"![image](local)"},
		References: "\n\n## Sources\n[gork-sources]: #\n- [Example](https://example.test)\n",
		Annotations: []map[string]any{{
			"url":         "https://example.test",
			"title":       "Example",
			"start_index": 1,
			"end_index":   4,
		}},
		SearchSources: []map[string]any{{"url": "https://example.test", "title": "Example", "type": "web"}},
	}

	resp, err := buildNonStreamChatResponse(chatResponseBuildOptions{
		Model:      "grok-2",
		Message:    "prompt",
		ResponseID: "chatcmpl-fixed",
		EmitThink:  true,
		State:      state,
	})
	if err != nil {
		t.Fatalf("build err=%v", err)
	}
	choices := resp["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	content := message["content"].(string)
	for _, want := range []string{"answer", "![image](local)", "## Sources"} {
		if !strings.Contains(content, want) {
			t.Fatalf("content missing %q: %s", want, content)
		}
	}
	if message["reasoning_content"] != "reason" {
		t.Fatalf("reasoning=%#v", message["reasoning_content"])
	}
	if _, ok := message["annotations"]; !ok {
		t.Fatalf("missing annotations in message=%#v", message)
	}
	if !reflect.DeepEqual(resp["search_sources"], state.SearchSources) {
		t.Fatalf("search_sources=%#v", resp["search_sources"])
	}
}

func TestChatPrepareCompletionDefaultsToolsAndRetryConfig(t *testing.T) {
	resetChatDepsForTest(t)
	chatFeatureStream = func() bool { return false }
	chatFeatureThinking = func() bool { return false }
	chatSelectionMaxRetries = func() int { return 3 }
	chatRetryConfig = func() map[string]any { return map[string]any{"retry.on_codes": "418, 429"} }
	chatTimeoutSeconds = func() float64 { return 42.5 }
	chatResponseID = func() string { return "chatcmpl-fixed" }

	plan, err := prepareChatCompletion(chatCompletionOptions{
		Model: "grok-4.20-auto",
		Messages: []map[string]any{
			{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "hello"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,abc"}},
			}},
		},
		Tools:      []map[string]any{{"type": "function", "function": map[string]any{"name": "search", "description": "Search"}}},
		ToolChoice: map[string]any{"type": "required"},
	})
	if err != nil {
		t.Fatalf("prepare err=%v", err)
	}
	if plan.IsStream || plan.EmitThink {
		t.Fatalf("defaults stream/think=%t/%t", plan.IsStream, plan.EmitThink)
	}
	if plan.Spec.ModelName != "grok-4.20-auto" || plan.Spec.ModeID != model.ModeAuto {
		t.Fatalf("spec=%#v", plan.Spec)
	}
	if plan.MaxRetries != 3 || plan.TimeoutSeconds != 42.5 || plan.ResponseID != "chatcmpl-fixed" {
		t.Fatalf("retry/timeout/id=%d/%v/%q", plan.MaxRetries, plan.TimeoutSeconds, plan.ResponseID)
	}
	if _, ok := plan.RetryCodes[418]; !ok {
		t.Fatalf("retry codes=%#v", plan.RetryCodes)
	}
	if !reflect.DeepEqual(plan.Files, []string{"data:image/png;base64,abc"}) {
		t.Fatalf("files=%#v", plan.Files)
	}
	if !reflect.DeepEqual(plan.ToolNames, []string{"search"}) {
		t.Fatalf("tool names=%#v", plan.ToolNames)
	}
	if !strings.Contains(plan.Message, "[system]:") || !strings.Contains(plan.Message, "hello") || !strings.Contains(plan.Message, "MUST output a <tool_calls> XML block") {
		t.Fatalf("message=%s", plan.Message)
	}
}

func TestChatProductionDefaultsUseConfigRefreshAndInvalidCredentialsRuntime(t *testing.T) {
	oldConfig := platformconfig.GlobalConfig
	oldStrategy := dataaccount.CurrentStrategy()
	oldRefresh := controlaccount.GetRefreshService()
	t.Cleanup(func() {
		platformconfig.GlobalConfig = oldConfig
		_ = dataaccount.SetStrategy(oldStrategy)
		controlaccount.SetRefreshService(oldRefresh)
	})

	defaults := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaults, []byte("[features]\nstream = true\nthinking = true\n\n[chat]\ntimeout = 120.0\n\n[retry]\non_codes = \"429,401,503\"\n"), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(chatConfigBackend{data: map[string]any{
		"features": map[string]any{"stream": false, "thinking": false},
		"chat":     map[string]any{"timeout": 33.25},
		"retry":    map[string]any{"on_codes": []any{"418", 429}},
	}}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaults); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if chatFeatureStream() || chatFeatureThinking() {
		t.Fatalf("feature defaults stream/thinking = %t/%t", chatFeatureStream(), chatFeatureThinking())
	}
	if got := chatTimeoutSeconds(); got != 33.25 {
		t.Fatalf("timeout = %v", got)
	}
	retryCodes := configuredRetryCodes(chatRetryConfig())
	if _, ok := retryCodes[418]; !ok {
		t.Fatalf("retry codes missing 418: %#v", retryCodes)
	}

	refresh := &fakeRuntimeChatRefreshService{}
	controlaccount.SetRefreshService(refresh)
	if err := dataaccount.SetStrategy("random"); err != nil {
		t.Fatalf("set random strategy: %v", err)
	}
	quotaSync(context.Background(), "tok", 5)
	if refresh.refreshCalls != 0 {
		t.Fatalf("random strategy quota sync calls = %d", refresh.refreshCalls)
	}
	if err := dataaccount.SetStrategy("quota"); err != nil {
		t.Fatalf("set quota strategy: %v", err)
	}
	quotaSync(context.Background(), "tok", 5)
	failSync(context.Background(), "tok", 5, platform.NewUpstreamError("rate", 429, ""))
	if refresh.refreshCalls != 1 || refresh.failureCalls != 1 || refresh.onDemandCalls != 1 {
		t.Fatalf("refreshCalls=%d failureCalls=%d onDemandCalls=%d", refresh.refreshCalls, refresh.failureCalls, refresh.onDemandCalls)
	}

	invalid := platform.NewUpstreamError("invalid", 401, "token expired")
	if !isInvalidCredentials(invalid) {
		t.Fatalf("invalid credentials error should be detected")
	}
	if !shouldRetryUpstream(invalid, map[int]struct{}{}) {
		t.Fatalf("invalid credentials error should retry")
	}
	if got := feedbackKind(invalid); got != feedbackKindUnauthorized {
		t.Fatalf("feedback kind = %s", got)
	}
}

func TestChatPrepareCompletionOverridesAndConsole(t *testing.T) {
	resetChatDepsForTest(t)
	stream := false
	think := false
	plan, err := prepareChatCompletion(chatCompletionOptions{
		Model:     "grok-4.20-auto",
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
		Stream:    &stream,
		EmitThink: &think,
	})
	if err != nil {
		t.Fatalf("prepare err=%v", err)
	}
	if plan.IsStream || plan.EmitThink {
		t.Fatalf("overrides stream/think=%t/%t", plan.IsStream, plan.EmitThink)
	}

	plan, err = prepareChatCompletion(chatCompletionOptions{
		Model:    "grok-4.3-console",
		Messages: []map[string]any{{"role": "user", "content": "hello"}},
	})
	if err != nil {
		t.Fatalf("console prepare err=%v", err)
	}
	if !plan.IsConsole {
		t.Fatalf("console flag false for spec=%#v", plan.Spec)
	}
}

func TestChatPrepareCompletionRejectsEmptyMessage(t *testing.T) {
	resetChatDepsForTest(t)
	_, err := prepareChatCompletion(chatCompletionOptions{
		Model:    "grok-4.20-auto",
		Messages: []map[string]any{{"role": "user", "content": "   "}},
	})
	upstream, ok := err.(*platform.UpstreamError)
	if !ok || upstream.Status != 400 {
		t.Fatalf("empty err=%#v", err)
	}
}

func TestChatCompletionsDelegatesConsoleModels(t *testing.T) {
	resetChatDepsForTest(t)
	called := false
	consoleCompletions = func(_ context.Context, options chatCompletionOptions) (chatCompletionResult, error) {
		called = true
		if options.Model != "grok-4.3-console" || options.Stream == nil || options.EmitThink == nil {
			t.Fatalf("console options=%#v", options)
		}
		return chatCompletionResult{Response: map[string]any{"console": true}}, nil
	}

	result, err := Completions(context.Background(), chatCompletionOptions{
		Model:    "grok-4.3-console",
		Messages: []map[string]any{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("console err=%v", err)
	}
	if !called || result.Response["console"] != true {
		t.Fatalf("console result called=%t result=%#v", called, result)
	}
}

func TestChatLogTaskExceptionIgnoresNil(t *testing.T) {
	logTaskException(nil)
}

func TestChatCompletionsNonStreamRunsAccountStreamAndBuildsResponse(t *testing.T) {
	resetChatDepsForTest(t)
	stream := false
	dir := &fakeChatDirectory{accounts: []chatAccount{{Token: "tok1", ModeID: model.ModeAuto}}}
	chatDirectoryProvider = func() chatDirectory { return dir }
	streamPost = func(_ context.Context, req chatStreamRequest) (*chatStreamResponse, error) {
		if req.Token != "tok1" {
			t.Fatalf("stream token=%q", req.Token)
		}
		return &chatStreamResponse{StatusCode: 200, Lines: []string{
			`data: {"result":{"response":{"token":"hello ","isThinking":false,"messageTag":"final"}}}`,
			`data: {"result":{"response":{"token":"world","isThinking":false,"messageTag":"final"}}}`,
			`data: [DONE]`,
		}}, nil
	}

	result, err := Completions(context.Background(), chatCompletionOptions{
		Model:    "grok-4.20-auto",
		Messages: []map[string]any{{"role": "user", "content": "hi"}},
		Stream:   &stream,
	})
	if err != nil {
		t.Fatalf("Completions err=%v", err)
	}
	if result.IsStream || result.Response == nil {
		t.Fatalf("result=%#v", result)
	}
	choices := result.Response["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "hello world" {
		t.Fatalf("content=%#v", message["content"])
	}
	if dir.releases != 1 || len(dir.feedbacks) != 1 || dir.feedbacks[0].Kind != feedbackKindSuccess {
		t.Fatalf("dir release/feedback=%d/%#v", dir.releases, dir.feedbacks)
	}
}

func TestChatCompletionsStreamReturnsDataFrames(t *testing.T) {
	resetChatDepsForTest(t)
	stream := true
	dir := &fakeChatDirectory{accounts: []chatAccount{{Token: "tok1", ModeID: model.ModeAuto}}}
	chatDirectoryProvider = func() chatDirectory { return dir }
	streamPost = func(context.Context, chatStreamRequest) (*chatStreamResponse, error) {
		return &chatStreamResponse{StatusCode: 200, Lines: []string{
			`data: {"result":{"response":{"token":"hi","isThinking":false,"messageTag":"final"}}}`,
			`data: [DONE]`,
		}}, nil
	}

	result, err := Completions(context.Background(), chatCompletionOptions{
		Model:    "grok-4.20-auto",
		Messages: []map[string]any{{"role": "user", "content": "hi"}},
		Stream:   &stream,
	})
	if err != nil {
		t.Fatalf("Completions stream err=%v", err)
	}
	if !result.IsStream || len(result.StreamFrames) == 0 {
		t.Fatalf("stream result=%#v", result)
	}
	joined := strings.Join(result.StreamFrames, "")
	if !strings.Contains(joined, `"content":"hi"`) || !strings.Contains(joined, "data: [DONE]") {
		t.Fatalf("frames=%s", joined)
	}
}

func TestChatCompletionsRetriesConfiguredUpstreamStatusWithExcludedToken(t *testing.T) {
	resetChatDepsForTest(t)
	stream := false
	chatSelectionMaxRetries = func() int { return 1 }
	chatRetryConfig = func() map[string]any { return map[string]any{"retry.on_codes": "429"} }
	dir := &fakeChatDirectory{accounts: []chatAccount{
		{Token: "tok1", ModeID: model.ModeAuto},
		{Token: "tok2", ModeID: model.ModeFast},
	}}
	chatDirectoryProvider = func() chatDirectory { return dir }
	streamPost = func(_ context.Context, req chatStreamRequest) (*chatStreamResponse, error) {
		if req.Token == "tok1" {
			return &chatStreamResponse{StatusCode: 429, Body: "rate"}, nil
		}
		return &chatStreamResponse{StatusCode: 200, Lines: []string{
			`data: {"result":{"response":{"token":"ok","isThinking":false,"messageTag":"final"}}}`,
			`data: [DONE]`,
		}}, nil
	}

	result, err := Completions(context.Background(), chatCompletionOptions{
		Model:    "grok-4.20-auto",
		Messages: []map[string]any{{"role": "user", "content": "hi"}},
		Stream:   &stream,
	})
	if err != nil {
		t.Fatalf("Completions retry err=%v", err)
	}
	if result.Response == nil {
		t.Fatalf("missing response after retry")
	}
	if !reflect.DeepEqual(dir.excludes, [][]string{{}, {"tok1"}}) {
		t.Fatalf("excludes=%#v", dir.excludes)
	}
	if dir.releases != 2 || len(dir.feedbacks) != 2 {
		t.Fatalf("release/feedback=%d/%#v", dir.releases, dir.feedbacks)
	}
	if dir.feedbacks[0].Kind != feedbackKindRateLimited || dir.feedbacks[1].Kind != feedbackKindSuccess {
		t.Fatalf("feedback kinds=%#v", dir.feedbacks)
	}
}

func TestChatCompletionsStreamSieveEmitsToolCallChunks(t *testing.T) {
	resetChatDepsForTest(t)
	stream := true
	dir := &fakeChatDirectory{accounts: []chatAccount{{Token: "tok1", ModeID: model.ModeAuto}}}
	chatDirectoryProvider = func() chatDirectory { return dir }
	streamPost = func(context.Context, chatStreamRequest) (*chatStreamResponse, error) {
		return &chatStreamResponse{StatusCode: 200, Lines: []string{
			`data: {"result":{"response":{"token":"<tool_calls><tool_call><tool_name>search</tool_name><parameters>{\"q\":\"go\"}</parameters></tool_call></tool_calls>","isThinking":false,"messageTag":"final"}}}`,
			`data: [DONE]`,
		}}, nil
	}

	result, err := Completions(context.Background(), chatCompletionOptions{
		Model:    "grok-4.20-auto",
		Messages: []map[string]any{{"role": "user", "content": "hi"}},
		Stream:   &stream,
		Tools:    []map[string]any{{"type": "function", "function": map[string]any{"name": "search"}}},
	})
	if err != nil {
		t.Fatalf("Completions stream tool err=%v", err)
	}
	joined := strings.Join(result.StreamFrames, "")
	if strings.Contains(joined, "<tool_calls>") {
		t.Fatalf("raw XML leaked in frames=%s", joined)
	}
	if !strings.Contains(joined, `"tool_calls"`) || !strings.Contains(joined, `"finish_reason":"tool_calls"`) {
		t.Fatalf("missing tool call chunks=%s", joined)
	}
}

type fakeChatDirectory struct {
	accounts  []chatAccount
	releases  int
	feedbacks []chatFeedback
	excludes  [][]string
}

func (d *fakeChatDirectory) ReserveChatAccount(_ context.Context, _ model.ModelSpec, exclude []string) (chatAccount, bool, error) {
	d.excludes = append(d.excludes, append([]string{}, exclude...))
	if len(d.accounts) == 0 {
		return chatAccount{}, false, nil
	}
	account := d.accounts[0]
	d.accounts = d.accounts[1:]
	return account, true, nil
}

func (d *fakeChatDirectory) ReleaseChatAccount(context.Context, chatAccount) error {
	d.releases++
	return nil
}

func (d *fakeChatDirectory) FeedbackChatAccount(_ context.Context, feedback chatFeedback) error {
	d.feedbacks = append(d.feedbacks, feedback)
	return nil
}

type fakeChatRefreshService struct {
	refreshCalls  int
	recordCalls   int
	onDemandCalls int
	token         string
	modeID        int
}

func (s *fakeChatRefreshService) RefreshCall(_ context.Context, token string, modeID int) error {
	s.refreshCalls++
	s.token = token
	s.modeID = modeID
	return nil
}

func (s *fakeChatRefreshService) RecordFailure(context.Context, string, int, error) error {
	s.recordCalls++
	return nil
}

func (s *fakeChatRefreshService) RefreshOnDemand(context.Context) (chatRefreshResult, error) {
	s.onDemandCalls++
	return chatRefreshResult{Refreshed: 1}, nil
}

type fakeRuntimeChatRefreshService struct {
	refreshCalls  int
	failureCalls  int
	onDemandCalls int
}

func (s *fakeRuntimeChatRefreshService) RefreshScheduled(context.Context, *string) (controlaccount.RefreshResult, error) {
	return controlaccount.RefreshResult{}, nil
}

func (s *fakeRuntimeChatRefreshService) RefreshCallAsync(context.Context, string, int) error {
	s.refreshCalls++
	return nil
}

func (s *fakeRuntimeChatRefreshService) RecordFailureAsync(context.Context, string, int, error) error {
	s.failureCalls++
	return nil
}

func (s *fakeRuntimeChatRefreshService) RefreshOnDemand(context.Context) (controlaccount.RefreshResult, error) {
	s.onDemandCalls++
	return controlaccount.RefreshResult{Refreshed: 1}, nil
}

type chatConfigBackend struct {
	data map[string]any
}

func (b chatConfigBackend) Load(context.Context) (map[string]any, error) {
	return b.data, nil
}

func (b chatConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return nil
}

func (b chatConfigBackend) Clear(context.Context) error {
	return nil
}

func (b chatConfigBackend) Version(context.Context) (any, error) {
	return "test", nil
}

func (b chatConfigBackend) Close(context.Context) error {
	return nil
}

func resetChatDepsForTest(t *testing.T) {
	t.Helper()
	oldImageFormat := imageFormatConfig
	oldProxyImagine := proxyImaginePublicConfig
	oldAppURL := appURLConfig
	oldDownload := downloadImageBytes
	oldSave := saveImage
	oldUpload := uploadInput
	oldPost := streamPost
	oldStrategy := currentAccountStrategy
	oldRefresh := chatRefreshService
	oldInvalid := isInvalidCredentials
	oldFeatureStream := chatFeatureStream
	oldFeatureThinking := chatFeatureThinking
	oldSelectionRetries := chatSelectionMaxRetries
	oldRetryConfig := chatRetryConfig
	oldTimeout := chatTimeoutSeconds
	oldResponseID := chatResponseID
	oldDirectory := chatDirectoryProvider
	oldConsole := consoleCompletions
	oldConsoleStream := consoleStreamChat

	imageFormatConfig = "grok_url"
	proxyImaginePublicConfig = false
	appURLConfig = ""
	downloadImageBytes = func(context.Context, string, string) ([]byte, string, error) {
		return []byte("hi"), "image/png", nil
	}
	saveImage = func([]byte, string, string) string { return "file_123" }
	uploadInput = func(context.Context, string, string) (string, string, error) {
		return "", "", nil
	}
	streamPost = func(context.Context, chatStreamRequest) (*chatStreamResponse, error) {
		return &chatStreamResponse{StatusCode: 200}, nil
	}
	currentAccountStrategy = func() string { return "quota" }
	chatRefreshService = func() chatRefreshProvider { return nil }
	isInvalidCredentials = func(error) bool { return false }
	chatFeatureStream = func() bool { return true }
	chatFeatureThinking = func() bool { return true }
	chatSelectionMaxRetries = func() int { return 1 }
	chatRetryConfig = func() map[string]any { return map[string]any{"retry.retry_status_codes": "429,401,503"} }
	chatTimeoutSeconds = func() float64 { return 120.0 }
	chatResponseID = func() string { return "chatcmpl-test" }
	chatDirectoryProvider = func() chatDirectory { return nil }
	consoleCompletions = func(context.Context, chatCompletionOptions) (chatCompletionResult, error) {
		return chatCompletionResult{}, errors.New("console chat completions are not configured")
	}
	consoleStreamChat = func(context.Context, string, map[string]any, float64) ([]protocol.ConsoleStreamEvent, error) {
		return nil, errors.New("console stream is not configured")
	}

	t.Cleanup(func() {
		imageFormatConfig = oldImageFormat
		proxyImaginePublicConfig = oldProxyImagine
		appURLConfig = oldAppURL
		downloadImageBytes = oldDownload
		saveImage = oldSave
		uploadInput = oldUpload
		streamPost = oldPost
		currentAccountStrategy = oldStrategy
		chatRefreshService = oldRefresh
		isInvalidCredentials = oldInvalid
		chatFeatureStream = oldFeatureStream
		chatFeatureThinking = oldFeatureThinking
		chatSelectionMaxRetries = oldSelectionRetries
		chatRetryConfig = oldRetryConfig
		chatTimeoutSeconds = oldTimeout
		chatResponseID = oldResponseID
		chatDirectoryProvider = oldDirectory
		consoleCompletions = oldConsole
		consoleStreamChat = oldConsoleStream
	})
}
