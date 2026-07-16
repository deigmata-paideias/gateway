package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/provider"
)

type CallStream struct {
	service    *Service
	state      callState
	upstream   provider.Stream
	input      CallInput
	operation  string
	current    provider.Event
	err        error
	usage      model.Usage
	publicID   string
	upstreamID string
	firstAt    time.Time
	finishOnce sync.Once
}

func newCallStream(
	service *Service,
	state callState,
	upstream provider.Stream,
	input CallInput,
	operation string,
) *CallStream {
	return &CallStream{
		service: service, state: state, upstream: upstream, input: input, operation: operation,
	}
}

func (s *CallStream) Next() bool {
	if s.err != nil {
		return false
	}
	if !s.upstream.Next() {
		if err := s.upstream.Err(); err != nil {
			s.err = mapProviderError(err)
			s.finish("failed", http.StatusBadGateway, "upstream_stream_failed")
			return false
		}
		s.finish("succeeded", http.StatusOK, "")
		return false
	}
	event := s.upstream.Current()
	if s.firstAt.IsZero() {
		s.firstAt = time.Now()
	}
	if event.Usage != (model.Usage{}) {
		s.usage = event.Usage
	}
	data := event.Data
	var err error
	if s.operation == "responses" {
		if s.publicID == "" && event.UpstreamID != "" {
			s.upstreamID = event.UpstreamID
			s.publicID, err = s.service.bindResponse(
				s.state.ctx,
				s.input.Token.ID,
				s.input.ModelAlias,
				s.state.route,
				event.UpstreamID,
			)
		}
		if err == nil && s.publicID != "" {
			data, err = provider.RewriteString(data, s.upstreamID, s.publicID)
		}
		if err == nil {
			data, err = provider.RewriteString(data, s.state.route.Target.UpstreamModel, s.input.ModelAlias)
		}
	} else {
		data, err = provider.RewriteTopLevel(data, map[string]string{"model": s.input.ModelAlias})
	}
	if err != nil {
		s.err = err
		s.finish("failed", http.StatusBadGateway, "invalid_upstream_response")
		return false
	}
	event.Data = data
	event.UpstreamID = s.publicID
	s.current = event
	if event.Terminal {
		status := "succeeded"
		code := ""
		if strings.HasSuffix(event.Type, ".failed") || event.Type == "error" {
			status = "failed"
			code = "upstream_stream_failed"
		}
		s.finish(status, http.StatusOK, code)
	}
	return true
}

func (s *CallStream) Current() provider.Event {
	return s.current
}

func (s *CallStream) Err() error {
	return s.err
}

func (s *CallStream) AuditID() string {
	return s.state.audit.ID
}

func (s *CallStream) Revision() int64 {
	return s.state.route.Revision
}

func (s *CallStream) BackendID() string {
	return s.state.route.Backend.ID
}

func (s *CallStream) ResponseID() string {
	return s.publicID
}

func (s *CallStream) Close() error {
	closeErr := s.upstream.Close()
	if s.state.ctx.Err() != nil {
		s.finish("cancelled", 499, "client_cancelled")
	} else {
		s.finish("failed", http.StatusBadGateway, "stream_closed")
	}
	if closeErr != nil {
		return fmt.Errorf("关闭 provider stream: %w", closeErr)
	}
	return nil
}

func (s *CallStream) finish(status string, httpStatus int, errorCode string) {
	s.finishOnce.Do(func() {
		firstTokenMillis := int64(0)
		if !s.firstAt.IsZero() {
			firstTokenMillis = s.firstAt.Sub(s.state.started).Milliseconds()
		}
		finish := model.AuditFinish{
			ID: s.state.audit.ID, Status: status, HTTPStatus: httpStatus, ErrorCode: errorCode,
			PublicResponseID: s.publicID, Usage: s.usage, FinishedAt: time.Now().UnixMilli(),
			DurationMillis:         time.Since(s.state.started).Milliseconds(),
			UpstreamDurationMillis: time.Since(s.state.started).Milliseconds(),
			TimeToFirstTokenMillis: firstTokenMillis,
		}
		ctx, cancel := finishContext(s.state.ctx)
		defer cancel()
		if err := s.service.store.FinishAudit(ctx, finish); err != nil {
			s.service.recorder.RecordAuditFailure(s.state.ctx, s.state.operation)
			s.err = errors.Join(s.err, newError(http.StatusServiceUnavailable, "audit_unavailable", "调用审计暂不可用", err))
		}
		s.service.recorder.RecordProviderCall(
			s.state.ctx,
			s.state.route.Backend.Provider,
			s.state.operation,
			s.input.ModelAlias,
			status,
			time.Since(s.state.started),
			s.usage,
		)
	})
}

var _ context.Context
