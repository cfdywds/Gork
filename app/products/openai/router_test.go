package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dslzl/gork/app/platform"
	"github.com/dslzl/gork/app/platform/auth"
	"github.com/dslzl/gork/app/platform/storage"
)

func TestRouterListModelsFiltersByAvailablePools(t *testing.T) {
	resetRouterDepsForTest(t)
	routerAvailablePools = func(*http.Request) map[string]struct{} {
		return map[string]struct{}{"basic": {}}
	}

	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeRouterJSON(t, rec)
	if body["object"] != "list" {
		t.Fatalf("object=%#v want list", body["object"])
	}
	ids := routerResponseModelIDs(t, body)
	if !containsString(ids, "grok-4.20-fast") {
		t.Fatalf("basic pool ids missing fast model: %v", ids)
	}
	if !containsString(ids, "grok-imagine-image-lite") {
		t.Fatalf("basic pool ids missing lite image model: %v", ids)
	}
	if containsString(ids, "grok-4.20-auto") {
		t.Fatalf("basic pool ids unexpectedly include super auto model: %v", ids)
	}
	if containsString(ids, "grok-4.20-heavy") {
		t.Fatalf("basic pool ids unexpectedly include heavy model: %v", ids)
	}
}

func TestRouterGetModelReturnsOpenAIShapeAndNotFound(t *testing.T) {
	resetRouterDepsForTest(t)
	routerAvailablePools = func(*http.Request) map[string]struct{} {
		return map[string]struct{}{"basic": {}}
	}

	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/grok-4.20-fast", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeRouterJSON(t, rec)
	if body["id"] != "grok-4.20-fast" || body["object"] != "model" || body["owned_by"] != "xai" {
		t.Fatalf("model body=%#v", body)
	}
	if body["name"] != "Grok 4.20 Fast" {
		t.Fatalf("name=%#v", body["name"])
	}

	rec = httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/grok-4.20-auto", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	errBody := decodeRouterJSON(t, rec)["error"].(map[string]any)
	if errBody["type"] != "invalid_request_error" || errBody["message"] != "Model 'grok-4.20-auto' not found" {
		t.Fatalf("error=%#v", errBody)
	}
}

func TestRouterServesLocalImageAndVideoFiles(t *testing.T) {
	resetRouterDepsForTest(t)
	t.Setenv("DATA_DIR", t.TempDir())

	imageID := "1234567890abcdef"
	imageDir, err := storage.ImageFilesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, imageID+".png"), []byte("png-body"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/files/image?id="+imageID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("image status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "png-body" {
		t.Fatalf("image body=%q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("image content-type=%q", got)
	}

	videoID := "fedcba0987654321"
	videoDir, err := storage.VideoFilesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(videoDir, videoID+".mp4"), []byte("mp4-body"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/files/video?id="+videoID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("video status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "mp4-body" {
		t.Fatalf("video body=%q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("video content-type=%q", got)
	}
}

func TestRouterProtectedEndpointsVerifyAPIKey(t *testing.T) {
	resetRouterDepsForTest(t)
	routerAuthSettings = func() auth.AuthSettings {
		return auth.AuthSettings{APIKey: "secret"}
	}
	routerAvailablePools = func(*http.Request) map[string]struct{} {
		return map[string]struct{}{"basic": {}}
	}

	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status=%d body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("x-api-key", "secret")
	rec = httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid key status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRouterValidationHelpersMatchPythonBounds(t *testing.T) {
	resetRouterDepsForTest(t)

	if err := validateImageN("grok-imagine-image-lite", 5, "n"); !isValidationParam(err, "n") {
		t.Fatalf("lite n err=%#v", err)
	}
	if err := validateImageN("grok-imagine-image", 11, "image_config.n"); !isValidationParam(err, "image_config.n") {
		t.Fatalf("image n err=%#v", err)
	}
	if err := validateImageEditN(3, "n"); !isValidationParam(err, "n") {
		t.Fatalf("edit n err=%#v", err)
	}
	if err := validateChat(ChatCompletionRequest{
		Model: "grok-4.20-fast",
		Messages: []MessageItem{
			{Role: "alien", Content: "hello"},
		},
	}); !isValidationParam(err, "messages.0.role") {
		t.Fatalf("role err=%#v", err)
	}
}

func TestRouterChatCompletionsDispatchesOptions(t *testing.T) {
	resetRouterDepsForTest(t)
	var got chatCompletionOptions
	routerCompletions = func(_ context.Context, options chatCompletionOptions) (chatCompletionResult, error) {
		got = options
		return chatCompletionResult{Response: map[string]any{"id": "chat-ok"}}, nil
	}

	body := `{"model":"grok-4.20-fast","messages":[{"role":"user","content":"hello"}],"stream":true,"reasoning_effort":"none","temperature":0.2,"top_p":0.3}`
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got.Model != "grok-4.20-fast" || len(got.Messages) != 1 || got.Messages[0]["role"] != "user" || got.Messages[0]["content"] != "hello" {
		t.Fatalf("chat options=%#v", got)
	}
	if got.Stream == nil || !*got.Stream {
		t.Fatalf("stream=%#v", got.Stream)
	}
	if got.EmitThink == nil || *got.EmitThink {
		t.Fatalf("emitThink=%#v", got.EmitThink)
	}
	if got.Temperature != 0.2 || got.TopP != 0.3 {
		t.Fatalf("sampling=%v/%v", got.Temperature, got.TopP)
	}
}

func TestRouterResponsesDispatchesOptions(t *testing.T) {
	resetRouterDepsForTest(t)
	var got responseOptions
	routerResponses = func(_ context.Context, options responseOptions) (chatCompletionResult, error) {
		got = options
		return chatCompletionResult{Response: map[string]any{"id": "resp-ok"}}, nil
	}

	body := `{"model":"grok-4.20-fast","input":"hello","instructions":"sys","stream":true,"reasoning":{"effort":"none"},"temperature":0.4,"top_p":0.5,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":"auto"}`
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got.Model != "grok-4.20-fast" || got.Input != "hello" || got.Instructions != "sys" {
		t.Fatalf("responses options=%#v", got)
	}
	if !got.Stream || got.EmitThink {
		t.Fatalf("stream/emit=%v/%v", got.Stream, got.EmitThink)
	}
	if got.Temperature != 0.4 || got.TopP != 0.5 || got.ToolChoice != "auto" {
		t.Fatalf("responses sampling/tool_choice=%#v", got)
	}
	if len(got.Tools) != 1 || got.Tools[0]["name"] != "lookup" {
		t.Fatalf("tools=%#v", got.Tools)
	}
}

func TestRouterImageGenerationDispatchesOptions(t *testing.T) {
	resetRouterDepsForTest(t)
	var got imageGenerationOptions
	routerGenerateImages = func(_ context.Context, options imageGenerationOptions) (imageResult, error) {
		got = options
		return imageResult{Response: map[string]any{"created": float64(1), "data": []any{}}}, nil
	}

	body := `{"model":"grok-imagine-image-lite","prompt":"draw","n":2,"size":"1024x1024","response_format":"b64_json"}`
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got.Model != "grok-imagine-image-lite" || got.Prompt != "draw" || got.N != 2 || got.Size != "1024x1024" || got.ResponseFormat != "b64_json" {
		t.Fatalf("image generation options=%#v", got)
	}
	if got.Stream || got.ChatFormat {
		t.Fatalf("stream/chat_format=%v/%v", got.Stream, got.ChatFormat)
	}
}

func TestRouterImageEditsMultipartDispatchesOptions(t *testing.T) {
	resetRouterDepsForTest(t)
	var got imageEditOptions
	routerEditImages = func(_ context.Context, options imageEditOptions) (imageResult, error) {
		got = options
		return imageResult{Response: map[string]any{"created": float64(1), "data": []any{}}}, nil
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "grok-imagine-image-edit"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("prompt", "edit prompt"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("n", "2"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("size", "1024x1024"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("response_format", "b64_json"); err != nil {
		t.Fatal(err)
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="image"; filename="ref.png"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("image-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got.Model != "grok-imagine-image-edit" || got.N != 2 || got.Size != "1024x1024" || got.ResponseFormat != "b64_json" {
		t.Fatalf("image edit options=%#v", got)
	}
	content, ok := got.Messages[0]["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("content=%#v", got.Messages)
	}
	textPart := content[0].(map[string]any)
	imagePart := content[1].(map[string]any)
	imageURL := imagePart["image_url"].(map[string]any)["url"].(string)
	if textPart["text"] != "edit prompt" || !strings.HasPrefix(imageURL, "data:") || !strings.Contains(imageURL, "base64,") {
		t.Fatalf("content=%#v", content)
	}
	if got.Stream || got.ChatFormat {
		t.Fatalf("stream/chat_format=%v/%v", got.Stream, got.ChatFormat)
	}
}

func TestRouterSerializesTypedUpstreamError(t *testing.T) {
	resetRouterDepsForTest(t)
	routerCompletions = func(context.Context, chatCompletionOptions) (chatCompletionResult, error) {
		return chatCompletionResult{}, platform.NewUpstreamError("proxy failed", http.StatusBadGateway, "")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-4.20-fast","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	errBody := decodeRouterJSON(t, rec)["error"].(map[string]any)
	if errBody["type"] != string(platform.ErrorKindUpstream) || errBody["code"] != "upstream_error" {
		t.Fatalf("error body=%#v", errBody)
	}
}

func TestRouterRouteGoldenStatusHeadersAndShapes(t *testing.T) {
	resetRouterDepsForTest(t)
	resetVideoDepsForTest(t)
	t.Setenv("DATA_DIR", t.TempDir())

	routerAvailablePools = func(*http.Request) map[string]struct{} {
		return map[string]struct{}{"basic": {}, "super": {}}
	}
	routerCompletions = func(_ context.Context, _ chatCompletionOptions) (chatCompletionResult, error) {
		return chatCompletionResult{Response: map[string]any{"id": "chatcmpl_golden", "object": "chat.completion"}}, nil
	}
	routerResponses = func(_ context.Context, _ responseOptions) (chatCompletionResult, error) {
		return chatCompletionResult{Response: map[string]any{"id": "resp_golden", "object": "response"}}, nil
	}
	routerGenerateImages = func(_ context.Context, _ imageGenerationOptions) (imageResult, error) {
		return imageResult{Response: map[string]any{"created": float64(1), "data": []any{map[string]any{"url": "https://example.test/image.png"}}}}, nil
	}
	routerEditImages = func(_ context.Context, _ imageEditOptions) (imageResult, error) {
		return imageResult{Response: map[string]any{"created": float64(2), "data": []any{map[string]any{"b64_json": "aW1n"}}}}, nil
	}
	videoStartJob = func(context.Context, *VideoJob, videoJobOptions) {}

	imageID := "1234567890abcdef"
	imageDir, err := storage.ImageFilesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, imageID+".png"), []byte("png-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	videoFileID := "fedcba0987654321"
	videoDir, err := storage.VideoFilesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(videoDir, videoFileID+".mp4"), []byte("mp4-body"), 0o644); err != nil {
		t.Fatal(err)
	}

	videoID := "video_testid"

	for _, tt := range []struct {
		name        string
		routePath   string
		method      string
		path        string
		body        string
		requestType string
		contentType string
		status      int
		json        map[string]any
		bodyText    string
		bodyPrefix  string
	}{
		{name: "list models", method: http.MethodGet, path: "/v1/models", status: http.StatusOK, json: map[string]any{"object": "list"}},
		{name: "get model", method: http.MethodGet, path: "/v1/models/grok-4.20-fast", status: http.StatusOK, json: map[string]any{"id": "grok-4.20-fast", "object": "model", "owned_by": "xai"}},
		{name: "chat completions", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"grok-4.20-fast","messages":[{"role":"user","content":"hello"}]}`, status: http.StatusOK, json: map[string]any{"id": "chatcmpl_golden", "object": "chat.completion"}},
		{name: "responses", method: http.MethodPost, path: "/v1/responses", body: `{"model":"grok-4.20-fast","input":"hello"}`, status: http.StatusOK, json: map[string]any{"id": "resp_golden", "object": "response"}},
		{name: "image generations", method: http.MethodPost, path: "/v1/images/generations", body: `{"model":"grok-imagine-image-lite","prompt":"draw"}`, status: http.StatusOK, json: map[string]any{"created": float64(1)}},
		{name: "video create", method: http.MethodPost, path: "/v1/videos", body: "model=grok-imagine-video&prompt=golden&seconds=6&size=720x1280", requestType: "application/x-www-form-urlencoded", status: http.StatusOK, json: map[string]any{"id": videoID, "object": "video", "status": "queued"}},
		{name: "video retrieve", method: http.MethodGet, path: "/v1/videos/" + videoID, status: http.StatusOK, json: map[string]any{"id": videoID, "object": "video", "status": "queued"}},
		{name: "video content conflict", method: http.MethodGet, path: "/v1/videos/" + videoID + "/content", status: http.StatusConflict, json: map[string]any{"error.type": "invalid_request_error"}},
		{name: "serve image", routePath: "/v1/files/image", method: http.MethodGet, path: "/v1/files/image?id=" + imageID, status: http.StatusOK, contentType: "image/png", bodyText: "png-body"},
		{name: "serve video", routePath: "/v1/files/video", method: http.MethodGet, path: "/v1/files/video?id=" + videoFileID, status: http.StatusOK, contentType: "video/mp4", bodyText: "mp4-body"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.routePath != "" && !strings.HasPrefix(tt.path, tt.routePath) {
				t.Fatalf("path=%q does not exercise route %q", tt.path, tt.routePath)
			}
			req := routerRequest(t, tt.method, tt.path, strings.NewReader(tt.body))
			if tt.requestType != "" {
				req.Header.Set("Content-Type", tt.requestType)
			}
			rec := httptest.NewRecorder()
			NewRouter().ServeHTTP(rec, req)
			if tt.json != nil {
				assertRouterGoldenJSON(t, rec, tt.status, tt.json)
				return
			}
			if rec.Code != tt.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tt.contentType != "" && rec.Header().Get("Content-Type") != tt.contentType {
				t.Fatalf("content-type=%q want %q", rec.Header().Get("Content-Type"), tt.contentType)
			}
			if tt.bodyText != "" && rec.Body.String() != tt.bodyText {
				t.Fatalf("body=%q want %q", rec.Body.String(), tt.bodyText)
			}
			if tt.bodyPrefix != "" && !strings.HasPrefix(rec.Body.String(), tt.bodyPrefix) {
				t.Fatalf("body=%q want prefix %q", rec.Body.String(), tt.bodyPrefix)
			}
		})
	}

	editBody, editContentType := routerMultipartImageEditBody(t)
	imageEditRoutePath := "/v1/images/edits"
	req := routerRequest(t, http.MethodPost, "/v1/images/edits", &editBody)
	if !strings.HasPrefix(req.URL.Path, imageEditRoutePath) {
		t.Fatalf("path=%q does not exercise route %q", req.URL.Path, imageEditRoutePath)
	}
	req.Header.Set("Content-Type", editContentType)
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)
	assertRouterGoldenJSON(t, rec, http.StatusOK, map[string]any{"created": float64(2)})

	req = routerRequest(t, http.MethodPost, "/v1/models", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)
	assertRouterGoldenJSON(t, rec, http.StatusMethodNotAllowed, map[string]any{"error.type": "invalid_request_error", "error.message": "Method not allowed"})
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("allow=%q want GET", rec.Header().Get("Allow"))
	}
}

func decodeRouterJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json body=%q err=%v", rec.Body.String(), err)
	}
	return body
}

func routerResponseModelIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	rawData, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("data=%#v", body["data"])
	}
	ids := make([]string, 0, len(rawData))
	for _, item := range rawData {
		modelBody, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("model item=%#v", item)
		}
		ids = append(ids, modelBody["id"].(string))
	}
	return ids
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func routerRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	if body == nil {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func routerMultipartImageEditBody(t *testing.T) (bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"model":           "grok-imagine-image-edit",
		"prompt":          "edit prompt",
		"response_format": "b64_json",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="image"; filename="ref.png"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("image-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func assertRouterGoldenJSON(t *testing.T, rec *httptest.ResponseRecorder, status int, want map[string]any) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, status, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type=%q", got)
	}
	body := decodeRouterJSON(t, rec)
	for key, wantValue := range want {
		gotValue, ok := routerGoldenJSONValue(body, key)
		if !ok {
			t.Fatalf("json missing %q in %#v", key, body)
		}
		if gotValue != wantValue {
			t.Fatalf("json[%s]=%#v want %#v body=%s", key, gotValue, wantValue, rec.Body.String())
		}
	}
}

func routerGoldenJSONValue(body map[string]any, dotted string) (any, bool) {
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

func isValidationParam(err error, param string) bool {
	var validation *platform.ValidationError
	return errors.As(err, &validation) && validation.Param == param
}

func resetRouterDepsForTest(t *testing.T) {
	t.Helper()
	oldPools := routerAvailablePools
	oldCompletions := routerCompletions
	oldResponses := routerResponses
	oldGenerate := routerGenerateImages
	oldEdit := routerEditImages
	oldAuthSettings := routerAuthSettings

	routerAvailablePools = func(*http.Request) map[string]struct{} { return map[string]struct{}{} }
	routerAuthSettings = func() auth.AuthSettings { return auth.AuthSettings{} }
	routerCompletions = func(context.Context, chatCompletionOptions) (chatCompletionResult, error) {
		return chatCompletionResult{}, errors.New("router chat completions are not configured")
	}
	routerResponses = func(context.Context, responseOptions) (chatCompletionResult, error) {
		return chatCompletionResult{}, errors.New("router responses are not configured")
	}
	routerGenerateImages = func(context.Context, imageGenerationOptions) (imageResult, error) {
		return imageResult{}, errors.New("router image generation is not configured")
	}
	routerEditImages = func(context.Context, imageEditOptions) (imageResult, error) {
		return imageResult{}, errors.New("router image edit is not configured")
	}

	t.Cleanup(func() {
		routerAvailablePools = oldPools
		routerCompletions = oldCompletions
		routerResponses = oldResponses
		routerGenerateImages = oldGenerate
		routerEditImages = oldEdit
		routerAuthSettings = oldAuthSettings
	})
}
