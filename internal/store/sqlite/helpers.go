package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
)

func requireOne(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取 sqlite rows affected: %w", err)
	}
	if rows != 1 {
		return ErrNotFound
	}
	return nil
}

func isConstraint(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "constraint") || strings.Contains(message, "unique")
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullPositiveInt(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullPositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}
