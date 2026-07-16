package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/deigmata-paideias/gateway/internal/gateway"
	"github.com/deigmata-paideias/gateway/internal/provider"
)

type dataAPI struct {
	service      *gateway.Service
	maxBodyBytes int64
}

type requestMetadata struct {
	Model              string `json:"model"`
	Stream             bool   `json:"stream"`
	PreviousResponseID string `json:"previous_response_id"`
	Background         bool   `json:"background"`
}

func NewDataHandler(service *gateway.Service) http.Handler {
	limits := service.CurrentSnapshot().Config().Limits
	api := &dataAPI{service: service, maxBodyBytes: limits.RequestBodyBytes}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", api.chat)
	mux.HandleFunc("POST /v1/responses", api.responses)
	mux.HandleFunc("POST /v1/images/generations", api.image)
	mux.HandleFunc("GET /v1/models", api.models)
	mux.HandleFunc("GET /v1/token", api.token)
	return requestIDMiddleware(authMiddleware(service, mux))
}

func (a *dataAPI) chat(w http.ResponseWriter, r *http.Request) {
	body, metadata, err := a.readMetadata(r)
	if err != nil {
		writeError(w, err)
		return
	}
	input := gateway.CallInput{
		Token: tokenFrom(r.Context()), RequestID: requestIDFrom(r.Context()),
		ModelAlias: metadata.Model, Body: body, Stream: metadata.Stream,
	}
	if metadata.Stream {
		stream, streamErr := a.service.ChatStream(r.Context(), input)
		if streamErr != nil {
			writeError(w, streamErr)
			return
		}
		a.writeStream(w, stream, true)
		return
	}
	result, err := a.service.Chat(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeCallResult(w, result)
}

func (a *dataAPI) responses(w http.ResponseWriter, r *http.Request) {
	body, metadata, err := a.readMetadata(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if metadata.Background {
		writeError(w, &gateway.Error{
			Status: http.StatusUnprocessableEntity, Code: "unsupported_responses_parameter",
			Message: "暂不支持 background Responses", Err: errors.New("background=true 不受支持"),
		})
		return
	}
	input := gateway.CallInput{
		Token: tokenFrom(r.Context()), RequestID: requestIDFrom(r.Context()),
		ModelAlias: metadata.Model, Body: body, Stream: metadata.Stream,
		PreviousID: metadata.PreviousResponseID,
	}
	if metadata.Stream {
		stream, streamErr := a.service.ResponsesStream(r.Context(), input)
		if streamErr != nil {
			writeError(w, streamErr)
			return
		}
		a.writeStream(w, stream, false)
		return
	}
	result, err := a.service.Responses(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeCallResult(w, result)
}

func (a *dataAPI) image(w http.ResponseWriter, r *http.Request) {
	body, metadata, err := a.readMetadata(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if metadata.Stream {
		writeError(w, &gateway.Error{
			Status: http.StatusUnprocessableEntity, Code: "unsupported_image_parameter",
			Message: "图片接口不支持流式响应", Err: errors.New("stream=true 不受支持"),
		})
		return
	}
	result, err := a.service.Image(r.Context(), gateway.CallInput{
		Token: tokenFrom(r.Context()), RequestID: requestIDFrom(r.Context()),
		ModelAlias: metadata.Model, Body: body,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeCallResult(w, result)
}

func (a *dataAPI) models(w http.ResponseWriter, r *http.Request) {
	result, err := a.service.Models(r.Context(), tokenFrom(r.Context()), requestIDFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	models := make([]map[string]any, 0, len(result.Value))
	for _, route := range result.Value {
		models = append(models, map[string]any{
			"id": route.ModelAlias, "object": "model", "owned_by": "ai-gateway", "capability": route.Operation,
		})
	}
	w.Header().Set("X-AI-Gateway-Revision", fmt.Sprint(result.Revision))
	w.Header().Set("X-AI-Gateway-Audit-ID", result.AuditID)
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
}

func (a *dataAPI) token(w http.ResponseWriter, r *http.Request) {
	result, err := a.service.CurrentToken(r.Context(), tokenFrom(r.Context()), requestIDFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("X-AI-Gateway-Revision", fmt.Sprint(result.Revision))
	w.Header().Set("X-AI-Gateway-Audit-ID", result.AuditID)
	writeJSON(w, http.StatusOK, tokenView(result.Value))
}

func (a *dataAPI) readMetadata(r *http.Request) ([]byte, requestMetadata, error) {
	body, err := readBody(r, a.maxBodyBytes)
	if err != nil {
		return nil, requestMetadata{}, err
	}
	var metadata requestMetadata
	if err := json.Unmarshal(body, &metadata); err != nil || strings.TrimSpace(metadata.Model) == "" {
		return nil, requestMetadata{}, &gateway.Error{
			Status: http.StatusUnprocessableEntity, Code: "invalid_model",
			Message: "model 必须是非空模型别名", Err: err,
		}
	}
	return body, metadata, nil
}

func writeCallResult(w http.ResponseWriter, result gateway.CallResult) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-AI-Gateway-Revision", fmt.Sprint(result.Revision))
	w.Header().Set("X-AI-Gateway-Backend", result.BackendID)
	w.Header().Set("X-AI-Gateway-Audit-ID", result.AuditID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Body)
}

func (a *dataAPI) writeStream(w http.ResponseWriter, stream *gateway.CallStream, chat bool) {
	defer stream.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, &gateway.Error{
			Status: http.StatusInternalServerError, Code: "streaming_unsupported",
			Message: "当前 HTTP Writer 不支持流式响应", Err: errors.New("缺少 http flusher"),
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-AI-Gateway-Revision", fmt.Sprint(stream.Revision()))
	w.Header().Set("X-AI-Gateway-Backend", stream.BackendID())
	w.Header().Set("X-AI-Gateway-Audit-ID", stream.AuditID())
	w.WriteHeader(http.StatusOK)
	writer := bufio.NewWriter(w)
	for stream.Next() {
		event := stream.Current()
		if event.Type != "" {
			_, _ = writer.WriteString("event: " + event.Type + "\n")
		}
		_, _ = writer.WriteString("data: ")
		_, _ = writer.Write(bytes.TrimSpace(event.Data))
		_, _ = writer.WriteString("\n\n")
		_ = writer.Flush()
		flusher.Flush()
	}
	if err := stream.Err(); err != nil {
		encoded, _ := json.Marshal(errorBody{Error: errorDetail{
			Message: "流式响应中断", Type: "gateway_error", Code: "stream_failed",
		}})
		_, _ = writer.WriteString("event: error\ndata: ")
		_, _ = writer.Write(encoded)
		_, _ = writer.WriteString("\n\n")
	} else if chat {
		_, _ = writer.WriteString("data: [DONE]\n\n")
	}
	_ = writer.Flush()
	flusher.Flush()
}

var _ provider.Event
