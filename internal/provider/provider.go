// Package provider 定义模型供应商适配器的窄接口和规范化结果。
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/deigmata-paideias/gateway/internal/model"
)

var (
	ErrInvalidRequest   = errors.New("provider: invalid request")
	ErrInvalidResponse  = errors.New("provider: invalid response")
	ErrImageTooLarge    = errors.New("provider: image too large")
	ErrUnsupportedImage = errors.New("provider: unsupported image source")
)

type Request struct {
	Body               json.RawMessage
	ModelAlias         string
	UpstreamModel      string
	PreviousResponseID string
}

type Result struct {
	Body          json.RawMessage
	Usage         model.Usage
	UpstreamID    string
	HTTPStatus    int
	ImageCount    int64
	RawImageBytes int64
}

type Event struct {
	Type       string
	Data       json.RawMessage
	Usage      model.Usage
	UpstreamID string
	Terminal   bool
}

type Stream interface {
	Next() bool
	Current() Event
	Err() error
	Close() error
}

type Client interface {
	Chat(ctx context.Context, request Request) (Result, error)
	ChatStream(ctx context.Context, request Request) (Stream, error)
	Responses(ctx context.Context, request Request) (Result, error)
	ResponsesStream(ctx context.Context, request Request) (Stream, error)
	Image(ctx context.Context, request Request) (Result, error)
}

type Error struct {
	Status    int
	Code      string
	Retryable bool
	Err       error
}

func (e *Error) Error() string {
	return fmt.Sprintf("provider request failed: status=%d code=%s: %v", e.Status, e.Code, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}
