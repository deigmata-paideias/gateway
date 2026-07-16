package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/deigmata-paideias/gateway/internal/model"
)

func (s *Store) PutBinding(ctx context.Context, binding model.ResponseBinding) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO response_bindings
			(public_response_id, gateway_token_id, backend_id, provider, model_alias,
			 upstream_response_id, revision, expires_at, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			binding.PublicResponseID, binding.GatewayTokenID, binding.BackendID, binding.Provider,
			binding.ModelAlias, binding.UpstreamResponseID, binding.Revision, binding.ExpiresAt, binding.State)
		if err != nil {
			if isConstraint(err) {
				return ErrConflict
			}
			return fmt.Errorf("保存 response binding: %w", err)
		}
		return nil
	})
}

func (s *Store) ResolveBinding(ctx context.Context, publicID, tokenID string) (model.ResponseBinding, error) {
	row := s.db.QueryRowContext(ctx, `SELECT public_response_id, gateway_token_id, backend_id, provider,
		model_alias, upstream_response_id, revision, expires_at, state FROM response_bindings
		WHERE public_response_id = ? AND gateway_token_id = ? AND expires_at > ?`, publicID, tokenID, time.Now().UnixMilli())
	var item model.ResponseBinding
	if err := row.Scan(
		&item.PublicResponseID, &item.GatewayTokenID, &item.BackendID, &item.Provider,
		&item.ModelAlias, &item.UpstreamResponseID, &item.Revision, &item.ExpiresAt, &item.State,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ResponseBinding{}, ErrNotFound
		}
		return model.ResponseBinding{}, fmt.Errorf("读取 response binding: %w", err)
	}
	return item, nil
}

func (s *Store) Cleanup(ctx context.Context, auditsBefore, bindingsBefore int64, batchSize int) (int64, error) {
	if batchSize < 1 {
		batchSize = 1000
	}
	var affected int64
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		bindingResult, err := tx.ExecContext(ctx, `DELETE FROM response_bindings WHERE public_response_id IN
			(SELECT public_response_id FROM response_bindings WHERE expires_at < ? ORDER BY expires_at LIMIT ?)`, bindingsBefore, batchSize)
		if err != nil {
			return fmt.Errorf("清理 response bindings: %w", err)
		}
		auditResult, err := tx.ExecContext(ctx, `DELETE FROM request_audits WHERE audit_id IN
			(SELECT audit_id FROM request_audits WHERE started_at < ? AND status != 'started' ORDER BY started_at LIMIT ?)`, auditsBefore, batchSize)
		if err != nil {
			return fmt.Errorf("清理 request audits: %w", err)
		}
		bindingRows, err := bindingResult.RowsAffected()
		if err != nil {
			return fmt.Errorf("读取 binding 清理结果: %w", err)
		}
		auditRows, err := auditResult.RowsAffected()
		if err != nil {
			return fmt.Errorf("读取 audit 清理结果: %w", err)
		}
		affected = bindingRows + auditRows
		return nil
	})
	return affected, err
}

func (s *Store) MarkAbandoned(ctx context.Context, before int64) (int64, error) {
	var affected int64
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE request_audits SET status = 'abandoned', finished_at = ?,
			duration_ms = ? - started_at, error_code = 'gateway_restarted' WHERE status = 'started' AND started_at < ?`,
			before, before, before)
		if err != nil {
			return fmt.Errorf("标记 abandoned audit: %w", err)
		}
		affected, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("读取 abandoned 结果: %w", err)
		}
		return nil
	})
	return affected, err
}
