package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	proxycontrol "github.com/dslzl/gork/app/control/proxy"
	"github.com/dslzl/gork/app/dataplane/reverse/protocol"
	reverseruntime "github.com/dslzl/gork/app/dataplane/reverse/runtime"
	"github.com/dslzl/gork/app/dataplane/reverse/transport"
	"github.com/dslzl/gork/app/platform"
)

func streamChat(ctx context.Context, options chatStreamOptions) ([]string, error) {
	attachments, err := prepareFileAttachments(ctx, options.Token, options.Files)
	if err != nil {
		return nil, err
	}

	payload := protocol.BuildChatPayload(protocol.ChatPayloadOptions{
		Message:             options.Message,
		ModeID:              options.ModeID,
		FileAttachments:     attachments,
		ToolOverrides:       options.ToolOverrides,
		ModelConfigOverride: options.ModelConfigOverride,
		RequestOverrides:    options.RequestOverrides,
	})
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	response, err := streamPost(ctx, chatStreamRequest{
		Token: options.Token,
		Headers: map[string]string{
			"authorization": "Bearer " + options.Token,
			"content-type":  "application/json",
			"origin":        "https://grok.com",
			"referer":       "https://grok.com/",
		},
		PayloadBytes:   payloadBytes,
		TimeoutSeconds: options.TimeoutSeconds,
	})
	if err != nil {
		return nil, transportUpstreamError(err, "Chat transport failed")
	}
	if response == nil {
		return nil, platform.NewUpstreamError("Chat upstream returned 502", 502, "")
	}
	if response.StatusCode != 200 {
		body := response.Body
		if len(body) > 400 {
			body = body[:400]
		}
		return nil, platform.NewUpstreamError(fmt.Sprintf("Chat upstream returned %d", response.StatusCode), response.StatusCode, body)
	}
	return append([]string{}, response.Lines...), nil
}

func defaultChatStreamPost(ctx context.Context, request chatStreamRequest) (*chatStreamResponse, error) {
	proxyDirectory, err := proxycontrol.GetProxyDirectory(ctx)
	if err != nil {
		return nil, err
	}
	lease, err := proxyDirectory.Acquire(ctx, proxycontrol.AcquireOptions{
		Scope:           proxycontrol.ProxyScopeApp,
		Kind:            proxycontrol.RequestKindHTTP,
		ClearanceOrigin: reverseruntime.Base,
	})
	if err != nil {
		return nil, err
	}

	stream, err := transport.PostStream(ctx, reverseruntime.Chat, request.Token, request.PayloadBytes, transport.HTTPOptions{
		Timeout:     secondsDuration(request.TimeoutSeconds, 120*time.Second),
		ContentType: request.Headers["content-type"],
		Origin:      request.Headers["origin"],
		Referer:     request.Headers["referer"],
		Lease:       &lease,
	})
	if err != nil {
		feedbackChatProxyError(ctx, proxyDirectory, lease, err)
		return nil, err
	}
	defer stream.Close()

	lines := []string{}
	for {
		line, ok, err := stream.Next()
		if err != nil {
			if err := proxyDirectory.Feedback(ctx, lease, proxycontrol.NewProxyFeedback(proxycontrol.ProxyFeedbackTransportError)); err != nil {
				slog.Warn("proxy feedback failed after stream error", "error", err)
			}
			return nil, err
		}
		if !ok {
			status := 200
			if err := proxyDirectory.Feedback(ctx, lease, proxycontrol.ProxyFeedback{Kind: proxycontrol.ProxyFeedbackSuccess, StatusCode: &status}); err != nil {
				slog.Warn("proxy feedback failed after stream success", "error", err)
			}
			return &chatStreamResponse{StatusCode: 200, Lines: lines}, nil
		}
		lines = append(lines, line)
	}
}

func feedbackChatProxyError(ctx context.Context, directory *proxycontrol.ProxyDirectory, lease proxycontrol.ProxyLease, err error) {
	var upstream *platform.UpstreamError
	if errors.As(err, &upstream) {
		if err := directory.Feedback(ctx, lease, transport.UpstreamFeedback(upstream)); err != nil {
			slog.Warn("proxy feedback failed for upstream error", "error", err)
		}
		return
	}
	if err := directory.Feedback(ctx, lease, proxycontrol.NewProxyFeedback(proxycontrol.ProxyFeedbackTransportError)); err != nil {
		slog.Warn("proxy feedback failed for transport error", "error", err)
	}
}
