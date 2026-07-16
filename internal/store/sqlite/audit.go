package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/deigmata-paideias/gateway/internal/model"
)

func (s *Store) StartAudit(ctx context.Context, audit model.Audit) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO request_audits
			(audit_id, request_id, gateway_token_id, operation, model_alias, backend_id, provider,
			 config_revision, public_response_id, stream, status, fallback_count, started_at, trace_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'started', ?, ?, ?)`,
			audit.ID, audit.RequestID, audit.GatewayTokenID, audit.Operation, nullString(audit.ModelAlias),
			nullString(audit.BackendID), nullString(audit.Provider), audit.ConfigRevision,
			nullString(audit.PublicResponseID), audit.Stream, audit.FallbackCount, audit.StartedAt, nullString(audit.TraceID))
		if err != nil {
			return fmt.Errorf("创建请求审计: %w", err)
		}
		result, err := tx.ExecContext(ctx, "UPDATE gateway_tokens SET last_used_at = ? WHERE token_id = ?", audit.StartedAt, audit.GatewayTokenID)
		if err != nil {
			return fmt.Errorf("更新 token last_used_at: %w", err)
		}
		return requireOne(result)
	})
}

func (s *Store) FinishAudit(ctx context.Context, finish model.AuditFinish) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE request_audits SET
			status = ?, http_status = ?, upstream_status = ?, error_code = ?, public_response_id = ?,
			input_tokens = ?, cached_input_tokens = ?, output_tokens = ?, reasoning_output_tokens = ?, total_tokens = ?,
			image_count = ?, raw_image_bytes = ?, finished_at = ?, duration_ms = ?, upstream_duration_ms = ?,
			time_to_first_token_ms = ? WHERE audit_id = ? AND status = 'started'`,
			finish.Status, nullPositiveInt(finish.HTTPStatus), nullPositiveInt(finish.UpstreamStatus), nullString(finish.ErrorCode),
			nullString(finish.PublicResponseID), finish.Usage.InputTokens, finish.Usage.CachedInputTokens,
			finish.Usage.OutputTokens, finish.Usage.ReasoningOutputTokens, finish.Usage.TotalTokens,
			finish.Usage.ImageCount, finish.Usage.RawImageBytes, finish.FinishedAt, finish.DurationMillis,
			nullPositiveInt64(finish.UpstreamDurationMillis), nullPositiveInt64(finish.TimeToFirstTokenMillis), finish.ID)
		if err != nil {
			return fmt.Errorf("完成请求审计: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("读取审计更新结果: %w", err)
		}
		if rows != 1 {
			return ErrAuditState
		}
		return nil
	})
}

func (s *Store) Audit(ctx context.Context, id string) (model.Audit, error) {
	row := s.db.QueryRowContext(ctx, auditSelect+" WHERE audit_id = ?", id)
	return scanAudit(row)
}

func (s *Store) Audits(ctx context.Context, filter model.AuditFilter) ([]model.Audit, error) {
	clauses := []string{"1 = 1"}
	args := []any{}
	add := func(value, column string) {
		if value != "" {
			clauses = append(clauses, column+" = ?")
			args = append(args, value)
		}
	}
	add(filter.TokenID, "gateway_token_id")
	add(filter.Operation, "operation")
	add(filter.ModelAlias, "model_alias")
	add(filter.BackendID, "backend_id")
	add(filter.Status, "status")
	if filter.From != nil {
		clauses = append(clauses, "started_at >= ?")
		args = append(args, *filter.From)
	}
	if filter.To != nil {
		clauses = append(clauses, "started_at < ?")
		args = append(args, *filter.To)
	}
	if filter.BeforeID != "" {
		clauses = append(clauses, "audit_id < ?")
		args = append(args, filter.BeforeID)
	}
	limit := filter.Limit
	if limit < 1 || limit > 500 {
		limit = 100
	}
	args = append(args, limit)
	query := auditSelect + " WHERE " + strings.Join(clauses, " AND ") + " ORDER BY started_at DESC, audit_id DESC LIMIT ?"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询请求审计: %w", err)
	}
	defer rows.Close()
	items := make([]model.Audit, 0, limit)
	for rows.Next() {
		item, scanErr := scanAudit(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历请求审计: %w", err)
	}
	return items, nil
}

func (s *Store) AggregateUsage(ctx context.Context, tokenID, groupBy string, from, to *int64) ([]model.UsageGroup, error) {
	expression := "operation"
	switch groupBy {
	case "day":
		expression = "strftime('%Y-%m-%d', started_at / 1000, 'unixepoch')"
	case "model":
		expression = "COALESCE(model_alias, '')"
	case "operation", "":
	default:
		return nil, fmt.Errorf("%w: invalid group_by", ErrConflict)
	}
	clauses := []string{"gateway_token_id = ?"}
	args := []any{tokenID}
	if from != nil {
		clauses = append(clauses, "started_at >= ?")
		args = append(args, *from)
	}
	if to != nil {
		clauses = append(clauses, "started_at < ?")
		args = append(args, *to)
	}
	query := `SELECT ` + expression + ` AS group_key,
		COUNT(*), SUM(status = 'succeeded'), SUM(status IN ('failed', 'cancelled', 'abandoned')),
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(cached_input_tokens), 0),
		COALESCE(SUM(output_tokens), 0), COALESCE(SUM(reasoning_output_tokens), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(image_count), 0),
		SUM(CASE WHEN input_tokens IS NULL AND output_tokens IS NULL AND total_tokens IS NULL AND image_count IS NULL THEN 1 ELSE 0 END)
		FROM request_audits WHERE ` + strings.Join(clauses, " AND ") + ` GROUP BY group_key ORDER BY group_key`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("聚合 token usage: %w", err)
	}
	defer rows.Close()
	groups := []model.UsageGroup{}
	for rows.Next() {
		var group model.UsageGroup
		if err := rows.Scan(
			&group.Key, &group.Requests, &group.Succeeded, &group.Failed,
			&group.InputTokens, &group.CachedInputTokens, &group.OutputTokens,
			&group.ReasoningOutputTokens, &group.TotalTokens, &group.Images,
			&group.UsageIncompleteRecords,
		); err != nil {
			return nil, fmt.Errorf("读取 token usage: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 token usage: %w", err)
	}
	return groups, nil
}

const auditSelect = `SELECT audit_id, request_id, gateway_token_id, operation, model_alias, backend_id,
	provider, config_revision, public_response_id, stream, status, http_status, upstream_status, error_code,
	fallback_count, input_tokens, cached_input_tokens, output_tokens, reasoning_output_tokens, total_tokens,
	image_count, raw_image_bytes, started_at, finished_at, duration_ms, upstream_duration_ms,
	time_to_first_token_ms, trace_id FROM request_audits`

func scanAudit(row rowScanner) (model.Audit, error) {
	var item model.Audit
	var modelAlias, backendID, provider, publicResponseID, errorCode, traceID sql.NullString
	var revision, input, cached, output, reasoning, total, images, rawBytes sql.NullInt64
	var httpStatus, upstreamStatus, finishedAt, duration, upstreamDuration, firstToken sql.NullInt64
	if err := row.Scan(
		&item.ID, &item.RequestID, &item.GatewayTokenID, &item.Operation, &modelAlias, &backendID,
		&provider, &revision, &publicResponseID, &item.Stream, &item.Status, &httpStatus, &upstreamStatus,
		&errorCode, &item.FallbackCount, &input, &cached, &output, &reasoning, &total, &images, &rawBytes,
		&item.StartedAt, &finishedAt, &duration, &upstreamDuration, &firstToken, &traceID,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Audit{}, ErrNotFound
		}
		return model.Audit{}, fmt.Errorf("读取请求审计: %w", err)
	}
	item.ModelAlias = modelAlias.String
	item.BackendID = backendID.String
	item.Provider = provider.String
	item.ConfigRevision = nullableInt64(revision)
	item.PublicResponseID = publicResponseID.String
	item.HTTPStatus = nullableInt(httpStatus)
	item.UpstreamStatus = nullableInt(upstreamStatus)
	item.ErrorCode = errorCode.String
	item.Usage = model.Usage{
		InputTokens: nullableInt64(input), CachedInputTokens: nullableInt64(cached), OutputTokens: nullableInt64(output),
		ReasoningOutputTokens: nullableInt64(reasoning), TotalTokens: nullableInt64(total), ImageCount: nullableInt64(images),
		RawImageBytes: nullableInt64(rawBytes),
	}
	item.FinishedAt = nullableInt64(finishedAt)
	item.DurationMillis = nullableInt64(duration)
	item.UpstreamDurationMillis = nullableInt64(upstreamDuration)
	item.TimeToFirstTokenMillis = nullableInt64(firstToken)
	item.TraceID = traceID.String
	return item, nil
}
