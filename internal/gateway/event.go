package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/deigmata-paideias/gateway/internal/identity"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

func (s *Service) recordAdminEvent(ctx context.Context, action, resourceType, resourceID, requestID string) error {
	id, err := identity.New("evt_")
	if err != nil {
		return fmt.Errorf("生成 admin event id: %w", err)
	}
	if requestID == "" {
		requestID, err = identity.New("req_")
		if err != nil {
			return fmt.Errorf("生成 request id: %w", err)
		}
	}
	if err := s.store.RecordAdminEvent(ctx, sqlite.AdminEvent{
		ID: id, Action: action, ResourceType: resourceType, ResourceID: resourceID,
		RequestID: requestID, Result: "succeeded", TraceID: traceID(ctx), CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		return mapStoreError(err)
	}
	return nil
}
