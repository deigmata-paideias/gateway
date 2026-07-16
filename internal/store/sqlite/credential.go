package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/deigmata-paideias/gateway/internal/model"
)

func (s *Store) CreateCredential(ctx context.Context, credential model.Credential) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO provider_credentials
			(credential_id, provider, name, status, cipher_version, key_version, secret_ciphertext, created_at, updated_at, rotated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
			credential.ID, credential.Provider, credential.Name, credential.Status, credential.KeyVersion,
			credential.Ciphertext, credential.CreatedAt, credential.UpdatedAt, credential.RotatedAt)
		if err != nil {
			if isConstraint(err) {
				return ErrConflict
			}
			return fmt.Errorf("创建 credential: %w", err)
		}
		return nil
	})
}

func (s *Store) CreateCredentialIfMissing(ctx context.Context, credential model.Credential) (bool, error) {
	created := false
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO provider_credentials
			(credential_id, provider, name, status, cipher_version, key_version, secret_ciphertext, created_at, updated_at, rotated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
			credential.ID, credential.Provider, credential.Name, credential.Status, credential.KeyVersion,
			credential.Ciphertext, credential.CreatedAt, credential.UpdatedAt, credential.RotatedAt)
		if err != nil {
			return fmt.Errorf("导入 credential: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("读取 credential 导入结果: %w", err)
		}
		created = rows == 1
		return nil
	})
	return created, err
}

func (s *Store) Credential(ctx context.Context, id string) (model.Credential, error) {
	row := s.db.QueryRowContext(ctx, `SELECT credential_id, provider, name, status, secret_ciphertext,
		key_version, created_at, updated_at, rotated_at FROM provider_credentials WHERE credential_id = ?`, id)
	return scanCredential(row)
}

func (s *Store) Credentials(ctx context.Context) ([]model.Credential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT credential_id, provider, name, status, secret_ciphertext,
		key_version, created_at, updated_at, rotated_at FROM provider_credentials ORDER BY credential_id`)
	if err != nil {
		return nil, fmt.Errorf("查询 credentials: %w", err)
	}
	defer rows.Close()
	items := []model.Credential{}
	for rows.Next() {
		item, scanErr := scanCredential(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 credentials: %w", err)
	}
	return items, nil
}

func (s *Store) RotateCredential(ctx context.Context, id string, ciphertext []byte, keyVersion int) error {
	now := time.Now().UnixMilli()
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE provider_credentials SET secret_ciphertext = ?, key_version = ?,
			updated_at = ?, rotated_at = ? WHERE credential_id = ?`, ciphertext, keyVersion, now, now, id)
		if err != nil {
			return fmt.Errorf("轮换 credential: %w", err)
		}
		return requireOne(result)
	})
}

func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM provider_credentials WHERE credential_id = ?", id)
		if err != nil {
			return fmt.Errorf("删除 credential: %w", err)
		}
		return requireOne(result)
	})
}

func scanCredential(row rowScanner) (model.Credential, error) {
	var item model.Credential
	var rotatedAt sql.NullInt64
	if err := row.Scan(
		&item.ID, &item.Provider, &item.Name, &item.Status, &item.Ciphertext,
		&item.KeyVersion, &item.CreatedAt, &item.UpdatedAt, &rotatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Credential{}, ErrNotFound
		}
		return model.Credential{}, fmt.Errorf("读取 credential: %w", err)
	}
	item.RotatedAt = nullableInt64(rotatedAt)
	return item, nil
}
