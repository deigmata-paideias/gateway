//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	dataURL  = "http://127.0.0.1:8080"
	adminURL = "http://127.0.0.1:9090"
	mockURL  = "http://127.0.0.1:18080"
)

func TestGatewayComposeE2E(t *testing.T) {
	if os.Getenv("AI_GATEWAY_E2E") != "1" {
		t.Skip("设置 AI_GATEWAY_E2E=1 后运行 Docker Compose E2E")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	project := fmt.Sprintf("ai-gateway-e2e-%d", time.Now().UnixNano())
	harness := composeHarness{root: root, project: project}
	if output, err := harness.run(t.Context(), "config", "--quiet"); err != nil {
		t.Fatalf("compose config: %v\n%s", err, output)
	}
	t.Cleanup(func() {
		_, _ = harness.run(context.Background(), "down", "--volumes", "--remove-orphans")
	})
	t.Cleanup(func() {
		output, _ := harness.run(context.Background(), "logs", "--no-color")
		logDirectory := filepath.Join(root, "build-logs")
		_ = os.MkdirAll(logDirectory, 0o755)
		_ = os.WriteFile(filepath.Join(logDirectory, "compose-e2e.log"), []byte(output), 0o600)
	})
	if output, err := harness.run(t.Context(), "up", "--build", "--detach", "--wait", "--wait-timeout", "60"); err != nil {
		t.Fatalf("compose up: %v\n%s", err, output)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	unauthenticated := do(t, client, http.MethodGet, dataURL+"/v1/models", "", nil, nil)
	assertStatus(t, unauthenticated, http.StatusUnauthorized)

	created := do(t, client, http.MethodPost, adminURL+"/admin/v1/tokens", "", map[string]any{"name": "e2e-client"}, nil)
	assertStatus(t, created, http.StatusCreated)
	var tokenResult struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	decode(t, created.body, &tokenResult)
	if tokenResult.ID == "" || tokenResult.Token == "" {
		t.Fatalf("create token response = %s", created.body)
	}
	token := tokenResult.Token

	models := do(t, client, http.MethodGet, dataURL+"/v1/models", token, nil, nil)
	assertStatus(t, models, http.StatusOK)
	assertContains(t, models.body, "chat-default", "responses-default", "image-default")

	chat := do(t, client, http.MethodPost, dataURL+"/v1/chat/completions", token, map[string]any{
		"model": "chat-default", "messages": []any{map[string]string{"role": "user", "content": "hello"}},
	}, nil)
	assertStatus(t, chat, http.StatusOK)
	assertHeader(t, chat, "X-AI-Gateway-Backend", "openai-mock")
	assertContains(t, chat.body, "mock reply", `"model":"chat-default"`)
	chatStream := do(t, client, http.MethodPost, dataURL+"/v1/chat/completions", token, map[string]any{
		"model": "chat-default", "messages": []any{}, "stream": true,
	}, nil)
	assertStatus(t, chatStream, http.StatusOK)
	assertContains(t, chatStream.body, "data: [DONE]", `"model":"chat-default"`)

	response := do(t, client, http.MethodPost, dataURL+"/v1/responses", token, map[string]any{
		"model": "responses-default", "input": "hello",
	}, nil)
	assertStatus(t, response, http.StatusOK)
	var responseResult struct {
		ID string `json:"id"`
	}
	decode(t, response.body, &responseResult)
	if !strings.HasPrefix(responseResult.ID, "resp_") || strings.Contains(string(response.body), "resp_mock_") {
		t.Fatalf("public response id 未改写: %s", response.body)
	}
	responseStream := do(t, client, http.MethodPost, dataURL+"/v1/responses", token, map[string]any{
		"model": "responses-default", "input": "stream", "stream": true,
	}, nil)
	assertStatus(t, responseStream, http.StatusOK)
	assertContains(t, responseStream.body, "event: response.completed", "resp_")

	image := do(t, client, http.MethodPost, dataURL+"/v1/images/generations", token, map[string]any{
		"model": "image-default", "prompt": "one pixel",
	}, nil)
	assertStatus(t, image, http.StatusOK)
	assertImageBase64(t, image.body)

	secondTokenResponse := do(t, client, http.MethodPost, adminURL+"/admin/v1/tokens", "", map[string]any{"name": "other-client"}, nil)
	assertStatus(t, secondTokenResponse, http.StatusCreated)
	var secondToken struct {
		Token string `json:"token"`
	}
	decode(t, secondTokenResponse.body, &secondToken)
	crossToken := do(t, client, http.MethodPost, dataURL+"/v1/responses", secondToken.Token, map[string]any{
		"model": "responses-default", "input": "continue", "previous_response_id": responseResult.ID,
	}, nil)
	assertStatus(t, crossToken, http.StatusNotFound)

	configResponse := do(t, client, http.MethodGet, adminURL+"/admin/v1/config", "", nil, nil)
	assertStatus(t, configResponse, http.StatusOK)
	var currentConfig struct {
		Revision int64 `json:"revision"`
	}
	decode(t, configResponse.body, &currentConfig)
	revision := switchBackend(t, client, currentConfig.Revision, "chat-default", "dashscope-mock")
	revision = switchBackend(t, client, revision, "responses-default", "dashscope-mock")
	revision = switchBackend(t, client, revision, "image-default", "dashscope-mock")

	dashChat := do(t, client, http.MethodPost, dataURL+"/v1/chat/completions", token, map[string]any{
		"model": "chat-default", "messages": []any{},
	}, nil)
	assertStatus(t, dashChat, http.StatusOK)
	assertHeader(t, dashChat, "X-AI-Gateway-Backend", "dashscope-mock")
	if dashChat.headers.Get("X-AI-Gateway-Revision") != fmt.Sprint(revision) {
		t.Fatalf("dynamic revision = %q, want %d", dashChat.headers.Get("X-AI-Gateway-Revision"), revision)
	}
	dashStream := do(t, client, http.MethodPost, dataURL+"/v1/chat/completions", token, map[string]any{
		"model": "chat-default", "messages": []any{}, "stream": true,
	}, nil)
	assertStatus(t, dashStream, http.StatusOK)
	assertHeader(t, dashStream, "X-AI-Gateway-Backend", "dashscope-mock")

	pinned := do(t, client, http.MethodPost, dataURL+"/v1/responses", token, map[string]any{
		"model": "responses-default", "input": "continue", "previous_response_id": responseResult.ID,
	}, nil)
	assertStatus(t, pinned, http.StatusOK)
	assertHeader(t, pinned, "X-AI-Gateway-Backend", "openai-mock")
	newDashResponse := do(t, client, http.MethodPost, dataURL+"/v1/responses", token, map[string]any{
		"model": "responses-default", "input": "new",
	}, nil)
	assertStatus(t, newDashResponse, http.StatusOK)
	assertHeader(t, newDashResponse, "X-AI-Gateway-Backend", "dashscope-mock")

	dashImage := do(t, client, http.MethodPost, dataURL+"/v1/images/generations", token, map[string]any{
		"model": "image-default", "prompt": "one pixel",
	}, nil)
	assertStatus(t, dashImage, http.StatusOK)
	assertImageBase64(t, dashImage.body)
	if strings.Contains(string(dashImage.body), `"url"`) {
		t.Fatalf("DashScope 图片未归一化: %s", dashImage.body)
	}

	setScenario(t, client, "429")
	rateLimited := do(t, client, http.MethodPost, dataURL+"/v1/chat/completions", token, map[string]any{
		"model": "chat-default", "messages": []any{},
	}, nil)
	assertStatus(t, rateLimited, http.StatusTooManyRequests)
	setScenario(t, client, "malformed_sse")
	brokenStream := do(t, client, http.MethodPost, dataURL+"/v1/chat/completions", token, map[string]any{
		"model": "chat-default", "messages": []any{}, "stream": true,
	}, nil)
	assertStatus(t, brokenStream, http.StatusOK)
	assertContains(t, brokenStream.body, "event: error", "stream_failed")

	audits := do(t, client, http.MethodGet, adminURL+"/admin/v1/audits?token_id="+tokenResult.ID+"&limit=100", "", nil, nil)
	assertStatus(t, audits, http.StatusOK)
	assertContains(t, audits.body, "chat.completions", "responses.create", "images.generate")
	usage := do(t, client, http.MethodGet, adminURL+"/admin/v1/tokens/"+tokenResult.ID+"/usage?group_by=operation", "", nil, nil)
	assertStatus(t, usage, http.StatusOK)
	assertContains(t, usage.body, "input_tokens", "total_tokens")

	if output, err := harness.run(t.Context(), "restart", "gateway"); err != nil {
		t.Fatalf("restart gateway: %v\n%s", err, output)
	}
	waitHTTP(t, client, 30*time.Second, func() bool {
		return doWithoutFail(client, http.MethodGet, dataURL+"/v1/models", token).status == http.StatusOK
	})
	persisted := do(t, client, http.MethodPost, dataURL+"/v1/responses", token, map[string]any{
		"model": "responses-default", "input": "after restart", "previous_response_id": responseResult.ID,
	}, nil)
	assertStatus(t, persisted, http.StatusOK)
	assertHeader(t, persisted, "X-AI-Gateway-Backend", "openai-mock")

	waitForCollectorEvidence(t, harness, token, secondToken.Token)

	rotated := do(t, client, http.MethodPost, adminURL+"/admin/v1/tokens/"+tokenResult.ID+"/rotate", "", map[string]any{}, nil)
	assertStatus(t, rotated, http.StatusOK)
	var rotatedToken struct {
		Token string `json:"token"`
	}
	decode(t, rotated.body, &rotatedToken)
	assertStatus(t, do(t, client, http.MethodGet, dataURL+"/v1/models", token, nil, nil), http.StatusUnauthorized)
	assertStatus(t, do(t, client, http.MethodGet, dataURL+"/v1/models", rotatedToken.Token, nil, nil), http.StatusOK)
	revoked := do(t, client, http.MethodPost, adminURL+"/admin/v1/tokens/"+tokenResult.ID+"/revoke", "", map[string]any{}, nil)
	assertStatus(t, revoked, http.StatusOK)
	assertStatus(t, do(t, client, http.MethodGet, dataURL+"/v1/models", rotatedToken.Token, nil, nil), http.StatusUnauthorized)
}

func switchBackend(t *testing.T, client *http.Client, revision int64, routeID, backendID string) int64 {
	t.Helper()
	response := do(t, client, http.MethodPut, adminURL+"/admin/v1/routes/"+routeID+"/active-backend", "", map[string]any{
		"backend_id": backendID,
	}, map[string]string{"If-Match": fmt.Sprint(revision)})
	assertStatus(t, response, http.StatusOK)
	var result struct {
		Revision int64 `json:"revision"`
	}
	decode(t, response.body, &result)
	return result.Revision
}

func setScenario(t *testing.T, client *http.Client, scenario string) {
	t.Helper()
	response := do(t, client, http.MethodPost, mockURL+"/__mock/scenarios/"+scenario, "", map[string]any{}, nil)
	assertStatus(t, response, http.StatusOK)
}

func waitForCollectorEvidence(t *testing.T, harness composeHarness, secrets ...string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		logs, _ := harness.run(t.Context(), "logs", "--no-color", "otel-collector")
		if strings.Contains(logs, "ai_gateway.provider.request.count") && strings.Contains(logs, "gen_ai.client.token.usage") {
			for _, value := range secrets {
				if value != "" && strings.Contains(logs, value) {
					t.Fatalf("Collector 日志泄露 Gateway Token")
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Collector 日志未出现网关指标，最后日志：\n%s", logs)
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-ticker.C:
		}
	}
}

func waitHTTP(t *testing.T, client *http.Client, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("等待 HTTP 服务超时")
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-ticker.C:
		}
	}
}

type composeHarness struct {
	root    string
	project string
}

func (h composeHarness) run(ctx context.Context, arguments ...string) (string, error) {
	args := []string{
		"compose", "--project-name", h.project,
		"--file", filepath.Join(h.root, "deploy", "compose.yaml"),
		"--file", filepath.Join(h.root, "deploy", "compose.e2e.yaml"),
	}
	args = append(args, arguments...)
	command := exec.CommandContext(ctx, "docker", args...)
	command.Dir = h.root
	output, err := command.CombinedOutput()
	return string(output), err
}

type httpResult struct {
	status  int
	headers http.Header
	body    []byte
}

func do(
	t *testing.T,
	client *http.Client,
	method,
	url,
	token string,
	body any,
	headers map[string]string,
) httpResult {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return httpResult{status: response.StatusCode, headers: response.Header.Clone(), body: data}
}

func doWithoutFail(client *http.Client, method, url, token string) httpResult {
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		return httpResult{}
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return httpResult{}
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return httpResult{status: response.StatusCode, headers: response.Header.Clone(), body: data}
}

func assertStatus(t *testing.T, result httpResult, want int) {
	t.Helper()
	if result.status != want {
		t.Fatalf("status = %d, want %d, body=%s", result.status, want, result.body)
	}
}

func assertHeader(t *testing.T, result httpResult, key, want string) {
	t.Helper()
	if got := result.headers.Get(key); got != want {
		t.Fatalf("header %s = %q, want %q", key, got, want)
	}
}

func assertContains(t *testing.T, body []byte, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(string(body), value) {
			t.Fatalf("body %s 不包含 %q", body, value)
		}
	}
}

func assertImageBase64(t *testing.T, body []byte) {
	t.Helper()
	var response struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	decode(t, body, &response)
	if len(response.Data) != 1 || response.Data[0].B64JSON == "" || response.Data[0].URL != "" {
		t.Fatalf("image response = %s", body)
	}
	if _, err := base64.StdEncoding.DecodeString(response.Data[0].B64JSON); err != nil {
		t.Fatalf("b64_json 无效: %v", err)
	}
}

func decode(t *testing.T, body []byte, value any) {
	t.Helper()
	if err := json.Unmarshal(body, value); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}
