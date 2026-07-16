// Package api 实现网关的数据面、管理面和运维面 HTTP 适配器。
package api

import (
	"context"

	"github.com/deigmata-paideias/gateway/internal/model"
)

type contextKey int

const (
	requestIDKey contextKey = iota
	tokenKey
)

func withRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

func requestIDFrom(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func withToken(ctx context.Context, token model.GatewayToken) context.Context {
	return context.WithValue(ctx, tokenKey, token)
}

func tokenFrom(ctx context.Context) model.GatewayToken {
	value, _ := ctx.Value(tokenKey).(model.GatewayToken)
	return value
}
