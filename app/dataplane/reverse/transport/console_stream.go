package transport

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	proxyadapters "github.com/dslzl/gork/app/dataplane/proxy/adapters"
	"github.com/dslzl/gork/app/dataplane/reverse/protocol"
	reverseruntime "github.com/dslzl/gork/app/dataplane/reverse/runtime"
)

const consoleStreamPostAttempts = 3

type ConsoleStreamPoster struct {
	Client HTTPClient
}

func (p ConsoleStreamPoster) PostConsoleStream(ctx context.Context, request protocol.ConsoleStreamRequest) (protocol.ConsoleStreamResponse, error) {
	payload, err := json.Marshal(request.Payload)
	if err != nil {
		return protocol.ConsoleStreamResponse{}, err
	}

	timeout := defaultPostStreamTimeout
	if request.TimeoutS > 0 {
		timeout = time.Duration(request.TimeoutS * float64(time.Second))
	}

	lease := request.Lease
	client := p.Client
	if client == nil {
		client = netHTTPClient{}
	}
	httpRequest := HTTPRequest{
		URL:     reverseruntime.ConsoleResponses,
		Token:   request.Token,
		Payload: payload,
		Lease:   &lease,
		Timeout: timeout,
		Headers: proxyadapters.BuildConsoleHeaders(request.Token, proxyadapters.ConsoleHeaderOptions{
			Lease: &lease,
		}),
		Stream: true,
	}
	for attempt := 0; attempt < consoleStreamPostAttempts; attempt++ {
		response, err := client.Post(ctx, httpRequest)
		if err != nil {
			if retryConsoleStreamTransportError(ctx, err) && attempt < consoleStreamPostAttempts-1 {
				sleepConsoleStreamRetry(attempt)
				continue
			}
			return protocol.ConsoleStreamResponse{}, err
		}

		result, err := readConsoleStreamResponse(response)
		if err == nil {
			return result, nil
		}
		if !retryConsoleStreamTransportError(ctx, err) || attempt == consoleStreamPostAttempts-1 {
			return protocol.ConsoleStreamResponse{}, err
		}
		sleepConsoleStreamRetry(attempt)
	}
	return protocol.ConsoleStreamResponse{}, err
}

func readConsoleStreamResponse(response HTTPResponse) (protocol.ConsoleStreamResponse, error) {
	if response.StatusCode != 200 {
		closeHTTPResponse(response)
		return protocol.ConsoleStreamResponse{StatusCode: response.StatusCode, Body: string(response.Body)}, nil
	}
	stream := newHTTPLineStream(response)
	defer stream.Close()
	lines := []string{}
	for {
		line, ok, err := stream.Next()
		if err != nil {
			return protocol.ConsoleStreamResponse{}, err
		}
		if !ok {
			return protocol.ConsoleStreamResponse{StatusCode: response.StatusCode, Lines: lines}, nil
		}
		lines = append(lines, line)
	}
}

func sleepConsoleStreamRetry(attempt int) {
	time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
}

func retryConsoleStreamTransportError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"tls handshake timeout",
		"i/o timeout",
		"eof",
		"connection reset",
		"connection refused",
		"malformed http response",
		"server closed idle connection",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}
