package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/deigmata-paideias/gateway/internal/model"
)

func (s *Store) CreateToken(ctx context.Context, token model.GatewayToken) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO gateway_tokens
			(token_id, name, status, secret_sha256, secret_ciphertext, cipher_version, key_version,
			 created_at, updated_at, expires_at, revoked_at, last_used_at)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`,
			token.ID, token.Name, token.Status, token.Digest, token.Ciphertext, token.KeyVersion,
			token.CreatedAt, token.UpdatedAt, token.ExpiresAt, token.RevokedAt, token.LastUsedAt)
		if err != nil {
			if isConstraint(err) {
				return ErrConflict
			}
			return fmt.Errorf("创建 gateway token: %w", err)
		}
		return nil
	})
}

func (s *Store) Token(ctx context.Context, id string) (model.GatewayToken, error) {
	row := s.db.QueryRowContext(ctx, tokenSelect+" WHERE token_id = ?", id)
	return scanToken(row)
}

func (s *Store) TokenByDigest(ctx context.Context, digest []byte) (model.GatewayToken, error) {
	row := s.db.QueryRowContext(ctx, tokenSelect+" WHERE secret_sha256 = ?", digest)
	token, err := scanToken(row)
	if err != nil {
		return model.GatewayToken{}, err
	}
	now := time.Now().UnixMilli()
	if token.Status != "active" || token.RevokedAt != nil || (token.ExpiresAt != nil && *token.ExpiresAt <= now) {
		return model.GatewayToken{}, ErrInactiveToken
	}
	return token, nil
}

func (s *Store) Tokens(ctx context.Context) ([]model.GatewayToken, error) {
	rows, err := s.db.QueryContext(ctx, tokenSelect+" ORDER BY token_id")
	if err != nil {
		return nil, fmt.Errorf("查询 gateway tokens: %w", err)
	}
	defer rows.Close()
	items := []model.GatewayToken{}
	for rows.Next() {
		item, scanErr := scanToken(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 gateway tokens: %w", err)
	}
	return items, nil
}

func (s *Store) RotateToken(ctx context.Context, id string, digest, ciphertext []byte, keyVersion int) error {
	now := time.Now().UnixMilli()
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE gateway_tokens SET secret_sha256 = ?, secret_ciphertext = ?,
			key_version = ?, updated_at = ? WHERE token_id = ? AND status = 'active'`,
			digest, ciphertext, keyVersion, now, id)
		if err != nil {
			if isConstraint(err) {
				return ErrConflict
			}
			return fmt.Errorf("轮换 gateway token: %w", err)
		}
		return requireOne(result)
	})
}

func (s *Store) RevokeToken(ctx context.Context, id string) error {
	now := time.Now().UnixMilli()
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE gateway_tokens SET status = 'revoked', revoked_at = ?, updated_at = ?
			WHERE token_id = ? AND status = 'active'`, now, now, id)
		if err != nil {
			return fmt.Errorf("吊销 gateway token: %w", err)
		}
		return requireOne(result)
	})
}

func (s *Store) DeleteToken(ctx context.Context, id string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM gateway_tokens WHERE token_id = ? AND status = 'revoked'", id)
		if err != nil {
			if isConstraint(err) {
				return ErrConflict
			}
			return fmt.Errorf("删除 gateway token: %w", err)
		}
		return requireOne(result)
	})
}

const tokenSelect = `SELECT token_id, name, status, secret_sha256, secret_ciphertext, key_version,
	created_at, updated_at, expires_at, revoked_at, last_used_at FROM gateway_tokens`

func scanToken(row rowScanner) (model.GatewayToken, error) {
	var item model.GatewayToken
	var expiresAt, revokedAt, lastUsedAt sql.NullInt64
	if err := row.Scan(
		&item.ID, &item.Name, &item.Status, &item.Digest, &item.Ciphertext, &item.KeyVersion,
		&item.CreatedAt, &item.UpdatedAt, &expiresAt, &revokedAt, &lastUsedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.GatewayToken{}, ErrNotFound
		}
		return model.GatewayToken{}, fmt.Errorf("读取 gateway token: %w", err)
	}
	item.ExpiresAt = nullableInt64(expiresAt)
	item.RevokedAt = nullableInt64(revokedAt)
	item.LastUsedAt = nullableInt64(lastUsedAt)
	return item, nil
}
