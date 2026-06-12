package openai

import (
	"context"

	"github.com/dslzl/gork/app/control/model"
	reverseconsole "github.com/dslzl/gork/app/dataplane/reverse/console"
	"github.com/dslzl/gork/app/dataplane/reverse/protocol"
	"github.com/dslzl/gork/app/platform"
)

var consoleStreamChat = func(ctx context.Context, token string, payload map[string]any, timeoutS float64) ([]protocol.ConsoleStreamEvent, error) {
	return reverseconsole.StreamChat(ctx, token, payload, timeoutS)
}

func init() {
	consoleCompletions = ConsoleCompletions
}

func consoleReasoningEffort(emitThink *bool) string {
	if emitThink != nil && !*emitThink {
		return "none"
	}
	return "low"
}

func ConsoleCompletions(ctx context.Context, options chatCompletionOptions) (chatCompletionResult, error) {
	spec, err := model.Resolve(options.Model)
	if err != nil {
		return chatCompletionResult{}, err
	}
	directory := chatDirectoryProvider()
	if directory == nil {
		return chatCompletionResult{}, platform.NewRateLimitError("Account directory not initialised")
	}

	isStream := false
	if options.Stream != nil {
		isStream = *options.Stream
	}
	maxRetries := chatSelectionMaxRetries()
	retryCodes := configuredRetryCodes(chatRetryConfig())
	responseID := chatResponseID()
	timeoutS := chatTimeoutSeconds()
	excluded := []string{}
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		account, ok, err := directory.ReserveChatAccount(ctx, spec, excluded)
		if err != nil {
			return chatCompletionResult{}, err
		}
		if !ok {
			return chatCompletionResult{}, platform.NewRateLimitError("No available accounts for this model tier")
		}

		result, err := runConsoleCompletionAttempt(ctx, options, account, responseID, isStream, timeoutS)
		finishChatAttempt(ctx, directory, account, err == nil, err)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if shouldRetryUpstream(err, retryCodes) && attempt < maxRetries {
			excluded = append(excluded, account.Token)
			continue
		}
		return chatCompletionResult{}, err
	}
	if lastErr != nil {
		return chatCompletionResult{}, lastErr
	}
	return chatCompletionResult{}, platform.NewRateLimitError("No available accounts after retries")
}

func runConsoleCompletionAttempt(ctx context.Context, options chatCompletionOptions, account chatAccount, responseID string, isStream bool, timeoutS float64) (chatCompletionResult, error) {
	upstreamStream := true
	payload := protocol.BuildConsolePayload(protocol.ConsolePayloadOptions{
		Messages:        options.Messages,
		Model:           options.Model,
		Temperature:     options.Temperature,
		TopP:            options.TopP,
		ReasoningEffort: consoleReasoningEffort(options.EmitThink),
		Stream:          &upstreamStream,
	})

	events, err := consoleStreamChat(ctx, account.Token, payload, timeoutS)
	if err != nil {
		return chatCompletionResult{}, err
	}

	adapter := protocol.NewConsoleStreamAdapter()
	frames := []string{}
	for _, event := range events {
		tokens, err := adapter.Feed(event.EventType, event.Data)
		if err != nil {
			return chatCompletionResult{}, err
		}
		if isStream {
			for _, token := range tokens {
				frames = append(frames, formatChatDataFrame(MakeStreamChunk(StreamChunkParams{
					ResponseID: responseID,
					Model:      options.Model,
					Content:    token,
				})))
			}
		}
	}

	usage := consoleUsage(adapter, options.Messages)
	if isStream {
		frames = append(frames, formatChatDataFrame(MakeStreamChunk(StreamChunkParams{
			ResponseID:   responseID,
			Model:        options.Model,
			Content:      "",
			IsFinal:      true,
			Usage:        usage,
			FinishReason: "stop",
		})), "data: [DONE]\n\n")
		return chatCompletionResult{IsStream: true, StreamFrames: frames}, nil
	}

	return chatCompletionResult{Response: MakeChatResponse(ChatResponseParams{
		Model:         options.Model,
		Content:       adapter.FullText(),
		PromptContent: options.Messages,
		ResponseID:    responseID,
		Usage:         usage,
	})}, nil
}

func consoleUsage(adapter *protocol.ConsoleStreamAdapter, messages []map[string]any) map[string]any {
	if adapter.Usage != nil {
		inputTokens := intFromAny(adapter.Usage["input_tokens"])
		outputTokens := intFromAny(adapter.Usage["output_tokens"])
		return BuildUsage(inputTokens, outputTokens)
	}
	return BuildUsage(platform.EstimatePromptTokens(messages, platform.PromptOverhead), platform.EstimateTokens(adapter.FullText()))
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}
