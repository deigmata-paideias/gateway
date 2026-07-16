package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/deigmata-paideias/gateway/internal/gateway"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

func writeError(w http.ResponseWriter, err error) {
	var gatewayError *gateway.Error
	if !errors.As(err, &gatewayError) {
		gatewayError = &gateway.Error{
			Status: http.StatusInternalServerError, Code: "internal_error",
			Message: "网关内部错误", Err: err,
		}
	}
	writeJSON(w, gatewayError.Status, errorBody{Error: errorDetail{
		Message: gatewayError.Message,
		Type:    "gateway_error",
		Code:    gatewayError.Code,
	}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusNoContent {
		return
	}
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

func decodeJSON(r *http.Request, limit int64, dst any) error {
	reader := http.MaxBytesReader(nil, r.Body, limit)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return &gateway.Error{
			Status: http.StatusBadRequest, Code: "invalid_json", Message: "请求 JSON 无效", Err: err,
		}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &gateway.Error{
			Status: http.StatusBadRequest, Code: "invalid_json", Message: "请求只能包含一个 JSON 对象", Err: err,
		}
	}
	return nil
}

func readBody(r *http.Request, limit int64) ([]byte, error) {
	reader := http.MaxBytesReader(nil, r.Body, limit)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, &gateway.Error{
			Status: http.StatusRequestEntityTooLarge, Code: "request_too_large",
			Message: "请求体超过大小限制", Err: err,
		}
	}
	if len(data) == 0 || !json.Valid(data) {
		return nil, &gateway.Error{
			Status: http.StatusBadRequest, Code: "invalid_json", Message: "请求 JSON 无效", Err: errors.New("空或非法 json"),
		}
	}
	return data, nil
}
