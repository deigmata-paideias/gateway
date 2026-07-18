package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/gateway"
	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/secret"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

func TestDataHandlerSyncEndpoints(t *testing.T) {
	t.Parallel()

	fixture := newAPIFixture(t)
	handler := NewDataHandler(fixture.service)

	chat := fixture.request(handler, http.MethodPost, "/v1/chat/completions", `{"model":"chat-model","messages":[]}`, true)
	assertStatus(t, chat, http.StatusOK)
	assertHeader(t, chat, "X-AI-Gateway-Backend", "dashscope-main")
	assertHeader(t, chat, "X-AI-Gateway-Revision", "1")
	if chat.Header().Get("X-AI-Gateway-Audit-ID") == "" || chat.Header().Get("X-Request-ID") == "" {
		t.Fatalf("chat headers = %#v", chat.Header())
	}
	assertBodyContains(t, chat, `"model":"chat-model"`)

	response := fixture.request(handler, http.MethodPost, "/v1/responses", `{"model":"response-model","input":"hi"}`, true)
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, `"model":"response-model"`)
	var responseBody map[string]any
	decodeResponse(t, response, &responseBody)
	publicID, _ := responseBody["id"].(string)
	if !strings.HasPrefix(publicID, "resp_") {
		t.Fatalf("response id = %q", publicID)
	}
	continued := fixture.request(handler, http.MethodPost, "/v1/responses", `{"model":"response-model","input":"continue","previous_response_id":"`+publicID+`"}`, true)
	assertStatus(t, continued, http.StatusOK)
	fixture.transport.mu.Lock()
	lastBody := append([]byte(nil), fixture.transport.lastBody...)
	fixture.transport.mu.Unlock()
	assertJSONValue(t, lastBody, "previous_response_id", "resp_upstream")

	image := fixture.request(handler, http.MethodPost, "/v1/images/generations", `{"model":"image-model","prompt":"cat"}`, true)
	assertStatus(t, image, http.StatusOK)
	assertBodyContains(t, image, "aW1n")

	models := fixture.request(handler, http.MethodGet, "/v1/models", "", true)
	assertStatus(t, models, http.StatusOK)
	assertHeader(t, models, "X-AI-Gateway-Revision", "1")
	assertBodyContains(t, models, `"object":"list"`)
	assertBodyContains(t, models, `"id":"chat-model"`)

	token := fixture.request(handler, http.MethodGet, "/v1/token", "", true)
	assertStatus(t, token, http.StatusOK)
	assertBodyContains(t, token, `"id":"`+fixture.token.ID+`"`)
	if strings.Contains(token.Body.String(), fixture.rawToken) || strings.Contains(token.Body.String(), "cipher") {
		t.Fatal("token 查询泄露了 secret")
	}
}

func TestDataHandlerStreams(t *testing.T) {
	t.Parallel()

	fixture := newAPIFixture(t)
	handler := NewDataHandler(fixture.service)
	chat := fixture.request(handler, http.MethodPost, "/v1/chat/completions", `{"model":"chat-model","messages":[],"stream":true}`, true)
	assertStatus(t, chat, http.StatusOK)
	assertHeader(t, chat, "Content-Type", "text/event-stream")
	assertBodyContains(t, chat, "data: [DONE]")
	assertBodyContains(t, chat, `"model":"chat-model"`)

	response := fixture.request(handler, http.MethodPost, "/v1/responses", `{"model":"response-model","input":"hi","stream":true}`, true)
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "event: response.completed")
	assertBodyContains(t, response, `"model":"response-model"`)
	if strings.Contains(response.Body.String(), "data: [DONE]") {
		t.Fatal("Responses SSE 不应输出 Chat 的 [DONE]")
	}

	fixture.transport.setScenario("stream-error")
	failed := fixture.request(handler, http.MethodPost, "/v1/chat/completions", `{"model":"chat-model","messages":[],"stream":true}`, true)
	assertStatus(t, failed, http.StatusOK)
	assertBodyContains(t, failed, "event: error")
	assertBodyContains(t, failed, `"code":"stream_failed"`)
}

func TestDataAuthenticationAndValidation(t *testing.T) {
	t.Parallel()

	fixture := newAPIFixture(t)
	handler := NewDataHandler(fixture.service)
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		auth       string
		wantStatus int
		wantCode   string
	}{
		{"missing-auth", http.MethodGet, "/v1/models", "", "", 401, "invalid_gateway_token"},
		{"bad-scheme", http.MethodGet, "/v1/models", "", "Basic value", 401, "invalid_gateway_token"},
		{"bad-spacing", http.MethodGet, "/v1/models", "", "Bearer  value", 401, "invalid_gateway_token"},
		{"invalid-token", http.MethodGet, "/v1/models", "", "Bearer agw_abcdefghijklmnopqrstuvwxyz012345", 401, "invalid_gateway_token"},
		{"empty-body", http.MethodPost, "/v1/chat/completions", "", "Bearer " + fixture.rawToken, 400, "invalid_json"},
		{"bad-json", http.MethodPost, "/v1/chat/completions", `{`, "Bearer " + fixture.rawToken, 400, "invalid_json"},
		{"missing-model", http.MethodPost, "/v1/chat/completions", `{}`, "Bearer " + fixture.rawToken, 422, "invalid_model"},
		{"empty-model", http.MethodPost, "/v1/chat/completions", `{"model":" "}`, "Bearer " + fixture.rawToken, 422, "invalid_model"},
		{"background", http.MethodPost, "/v1/responses", `{"model":"response-model","background":true}`, "Bearer " + fixture.rawToken, 422, "unsupported_responses_parameter"},
		{"image-stream", http.MethodPost, "/v1/images/generations", `{"model":"image-model","stream":true}`, "Bearer " + fixture.rawToken, 422, "unsupported_image_parameter"},
		{"unknown-model", http.MethodPost, "/v1/chat/completions", `{"model":"missing","messages":[]}`, "Bearer " + fixture.rawToken, 404, "model_not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.auth != "" {
				request.Header.Set("Authorization", test.auth)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertStatus(t, response, test.wantStatus)
			assertErrorCode(t, response, test.wantCode)
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Add("Authorization", "Bearer "+fixture.rawToken)
	request.Header.Add("Authorization", "Bearer "+fixture.rawToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusUnauthorized)

	request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 600))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusUnauthorized)

	large := `{"model":"chat-model","input":"` + strings.Repeat("x", 8192) + `"}`
	response = fixture.request(handler, http.MethodPost, "/v1/chat/completions", large, true)
	assertStatus(t, response, http.StatusRequestEntityTooLarge)
	assertErrorCode(t, response, "request_too_large")
}

func TestDataProviderAndWriterFailures(t *testing.T) {
	t.Parallel()

	fixture := newAPIFixture(t)
	handler := NewDataHandler(fixture.service)
	fixture.transport.setScenario("500")
	response := fixture.request(handler, http.MethodPost, "/v1/chat/completions", `{"model":"chat-model","messages":[]}`, true)
	assertStatus(t, response, http.StatusBadGateway)
	assertErrorCode(t, response, "upstream_error")
	response = fixture.request(handler, http.MethodPost, "/v1/responses", `{"model":"response-model","input":"hi"}`, true)
	assertStatus(t, response, http.StatusBadGateway)
	response = fixture.request(handler, http.MethodPost, "/v1/images/generations", `{"model":"image-model","prompt":"cat"}`, true)
	assertStatus(t, response, http.StatusBadGateway)

	fixture.transport.setScenario("")
	response = fixture.request(handler, http.MethodPost, "/v1/chat/completions", `{"model":"missing","stream":true}`, true)
	assertStatus(t, response, http.StatusNotFound)
	response = fixture.request(handler, http.MethodPost, "/v1/responses", `{"model":"missing","stream":true}`, true)
	assertStatus(t, response, http.StatusNotFound)
	response = fixture.request(handler, http.MethodPost, "/v1/responses", `{"model":"missing","input":"hi"}`, true)
	assertStatus(t, response, http.StatusNotFound)
	response = fixture.request(handler, http.MethodPost, "/v1/images/generations", `{"model":"missing","prompt":"cat"}`, true)
	assertStatus(t, response, http.StatusNotFound)
	stream, err := fixture.service.ChatStream(context.Background(), gateway.CallInput{
		Token: fixture.token, RequestID: "req_non_flusher", ModelAlias: "chat-model", Body: []byte(`{}`), Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	writer := &plainResponseWriter{header: make(http.Header)}
	(&dataAPI{service: fixture.service}).writeStream(writer, stream, true)
	if writer.status != http.StatusInternalServerError || !strings.Contains(writer.body.String(), "streaming_unsupported") {
		t.Fatalf("non-flusher response = %d %s", writer.status, writer.body.String())
	}
}

func TestDataDirectStorageFailures(t *testing.T) {
	t.Parallel()

	fixture := newAPIFixture(t)
	api := &dataAPI{service: fixture.service, maxBodyBytes: 4096}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := withRequestID(withToken(context.Background(), fixture.token), "req_direct_storage")
	for _, test := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"models", api.models},
		{"token", api.token},
	} {
		request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		response := httptest.NewRecorder()
		test.call(response, request)
		assertStatus(t, response, http.StatusServiceUnavailable)
		assertErrorCode(t, response, "audit_unavailable")
	}
}

func TestAdminCredentialAndTokenEndpoints(t *testing.T) {
	t.Parallel()

	fixture := newAPIFixture(t)
	handler := NewAdminHandler(fixture.service)

	createdCredential := adminRequest(t, handler, http.MethodPost, "/admin/v1/credentials", `{"id":"unused","provider":"openai","name":"Unused","secret":"secret"}`, nil)
	assertStatus(t, createdCredential, http.StatusCreated)
	if strings.Contains(createdCredential.Body.String(), "secret") || strings.Contains(createdCredential.Body.String(), "cipher") {
		t.Fatal("credential 响应泄露 secret")
	}
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/credentials", "", nil), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/credentials/unused", "", nil), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/credentials/missing", "", nil), http.StatusNotFound)
	assertStatus(t, adminRequest(t, handler, http.MethodPost, "/admin/v1/credentials/unused/rotate", `{"secret":"new"}`, nil), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodPost, "/admin/v1/credentials/unused/rotate", `{"unknown":true}`, nil), http.StatusBadRequest)
	assertStatus(t, adminRequest(t, handler, http.MethodPost, "/admin/v1/credentials", `{"unknown":true}`, nil), http.StatusBadRequest)
	assertStatus(t, adminRequest(t, handler, http.MethodDelete, "/admin/v1/credentials/dashscope-main", "", nil), http.StatusConflict)
	assertStatus(t, adminRequest(t, handler, http.MethodDelete, "/admin/v1/credentials/unused", "", nil), http.StatusNoContent)
	assertStatus(t, adminRequest(t, handler, http.MethodDelete, "/admin/v1/credentials/unused", "", nil), http.StatusNotFound)

	createdToken := adminRequest(t, handler, http.MethodPost, "/admin/v1/tokens", `{"name":"admin-created"}`, nil)
	assertStatus(t, createdToken, http.StatusCreated)
	assertHeader(t, createdToken, "Cache-Control", "no-store")
	var tokenBody map[string]any
	decodeResponse(t, createdToken, &tokenBody)
	tokenID, _ := tokenBody["id"].(string)
	issuedSecret, _ := tokenBody["token"].(string)
	if tokenID == "" || issuedSecret == "" {
		t.Fatalf("create token response = %#v", tokenBody)
	}
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/tokens", "", nil), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/tokens/"+tokenID, "", nil), http.StatusOK)
	secretResponse := adminRequest(t, handler, http.MethodGet, "/admin/v1/tokens/"+tokenID+"/secret", "", nil)
	assertStatus(t, secretResponse, http.StatusOK)
	assertBodyContains(t, secretResponse, issuedSecret)
	rotated := adminRequest(t, handler, http.MethodPost, "/admin/v1/tokens/"+tokenID+"/rotate", `{}`, nil)
	assertStatus(t, rotated, http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodPost, "/admin/v1/tokens/"+tokenID+"/revoke", `{}`, nil), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodPost, "/admin/v1/tokens/"+tokenID+"/rotate", `{}`, nil), http.StatusConflict)
	assertStatus(t, adminRequest(t, handler, http.MethodDelete, "/admin/v1/tokens/"+tokenID, "", nil), http.StatusNoContent)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/tokens/"+tokenID, "", nil), http.StatusNotFound)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/tokens/"+tokenID+"/secret", "", nil), http.StatusNotFound)
	assertStatus(t, adminRequest(t, handler, http.MethodPost, "/admin/v1/tokens/"+tokenID+"/revoke", `{}`, nil), http.StatusNotFound)
	assertStatus(t, adminRequest(t, handler, http.MethodDelete, "/admin/v1/tokens/"+tokenID, "", nil), http.StatusNotFound)

	assertStatus(t, adminRequest(t, handler, http.MethodPost, "/admin/v1/tokens", `{}`, nil), http.StatusUnprocessableEntity)
	assertStatus(t, adminRequest(t, handler, http.MethodPost, "/admin/v1/tokens", `{"unknown":true}`, nil), http.StatusBadRequest)
}

func TestAdminConfigBackendAndRevisionEndpoints(t *testing.T) {
	t.Parallel()

	fixture := newAPIFixture(t)
	handler := NewAdminHandler(fixture.service)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/config", "", nil), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/backends", "", nil), http.StatusOK)

	missingMatch := adminRequest(t, handler, http.MethodPut, "/admin/v1/config", `{}`, nil)
	assertStatus(t, missingMatch, http.StatusPreconditionRequired)
	badMatch := adminRequest(t, handler, http.MethodPut, "/admin/v1/config", `{}`, map[string]string{"If-Match": "wrong"})
	assertStatus(t, badMatch, http.StatusBadRequest)
	invalidJSON := adminRequest(t, handler, http.MethodPut, "/admin/v1/config", `{`, map[string]string{"If-Match": "1"})
	assertStatus(t, invalidJSON, http.StatusBadRequest)
	invalidConfig := adminRequest(t, handler, http.MethodPut, "/admin/v1/config", `{}`, map[string]string{"If-Match": "1"})
	assertStatus(t, invalidConfig, http.StatusUnprocessableEntity)

	_, current := fixture.service.Config()
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	updated := adminRequest(t, handler, http.MethodPut, "/admin/v1/config", string(encoded), map[string]string{"If-Match": `"1"`})
	assertStatus(t, updated, http.StatusOK)
	assertBodyContains(t, updated, `"revision":2`)

	backend := current.Backends[0]
	backend.BaseURL = "https://dashscope.test/v2"
	backendJSON, _ := json.Marshal(backend)
	replaced := adminRequest(t, handler, http.MethodPut, "/admin/v1/backends/dashscope-main", string(backendJSON), map[string]string{"If-Match": "2"})
	assertStatus(t, replaced, http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodPut, "/admin/v1/backends/dashscope-main", `{`, map[string]string{"If-Match": "3"}), http.StatusBadRequest)
	if _, err := fixture.service.CreateCredential(context.Background(), gateway.CredentialInput{
		ID: "extra-credential", Provider: "openai", Name: "Extra", Secret: []byte("key"),
	}, "req_extra_credential"); err != nil {
		t.Fatal(err)
	}
	extraBackend := config.Backend{
		ID: "ignored-body-id", Provider: "openai", BaseURL: "https://openai.test/v1", CredentialID: "extra-credential",
		Capabilities: []string{"chat"}, Timeouts: config.BackendTimeouts{Request: config.Duration(time.Second), StreamIdle: config.Duration(time.Second)},
	}
	extraJSON, _ := json.Marshal(extraBackend)
	assertStatus(t, adminRequest(t, handler, http.MethodPut, "/admin/v1/backends/extra", string(extraJSON), map[string]string{"If-Match": "3"}), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodDelete, "/admin/v1/backends/extra", "", map[string]string{"If-Match": "4"}), http.StatusOK)

	inUse := adminRequest(t, handler, http.MethodDelete, "/admin/v1/backends/dashscope-main", "", map[string]string{"If-Match": "5"})
	assertStatus(t, inUse, http.StatusConflict)
	missing := adminRequest(t, handler, http.MethodDelete, "/admin/v1/backends/missing", "", map[string]string{"If-Match": "5"})
	assertStatus(t, missing, http.StatusNotFound)

	switched := adminRequest(t, handler, http.MethodPut, "/admin/v1/routes/chat-route/active-backend", `{"backend_id":"dashscope-main"}`, map[string]string{"If-Match": "5"})
	assertStatus(t, switched, http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodPut, "/admin/v1/routes/chat-route/active-backend", `{`, map[string]string{"If-Match": "6"}), http.StatusBadRequest)
	assertStatus(t, adminRequest(t, handler, http.MethodPut, "/admin/v1/routes/missing/active-backend", `{"backend_id":"dashscope-main"}`, map[string]string{"If-Match": "6"}), http.StatusNotFound)

	list := adminRequest(t, handler, http.MethodGet, "/admin/v1/revisions?limit=2", "", nil)
	assertStatus(t, list, http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/revisions/wrong", "", nil), http.StatusBadRequest)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/revisions/999", "", nil), http.StatusNotFound)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/revisions/1", "", nil), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodPost, "/admin/v1/revisions/wrong/restore", `{}`, map[string]string{"If-Match": "6"}), http.StatusBadRequest)
	assertStatus(t, adminRequest(t, handler, http.MethodPost, "/admin/v1/revisions/999/restore", `{}`, map[string]string{"If-Match": "6"}), http.StatusNotFound)
	restored := adminRequest(t, handler, http.MethodPost, "/admin/v1/revisions/1/restore", `{}`, map[string]string{"If-Match": "6"})
	assertStatus(t, restored, http.StatusOK)
}

func TestAdminStorageFailures(t *testing.T) {
	t.Parallel()

	fixture := newAPIFixture(t)
	handler := NewAdminHandler(fixture.service)
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		method  string
		path    string
		body    string
		headers map[string]string
		status  int
	}{
		{http.MethodGet, "/admin/v1/revisions", "", nil, 503},
		{http.MethodGet, "/admin/v1/revisions/1", "", nil, 404},
		{http.MethodGet, "/admin/v1/credentials", "", nil, 503},
		{http.MethodPost, "/admin/v1/credentials", `{"id":"new","provider":"openai","name":"New","secret":"key"}`, nil, 503},
		{http.MethodPost, "/admin/v1/credentials/dashscope-main/rotate", `{"secret":"key"}`, nil, 503},
		{http.MethodGet, "/admin/v1/tokens", "", nil, 503},
		{http.MethodPost, "/admin/v1/tokens", `{"name":"new"}`, nil, 503},
		{http.MethodGet, "/admin/v1/tokens/" + fixture.token.ID + "/secret", "", nil, 503},
		{http.MethodPost, "/admin/v1/tokens/" + fixture.token.ID + "/revoke", `{}`, nil, 503},
		{http.MethodDelete, "/admin/v1/tokens/" + fixture.token.ID, "", nil, 503},
		{http.MethodGet, "/admin/v1/audits", "", nil, 503},
		{http.MethodGet, "/admin/v1/audits/missing", "", nil, 404},
		{http.MethodPut, "/admin/v1/config", mustJSON(t, apiGatewayConfig()), map[string]string{"If-Match": "1"}, 503},
	}
	for _, test := range tests {
		response := adminRequest(t, handler, test.method, test.path, test.body, test.headers)
		assertStatus(t, response, test.status)
	}
}

func TestAdminAuditAndUsageEndpoints(t *testing.T) {
	t.Parallel()

	fixture := newAPIFixture(t)
	dataHandler := NewDataHandler(fixture.service)
	call := fixture.request(dataHandler, http.MethodPost, "/v1/chat/completions", `{"model":"chat-model","messages":[]}`, true)
	assertStatus(t, call, http.StatusOK)
	auditID := call.Header().Get("X-AI-Gateway-Audit-ID")
	handler := NewAdminHandler(fixture.service)

	list := adminRequest(t, handler, http.MethodGet, "/admin/v1/audits?token_id="+fixture.token.ID+"&limit=1", "", nil)
	assertStatus(t, list, http.StatusOK)
	assertBodyContains(t, list, auditID)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/audits?from=bad", "", nil), http.StatusBadRequest)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/audits/"+auditID, "", nil), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/audits/missing", "", nil), http.StatusNotFound)

	usage := adminRequest(t, handler, http.MethodGet, "/admin/v1/tokens/"+fixture.token.ID+"/usage?group_by=operation", "", nil)
	assertStatus(t, usage, http.StatusOK)
	assertBodyContains(t, usage, `"requests":1`)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/tokens/"+fixture.token.ID+"/usage?group_by=wrong", "", nil), http.StatusUnprocessableEntity)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/admin/v1/tokens/"+fixture.token.ID+"/usage?from=bad", "", nil), http.StatusBadRequest)
}

func TestOperationsHandler(t *testing.T) {
	t.Parallel()

	fixture := newAPIFixture(t)
	handler := NewOperationsHandler(fixture.service, time.Second)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/livez", "", nil), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/readyz", "", nil), http.StatusOK)
	assertStatus(t, adminRequest(t, handler, http.MethodGet, "/healthz", "", nil), http.StatusOK)

	fixture.manager.Store(nil)
	response := adminRequest(t, handler, http.MethodGet, "/readyz", "", nil)
	assertStatus(t, response, http.StatusServiceUnavailable)
	assertBodyContains(t, response, `"reason":"config"`)
	fixture.manager.Store(fixture.snapshot)
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	response = adminRequest(t, handler, http.MethodGet, "/readyz", "", nil)
	assertStatus(t, response, http.StatusServiceUnavailable)
	assertBodyContains(t, response, `"reason":"storage"`)
}

func TestResponseAndParsingHelpers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ctx = withRequestID(ctx, "req_1")
	ctx = withToken(ctx, model.GatewayToken{ID: "tok_1"})
	if requestIDFrom(ctx) != "req_1" || tokenFrom(ctx).ID != "tok_1" || requestIDFrom(context.Background()) != "" || tokenFrom(context.Background()).ID != "" {
		t.Fatal("context helper 结果错误")
	}

	request := httptest.NewRequest(http.MethodGet, "/?limit=5", nil)
	if parseLimit(request, 100) != 5 {
		t.Fatal("parseLimit(valid) 错误")
	}
	for _, raw := range []string{"", "0", "501", "bad"} {
		request = httptest.NewRequest(http.MethodGet, "/?limit="+raw, nil)
		if parseLimit(request, 77) != 77 {
			t.Errorf("parseLimit(%q) 错误", raw)
		}
	}
	if value, err := parseTimeQuery(""); err != nil || value != nil {
		t.Fatalf("parseTimeQuery(empty) = %v, %v", value, err)
	}
	if value, err := parseTimeQuery("123"); err != nil || value == nil || *value != 123 {
		t.Fatalf("parseTimeQuery(ms) = %v, %v", value, err)
	}
	if value, err := parseTimeQuery("2026-01-02T03:04:05Z"); err != nil || value == nil {
		t.Fatalf("parseTimeQuery(RFC3339) = %v, %v", value, err)
	}
	if _, err := parseTimeQuery("bad"); err == nil {
		t.Fatal("parseTimeQuery(bad) 应失败")
	}

	for _, header := range []string{"", "0", "bad"} {
		request = httptest.NewRequest(http.MethodPut, "/", nil)
		request.Header.Set("If-Match", header)
		if _, err := parseIfMatch(request); err == nil {
			t.Errorf("parseIfMatch(%q) 应失败", header)
		}
	}
	request.Header.Set("If-Match", `"12"`)
	if value, err := parseIfMatch(request); err != nil || value != 12 {
		t.Fatalf("parseIfMatch(valid) = %d, %v", value, err)
	}

	response := httptest.NewRecorder()
	writeError(response, errors.New("plain"))
	assertStatus(t, response, http.StatusInternalServerError)
	assertErrorCode(t, response, "internal_error")
	response = httptest.NewRecorder()
	writeJSON(response, http.StatusNoContent, nil)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("writeJSON(204) = %d %q", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	writeJSON(response, http.StatusOK, failingJSON{})
	if response.Code != http.StatusOK {
		t.Fatalf("writeJSON(marshal error) status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x","extra":true}`))
	var decoded struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(request, 1024, &decoded); apiErrorCode(err) != "invalid_json" {
		t.Fatalf("decodeJSON(unknown) error = %v", err)
	}
	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"} {}`))
	if err := decodeJSON(request, 1024, &decoded); apiErrorCode(err) != "invalid_json" {
		t.Fatalf("decodeJSON(multiple) error = %v", err)
	}
	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"}`))
	if err := decodeJSON(request, 1024, &decoded); err != nil || decoded.Name != "x" {
		t.Fatalf("decodeJSON(valid) = %#v, %v", decoded, err)
	}

	now := time.Now().UnixMilli()
	token := model.GatewayToken{ID: "id", Name: "name", Status: "active", CreatedAt: now, UpdatedAt: now}
	if view := tokenView(token); view.ID != "id" || view.Token != "" {
		t.Fatalf("tokenView() = %#v", view)
	}
	credential := model.Credential{ID: "id", Provider: "openai", Name: "name", Status: "active", CreatedAt: now, UpdatedAt: now}
	if view := credentialView(credential); view.ID != "id" || view.Provider != "openai" {
		t.Fatalf("credentialView() = %#v", view)
	}
}

type apiFixture struct {
	store     *sqlite.Store
	service   *gateway.Service
	manager   *config.Manager
	snapshot  *config.Snapshot
	transport *apiTransport
	token     model.GatewayToken
	rawToken  string
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Options{Path: filepath.Join(t.TempDir(), "gateway.db"), MaxOpenConns: 4, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cipher, err := secret.NewCipher([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	cfg := apiGatewayConfig()
	encoded, _ := json.Marshal(cfg)
	if _, err := store.SaveConfig(ctx, 0, encoded, "bootstrap", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := config.NewSnapshot(1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager := config.NewManager(snapshot)
	ciphertext, err := cipher.Encrypt("credential", "dashscope-main", "dashscope", []byte("provider-key"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if err := store.CreateCredential(ctx, model.Credential{
		ID: "dashscope-main", Provider: "dashscope", Name: "DashScope", Status: "active",
		Ciphertext: ciphertext, KeyVersion: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	transport := &apiTransport{}
	service, err := gateway.New(store, cipher, manager, transport)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.CreateToken(ctx, "api-client", nil, "req_fixture")
	if err != nil {
		t.Fatal(err)
	}
	return &apiFixture{
		store: store, service: service, manager: manager, snapshot: snapshot,
		transport: transport, token: issued.Token, rawToken: issued.Secret,
	}
}

func apiGatewayConfig() config.Gateway {
	route := func(id, operation, alias, upstream string) config.Route {
		return config.Route{
			ID: id, Operation: operation, ModelAlias: alias, ActiveBackend: "dashscope-main",
			Targets: []config.Target{{BackendID: "dashscope-main", UpstreamModel: upstream}},
		}
	}
	return config.Gateway{
		APIVersion: config.APIVersion, Kind: config.KindGateway,
		Backends: []config.Backend{{
			ID: "dashscope-main", Provider: "dashscope", BaseURL: "https://dashscope.test/v1", CredentialID: "dashscope-main",
			Capabilities: []string{"chat", "responses", "image"},
			Timeouts:     config.BackendTimeouts{Request: config.Duration(time.Second), StreamIdle: config.Duration(time.Second)},
		}},
		Routes: []config.Route{
			route("chat-route", "chat", "chat-model", "qwen-chat"),
			route("responses-route", "responses", "response-model", "qwen-response"),
			route("image-route", "image", "image-model", "wanx-image"),
		},
		Audit:     config.Audit{Retention: config.Duration(time.Hour), CleanupInterval: config.Duration(time.Minute), AbandonedAfter: config.Duration(time.Minute), CleanupBatchSize: 100},
		Responses: config.Responses{BindingRetention: config.Duration(time.Hour)},
		Limits: config.Limits{
			RequestBodyBytes: 4096, MaxBackends: 10, MaxRoutes: 10,
			ChatConcurrency: 4, ResponsesConcurrency: 4, ImageConcurrency: 2,
			ImagesPerRequest: 2, ImageRawBytesPerResponse: 1 << 20,
		},
	}
}

func (fixture *apiFixture) request(handler http.Handler, method, path, body string, authenticated bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-Request-ID", "req_api_"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+fixture.rawToken)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type apiTransport struct {
	mu       sync.Mutex
	scenario string
	lastBody []byte
}

func (transport *apiTransport) setScenario(value string) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.scenario = value
}

func (transport *apiTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.lastBody = append([]byte(nil), body...)
	scenario := transport.scenario
	transport.mu.Unlock()
	status := http.StatusOK
	if scenario == "500" {
		status = http.StatusInternalServerError
	}
	responseBody := `{"error":{"message":"mock","code":"upstream_error"}}`
	contentType := "application/json"
	if status == http.StatusOK {
		responseBody, contentType = apiProviderResponse(request.URL.Path, body, scenario)
	}
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}},
		Body: io.NopCloser(strings.NewReader(responseBody)), Request: request,
	}, nil
}

func apiProviderResponse(path string, body []byte, scenario string) (string, string) {
	var request map[string]any
	_ = json.Unmarshal(body, &request)
	stream, _ := request["stream"].(bool)
	modelName, _ := request["model"].(string)
	if scenario == "stream-error" {
		return "data: not-json\n\n", "text/event-stream"
	}
	if stream {
		return strings.Join([]string{
			"event: response.completed",
			`data: {"id":"resp_upstream","model":"` + modelName + `","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
			"", "data: [DONE]", "", "",
		}, "\n"), "text/event-stream"
	}
	switch path {
	case "/v1/chat/completions":
		return `{"id":"chat_upstream","model":"` + modelName + `","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`, "application/json"
	case "/v1/responses":
		return `{"id":"resp_upstream","model":"` + modelName + `","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`, "application/json"
	case "/v1/images/generations", "/api/v1/services/aigc/multimodal-generation/generation":
		return `{"data":[{"b64_json":"aW1n"}]}`, "application/json"
	default:
		return `{}`, "application/json"
	}
}

func adminRequest(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d, body=%s", response.Code, want, response.Body.String())
	}
}

func assertHeader(t *testing.T, response *httptest.ResponseRecorder, key, want string) {
	t.Helper()
	if got := response.Header().Get(key); got != want {
		t.Fatalf("header %s = %q, want %q", key, got, want)
	}
}

func assertBodyContains(t *testing.T, response *httptest.ResponseRecorder, value string) {
	t.Helper()
	if !strings.Contains(response.Body.String(), value) {
		t.Fatalf("body %q 不包含 %q", response.Body.String(), value)
	}
}

func assertErrorCode(t *testing.T, response *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body errorBody
	decodeResponse(t, response, &body)
	if body.Error.Code != want {
		t.Fatalf("error code = %q, want %q, body=%s", body.Error.Code, want, response.Body.String())
	}
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, value any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), value); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}

func assertJSONValue(t *testing.T, data []byte, key string, want any) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode json %q: %v", data, err)
	}
	if body[key] != want {
		t.Fatalf("%s = %#v, want %#v; body=%s", key, body[key], want, data)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func apiErrorCode(err error) string {
	var gatewayError *gateway.Error
	if errors.As(err, &gatewayError) {
		return gatewayError.Code
	}
	return ""
}

type plainResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (writer *plainResponseWriter) Header() http.Header { return writer.header }
func (writer *plainResponseWriter) Write(data []byte) (int, error) {
	return writer.body.Write(data)
}
func (writer *plainResponseWriter) WriteHeader(status int) { writer.status = status }

type failingJSON struct{}

func (failingJSON) MarshalJSON() ([]byte, error) { return nil, errors.New("marshal") }
