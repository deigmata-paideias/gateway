package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

type imageEnvelope struct {
	Created int64 `json:"created"`
	Data    []struct {
		B64JSON       string `json:"b64_json,omitempty"`
		URL           string `json:"url,omitempty"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	} `json:"data"`
	Usage json.RawMessage `json:"usage,omitempty"`
}

func NormalizeImages(
	ctx context.Context,
	data []byte,
	client *http.Client,
	providerBaseURL string,
	maxImages int,
	maxRawBytes int64,
) (Result, error) {
	var envelope imageEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Result{}, fmt.Errorf("%w: 解析图片响应: %v", ErrInvalidResponse, err)
	}
	if len(envelope.Data) == 0 || len(envelope.Data) > maxImages {
		return Result{}, fmt.Errorf("%w: 图片数量 %d 无效", ErrInvalidResponse, len(envelope.Data))
	}
	var total int64
	for index := range envelope.Data {
		item := &envelope.Data[index]
		var raw []byte
		var err error
		switch {
		case item.B64JSON != "":
			raw, err = base64.StdEncoding.DecodeString(item.B64JSON)
			if err != nil {
				return Result{}, fmt.Errorf("%w: 图片 base64 无效", ErrInvalidResponse)
			}
		case item.URL != "":
			raw, err = downloadImage(ctx, client, providerBaseURL, item.URL, maxRawBytes-total)
			if err != nil {
				return Result{}, err
			}
		default:
			return Result{}, fmt.Errorf("%w: 图片缺少 b64_json 或 url", ErrInvalidResponse)
		}
		total += int64(len(raw))
		if total > maxRawBytes {
			return Result{}, ErrImageTooLarge
		}
		item.B64JSON = base64.StdEncoding.EncodeToString(raw)
		item.URL = ""
		clear(raw)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return Result{}, fmt.Errorf("编码图片响应: %w", err)
	}
	imageCount := int64(len(envelope.Data))
	usage := ExtractUsage(encoded)
	usage.ImageCount = &imageCount
	usage.RawImageBytes = &total
	return Result{
		Body: encoded, Usage: usage, HTTPStatus: http.StatusOK,
		ImageCount: imageCount, RawImageBytes: total,
	}, nil
}

func downloadImage(
	ctx context.Context,
	client *http.Client,
	providerBaseURL string,
	imageURL string,
	remaining int64,
) ([]byte, error) {
	if remaining <= 0 {
		return nil, ErrImageTooLarge
	}
	parsed, err := validateImageURL(providerBaseURL, imageURL)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("创建图片下载请求: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("下载图片: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: 图片下载 status %d", ErrInvalidResponse, response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "image/") {
		return nil, fmt.Errorf("%w: 图片 content-type 无效", ErrInvalidResponse)
	}
	limited := io.LimitReader(response.Body, remaining+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("读取图片: %w", err)
	}
	if int64(len(raw)) > remaining {
		clear(raw)
		return nil, ErrImageTooLarge
	}
	return raw, nil
}

func validateImageURL(providerBaseURL, imageURL string) (*url.URL, error) {
	parsed, err := url.Parse(imageURL)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return nil, ErrUnsupportedImage
	}
	if parsed.Scheme == "https" {
		return parsed, nil
	}
	base, baseErr := url.Parse(providerBaseURL)
	if baseErr == nil && parsed.Scheme == "http" && base.Scheme == "http" && strings.EqualFold(parsed.Hostname(), base.Hostname()) {
		return parsed, nil
	}
	return nil, ErrUnsupportedImage
}

func ImageHTTPClient(base *http.Client, providerBaseURL string) *http.Client {
	copyClient := *base
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("图片重定向次数过多")
		}
		_, err := validateImageURL(providerBaseURL, request.URL.String())
		return err
	}
	return &copyClient
}
