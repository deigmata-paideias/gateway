package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/provider"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

type CallInput struct {
	Token      model.GatewayToken
	RequestID  string
	ModelAlias string
	Body       []byte
	Stream     bool
	PreviousID string
}

type CallResult struct {
	Body       []byte
	Usage      model.Usage
	AuditID    string
	Revision   int64
	BackendID  string
	Provider   string
	ResponseID string
}

type callState struct {
	ctx       context.Context
	audit     model.Audit
	started   time.Time
	route     config.ResolvedRoute
	operation string
}

func (s *Service) Chat(ctx context.Context, input CallInput) (CallResult, error) {
	state, err := s.startCall(ctx, input, "chat", "chat.completions")
	if err != nil {
		return CallResult{}, err
	}
	client, err := s.newProvider(ctx, state.route)
	if err != nil {
		return CallResult{}, s.failCall(state, err)
	}
	upstreamStarted := time.Now()
	result, err := client.Chat(ctx, provider.Request{
		Body: input.Body, ModelAlias: input.ModelAlias, UpstreamModel: state.route.Target.UpstreamModel,
	})
	if err != nil {
		return CallResult{}, s.failProviderCall(state, err, time.Since(upstreamStarted))
	}
	rewritten, err := provider.RewriteTopLevel(result.Body, map[string]string{"model": input.ModelAlias})
	if err != nil {
		return CallResult{}, s.failProviderCall(state, err, time.Since(upstreamStarted))
	}
	if err := s.succeedCall(state, result.Usage, "", time.Since(upstreamStarted), 0); err != nil {
		return CallResult{}, err
	}
	return callResult(state, rewritten, result.Usage, ""), nil
}

func (s *Service) Image(ctx context.Context, input CallInput) (CallResult, error) {
	state, err := s.startCall(ctx, input, "image", "images.generate")
	if err != nil {
		return CallResult{}, err
	}
	client, err := s.newProvider(ctx, state.route)
	if err != nil {
		return CallResult{}, s.failCall(state, err)
	}
	upstreamStarted := time.Now()
	result, err := client.Image(ctx, provider.Request{
		Body: input.Body, ModelAlias: input.ModelAlias, UpstreamModel: state.route.Target.UpstreamModel,
	})
	if err != nil {
		return CallResult{}, s.failProviderCall(state, err, time.Since(upstreamStarted))
	}
	if err := s.succeedCall(state, result.Usage, "", time.Since(upstreamStarted), 0); err != nil {
		return CallResult{}, err
	}
	return callResult(state, result.Body, result.Usage, ""), nil
}

func (s *Service) Responses(ctx context.Context, input CallInput) (CallResult, error) {
	state, upstreamPreviousID, err := s.startResponseCall(ctx, input)
	if err != nil {
		return CallResult{}, err
	}
	client, err := s.newProvider(ctx, state.route)
	if err != nil {
		return CallResult{}, s.failCall(state, err)
	}
	upstreamStarted := time.Now()
	result, err := client.Responses(ctx, provider.Request{
		Body: input.Body, ModelAlias: input.ModelAlias, UpstreamModel: state.route.Target.UpstreamModel,
		PreviousResponseID: upstreamPreviousID,
	})
	if err != nil {
		return CallResult{}, s.failProviderCall(state, err, time.Since(upstreamStarted))
	}
	publicID, err := s.bindResponse(ctx, input.Token.ID, input.ModelAlias, state.route, result.UpstreamID)
	if err != nil {
		return CallResult{}, s.failCall(state, err)
	}
	rewritten, err := provider.RewriteString(result.Body, result.UpstreamID, publicID)
	if err == nil {
		rewritten, err = provider.RewriteString(rewritten, state.route.Target.UpstreamModel, input.ModelAlias)
	}
	if err != nil {
		return CallResult{}, s.failProviderCall(state, err, time.Since(upstreamStarted))
	}
	if err := s.succeedCall(state, result.Usage, publicID, time.Since(upstreamStarted), 0); err != nil {
		return CallResult{}, err
	}
	return callResult(state, rewritten, result.Usage, publicID), nil
}

func (s *Service) ChatStream(ctx context.Context, input CallInput) (*CallStream, error) {
	state, err := s.startCall(ctx, input, "chat", "chat.completions")
	if err != nil {
		return nil, err
	}
	client, err := s.newProvider(ctx, state.route)
	if err != nil {
		return nil, s.failCall(state, err)
	}
	stream, err := client.ChatStream(ctx, provider.Request{
		Body: input.Body, ModelAlias: input.ModelAlias, UpstreamModel: state.route.Target.UpstreamModel,
	})
	if err != nil {
		return nil, s.failProviderCall(state, err, 0)
	}
	return newCallStream(s, state, stream, input, "chat"), nil
}

func (s *Service) ResponsesStream(ctx context.Context, input CallInput) (*CallStream, error) {
	state, upstreamPreviousID, err := s.startResponseCall(ctx, input)
	if err != nil {
		return nil, err
	}
	client, err := s.newProvider(ctx, state.route)
	if err != nil {
		return nil, s.failCall(state, err)
	}
	stream, err := client.ResponsesStream(ctx, provider.Request{
		Body: input.Body, ModelAlias: input.ModelAlias, UpstreamModel: state.route.Target.UpstreamModel,
		PreviousResponseID: upstreamPreviousID,
	})
	if err != nil {
		return nil, s.failProviderCall(state, err, 0)
	}
	return newCallStream(s, state, stream, input, "responses"), nil
}

func (s *Service) startResponseCall(ctx context.Context, input CallInput) (callState, string, error) {
	if input.PreviousID == "" {
		state, err := s.startCall(ctx, input, "responses", "responses.create")
		return state, "", err
	}
	binding, err := s.store.ResolveBinding(ctx, input.PreviousID, input.Token.ID)
	if err != nil {
		return callState{}, "", newError(http.StatusNotFound, "response_not_found", "Response 不存在或已过期", err)
	}
	if binding.ModelAlias != input.ModelAlias {
		return callState{}, "", newError(http.StatusNotFound, "response_not_found", "Response 不存在或已过期", sqlite.ErrNotFound)
	}
	route, err := s.resolveBoundRoute(ctx, binding)
	if err != nil {
		return callState{}, "", err
	}
	state, err := s.startResolvedCall(ctx, input, route, "responses.create")
	return state, binding.UpstreamResponseID, err
}

func (s *Service) resolveBoundRoute(ctx context.Context, binding model.ResponseBinding) (config.ResolvedRoute, error) {
	current := s.configs.Current()
	if current.Revision() == binding.Revision {
		resolved, err := current.ResolveBackend("responses", binding.ModelAlias, binding.BackendID)
		if err != nil {
			return config.ResolvedRoute{}, mapRouteError(err)
		}
		return resolved, nil
	}
	revision, err := s.store.ConfigRevision(ctx, binding.Revision)
	if err != nil {
		return config.ResolvedRoute{}, mapStoreError(err)
	}
	var archived config.Gateway
	if err := jsonUnmarshal(revision.ConfigJSON, &archived); err != nil {
		return config.ResolvedRoute{}, newError(http.StatusServiceUnavailable, "response_backend_unavailable", "Response 原 Backend 不可用", err)
	}
	snapshot, err := config.NewSnapshot(binding.Revision, archived)
	if err != nil {
		return config.ResolvedRoute{}, newError(http.StatusServiceUnavailable, "response_backend_unavailable", "Response 原 Backend 不可用", err)
	}
	resolved, err := snapshot.ResolveBackend("responses", binding.ModelAlias, binding.BackendID)
	if err != nil {
		return config.ResolvedRoute{}, newError(http.StatusServiceUnavailable, "response_backend_unavailable", "Response 原 Backend 不可用", err)
	}
	return resolved, nil
}

func (s *Service) startCall(ctx context.Context, input CallInput, operation, auditOperation string) (callState, error) {
	route, err := s.configs.Current().Resolve(operation, input.ModelAlias)
	if err != nil {
		return callState{}, mapRouteError(err)
	}
	return s.startResolvedCall(ctx, input, route, auditOperation)
}

func (s *Service) startResolvedCall(ctx context.Context, input CallInput, route config.ResolvedRoute, auditOperation string) (callState, error) {
	auditID, err := newInternalID("aud_")
	if err != nil {
		return callState{}, err
	}
	requestID := input.RequestID
	if requestID == "" {
		requestID, err = newInternalID("req_")
		if err != nil {
			return callState{}, err
		}
	}
	revision := route.Revision
	started := time.Now()
	audit := model.Audit{
		ID: auditID, RequestID: requestID, GatewayTokenID: input.Token.ID,
		Operation: auditOperation, ModelAlias: input.ModelAlias, BackendID: route.Backend.ID,
		Provider: route.Backend.Provider, ConfigRevision: &revision, Stream: input.Stream,
		Status: "started", StartedAt: started.UnixMilli(), TraceID: traceID(ctx),
	}
	if err := s.store.StartAudit(ctx, audit); err != nil {
		return callState{}, newError(http.StatusServiceUnavailable, "audit_unavailable", "调用审计暂不可用", err)
	}
	return callState{ctx: ctx, audit: audit, started: started, route: route, operation: auditOperation}, nil
}

func (s *Service) bindResponse(
	ctx context.Context,
	tokenID,
	modelAlias string,
	route config.ResolvedRoute,
	upstreamID string,
) (string, error) {
	if upstreamID == "" {
		return "", newError(http.StatusBadGateway, "invalid_upstream_response", "Responses 响应缺少 ID", provider.ErrInvalidResponse)
	}
	publicID, err := newInternalID("resp_")
	if err != nil {
		return "", err
	}
	expiresAt := time.Now().Add(s.configs.Current().Config().Responses.BindingRetention.Value()).UnixMilli()
	if err := s.store.PutBinding(ctx, model.ResponseBinding{
		PublicResponseID: publicID, GatewayTokenID: tokenID, BackendID: route.Backend.ID,
		Provider: route.Backend.Provider, ModelAlias: modelAlias, UpstreamResponseID: upstreamID,
		Revision: route.Revision, ExpiresAt: expiresAt, State: "active",
	}); err != nil {
		return "", mapStoreError(err)
	}
	return publicID, nil
}

func (s *Service) succeedCall(state callState, usage model.Usage, responseID string, upstream, firstToken time.Duration) error {
	finish := model.AuditFinish{
		ID: state.audit.ID, Status: "succeeded", HTTPStatus: http.StatusOK, PublicResponseID: responseID,
		Usage: usage, FinishedAt: time.Now().UnixMilli(), DurationMillis: time.Since(state.started).Milliseconds(),
		UpstreamDurationMillis: upstream.Milliseconds(), TimeToFirstTokenMillis: firstToken.Milliseconds(),
	}
	finishCtx, cancel := finishContext(state.ctx)
	defer cancel()
	if err := s.store.FinishAudit(finishCtx, finish); err != nil {
		s.recorder.RecordAuditFailure(state.ctx, state.operation)
		return newError(http.StatusServiceUnavailable, "audit_unavailable", "调用审计暂不可用", err)
	}
	s.recorder.RecordProviderCall(
		state.ctx,
		state.route.Backend.Provider,
		state.operation,
		state.audit.ModelAlias,
		"succeeded",
		upstream,
		usage,
	)
	return nil
}

func (s *Service) failProviderCall(state callState, err error, upstream time.Duration) error {
	mapped := mapProviderError(err)
	return s.finishFailed(state, mapped, upstream)
}

func (s *Service) failCall(state callState, err error) error {
	var mapped *Error
	if errors.As(err, &mapped) {
		return s.finishFailed(state, mapped, 0)
	}
	return s.finishFailed(state, newError(http.StatusInternalServerError, "internal_error", "网关内部错误", err), 0)
}

func (s *Service) finishFailed(state callState, mapped *Error, upstream time.Duration) error {
	finish := model.AuditFinish{
		ID: state.audit.ID, Status: "failed", HTTPStatus: mapped.Status, ErrorCode: mapped.Code,
		FinishedAt: time.Now().UnixMilli(), DurationMillis: time.Since(state.started).Milliseconds(),
		UpstreamDurationMillis: upstream.Milliseconds(),
	}
	finishCtx, cancel := finishContext(state.ctx)
	defer cancel()
	if err := s.store.FinishAudit(finishCtx, finish); err != nil {
		s.recorder.RecordAuditFailure(state.ctx, state.operation)
		return newError(http.StatusServiceUnavailable, "audit_unavailable", "调用审计暂不可用", errors.Join(mapped, err))
	}
	s.recorder.RecordProviderCall(
		state.ctx,
		state.route.Backend.Provider,
		state.operation,
		state.audit.ModelAlias,
		"failed",
		upstream,
		model.Usage{},
	)
	return mapped
}

func callResult(state callState, body []byte, usage model.Usage, responseID string) CallResult {
	return CallResult{
		Body: body, Usage: usage, AuditID: state.audit.ID, Revision: state.route.Revision,
		BackendID: state.route.Backend.ID, Provider: state.route.Backend.Provider, ResponseID: responseID,
	}
}
