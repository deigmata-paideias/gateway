package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/model"
)

type QueryResult[T any] struct {
	Value    T
	AuditID  string
	Revision int64
}

func (s *Service) Models(ctx context.Context, token model.GatewayToken, requestID string) (QueryResult[[]config.Route], error) {
	snapshot := s.configs.Current()
	audit, started, err := s.startQueryAudit(ctx, token, requestID, "models.list", snapshot.Revision())
	if err != nil {
		return QueryResult[[]config.Route]{}, err
	}
	finish := model.AuditFinish{
		ID: audit.ID, Status: "succeeded", HTTPStatus: http.StatusOK,
		FinishedAt: time.Now().UnixMilli(), DurationMillis: time.Since(started).Milliseconds(),
	}
	if err := s.store.FinishAudit(ctx, finish); err != nil {
		return QueryResult[[]config.Route]{}, newError(http.StatusServiceUnavailable, "audit_unavailable", "调用审计暂不可用", err)
	}
	return QueryResult[[]config.Route]{Value: snapshot.Models(), AuditID: audit.ID, Revision: snapshot.Revision()}, nil
}

func (s *Service) CurrentToken(ctx context.Context, token model.GatewayToken, requestID string) (QueryResult[model.GatewayToken], error) {
	snapshot := s.configs.Current()
	audit, started, err := s.startQueryAudit(ctx, token, requestID, "token.get", snapshot.Revision())
	if err != nil {
		return QueryResult[model.GatewayToken]{}, err
	}
	finish := model.AuditFinish{
		ID: audit.ID, Status: "succeeded", HTTPStatus: http.StatusOK,
		FinishedAt: time.Now().UnixMilli(), DurationMillis: time.Since(started).Milliseconds(),
	}
	if err := s.store.FinishAudit(ctx, finish); err != nil {
		return QueryResult[model.GatewayToken]{}, newError(http.StatusServiceUnavailable, "audit_unavailable", "调用审计暂不可用", err)
	}
	return QueryResult[model.GatewayToken]{Value: token, AuditID: audit.ID, Revision: snapshot.Revision()}, nil
}

func (s *Service) startQueryAudit(
	ctx context.Context,
	token model.GatewayToken,
	requestID,
	operation string,
	revision int64,
) (model.Audit, time.Time, error) {
	if requestID == "" {
		generated, err := newInternalID("req_")
		if err != nil {
			return model.Audit{}, time.Time{}, err
		}
		requestID = generated
	}
	auditID, err := newInternalID("aud_")
	if err != nil {
		return model.Audit{}, time.Time{}, err
	}
	started := time.Now()
	audit := model.Audit{
		ID: auditID, RequestID: requestID, GatewayTokenID: token.ID, Operation: operation,
		ConfigRevision: &revision, Status: "started", StartedAt: started.UnixMilli(), TraceID: traceID(ctx),
	}
	if err := s.store.StartAudit(ctx, audit); err != nil {
		return model.Audit{}, time.Time{}, newError(http.StatusServiceUnavailable, "audit_unavailable", "调用审计暂不可用", err)
	}
	return audit, started, nil
}
