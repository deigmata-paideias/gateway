package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var onePixelPNG = mustDecode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")

type mockServer struct {
	mu       sync.Mutex
	scenario string
	nextID   int64
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		if err := checkHealth(); err != nil {
			slog.Error("mock provider 未就绪", "error", err)
			os.Exit(1)
		}
		return
	}
	address := flag.String("listen", "0.0.0.0:18080", "监听地址")
	flag.Parse()
	server := &mockServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__mock/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /__mock/scenarios/{name}", server.setScenario)
	mux.HandleFunc("GET /objects/{id}", server.object)
	mux.HandleFunc("POST /openai/v1/chat/completions", server.chat)
	mux.HandleFunc("POST /dashscope/v1/chat/completions", server.chat)
	mux.HandleFunc("POST /openai/v1/responses", server.responses)
	mux.HandleFunc("POST /dashscope/v1/responses", server.responses)
	mux.HandleFunc("POST /openai/v1/images/generations", server.image)
	mux.HandleFunc("POST /dashscope/api/v1/services/aigc/multimodal-generation/generation", server.image)
	slog.Info("mock provider 启动", "address", *address)
	if err := http.ListenAndServe(*address, mux); err != nil {
		slog.Error("mock provider 退出", "error", err)
		os.Exit(1)
	}
}

func checkHealth() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:18080/__mock/healthz", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", response.StatusCode)
	}
	return nil
}

func (s *mockServer) setScenario(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.scenario = r.PathValue("name")
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"scenario": r.PathValue("name")})
}

func (s *mockServer) consumeScenario() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.scenario
	s.scenario = ""
	return value
}

func (s *mockServer) before(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"message": "missing key", "code": "invalid_api_key"}})
		return false
	}
	switch s.consumeScenario() {
	case "401":
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"message": "unauthorized"}})
		return false
	case "429":
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{"message": "rate limited"}})
		return false
	case "500":
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"message": "mock failure"}})
		return false
	case "slow":
		time.Sleep(2 * time.Second)
	case "malformed":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{"))
		return false
	case "malformed_sse":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: not-json\n\n"))
		return false
	}
	return true
}

func (s *mockServer) chat(w http.ResponseWriter, r *http.Request) {
	if !s.before(w, r) {
		return
	}
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	id := s.id("chatcmpl_mock_")
	if request.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "", map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": request.Model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": "mock "}, "finish_reason": nil}},
		})
		writeSSE(w, "", map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": request.Model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "reply"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": request.Model,
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]string{"role": "assistant", "content": "mock reply"}, "finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
	})
}

func (s *mockServer) responses(w http.ResponseWriter, r *http.Request) {
	if !s.before(w, r) {
		return
	}
	var request struct {
		Model              string `json:"model"`
		Stream             bool   `json:"stream"`
		PreviousResponseID string `json:"previous_response_id"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	id := s.id("resp_mock_")
	response := map[string]any{
		"id": id, "object": "response", "created_at": time.Now().Unix(), "status": "completed", "model": request.Model,
		"previous_response_id": emptyAsNil(request.PreviousResponseID),
		"output": []any{map[string]any{
			"id": s.id("msg_mock_"), "type": "message", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": "mock response", "annotations": []any{}}},
		}},
		"usage": map[string]any{
			"input_tokens": 4, "input_tokens_details": map[string]int{"cached_tokens": 1},
			"output_tokens": 3, "output_tokens_details": map[string]int{"reasoning_tokens": 1}, "total_tokens": 7,
		},
	}
	if request.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		created := map[string]any{"type": "response.created", "sequence_number": 0, "response": map[string]any{
			"id": id, "object": "response", "created_at": time.Now().Unix(), "status": "in_progress", "model": request.Model, "output": []any{},
		}}
		writeSSE(w, "response.created", created)
		writeSSE(w, "response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "sequence_number": 1, "item_id": "msg_mock", "output_index": 0, "content_index": 0, "delta": "mock response",
		})
		writeSSE(w, "response.completed", map[string]any{"type": "response.completed", "sequence_number": 2, "response": response})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *mockServer) image(w http.ResponseWriter, r *http.Request) {
	if !s.before(w, r) {
		return
	}
	var request struct {
		Model string `json:"model"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/dashscope/") {
		writeJSON(w, http.StatusOK, map[string]any{
			"request_id": s.id("image_mock_"),
			"output": map[string]any{"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message": map[string]any{
					"role":    "assistant",
					"content": []any{map[string]string{"image": "http://" + r.Host + "/objects/fixed.png"}},
				},
			}}},
			"usage": map[string]any{"image_count": 1, "width": 1, "height": 1},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": time.Now().Unix(),
		"data": []any{map[string]any{
			"b64_json": base64.StdEncoding.EncodeToString(onePixelPNG), "revised_prompt": "mock prompt",
		}},
		"usage": map[string]any{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3},
	})
}

func (s *mockServer) object(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(onePixelPNG)
}

func (s *mockServer) id(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	return fmt.Sprintf("%s%06d", prefix, s.nextID)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeSSE(w http.ResponseWriter, event string, value any) {
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	data, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func mustDecode(value string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return decoded
}

func emptyAsNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}
