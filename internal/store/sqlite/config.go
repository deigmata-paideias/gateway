package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type ConfigRevision struct {
	Revision   int64
	ConfigJSON []byte
	SHA256     []byte
	Operation  string
	RequestID  string
	CreatedAt  int64
}

func (s *Store) CurrentConfig(ctx context.Context) (ConfigRevision, error) {
	row := s.db.QueryRowContext(ctx, `SELECT c.revision, c.config_json, c.sha256, c.operation, c.request_id, c.created_at
		FROM config_revisions c
		JOIN runtime_meta m ON m.key = 'current_revision' AND CAST(m.value AS INTEGER) = c.revision`)
	return scanConfigRevision(row)
}

func (s *Store) ConfigRevision(ctx context.Context, revision int64) (ConfigRevision, error) {
	row := s.db.QueryRowContext(ctx, `SELECT revision, config_json, sha256, operation, request_id, created_at
		FROM config_revisions WHERE revision = ?`, revision)
	return scanConfigRevision(row)
}

func (s *Store) ConfigRevisions(ctx context.Context, limit int) ([]ConfigRevision, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT revision, config_json, sha256, operation, request_id, created_at
		FROM config_revisions ORDER BY revision DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询 config revisions: %w", err)
	}
	defer rows.Close()
	items := make([]ConfigRevision, 0, limit)
	for rows.Next() {
		item, scanErr := scanConfigRevision(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 config revisions: %w", err)
	}
	return items, nil
}

func (s *Store) SaveConfig(
	ctx context.Context,
	expectedRevision int64,
	configJSON []byte,
	operation string,
	requestID string,
) (ConfigRevision, error) {
	digest := sha256.Sum256(configJSON)
	createdAt := time.Now().UnixMilli()
	var saved ConfigRevision
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		current, err := currentRevisionTx(ctx, tx)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if errors.Is(err, ErrNotFound) {
			current = 0
		}
		if current != expectedRevision {
			return fmt.Errorf("%w: expected revision %d, current %d", ErrConflict, expectedRevision, current)
		}
		newRevision := current + 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO config_revisions
			(revision, config_json, sha256, operation, request_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			newRevision, string(configJSON), digest[:], operation, requestID, createdAt); err != nil {
			return fmt.Errorf("保存 config revision: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_meta(key, value) VALUES ('current_revision', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, newRevision); err != nil {
			return fmt.Errorf("更新 current revision: %w", err)
		}
		saved = ConfigRevision{
			Revision: newRevision, ConfigJSON: append([]byte(nil), configJSON...), SHA256: append([]byte(nil), digest[:]...),
			Operation: operation, RequestID: requestID, CreatedAt: createdAt,
		}
		return nil
	})
	return saved, err
}

func currentRevisionTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var value string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM runtime_meta WHERE key = 'current_revision'").Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("查询 current revision: %w", err)
	}
	var revision int64
	if _, err := fmt.Sscan(value, &revision); err != nil {
		return 0, fmt.Errorf("解析 current revision: %w", err)
	}
	return revision, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanConfigRevision(row rowScanner) (ConfigRevision, error) {
	var item ConfigRevision
	var configJSON string
	if err := row.Scan(
		&item.Revision,
		&configJSON,
		&item.SHA256,
		&item.Operation,
		&item.RequestID,
		&item.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ConfigRevision{}, ErrNotFound
		}
		return ConfigRevision{}, fmt.Errorf("读取 config revision: %w", err)
	}
	item.ConfigJSON = []byte(configJSON)
	return item, nil
}
