package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/deigmata-paideias/gateway/internal/gateway"
	"github.com/deigmata-paideias/gateway/internal/identity"
)

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" || len(requestID) > 128 {
			var err error
			requestID, err = identity.New("req_")
			if err != nil {
				writeError(w, &gateway.Error{
					Status: http.StatusInternalServerError, Code: "internal_error",
					Message: "无法生成请求 ID", Err: err,
				})
				return
			}
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(withRequestID(r.Context(), requestID)))
	})
}

func authMiddleware(service *gateway.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 || len(values[0]) > 512 {
			writeInvalidToken(w)
			return
		}
		parts := strings.SplitN(values[0], " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) != parts[1] {
			writeInvalidToken(w)
			return
		}
		token, err := service.Authenticate(r.Context(), parts[1])
		if err != nil {
			writeError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(withToken(r.Context(), token)))
	})
}

func writeInvalidToken(w http.ResponseWriter) {
	writeError(w, &gateway.Error{
		Status: http.StatusUnauthorized, Code: "invalid_gateway_token",
		Message: "Gateway Token 无效", Err: errors.New("authorization header 无效"),
	})
}
