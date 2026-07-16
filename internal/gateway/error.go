// Package gateway 编排鉴权、路由、Provider 调用和 Token 粒度审计。
package gateway

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/provider"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

type Error struct {
	Status  int
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	return fmt.Sprintf("gateway error: status=%d code=%s: %v", e.Status, e.Code, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

func newError(status int, code, message string, err error) *Error {
	return &Error{Status: status, Code: code, Message: message, Err: err}
}

func mapProviderError(err error) *Error {
	var providerError *provider.Error
	if errors.As(err, &providerError) {
		status := http.StatusBadGateway
		if providerError.Status == http.StatusTooManyRequests {
			status = http.StatusTooManyRequests
		}
		if providerError.Status == http.StatusUnauthorized || providerError.Status == http.StatusForbidden {
			status = http.StatusBadGateway
		}
		return newError(status, providerError.Code, "模型服务调用失败", err)
	}
	if errors.Is(err, provider.ErrInvalidRequest) {
		return newError(http.StatusUnprocessableEntity, "invalid_request", "请求参数不受支持", err)
	}
	if errors.Is(err, provider.ErrImageTooLarge) {
		return newError(http.StatusBadGateway, "image_too_large", "模型返回的图片超过大小限制", err)
	}
	if errors.Is(err, provider.ErrInvalidResponse) || errors.Is(err, provider.ErrUnsupportedImage) {
		return newError(http.StatusBadGateway, "invalid_upstream_response", "模型服务返回无效响应", err)
	}
	return newError(http.StatusBadGateway, "upstream_unavailable", "模型服务暂不可用", err)
}

func mapStoreError(err error) *Error {
	switch {
	case errors.Is(err, sqlite.ErrNotFound):
		return newError(http.StatusNotFound, "not_found", "资源不存在", err)
	case errors.Is(err, sqlite.ErrConflict):
		return newError(http.StatusConflict, "revision_conflict", "资源状态冲突", err)
	default:
		return newError(http.StatusServiceUnavailable, "storage_unavailable", "存储暂不可用", err)
	}
}

func mapRouteError(err error) *Error {
	if errors.Is(err, config.ErrRouteNotFound) || errors.Is(err, config.ErrBackendNotFound) {
		return newError(http.StatusNotFound, "model_not_found", "模型别名不可用", err)
	}
	return newError(http.StatusUnprocessableEntity, "invalid_configuration", "网关配置无效", err)
}
