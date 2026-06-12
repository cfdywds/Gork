package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	controlproxy "github.com/dslzl/gork/app/control/proxy"
	proxyadapters "github.com/dslzl/gork/app/dataplane/proxy/adapters"
	platform "github.com/dslzl/gork/app/platform"
)

const (
	defaultPostStreamTimeout = 120 * time.Second
	defaultJSONTimeout       = 30 * time.Second
	defaultBytesTimeout      = 120 * time.Second
	defaultLineBufferSize    = 64 * 1024
	maxLineBufferSize        = 16 * 1024 * 1024
)

type HTTPClient interface {
	Post(ctx context.Context, request HTTPRequest) (HTTPResponse, error)
	Get(ctx context.Context, request HTTPRequest) (HTTPResponse, error)
	Delete(ctx context.Context, request HTTPRequest) (HTTPResponse, error)
}

type HTTPOptions struct {
	Client       HTTPClient
	Lease        *controlproxy.ProxyLease
	Timeout      time.Duration
	ContentType  string
	Origin       string
	Referer      string
	Params       map[string]any
	ExtraHeaders map[string]string
}

type HTTPRequest struct {
	URL            string
	Token          string
	Payload        []byte
	Params         map[string]any
	Lease          *controlproxy.ProxyLease
	Timeout        time.Duration
	ContentType    string
	Origin         string
	Referer        string
	Headers        map[string]string
	Stream         bool
	AllowRedirects bool
}

type HTTPResponse struct {
	StatusCode int
	Body       []byte
	Headers    map[string]string
	Stream     io.ReadCloser
}

type HTTPLineStream struct {
	scanner *bufio.Scanner
	closer  io.Closer
}

func PostStream(ctx context.Context, rawURL string, token string, payload []byte, options ...HTTPOptions) (*HTTPLineStream, error) {
	option := httpOptions(defaultPostStreamTimeout, options...)
	request := newHTTPRequest(rawURL, token, option)
	request.Payload = payload
	request.Stream = true
	response, err := option.Client.Post(ctx, request)
	if err != nil {
		return nil, httpTransportError(err)
	}
	if response.StatusCode != 200 {
		closeHTTPResponse(response)
		return nil, httpStatusError(response, 400)
	}
	return newHTTPLineStream(response), nil
}

func PostJSON(ctx context.Context, rawURL string, token string, payload []byte, options ...HTTPOptions) (map[string]any, error) {
	option := httpOptions(defaultJSONTimeout, options...)
	request := newHTTPRequest(rawURL, token, option)
	request.Payload = payload
	response, err := option.Client.Post(ctx, request)
	if err != nil {
		return nil, httpTransportError(err)
	}
	if response.StatusCode != 200 && response.StatusCode != 201 && response.StatusCode != 204 {
		return nil, httpStatusError(response, 400)
	}
	return decodeHTTPJSON(response.Body, true)
}

func GetJSON(ctx context.Context, rawURL string, token string, options ...HTTPOptions) (map[string]any, error) {
	option := httpOptions(defaultJSONTimeout, options...)
	request := newHTTPRequest(rawURL, token, option)
	response, err := option.Client.Get(ctx, request)
	if err != nil {
		return nil, httpTransportError(err)
	}
	if response.StatusCode != 200 {
		return nil, httpStatusError(response, 400)
	}
	return decodeHTTPJSON(response.Body, false)
}

func DeleteJSON(ctx context.Context, rawURL string, token string, options ...HTTPOptions) (map[string]any, error) {
	option := httpOptions(defaultJSONTimeout, options...)
	request := newHTTPRequest(rawURL, token, option)
	response, err := option.Client.Delete(ctx, request)
	if err != nil {
		return nil, httpTransportError(err)
	}
	if response.StatusCode != 200 && response.StatusCode != 204 {
		return nil, httpStatusError(response, 400)
	}
	return decodeHTTPJSON(response.Body, true)
}

func GetBytesStream(ctx context.Context, rawURL string, token string, options ...HTTPOptions) (io.ReadCloser, error) {
	option := httpOptions(defaultBytesTimeout, options...)
	if option.Origin == "" {
		option.Origin = "https://assets.grok.com"
	}
	if option.Referer == "" {
		option.Referer = "https://grok.com/"
	}
	request := newHTTPRequest(rawURL, token, option)
	for key, value := range option.ExtraHeaders {
		request.Headers[key] = value
	}
	if request.Headers["Sec-Fetch-Mode"] == "navigate" {
		delete(request.Headers, "Content-Type")
		delete(request.Headers, "Origin")
	}
	request.Stream = true
	request.AllowRedirects = true
	response, err := option.Client.Get(ctx, request)
	if err != nil {
		return nil, httpTransportError(err)
	}
	if response.StatusCode != 200 {
		closeHTTPResponse(response)
		return nil, httpStatusError(response, 400)
	}
	if response.Stream != nil {
		return response.Stream, nil
	}
	return io.NopCloser(bytes.NewReader(response.Body)), nil
}

func httpOptions(defaultTimeout time.Duration, options ...HTTPOptions) HTTPOptions {
	option := HTTPOptions{Client: netHTTPClient{}, Timeout: defaultTimeout}
	if len(options) > 0 {
		option = options[0]
	}
	if option.Client == nil {
		option.Client = netHTTPClient{}
	}
	if option.Timeout <= 0 {
		option.Timeout = defaultTimeout
	}
	return option
}

func newHTTPRequest(rawURL string, token string, option HTTPOptions) HTTPRequest {
	headers := buildHTTPTransportHeaders(token, option)
	return HTTPRequest{
		URL:         rawURL,
		Token:       token,
		Params:      option.Params,
		Lease:       option.Lease,
		Timeout:     option.Timeout,
		ContentType: option.ContentType,
		Origin:      option.Origin,
		Referer:     option.Referer,
		Headers:     headers,
	}
}

func buildHTTPTransportHeaders(token string, option HTTPOptions) map[string]string {
	contentType := option.ContentType
	headers := proxyadapters.BuildHTTPHeaders(token, proxyadapters.HTTPHeaderOptions{
		ContentType: &contentType,
		Origin:      option.Origin,
		Referer:     option.Referer,
		Lease:       option.Lease,
	})
	return headers
}

func newHTTPLineStream(response HTTPResponse) *HTTPLineStream {
	reader := response.Stream
	if reader == nil {
		reader = io.NopCloser(bytes.NewReader(response.Body))
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, defaultLineBufferSize), maxLineBufferSize)
	return &HTTPLineStream{scanner: scanner, closer: reader}
}

func (s *HTTPLineStream) Next() (string, bool, error) {
	if s.scanner.Scan() {
		return s.scanner.Text(), true, nil
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, err
	}
	return "", false, nil
}

func (s *HTTPLineStream) Close() error {
	if s == nil || s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

func decodeHTTPJSON(body []byte, allowEmpty bool) (map[string]any, error) {
	if allowEmpty && len(strings.TrimSpace(string(body))) == 0 {
		return map[string]any{}, nil
	}
	result := map[string]any{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func httpStatusError(response HTTPResponse, limit int) *platform.UpstreamError {
	bodyText := truncateString(string(response.Body), limit)
	return platform.NewUpstreamError(fmt.Sprintf("Upstream returned %d", response.StatusCode), response.StatusCode, bodyText)
}

func httpTransportError(err error) error {
	var upstream *platform.UpstreamError
	if errors.As(err, &upstream) {
		return upstream
	}
	body := truncateString(strings.ReplaceAll(err.Error(), "\n", "\\n"), 400)
	return platform.NewUpstreamError(fmt.Sprintf("Transport request failed: %v", err), 502, body)
}

func closeHTTPResponse(response HTTPResponse) {
	if response.Stream != nil {
		_ = response.Stream.Close()
	}
}
