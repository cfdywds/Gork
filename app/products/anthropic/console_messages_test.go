package anthropic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	controlaccount "github.com/dslzl/gork/app/control/account"
	"github.com/dslzl/gork/app/control/model"
	dataaccount "github.com/dslzl/gork/app/dataplane/account"
	"github.com/dslzl/gork/app/dataplane/reverse/protocol"
	"github.com/dslzl/gork/app/platform"
	platformconfig "github.com/dslzl/gork/app/platform/config"
)

func TestConsoleMessagesNonStreamBuildsAnthropicMessage(t *testing.T) {
	resetConsoleMessagesDepsForTest(t)
	directory := &fakeConsoleMessagesDirectory{accounts: []consoleMessagesAccount{{Token: "tok", ModeID: model.ModeConsole}}}
	consoleMessagesDirectoryProvider = func() consoleMessagesDirectory { return directory }
	consoleMessagesStream = func(context.Context, string, map[string]any, float64) ([]protocol.ConsoleStreamEvent, error) {
		return []protocol.ConsoleStreamEvent{
			{EventType: "response.output_text.delta", Data: `{"delta":"hello"}`},
			{EventType: "response.output_text.delta", Data: `{"delta":" world"}`},
			{EventType: "response.completed", Data: `{"response":{"usage":{"input_tokens":3,"output_tokens":2}}}`},
		}, nil
	}

	result, err := ConsoleMessages(context.Background(), ConsoleMessagesOptions{
		Model: "grok-4.3-console",
		Messages: []map[string]any{
			{"role": "user", "content": "hello"},
		},
		Stream:      false,
		EmitThink:   true,
		Temperature: 0.2,
		TopP:        0.9,
		MessageID:   "msg_test",
	})
	if err != nil {
		t.Fatalf("ConsoleMessages err=%v", err)
	}
	if result.IsStream {
		t.Fatalf("result unexpectedly stream")
	}
	body := result.Response
	if body["id"] != "msg_test" || body["type"] != "message" || body["role"] != "assistant" {
		t.Fatalf("body=%#v", body)
	}
	content := body["content"].([]map[string]any)
	if content[0]["type"] != "text" || content[0]["text"] != "hello world" {
		t.Fatalf("content=%#v", content)
	}
	usage := body["usage"].(map[string]any)
	if usage["input_tokens"] != 3 || usage["output_tokens"] != 2 {
		t.Fatalf("usage=%#v", usage)
	}
	if len(directory.released) != 1 || len(directory.feedback) != 1 || directory.feedback[0].Kind != consoleMessagesFeedbackSuccess {
		t.Fatalf("directory released=%#v feedback=%#v", directory.released, directory.feedback)
	}
}

func TestConsoleMessagesStreamBuildsAnthropicSSE(t *testing.T) {
	resetConsoleMessagesDepsForTest(t)
	directory := &fakeConsoleMessagesDirectory{accounts: []consoleMessagesAccount{{Token: "tok", ModeID: model.ModeConsole}}}
	consoleMessagesDirectoryProvider = func() consoleMessagesDirectory { return directory }
	consoleMessagesStream = func(context.Context, string, map[string]any, float64) ([]protocol.ConsoleStreamEvent, error) {
		return []protocol.ConsoleStreamEvent{
			{EventType: "response.output_text.delta", Data: `{"delta":"one"}`},
			{EventType: "response.completed", Data: `{"response":{"usage":{"output_tokens":7}}}`},
		}, nil
	}

	result, err := ConsoleMessages(context.Background(), ConsoleMessagesOptions{
		Model:     "grok-4.3-console",
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
		Stream:    true,
		MessageID: "msg_stream",
	})
	if err != nil {
		t.Fatalf("ConsoleMessages stream err=%v", err)
	}
	if !result.IsStream {
		t.Fatalf("result not stream")
	}
	joined := strings.Join(result.StreamFrames, "")
	for _, want := range []string{
		"event: message_start",
		`"id":"msg_stream"`,
		"event: ping",
		"event: content_block_start",
		"event: content_block_delta",
		`"text":"one"`,
		"event: content_block_stop",
		"event: message_delta",
		`"output_tokens":7`,
		"event: message_stop",
		"data: [DONE]",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stream missing %q in %s", want, joined)
		}
	}
}

func TestConsoleMessagesRetriesConfiguredStatusAndExcludesToken(t *testing.T) {
	resetConsoleMessagesDepsForTest(t)
	directory := &fakeConsoleMessagesDirectory{accounts: []consoleMessagesAccount{
		{Token: "tok-first", ModeID: model.ModeConsole},
		{Token: "tok-second", ModeID: model.ModeConsole},
	}}
	consoleMessagesDirectoryProvider = func() consoleMessagesDirectory { return directory }
	consoleMessagesMaxRetries = func() int { return 1 }
	consoleMessagesRetryCodes = func() map[int]struct{} { return map[int]struct{}{418: {}} }
	calls := 0
	consoleMessagesStream = func(context.Context, string, map[string]any, float64) ([]protocol.ConsoleStreamEvent, error) {
		calls++
		if calls == 1 {
			return nil, platform.NewUpstreamError("retry me", 418, "")
		}
		return []protocol.ConsoleStreamEvent{
			{EventType: "response.output_text.delta", Data: `{"delta":"ok"}`},
			{EventType: "response.completed", Data: `{"response":{"usage":{"input_tokens":1,"output_tokens":1}}}`},
		}, nil
	}

	result, err := ConsoleMessages(context.Background(), ConsoleMessagesOptions{
		Model:     "grok-4.3-console",
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
		MessageID: "msg_retry",
	})
	if err != nil || result.Response["id"] != "msg_retry" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if calls != 2 || len(directory.released) != 2 || len(directory.excluded) != 2 {
		t.Fatalf("calls=%d released=%#v excluded=%#v", calls, directory.released, directory.excluded)
	}
	if len(directory.excluded[1]) != 1 || directory.excluded[1][0] != "tok-first" {
		t.Fatalf("second reserve excluded=%#v", directory.excluded)
	}
}

func TestConsoleMessagesUsesConfiguredTimeoutAndFeedbackMapping(t *testing.T) {
	resetConsoleMessagesDepsForTest(t)
	directory := &fakeConsoleMessagesDirectory{accounts: []consoleMessagesAccount{{Token: "tok", ModeID: model.ModeConsole}}}
	consoleMessagesDirectoryProvider = func() consoleMessagesDirectory { return directory }
	consoleMessagesTimeoutSeconds = func() float64 { return 33.5 }
	seenTimeout := 0.0
	consoleMessagesStream = func(_ context.Context, _ string, _ map[string]any, timeoutS float64) ([]protocol.ConsoleStreamEvent, error) {
		seenTimeout = timeoutS
		return nil, platform.NewUpstreamError("forbidden", 403, "token expired")
	}

	_, err := ConsoleMessages(context.Background(), ConsoleMessagesOptions{
		Model:     "grok-4.3-console",
		Messages:  []map[string]any{{"role": "user", "content": "hello"}},
		MessageID: "msg_fail",
	})
	if err == nil {
		t.Fatalf("expected upstream error")
	}
	if seenTimeout != 33.5 {
		t.Fatalf("timeout = %v", seenTimeout)
	}
	if len(directory.feedback) != 1 || directory.feedback[0].Kind != consoleMessagesFeedbackUnauthorized {
		t.Fatalf("feedback=%#v", directory.feedback)
	}
}

func TestConsoleMessagesProductionDefaultsUseConfigAndRefreshRuntime(t *testing.T) {
	oldConfig := platformconfig.GlobalConfig
	oldStrategy := dataaccount.CurrentStrategy()
	oldRefresh := controlaccount.GetRefreshService()
	t.Cleanup(func() {
		platformconfig.GlobalConfig = oldConfig
		_ = dataaccount.SetStrategy(oldStrategy)
		controlaccount.SetRefreshService(oldRefresh)
	})
	defaults := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaults, []byte("[chat]\ntimeout = 120.0\n\n[retry]\non_codes = \"429,401,503\"\n"), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(consoleMessagesConfigBackend{data: map[string]any{
		"chat":  map[string]any{"timeout": 44.5},
		"retry": map[string]any{"on_codes": []any{"418", 429}},
	}}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaults); err != nil {
		t.Fatalf("load config: %v", err)
	}

	if got := defaultConsoleMessagesTimeoutSeconds(); got != 44.5 {
		t.Fatalf("timeout = %v", got)
	}
	retryCodes := defaultConsoleMessagesRetryCodes()
	if _, ok := retryCodes[418]; !ok {
		t.Fatalf("retry codes missing 418: %#v", retryCodes)
	}

	refresh := &fakeConsoleMessagesRefreshService{}
	controlaccount.SetRefreshService(refresh)
	if err := dataaccount.SetStrategy("random"); err != nil {
		t.Fatalf("set random strategy: %v", err)
	}
	defaultConsoleMessagesQuotaSync(context.Background(), "tok", 5)
	if refresh.refreshCalls != 0 {
		t.Fatalf("random strategy quota sync calls = %d", refresh.refreshCalls)
	}
	if err := dataaccount.SetStrategy("quota"); err != nil {
		t.Fatalf("set quota strategy: %v", err)
	}
	defaultConsoleMessagesQuotaSync(context.Background(), "tok", 5)
	defaultConsoleMessagesFailSync(context.Background(), "tok", 5, errors.New("boom"))
	if refresh.refreshCalls != 1 || refresh.failureCalls != 1 {
		t.Fatalf("refreshCalls=%d failureCalls=%d", refresh.refreshCalls, refresh.failureCalls)
	}
}

func resetConsoleMessagesDepsForTest(t *testing.T) {
	t.Helper()
	oldDirectory := consoleMessagesDirectoryProvider
	oldStream := consoleMessagesStream
	oldMaxRetries := consoleMessagesMaxRetries
	oldRetryCodes := consoleMessagesRetryCodes
	oldTimeout := consoleMessagesTimeoutSeconds
	consoleMessagesDirectoryProvider = func() consoleMessagesDirectory { return nil }
	consoleMessagesStream = func(context.Context, string, map[string]any, float64) ([]protocol.ConsoleStreamEvent, error) {
		return nil, errConsoleMessagesStreamNotConfigured
	}
	consoleMessagesMaxRetries = func() int { return 0 }
	consoleMessagesRetryCodes = func() map[int]struct{} { return map[int]struct{}{429: {}, 401: {}, 503: {}} }
	consoleMessagesTimeoutSeconds = func() float64 { return 120.0 }
	t.Cleanup(func() {
		consoleMessagesDirectoryProvider = oldDirectory
		consoleMessagesStream = oldStream
		consoleMessagesMaxRetries = oldMaxRetries
		consoleMessagesRetryCodes = oldRetryCodes
		consoleMessagesTimeoutSeconds = oldTimeout
	})
}

type fakeConsoleMessagesDirectory struct {
	accounts []consoleMessagesAccount
	released []consoleMessagesAccount
	feedback []consoleMessagesFeedback
	excluded [][]string
}

func (d *fakeConsoleMessagesDirectory) ReserveConsoleMessagesAccount(_ context.Context, _ model.ModelSpec, excluded []string) (consoleMessagesAccount, bool, error) {
	d.excluded = append(d.excluded, append([]string(nil), excluded...))
	if len(d.accounts) == 0 {
		return consoleMessagesAccount{}, false, nil
	}
	account := d.accounts[0]
	d.accounts = d.accounts[1:]
	return account, true, nil
}

func (d *fakeConsoleMessagesDirectory) ReleaseConsoleMessagesAccount(_ context.Context, account consoleMessagesAccount) error {
	d.released = append(d.released, account)
	return nil
}

func (d *fakeConsoleMessagesDirectory) FeedbackConsoleMessagesAccount(_ context.Context, feedback consoleMessagesFeedback) error {
	d.feedback = append(d.feedback, feedback)
	return nil
}

type fakeConsoleMessagesRefreshService struct {
	refreshCalls int
	failureCalls int
}

func (s *fakeConsoleMessagesRefreshService) RefreshScheduled(context.Context, *string) (controlaccount.RefreshResult, error) {
	return controlaccount.RefreshResult{}, nil
}

func (s *fakeConsoleMessagesRefreshService) RefreshCallAsync(context.Context, string, int) error {
	s.refreshCalls++
	return nil
}

func (s *fakeConsoleMessagesRefreshService) RecordFailureAsync(context.Context, string, int, error) error {
	s.failureCalls++
	return nil
}

type consoleMessagesConfigBackend struct {
	data map[string]any
}

func (b consoleMessagesConfigBackend) Load(context.Context) (map[string]any, error) {
	return b.data, nil
}

func (b consoleMessagesConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return nil
}

func (b consoleMessagesConfigBackend) Clear(context.Context) error {
	return nil
}

func (b consoleMessagesConfigBackend) Version(context.Context) (any, error) {
	return "test", nil
}

func (b consoleMessagesConfigBackend) Close(context.Context) error {
	return nil
}
