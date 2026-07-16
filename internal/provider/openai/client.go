// Package openai 使用 OpenAI 官方 Go SDK 实现 Chat、Responses 和 Image。
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

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
	client        openaisdk.Client
	httpClient    *http.Client
	baseURL       string
	maxImages     int
	maxImageBytes int64
}

func New(options Options) (*Client, error) {
	if options.BaseURL == "" || options.APIKey == "" || options.HTTPClient == nil {
		return nil, fmt.Errorf("openai client 参数不完整")
	}
	if options.MaxImages < 1 || options.MaxImageBytes < 1 {
		return nil, fmt.Errorf("openai image limits 无效")
	}
	baseURL := strings.TrimRight(options.BaseURL, "/")
	client := openaisdk.NewClient(
		option.WithAPIKey(options.APIKey),
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(options.HTTPClient),
		option.WithMaxRetries(0),
	)
	return &Client{
		client: client, httpClient: provider.ImageHTTPClient(options.HTTPClient, baseURL),
		baseURL: baseURL, maxImages: options.MaxImages, maxImageBytes: options.MaxImageBytes,
	}, nil
}

func (c *Client) Chat(ctx context.Context, request provider.Request) (provider.Result, error) {
	params, err := chatParams(request)
	if err != nil {
		return provider.Result{}, err
	}
	response, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return provider.Result{}, mapError(err)
	}
	raw := []byte(response.RawJSON())
	return provider.Result{
		Body: raw, Usage: provider.ExtractUsage(raw), UpstreamID: response.ID, HTTPStatus: http.StatusOK,
	}, nil
}

func (c *Client) ChatStream(ctx context.Context, request provider.Request) (provider.Stream, error) {
	params, err := chatParams(request)
	if err != nil {
		return nil, err
	}
	stream := c.client.Chat.Completions.NewStreaming(ctx, params)
	return &chatStream{stream: stream}, nil
}

func chatParams(request provider.Request) (openaisdk.ChatCompletionNewParams, error) {
	var params openaisdk.ChatCompletionNewParams
	if err := json.Unmarshal(request.Body, &params); err != nil {
		return openaisdk.ChatCompletionNewParams{}, fmt.Errorf("%w: chat 参数: %v", provider.ErrInvalidRequest, err)
	}
	params.Model = shared.ChatModel(request.UpstreamModel)
	return params, nil
}

func (c *Client) Responses(ctx context.Context, request provider.Request) (provider.Result, error) {
	params, err := responseParams(request)
	if err != nil {
		return provider.Result{}, err
	}
	response, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return provider.Result{}, mapError(err)
	}
	raw := []byte(response.RawJSON())
	return provider.Result{
		Body: raw, Usage: provider.ExtractUsage(raw), UpstreamID: response.ID, HTTPStatus: http.StatusOK,
	}, nil
}

func (c *Client) ResponsesStream(ctx context.Context, request provider.Request) (provider.Stream, error) {
	params, err := responseParams(request)
	if err != nil {
		return nil, err
	}
	stream := c.client.Responses.NewStreaming(ctx, params)
	return &responseStream{stream: stream}, nil
}

func responseParams(request provider.Request) (responses.ResponseNewParams, error) {
	var params responses.ResponseNewParams
	if err := json.Unmarshal(request.Body, &params); err != nil {
		return responses.ResponseNewParams{}, fmt.Errorf("%w: responses 参数: %v", provider.ErrInvalidRequest, err)
	}
	params.Model = shared.ResponsesModel(request.UpstreamModel)
	params.Background = param.Opt[bool]{}
	if request.PreviousResponseID != "" {
		params.PreviousResponseID = param.NewOpt(request.PreviousResponseID)
	}
	return params, nil
}

func (c *Client) Image(ctx context.Context, request provider.Request) (provider.Result, error) {
	var params openaisdk.ImageGenerateParams
	if err := json.Unmarshal(request.Body, &params); err != nil {
		return provider.Result{}, fmt.Errorf("%w: image 参数: %v", provider.ErrInvalidRequest, err)
	}
	params.Model = openaisdk.ImageModel(request.UpstreamModel)
	params.ResponseFormat = openaisdk.ImageGenerateParamsResponseFormatB64JSON
	response, err := c.client.Images.Generate(ctx, params)
	if err != nil {
		return provider.Result{}, mapError(err)
	}
	return provider.NormalizeImages(
		ctx,
		[]byte(response.RawJSON()),
		c.httpClient,
		c.baseURL,
		c.maxImages,
		c.maxImageBytes,
	)
}

var _ provider.Client = (*Client)(nil)
