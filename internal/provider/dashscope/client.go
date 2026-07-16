// Package dashscope 使用受限 net/http 客户端实现 DashScope OpenAI 兼容协议。
package dashscope

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/deigmata-paideias/gateway/internal/provider"
)

type Options struct {
	BaseURL       string
	APIKey        string
	HTTPClient    *http.Client
	MaxImages     int
	MaxImageBytes int64
}

type Client struct {
	baseURL       string
	apiKey        string
	httpClient    *http.Client
	imageClient   *http.Client
	maxImages     int
	maxImageBytes int64
}

func New(options Options) (*Client, error) {
	if options.BaseURL == "" || options.APIKey == "" || options.HTTPClient == nil {
		return nil, fmt.Errorf("dashscope client 参数不完整")
	}
	if options.MaxImages < 1 || options.MaxImageBytes < 1 {
		return nil, fmt.Errorf("dashscope image limits 无效")
	}
	baseURL := strings.TrimRight(options.BaseURL, "/")
	return &Client{
		baseURL: baseURL, apiKey: options.APIKey, httpClient: options.HTTPClient,
		imageClient: provider.ImageHTTPClient(options.HTTPClient, baseURL),
		maxImages:   options.MaxImages, maxImageBytes: options.MaxImageBytes,
	}, nil
}

func (c *Client) Chat(ctx context.Context, request provider.Request) (provider.Result, error) {
	return c.request(ctx, "/chat/completions", request, false)
}

func (c *Client) ChatStream(ctx context.Context, request provider.Request) (provider.Stream, error) {
	return c.stream(ctx, "/chat/completions", request)
}

func (c *Client) Responses(ctx context.Context, request provider.Request) (provider.Result, error) {
	return c.request(ctx, "/responses", request, false)
}

func (c *Client) ResponsesStream(ctx context.Context, request provider.Request) (provider.Stream, error) {
	return c.stream(ctx, "/responses", request)
}

func (c *Client) Image(ctx context.Context, request provider.Request) (provider.Result, error) {
	result, err := c.request(ctx, "/images/generations", request, false)
	if err != nil {
		return provider.Result{}, err
	}
	return provider.NormalizeImages(
		ctx,
		result.Body,
		c.imageClient,
		c.baseURL,
		c.maxImages,
		c.maxImageBytes,
	)
}

func (c *Client) request(ctx context.Context, path string, request provider.Request, stream bool) (provider.Result, error) {
	body, err := provider.PrepareRequest(request.Body, request.UpstreamModel, request.PreviousResponseID, stream)
	if err != nil {
		return provider.Result{}, err
	}
	response, err := c.do(ctx, path, body)
	if err != nil {
		return provider.Result{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return provider.Result{}, fmt.Errorf("读取 dashscope 响应: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return provider.Result{}, upstreamError(response.StatusCode)
	}
	if !json.Valid(responseBody) {
		return provider.Result{}, provider.ErrInvalidResponse
	}
	return provider.Result{
		Body: responseBody, Usage: provider.ExtractUsage(responseBody), UpstreamID: provider.ExtractID(responseBody),
		HTTPStatus: response.StatusCode,
	}, nil
}

func (c *Client) stream(ctx context.Context, path string, request provider.Request) (provider.Stream, error) {
	body, err := provider.PrepareRequest(request.Body, request.UpstreamModel, request.PreviousResponseID, true)
	if err != nil {
		return nil, err
	}
	response, err := c.do(ctx, path, body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		closeErr := response.Body.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("%w; 关闭响应: %v", upstreamError(response.StatusCode), closeErr)
		}
		return nil, upstreamError(response.StatusCode)
	}
	return provider.NewSSEReader(response.Body, 4<<20), nil
}

func (c *Client) do(ctx context.Context, path string, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建 dashscope 请求: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, &provider.Error{Code: "upstream_unavailable", Retryable: true, Err: err}
	}
	return response, nil
}

func upstreamError(status int) error {
	return &provider.Error{
		Status:    status,
		Code:      "upstream_error",
		Retryable: status == http.StatusTooManyRequests || status >= 500,
		Err:       fmt.Errorf("dashscope api error"),
	}
}

var _ provider.Client = (*Client)(nil)
