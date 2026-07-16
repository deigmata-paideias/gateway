package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

func LoadSnapshot(ctx context.Context, store *sqlite.Store, initialPath string) (*config.Snapshot, error) {
	revision, err := store.CurrentConfig(ctx)
	if errors.Is(err, sqlite.ErrNotFound) {
		initial, loadErr := config.LoadGateway(initialPath)
		if loadErr != nil {
			return nil, loadErr
		}
		encoded, encodeErr := json.Marshal(initial)
		if encodeErr != nil {
			return nil, fmt.Errorf("编码初始配置: %w", encodeErr)
		}
		revision, err = store.SaveConfig(ctx, 0, encoded, "bootstrap", "bootstrap")
	}
	if err != nil {
		return nil, fmt.Errorf("加载当前配置: %w", err)
	}
	var gatewayConfig config.Gateway
	if err := json.Unmarshal(revision.ConfigJSON, &gatewayConfig); err != nil {
		return nil, fmt.Errorf("解析持久化配置: %w", err)
	}
	return config.NewSnapshot(revision.Revision, gatewayConfig)
}

func (s *Service) Config() (int64, config.Gateway) {
	snapshot := s.configs.Current()
	return snapshot.Revision(), snapshot.Config()
}

func (s *Service) UpdateConfig(
	ctx context.Context,
	expectedRevision int64,
	candidate config.Gateway,
	operation string,
	requestID string,
) (int64, error) {
	if err := config.ValidateGateway(candidate); err != nil {
		return 0, newError(http.StatusUnprocessableEntity, "invalid_configuration", "网关配置无效", err)
	}
	if err := s.validateCredentialReferences(ctx, candidate); err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		return 0, fmt.Errorf("编码 gateway config: %w", err)
	}
	saved, err := s.store.SaveConfig(ctx, expectedRevision, encoded, operation, requestID)
	if err != nil {
		return 0, mapStoreError(err)
	}
	snapshot, err := config.NewSnapshot(saved.Revision, candidate)
	if err != nil {
		return 0, newError(http.StatusUnprocessableEntity, "invalid_configuration", "网关配置无效", err)
	}
	s.configs.Store(snapshot)
	if err := s.recordAdminEvent(ctx, "config.updated", "config", fmt.Sprint(saved.Revision), requestID); err != nil {
		return 0, err
	}
	return saved.Revision, nil
}

func (s *Service) SwitchBackend(
	ctx context.Context,
	expectedRevision int64,
	routeID,
	backendID,
	requestID string,
) (int64, error) {
	current := s.configs.Current()
	if current.Revision() != expectedRevision {
		return 0, newError(http.StatusConflict, "revision_conflict", "配置 Revision 冲突", sqlite.ErrConflict)
	}
	updated, err := config.SwitchActive(current.Config(), routeID, backendID)
	if err != nil {
		return 0, mapRouteError(err)
	}
	return s.UpdateConfig(ctx, expectedRevision, updated, "switch_backend", requestID)
}

func (s *Service) RestoreConfig(ctx context.Context, expectedRevision, sourceRevision int64, requestID string) (int64, error) {
	source, err := s.store.ConfigRevision(ctx, sourceRevision)
	if err != nil {
		return 0, mapStoreError(err)
	}
	var candidate config.Gateway
	if err := json.Unmarshal(source.ConfigJSON, &candidate); err != nil {
		return 0, newError(http.StatusServiceUnavailable, "storage_unavailable", "历史配置不可读", err)
	}
	return s.UpdateConfig(ctx, expectedRevision, candidate, "restore", requestID)
}

func (s *Service) ConfigRevisions(ctx context.Context, limit int) ([]sqlite.ConfigRevision, error) {
	revisions, err := s.store.ConfigRevisions(ctx, limit)
	if err != nil {
		return nil, mapStoreError(err)
	}
	return revisions, nil
}

func (s *Service) validateCredentialReferences(ctx context.Context, candidate config.Gateway) error {
	for _, backend := range candidate.Backends {
		credential, err := s.store.Credential(ctx, backend.CredentialID)
		if err != nil {
			if errors.Is(err, sqlite.ErrNotFound) {
				return newError(http.StatusUnprocessableEntity, "credential_invalid", "Backend 引用的 Credential 不存在", err)
			}
			return mapStoreError(err)
		}
		if credential.Status != "active" || credential.Provider != backend.Provider {
			return newError(http.StatusUnprocessableEntity, "credential_invalid", "Backend 与 Credential 不匹配", errors.New("credential provider 或状态无效"))
		}
	}
	return nil
}
