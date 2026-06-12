package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dslzl/gork/app/platform"
	"github.com/dslzl/gork/app/platform/auth"
)

func TestAnthropicRouterMessagesDefaultsAndJSONResponse(t *testing.T) {
	resetAnthropicRouterDepsForTest(t)
	var seen MessagesOptions
	anthropicRouterMessages = func(_ context.Context, options MessagesOptions) (MessagesResult, error) {
		seen = options
		return MessagesResult{Response: map[string]any{"id": "msg_1", "type": "message"}}, nil
	}
	body := `{"model":"grok-4.20-auto","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`
	rec := postAnthropicMessages(body, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if seen.Model != "grok-4.20-auto" || seen.Stream != true || seen.EmitThink != false {
		t.Fatalf("seen=%#v", seen)
	}
	if seen.Temperature != 0.8 || seen.TopP != 0.95 {
		t.Fatalf("defaults temp/top_p=%v/%v", seen.Temperature, seen.TopP)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content-type=%q", rec.Header().Get("Content-Type"))
	}
}

func TestAnthropicRouterWritesStreamHeaders(t *testing.T) {
	resetAnthropicRouterDepsForTest(t)
	anthropicRouterMessages = func(context.Context, MessagesOptions) (MessagesResult, error) {
		return MessagesResult{IsStream: true, StreamFrames: []string{"event: message_start\n\n", "data: [DONE]\n\n"}}, nil
	}
	rec := postAnthropicMessages(`{"model":"grok-4.20-auto","messages":[{"role":"user","content":"hi"}],"stream":true}`, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type=%q", rec.Header().Get("Content-Type"))
	}
	for key, want := range map[string]string{"Cache-Control": "no-cache", "Connection": "keep-alive", "X-Accel-Buffering": "no"} {
		if rec.Header().Get(key) != want {
			t.Fatalf("%s=%q", key, rec.Header().Get(key))
		}
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("stream body=%s", rec.Body.String())
	}
}

func TestAnthropicRouterForwardsPayloadAndIgnoresTopLevelExtra(t *testing.T) {
	resetAnthropicRouterDepsForTest(t)
	var seen MessagesOptions
	anthropicRouterMessages = func(_ context.Context, options MessagesOptions) (MessagesResult, error) {
		seen = options
		return MessagesResult{Response: map[string]any{"id": "msg_payload"}}, nil
	}
	body := `{
		"model":"grok-4.20-auto",
		"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}],"ignored_nested":true}],
		"system":[{"type":"text","text":"sys"}],
		"max_tokens":128,
		"stream":false,
		"temperature":0.2,
		"top_p":0.7,
		"tools":[{"name":"lookup","description":"Lookup","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"tool","name":"lookup"},
		"thinking":{"type":"enabled"},
		"ignored_top_level":true
	}`
	rec := postAnthropicMessages(body, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if seen.Model != "grok-4.20-auto" || seen.Stream || !seen.EmitThink {
		t.Fatalf("seen route flags=%#v", seen)
	}
	if seen.Temperature != 0.2 || seen.TopP != 0.7 {
		t.Fatalf("temperature/top_p=%v/%v", seen.Temperature, seen.TopP)
	}
	if len(seen.Messages) != 1 || seen.Messages[0]["ignored_nested"] != true {
		t.Fatalf("messages=%#v", seen.Messages)
	}
	if system, ok := seen.System.([]any); !ok || len(system) != 1 {
		t.Fatalf("system=%#v", seen.System)
	}
	if len(seen.Tools) != 1 || seen.Tools[0]["name"] != "lookup" {
		t.Fatalf("tools=%#v", seen.Tools)
	}
	choice, ok := seen.ToolChoice.(map[string]any)
	if !ok || choice["name"] != "lookup" {
		t.Fatalf("tool_choice=%#v", seen.ToolChoice)
	}
}

func TestAnthropicRouterValidatesModelAndMessages(t *testing.T) {
	resetAnthropicRouterDepsForTest(t)
	rec := postAnthropicMessages(`{"model":"missing","messages":[{"role":"user","content":"hi"}]}`, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "model_not_found") {
		t.Fatalf("model validation status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = postAnthropicMessages(`{"model":"grok-4.20-auto","messages":[]}`, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "messages cannot be empty") {
		t.Fatalf("messages validation status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnthropicRouterRejectsInvalidJSON(t *testing.T) {
	resetAnthropicRouterDepsForTest(t)
	rec := postAnthropicMessages(`{"model":"grok-4.20-auto","messages":[`, "")

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_json") {
		t.Fatalf("invalid json status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnthropicRouterRejectsUnsupportedMethod(t *testing.T) {
	resetAnthropicRouterDepsForTest(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Body.String(), "Method not allowed") {
		t.Fatalf("method status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnthropicRouterRequiresAPIKeyWhenConfigured(t *testing.T) {
	resetAnthropicRouterDepsForTest(t)
	anthropicRouterAuthSettings = func() auth.AuthSettings { return auth.AuthSettings{APIKey: "secret"} }
	rec := postAnthropicMessages(`{"model":"grok-4.20-auto","messages":[{"role":"user","content":"hi"}]}`, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = postAnthropicMessages(`{"model":"grok-4.20-auto","messages":[{"role":"user","content":"hi"}]}`, "Bearer secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("valid key status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnthropicRouterSerializesTypedUpstreamError(t *testing.T) {
	resetAnthropicRouterDepsForTest(t)
	anthropicRouterMessages = func(context.Context, MessagesOptions) (MessagesResult, error) {
		return MessagesResult{}, platform.NewUpstreamError("proxy failed", http.StatusBadGateway, "")
	}

	rec := postAnthropicMessages(`{"model":"grok-4.20-auto","messages":[{"role":"user","content":"hi"}]}`, "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	errBody := decodeAnthropicRouterBody(t, rec)["error"].(map[string]any)
	if errBody["type"] != string(platform.ErrorKindUpstream) || errBody["code"] != "upstream_error" {
		t.Fatalf("error body=%#v", errBody)
	}
}

func TestAnthropicRouterRouteGoldenStatusHeadersAndShapes(t *testing.T) {
	resetAnthropicRouterDepsForTest(t)
	anthropicRouterMessages = func(_ context.Context, options MessagesOptions) (MessagesResult, error) {
		if options.Stream {
			return MessagesResult{IsStream: true, StreamFrames: []string{"event: message_start\n\n", "data: [DONE]\n\n"}}, nil
		}
		return MessagesResult{Response: map[string]any{"id": "msg_golden", "type": "message", "role": "assistant"}}, nil
	}

	routePath := "/v1/messages"
	for _, tt := range []struct {
		name        string
		method      string
		body        string
		auth        string
		status      int
		contentType string
		allow       string
		json        map[string]any
		bodyHas     string
	}{
		{name: "non stream", method: http.MethodPost, body: `{"model":"grok-4.20-auto","messages":[{"role":"user","content":"hi"}],"stream":false}`, status: http.StatusOK, contentType: "application/json", json: map[string]any{"id": "msg_golden", "type": "message", "role": "assistant"}},
		{name: "stream", method: http.MethodPost, body: `{"model":"grok-4.20-auto","messages":[{"role":"user","content":"hi"}],"stream":true}`, status: http.StatusOK, contentType: "text/event-stream", bodyHas: "data: [DONE]"},
		{name: "method guard", method: http.MethodGet, status: http.StatusMethodNotAllowed, contentType: "application/json", allow: http.MethodPost, json: map[string]any{"error.type": "invalid_request_error", "error.message": "Method not allowed"}},
		{name: "auth error", method: http.MethodPost, body: `{"model":"grok-4.20-auto","messages":[{"role":"user","content":"hi"}]}`, auth: "require-secret", status: http.StatusUnauthorized, contentType: "application/json", json: map[string]any{"error.type": "authentication_error"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.auth == "require-secret" {
				anthropicRouterAuthSettings = func() auth.AuthSettings { return auth.AuthSettings{APIKey: "secret"} }
				t.Cleanup(func() {
					anthropicRouterAuthSettings = func() auth.AuthSettings { return auth.AuthSettings{} }
				})
			}
			req := httptest.NewRequest(tt.method, routePath, bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			NewRouter().ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.status, rec.Body.String())
			}
			if tt.allow != "" && rec.Header().Get("Allow") != tt.allow {
				t.Fatalf("allow=%q want=%q", rec.Header().Get("Allow"), tt.allow)
			}
			if tt.contentType != "" && !strings.Contains(rec.Header().Get("Content-Type"), tt.contentType) {
				t.Fatalf("content-type=%q want %q", rec.Header().Get("Content-Type"), tt.contentType)
			}
			if tt.bodyHas != "" && !strings.Contains(rec.Body.String(), tt.bodyHas) {
				t.Fatalf("body=%q want contains %q", rec.Body.String(), tt.bodyHas)
			}
			if tt.json != nil {
				assertAnthropicRouterGoldenJSON(t, rec, tt.json)
			}
		})
	}
}

func postAnthropicMessages(body string, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)
	return rec
}

func assertAnthropicRouterGoldenJSON(t *testing.T, rec *httptest.ResponseRecorder, want map[string]any) {
	t.Helper()
	body := decodeAnthropicRouterBody(t, rec)
	for key, wantValue := range want {
		gotValue, ok := anthropicRouterGoldenJSONValue(body, key)
		if !ok {
			t.Fatalf("json missing %q in %#v", key, body)
		}
		if gotValue != wantValue {
			t.Fatalf("json[%s]=%#v want %#v body=%s", key, gotValue, wantValue, rec.Body.String())
		}
	}
}

func anthropicRouterGoldenJSONValue(body map[string]any, dotted string) (any, bool) {
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

func resetAnthropicRouterDepsForTest(t *testing.T) {
	t.Helper()
	oldMessages := anthropicRouterMessages
	oldBoolConfig := anthropicRouterBoolConfig
	oldAuth := anthropicRouterAuthSettings
	anthropicRouterMessages = func(context.Context, MessagesOptions) (MessagesResult, error) {
		return MessagesResult{Response: map[string]any{"id": "msg_default"}}, nil
	}
	anthropicRouterBoolConfig = func(key string, defaultValue bool) bool { return defaultValue }
	anthropicRouterAuthSettings = func() auth.AuthSettings { return auth.AuthSettings{} }
	t.Cleanup(func() {
		anthropicRouterMessages = oldMessages
		anthropicRouterBoolConfig = oldBoolConfig
		anthropicRouterAuthSettings = oldAuth
	})
}

func decodeAnthropicRouterBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}
