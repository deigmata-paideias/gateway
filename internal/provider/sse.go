package provider

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

type SSEReader struct {
	reader  *bufio.Reader
	closer  io.Closer
	current Event
	err     error
	done    bool
	maxSize int
}

func NewSSEReader(body io.ReadCloser, maxEventBytes int) *SSEReader {
	if maxEventBytes < 1024 {
		maxEventBytes = 4 << 20
	}
	return &SSEReader{
		reader:  bufio.NewReaderSize(body, 64<<10),
		closer:  body,
		maxSize: maxEventBytes,
	}
}

func (s *SSEReader) Next() bool {
	if s.err != nil || s.done {
		return false
	}
	var eventType string
	var data bytes.Buffer
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			s.err = fmt.Errorf("读取 sse: %w", err)
			return false
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if data.Len() > 0 {
				break
			}
			if errors.Is(err, io.EOF) {
				s.done = true
				return false
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			if data.Len() > s.maxSize {
				s.err = fmt.Errorf("%w: sse event 超过 %d 字节", ErrInvalidResponse, s.maxSize)
				return false
			}
		}
		if errors.Is(err, io.EOF) {
			if data.Len() == 0 {
				s.done = true
				return false
			}
			break
		}
	}
	if data.String() == "[DONE]" {
		s.done = true
		return false
	}
	currentData := append([]byte(nil), data.Bytes()...)
	if !jsonValid(currentData) {
		s.err = fmt.Errorf("%w: sse data 不是 json", ErrInvalidResponse)
		return false
	}
	s.current = Event{
		Type:       eventType,
		Data:       currentData,
		Usage:      ExtractUsage(currentData),
		UpstreamID: ExtractID(currentData),
		Terminal:   IsTerminalResponseEvent(eventType),
	}
	return true
}

func (s *SSEReader) Current() Event {
	return s.current
}

func (s *SSEReader) Err() error {
	return s.err
}

func (s *SSEReader) Close() error {
	if s.closer == nil {
		return nil
	}
	closer := s.closer
	s.closer = nil
	if err := closer.Close(); err != nil {
		return fmt.Errorf("关闭 sse body: %w", err)
	}
	return nil
}

func jsonValid(data []byte) bool {
	var value any
	return len(data) > 0 && jsonUnmarshal(data, &value) == nil
}
