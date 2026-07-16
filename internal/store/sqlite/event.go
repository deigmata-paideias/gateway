package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

type AdminEvent struct {
	ID           string `json:"id"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	RequestID    string `json:"request_id"`
	Result       string `json:"result"`
	TraceID      string `json:"trace_id,omitempty"`
	CreatedAt    int64  `json:"created_at"`
}

func (s *Store) RecordAdminEvent(ctx context.Context, event AdminEvent) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO admin_events
			(event_id, action, resource_type, resource_id, request_id, result, trace_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.Action, event.ResourceType, event.ResourceID,
			event.RequestID, event.Result, nullString(event.TraceID), event.CreatedAt)
		if err != nil {
			return fmt.Errorf("记录管理事件: %w", err)
		}
		return nil
	})
}

func (s *Store) AdminEvents(ctx context.Context, limit int) ([]AdminEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, action, resource_type, resource_id,
		request_id, result, trace_id, created_at FROM admin_events ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询管理事件: %w", err)
	}
	defer rows.Close()
	items := make([]AdminEvent, 0, limit)
	for rows.Next() {
		var item AdminEvent
		var traceID sql.NullString
		if err := rows.Scan(&item.ID, &item.Action, &item.ResourceType, &item.ResourceID,
			&item.RequestID, &item.Result, &traceID, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("读取管理事件: %w", err)
		}
		item.TraceID = traceID.String
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历管理事件: %w", err)
	}
	return items, nil
}
