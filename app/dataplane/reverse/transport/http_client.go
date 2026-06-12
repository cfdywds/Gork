package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type netHTTPClient struct{}

func (netHTTPClient) Post(ctx context.Context, request HTTPRequest) (HTTPResponse, error) {
	return doHTTPRequest(ctx, http.MethodPost, request)
}

func (netHTTPClient) Get(ctx context.Context, request HTTPRequest) (HTTPResponse, error) {
	return doHTTPRequest(ctx, http.MethodGet, request)
}

func (netHTTPClient) Delete(ctx context.Context, request HTTPRequest) (HTTPResponse, error) {
	return doHTTPRequest(ctx, http.MethodDelete, request)
}

func doHTTPRequest(ctx context.Context, method string, request HTTPRequest) (HTTPResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, request.Timeout)
	rawRequest, err := http.NewRequestWithContext(ctx, method, httpRequestURL(request), bytes.NewReader(request.Payload))
	if err != nil {
		cancel()
		return HTTPResponse{}, err
	}
	for key, value := range request.Headers {
		rawRequest.Header.Set(key, value)
	}
	client, err := httpClientForRequest(request)
	if err != nil {
		cancel()
		return HTTPResponse{}, err
	}
	response, err := client.Do(rawRequest)
	if err != nil {
		cancel()
		return HTTPResponse{}, err
	}
	if request.Stream && response.StatusCode == 200 {
		return HTTPResponse{
			StatusCode: response.StatusCode,
			Headers:    firstHeaderValues(response.Header),
			Stream:     &cancelOnCloseReader{ReadCloser: response.Body, cancel: cancel},
		}, nil
	}
	defer cancel()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return HTTPResponse{}, err
	}
	return HTTPResponse{StatusCode: response.StatusCode, Body: body, Headers: firstHeaderValues(response.Header)}, nil
}

var (
	httpClientCache   = make(map[string]*http.Client)
	httpClientCacheMu sync.RWMutex
)

func httpClientForRequest(request HTTPRequest) (*http.Client, error) {
	if request.Lease == nil || request.Lease.ProxyURL == nil || strings.TrimSpace(*request.Lease.ProxyURL) == "" {
		return http.DefaultClient, nil
	}
	proxyURL, err := parseHTTPProxyURL(*request.Lease.ProxyURL)
	if err != nil {
		return nil, err
	}

	cacheKey := proxyURL.String()
	httpClientCacheMu.RLock()
	if cached, ok := httpClientCache[cacheKey]; ok {
		httpClientCacheMu.RUnlock()
		return cached, nil
	}
	httpClientCacheMu.RUnlock()

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default HTTP transport has unexpected type %T", http.DefaultTransport)
	}
	cloned := transport.Clone()
	cloned.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Transport: cloned}

	httpClientCacheMu.Lock()
	httpClientCache[cacheKey] = client
	httpClientCacheMu.Unlock()

	return client, nil
}

func parseHTTPProxyURL(raw string) (*url.URL, error) {
	proxyURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "socks", "socks5h":
		proxyURL.Scheme = "socks5"
	case "socks4", "socks4a":
		return nil, fmt.Errorf("unsupported proxy scheme %q", proxyURL.Scheme)
	}
	return proxyURL, nil
}

func httpRequestURL(request HTTPRequest) string {
	if len(request.Params) == 0 {
		return request.URL
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return request.URL
	}
	query := parsed.Query()
	for key, value := range request.Params {
		query.Set(key, fmt.Sprint(value))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
