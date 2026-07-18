// Package dashscope 使用受限 net/http 客户端实现 DashScope OpenAI 兼容协议。
package dashscope

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	imageURL      string
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
	imageURL, err := nativeImageURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL: baseURL, imageURL: imageURL, apiKey: options.APIKey, httpClient: options.HTTPClient,
		imageClient: provider.ImageHTTPClient(options.HTTPClient, imageURL),
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
	body, err := prepareImageRequest(request.Body, request.UpstreamModel)
	if err != nil {
		return provider.Result{}, err
	}
	response, err := c.doURL(ctx, c.imageURL, body, "application/json")
	if err != nil {
		return provider.Result{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return provider.Result{}, fmt.Errorf("读取 dashscope 图片响应: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return provider.Result{}, upstreamError(response.StatusCode)
	}
	normalizedBody, requestID, err := normalizeImageResponse(responseBody)
	if err != nil {
		return provider.Result{}, err
	}
	result, err := provider.NormalizeImages(
		ctx,
		normalizedBody,
		c.imageClient,
		c.imageURL,
		c.maxImages,
		c.maxImageBytes,
	)
	if err != nil {
		return provider.Result{}, err
	}
	result.UpstreamID = requestID
	return result, nil
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
	return c.doURL(ctx, c.baseURL+path, body, "application/json, text/event-stream")
}

func (c *Client) doURL(ctx context.Context, endpoint string, body []byte, accept string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建 dashscope 请求: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", accept)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, &provider.Error{Code: "upstream_unavailable", Retryable: true, Err: err}
	}
	return response, nil
}

const imageGenerationPath = "/services/aigc/multimodal-generation/generation"

func nativeImageURL(compatibleBaseURL string) (string, error) {
	parsed, err := url.Parse(compatibleBaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("dashscope base url 无效")
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/compatible-mode/v1"):
		path = strings.TrimSuffix(path, "/compatible-mode/v1") + "/api/v1"
	case strings.HasSuffix(path, "/api/v1"):
	case strings.HasSuffix(path, "/v1"):
		path = strings.TrimSuffix(path, "/v1") + "/api/v1"
	default:
		path += "/api/v1"
	}
	parsed.Path = strings.TrimRight(path, "/") + imageGenerationPath
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

type imageRequest struct {
	Prompt         string `json:"prompt"`
	N              *int   `json:"n"`
	Size           string `json:"size"`
	NegativePrompt string `json:"negative_prompt"`
	PromptExtend   *bool  `json:"prompt_extend"`
	Watermark      *bool  `json:"watermark"`
	Seed           *int64 `json:"seed"`
}

func prepareImageRequest(data []byte, modelName string) ([]byte, error) {
	var request imageRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, fmt.Errorf("%w: image 参数: %v", provider.ErrInvalidRequest, err)
	}
	if strings.TrimSpace(request.Prompt) == "" || modelName == "" {
		return nil, fmt.Errorf("%w: image prompt 或 model 为空", provider.ErrInvalidRequest)
	}
	parameters := make(map[string]any)
	if request.N != nil {
		if *request.N < 1 {
			return nil, fmt.Errorf("%w: image n 必须为正数", provider.ErrInvalidRequest)
		}
		parameters["n"] = *request.N
	}
	if request.Size != "" {
		parameters["size"] = strings.ReplaceAll(strings.ToLower(request.Size), "x", "*")
	}
	if request.NegativePrompt != "" {
		parameters["negative_prompt"] = request.NegativePrompt
	}
	if request.PromptExtend != nil {
		parameters["prompt_extend"] = *request.PromptExtend
	}
	if request.Watermark != nil {
		parameters["watermark"] = *request.Watermark
	}
	if request.Seed != nil {
		parameters["seed"] = *request.Seed
	}
	payload := map[string]any{
		"model": modelName,
		"input": map[string]any{
			"messages": []any{map[string]any{
				"role":    "user",
				"content": []any{map[string]string{"text": request.Prompt}},
			}},
		},
	}
	if len(parameters) > 0 {
		payload["parameters"] = parameters
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: 编码 image 参数: %v", provider.ErrInvalidRequest, err)
	}
	return encoded, nil
}

func normalizeImageResponse(data []byte) ([]byte, string, error) {
	var envelope struct {
		Data      json.RawMessage `json:"data"`
		RequestID string          `json:"request_id"`
		Output    struct {
			Choices []struct {
				Message struct {
					Content []struct {
						Image string `json:"image"`
					} `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		} `json:"output"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, "", fmt.Errorf("%w: 解析 dashscope 图片响应: %v", provider.ErrInvalidResponse, err)
	}
	// 保留兼容响应用于私有部署和确定性 Mock；DashScope 公网响应使用 output.choices。
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		return append([]byte(nil), data...), envelope.RequestID, nil
	}
	items := make([]map[string]string, 0, len(envelope.Output.Choices))
	for _, choice := range envelope.Output.Choices {
		for _, content := range choice.Message.Content {
			if content.Image != "" {
				items = append(items, map[string]string{"url": content.Image})
			}
		}
	}
	if len(items) == 0 {
		return nil, "", fmt.Errorf("%w: dashscope 图片响应缺少 image", provider.ErrInvalidResponse)
	}
	normalized := struct {
		Data  []map[string]string `json:"data"`
		Usage json.RawMessage     `json:"usage,omitempty"`
	}{Data: items, Usage: envelope.Usage}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", fmt.Errorf("编码 dashscope 图片响应: %w", err)
	}
	return encoded, envelope.RequestID, nil
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
