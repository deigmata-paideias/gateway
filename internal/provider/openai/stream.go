package openai

import (
	"fmt"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"

	"github.com/deigmata-paideias/gateway/internal/provider"
)

type chatStream struct {
	stream  *ssestream.Stream[openaisdk.ChatCompletionChunk]
	current provider.Event
}

func (s *chatStream) Next() bool {
	if !s.stream.Next() {
		return false
	}
	chunk := s.stream.Current()
	raw := []byte(chunk.RawJSON())
	s.current = provider.Event{
		Data: raw, Usage: provider.ExtractUsage(raw), UpstreamID: chunk.ID,
	}
	return true
}

func (s *chatStream) Current() provider.Event { return s.current }
func (s *chatStream) Err() error              { return s.stream.Err() }
func (s *chatStream) Close() error {
	if err := s.stream.Close(); err != nil {
		return fmt.Errorf("关闭 openai chat stream: %w", err)
	}
	return nil
}

type responseStream struct {
	stream  *ssestream.Stream[responses.ResponseStreamEventUnion]
	current provider.Event
}

func (s *responseStream) Next() bool {
	if !s.stream.Next() {
		return false
	}
	event := s.stream.Current()
	raw := []byte(event.RawJSON())
	s.current = provider.Event{
		Type: event.Type, Data: raw, Usage: provider.ExtractUsage(raw),
		UpstreamID: provider.ExtractID(raw), Terminal: provider.IsTerminalResponseEvent(event.Type),
	}
	return true
}

func (s *responseStream) Current() provider.Event { return s.current }
func (s *responseStream) Err() error              { return s.stream.Err() }
func (s *responseStream) Close() error {
	if err := s.stream.Close(); err != nil {
		return fmt.Errorf("关闭 openai responses stream: %w", err)
	}
	return nil
}

var _ provider.Stream = (*chatStream)(nil)
var _ provider.Stream = (*responseStream)(nil)
