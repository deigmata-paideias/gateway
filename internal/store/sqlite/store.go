// Package sqlite 实现网关的单文件事务存储。
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound      = errors.New("sqlite: not found")
	ErrConflict      = errors.New("sqlite: conflict")
	ErrInactiveToken = errors.New("sqlite: inactive token")
	ErrAuditState    = errors.New("sqlite: invalid audit state")

	//go:embed migrations/*.sql
	migrations embed.FS
)

type Options struct {
	Path         string
	MaxOpenConns int
	BusyTimeout  time.Duration
}

type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

func Open(ctx context.Context, options Options) (*Store, error) {
	if options.Path == "" {
		return nil, errors.New("sqlite: path is required")
	}
	if options.MaxOpenConns < 1 {
		options.MaxOpenConns = 8
	}
	if options.BusyTimeout <= 0 {
		options.BusyTimeout = 5 * time.Second
	}
	dsn, err := makeDSN(options.Path, options.BusyTimeout)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 sqlite: %w", err)
	}
	db.SetMaxOpenConns(options.MaxOpenConns)
	db.SetMaxIdleConns(options.MaxOpenConns)
	store := &Store{db: db}
	if err := store.initialize(ctx); err != nil {
		closeErr := db.Close()
		return nil, errors.Join(err, closeErr)
	}
	return store, nil
}

func makeDSN(path string, busyTimeout time.Duration) (string, error) {
	base := path
	if !strings.HasPrefix(path, "file:") {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("规范化 sqlite path: %w", err)
		}
		base = (&url.URL{Scheme: "file", Path: absolute}).String()
	}
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	milliseconds := max(busyTimeout.Milliseconds(), 1)
	pragmas := []string{
		"_pragma=foreign_keys(1)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(FULL)",
		"_pragma=busy_timeout(" + strconv.FormatInt(milliseconds, 10) + ")",
		"_txlock=immediate",
		"_time_integer_format=unix_milli",
		"_timezone=UTC",
		"_dqs=false",
	}
	return base + separator + strings.Join(pragmas, "&"), nil
}

func (s *Store) initialize(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("连接 sqlite: %w", err)
	}
	if err := s.verifyPragmas(ctx); err != nil {
		return err
	}
	if err := s.applyMigrations(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Store) verifyPragmas(ctx context.Context) error {
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("读取 foreign_keys: %w", err)
	}
	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("读取 journal_mode: %w", err)
	}
	var synchronous int
	if err := s.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("读取 synchronous: %w", err)
	}
	if foreignKeys != 1 || !strings.EqualFold(journalMode, "wal") || synchronous != 2 {
		return fmt.Errorf("sqlite pragma 未生效: foreign_keys=%d journal_mode=%s synchronous=%d", foreignKeys, journalMode, synchronous)
	}
	return nil
}

func (s *Store) applyMigrations(ctx context.Context) error {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("读取 migration: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum BLOB NOT NULL,
			applied_at INTEGER NOT NULL
		)`); err != nil {
			return fmt.Errorf("创建 migration 表: %w", err)
		}
		for index, entry := range entries {
			version := index + 1
			data, readErr := migrations.ReadFile("migrations/" + entry.Name())
			if readErr != nil {
				return fmt.Errorf("读取 migration %q: %w", entry.Name(), readErr)
			}
			checksum := sha256.Sum256(data)
			var existing []byte
			scanErr := tx.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version = ?", version).Scan(&existing)
			switch {
			case scanErr == nil:
				if !equalBytes(existing, checksum[:]) {
					return fmt.Errorf("migration %d 校验和不匹配", version)
				}
				continue
			case !errors.Is(scanErr, sql.ErrNoRows):
				return fmt.Errorf("查询 migration %d: %w", version, scanErr)
			}
			if _, execErr := tx.ExecContext(ctx, string(data)); execErr != nil {
				return fmt.Errorf("执行 migration %d: %w", version, execErr)
			}
			if _, execErr := tx.ExecContext(
				ctx,
				"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
				version,
				entry.Name(),
				checksum[:],
				time.Now().UnixMilli(),
			); execErr != nil {
				return fmt.Errorf("记录 migration %d: %w", version, execErr)
			}
		}
		return nil
	})
}

func (s *Store) withWriteTx(ctx context.Context, fn func(*sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始 sqlite 事务: %w", err)
	}
	if err := fn(tx); err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("回滚 sqlite 事务: %w", rollbackErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 sqlite 事务: %w", err)
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite ping: %w", err)
	}
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("sqlite quick_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("sqlite quick_check: %s", result)
	}
	return nil
}

func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("关闭 sqlite: %w", err)
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}
