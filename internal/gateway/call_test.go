package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/provider"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

func TestSyncCallsQueriesAndAudit(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	token := fixture.token.Token
	chat, err := fixture.service.Chat(ctx, CallInput{
		Token: token, RequestID: "req_chat", ModelAlias: "chat-model",
		Body: []byte(`{"model":"chat-model","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil || chat.BackendID != "openai-main" || chat.Provider != "openai" || chat.Revision != 1 || chat.AuditID == "" {
		t.Fatalf("Chat() = %#v, %v", chat, err)
	}
	assertJSONField(t, chat.Body, "model", "chat-model")
	assertAudit(t, fixture.store, chat.AuditID, "succeeded", "chat.completions")
	chatRequest := fixture.transport.last()
	if chatRequest.host != "openai.test" || chatRequest.authorization != "Bearer openai-key" {
		t.Fatalf("chat upstream = %#v", chatRequest)
	}
	assertJSONField(t, chatRequest.body, "model", "gpt-up")

	image, err := fixture.service.Image(ctx, CallInput{
		Token: token, RequestID: "req_image", ModelAlias: "image-model",
		Body: []byte(`{"model":"image-model","prompt":"cat"}`),
	})
	if err != nil || image.BackendID != "openai-main" || !strings.Contains(string(image.Body), "aW1n") {
		t.Fatalf("Image() = %#v, %v", image, err)
	}
	assertAudit(t, fixture.store, image.AuditID, "succeeded", "images.generate")
	assertJSONField(t, fixture.transport.last().body, "model", "gpt-image")

	response, err := fixture.service.Responses(ctx, CallInput{
		Token: token, RequestID: "req_response", ModelAlias: "response-model",
		Body: []byte(`{"model":"response-model","input":"hi"}`),
	})
	if err != nil || !strings.HasPrefix(response.ResponseID, "resp_") ||
		!strings.Contains(string(response.Body), response.ResponseID) || strings.Contains(string(response.Body), "resp_upstream") ||
		!strings.Contains(string(response.Body), "response-model") {
		t.Fatalf("Responses() = %#v body=%s, error=%v", response, response.Body, err)
	}
	assertAudit(t, fixture.store, response.AuditID, "succeeded", "responses.create")
	binding, err := fixture.store.ResolveBinding(ctx, response.ResponseID, token.ID)
	if err != nil || binding.UpstreamResponseID != "resp_upstream" || binding.Revision != 1 {
		t.Fatalf("ResolveBinding() = %#v, %v", binding, err)
	}

	nextResponse, err := fixture.service.Responses(ctx, CallInput{
		Token: token, RequestID: "req_response_next", ModelAlias: "response-model", PreviousID: response.ResponseID,
		Body: []byte(`{"model":"response-model","input":"continue"}`),
	})
	if err != nil || nextResponse.ResponseID == response.ResponseID {
		t.Fatalf("Responses(previous) = %#v, %v", nextResponse, err)
	}
	assertJSONField(t, fixture.transport.last().body, "previous_response_id", "resp_upstream")

	models, err := fixture.service.Models(ctx, token, "req_models")
	if err != nil || len(models.Value) != 3 || models.AuditID == "" || models.Revision != 1 {
		t.Fatalf("Models() = %#v, %v", models, err)
	}
	assertAudit(t, fixture.store, models.AuditID, "succeeded", "models.list")
	currentToken, err := fixture.service.CurrentToken(ctx, token, "")
	if err != nil || currentToken.Value.ID != token.ID || currentToken.AuditID == "" {
		t.Fatalf("CurrentToken() = %#v, %v", currentToken, err)
	}
	assertAudit(t, fixture.store, currentToken.AuditID, "succeeded", "token.get")

	if len(fixture.recorder.calls) < 4 {
		t.Fatalf("recorder calls = %#v", fixture.recorder.calls)
	}
}

func TestDynamicBackendSwitchUsesDashScope(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	for index, route := range []string{"chat-route", "responses-route", "image-route"} {
		if _, err := fixture.service.SwitchBackend(ctx, int64(index+1), route, "dashscope-main", "req_switch"); err != nil {
			t.Fatalf("SwitchBackend(%s) error = %v", route, err)
		}
	}
	token := fixture.token.Token
	chat, err := fixture.service.Chat(ctx, CallInput{
		Token: token, RequestID: "req_dash_chat", ModelAlias: "chat-model", Body: []byte(`{"messages":[]}`),
	})
	if err != nil || chat.Provider != "dashscope" || chat.BackendID != "dashscope-main" || chat.Revision != 4 {
		t.Fatalf("Chat(dashscope) = %#v, %v", chat, err)
	}
	if record := fixture.transport.last(); record.host != "dashscope.test" || record.authorization != "Bearer dashscope-key" {
		t.Fatalf("dashscope chat request = %#v", record)
	}
	response, err := fixture.service.Responses(ctx, CallInput{
		Token: token, RequestID: "req_dash_response", ModelAlias: "response-model", Body: []byte(`{"input":"hi"}`),
	})
	if err != nil || response.Provider != "dashscope" {
		t.Fatalf("Responses(dashscope) = %#v, %v", response, err)
	}
	image, err := fixture.service.Image(ctx, CallInput{
		Token: token, RequestID: "req_dash_image", ModelAlias: "image-model", Body: []byte(`{"prompt":"cat"}`),
	})
	if err != nil || image.Provider != "dashscope" || !strings.Contains(string(image.Body), "aW1n") {
		t.Fatalf("Image(dashscope) = %#v, %v", image, err)
	}
}

func TestResponseBindingPinsArchivedBackend(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	input := CallInput{
		Token: fixture.token.Token, RequestID: "req_first", ModelAlias: "response-model", Body: []byte(`{"input":"first"}`),
	}
	first, err := fixture.service.Responses(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SwitchBackend(ctx, 1, "responses-route", "dashscope-main", "req_switch"); err != nil {
		t.Fatal(err)
	}
	input.RequestID, input.PreviousID = "req_pinned", first.ResponseID
	if _, err := fixture.service.Responses(ctx, input); err != nil {
		t.Fatalf("Responses(pinned) error = %v", err)
	}
	if record := fixture.transport.last(); record.host != "openai.test" {
		t.Fatalf("previous response 应固定到 openai，实际 %#v", record)
	}
	input.RequestID, input.PreviousID = "req_current", ""
	if _, err := fixture.service.Responses(ctx, input); err != nil {
		t.Fatal(err)
	}
	if record := fixture.transport.last(); record.host != "dashscope.test" {
		t.Fatalf("新 response 应使用 dashscope，实际 %#v", record)
	}
}

func TestSyncCallFailuresAreAudited(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scenario string
		call     func(*fixture, string) error
		code     string
		status   int
	}{
		{"401", "401", callChatError, "upstream_error", 502},
		{"429", "429", callChatError, "upstream_error", 429},
		{"500", "500", callChatError, "upstream_error", 502},
		{"transport", "transport-error", callChatError, "upstream_unavailable", 502},
		{"malformed", "malformed", callChatError, "upstream_unavailable", 502},
		{"invalid-request", "", func(fixture *fixture, requestID string) error {
			_, err := fixture.service.Chat(context.Background(), CallInput{
				Token: fixture.token.Token, RequestID: requestID, ModelAlias: "chat-model", Body: []byte(`{`),
			})
			return err
		}, "invalid_request", 422},
		{"response-no-id", "no-response-id", func(fixture *fixture, requestID string) error {
			_, err := fixture.service.Responses(context.Background(), CallInput{
				Token: fixture.token.Token, RequestID: requestID, ModelAlias: "response-model", Body: []byte(`{"input":"hi"}`),
			})
			return err
		}, "invalid_upstream_response", 502},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			fixture.transport.setScenario(test.scenario)
			requestID := "req_failure_" + test.name
			err := test.call(fixture, requestID)
			var gatewayError *Error
			if !errors.As(err, &gatewayError) || gatewayError.Code != test.code || gatewayError.Status != test.status {
				t.Fatalf("call error = %#v", err)
			}
			audits, queryErr := fixture.store.Audits(context.Background(), model.AuditFilter{Limit: 10})
			if queryErr != nil {
				t.Fatal(queryErr)
			}
			var found bool
			for _, audit := range audits {
				if audit.RequestID == requestID {
					found = true
					if audit.Status != "failed" || audit.ErrorCode != test.code {
						t.Fatalf("audit = %#v", audit)
					}
				}
			}
			if !found {
				t.Fatal("未找到失败审计")
			}
		})
	}
}

func TestImageAndResponseProviderFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		call func(*fixture) error
	}{
		{"image-provider", func(fixture *fixture) error {
			fixture.transport.setScenario("500")
			_, err := fixture.service.Image(context.Background(), CallInput{
				Token: fixture.token.Token, RequestID: "req_image_provider_error", ModelAlias: "image-model", Body: []byte(`{"prompt":"cat"}`),
			})
			return err
		}},
		{"image-invalid", func(fixture *fixture) error {
			_, err := fixture.service.Image(context.Background(), CallInput{
				Token: fixture.token.Token, RequestID: "req_image_invalid", ModelAlias: "image-model", Body: []byte(`{`),
			})
			return err
		}},
		{"response-provider", func(fixture *fixture) error {
			fixture.transport.setScenario("500")
			_, err := fixture.service.Responses(context.Background(), CallInput{
				Token: fixture.token.Token, RequestID: "req_response_provider_error", ModelAlias: "response-model", Body: []byte(`{"input":"hi"}`),
			})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			if err := test.call(fixture); err == nil {
				t.Fatal("调用应失败")
			}
		})
	}

	fixture := newFixture(t)
	if err := fixture.store.DeleteCredential(context.Background(), "openai-main"); err != nil {
		t.Fatal(err)
	}
	for index, call := range []func() error{
		func() error {
			_, err := fixture.service.Chat(context.Background(), CallInput{Token: fixture.token.Token, RequestID: "req_missing_cred_chat", ModelAlias: "chat-model", Body: []byte(`{}`)})
			return err
		},
		func() error {
			_, err := fixture.service.Image(context.Background(), CallInput{Token: fixture.token.Token, RequestID: "req_missing_cred_image", ModelAlias: "image-model", Body: []byte(`{}`)})
			return err
		},
		func() error {
			_, err := fixture.service.Responses(context.Background(), CallInput{Token: fixture.token.Token, RequestID: "req_missing_cred_response", ModelAlias: "response-model", Body: []byte(`{}`)})
			return err
		},
	} {
		if err := call(); errorCode(err) != "not_found" {
			t.Fatalf("missing credential call %d error = %v", index, err)
		}
	}
}

func TestCallStartAndAuditFinishFailures(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	input := CallInput{Token: fixture.token.Token, RequestID: "req_duplicate", ModelAlias: "chat-model", Body: []byte(`{"messages":[]}`)}
	if _, err := fixture.service.Chat(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Chat(ctx, input); errorCode(err) != "audit_unavailable" {
		t.Fatalf("duplicate request ID error = %v", err)
	}
	input.RequestID, input.ModelAlias = "req_missing_model", "missing"
	if _, err := fixture.service.Chat(ctx, input); errorCode(err) != "model_not_found" {
		t.Fatalf("missing model error = %v", err)
	}
	input.RequestID, input.ModelAlias = "req_missing_token", "chat-model"
	input.Token.ID = "missing-token"
	if _, err := fixture.service.Chat(ctx, input); errorCode(err) != "audit_unavailable" {
		t.Fatalf("missing token error = %v", err)
	}

	finishFixture := newFixture(t)
	finishFixture.transport.setCallback(func() { _ = finishFixture.store.Close() })
	_, err := finishFixture.service.Chat(ctx, CallInput{
		Token: finishFixture.token.Token, RequestID: "req_finish_failure", ModelAlias: "chat-model", Body: []byte(`{"messages":[]}`),
	})
	if errorCode(err) != "audit_unavailable" || finishFixture.recorder.auditFailures != 1 {
		t.Fatalf("finish audit failure = %v, recorder=%#v", err, finishFixture.recorder)
	}
}

func TestResponseLookupFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newFixture(t)
	input := CallInput{
		Token: fixture.token.Token, RequestID: "req_lookup", ModelAlias: "response-model",
		PreviousID: "resp_missing", Body: []byte(`{"input":"hi"}`),
	}
	if _, err := fixture.service.Responses(ctx, input); errorCode(err) != "response_not_found" {
		t.Fatalf("Responses(missing binding) error = %v", err)
	}
	binding := model.ResponseBinding{
		PublicResponseID: "resp_bound", GatewayTokenID: fixture.token.Token.ID, BackendID: "openai-main",
		Provider: "openai", ModelAlias: "other-model", UpstreamResponseID: "resp_up", Revision: 1,
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), State: "active",
	}
	if err := fixture.store.PutBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	input.PreviousID = binding.PublicResponseID
	if _, err := fixture.service.Responses(ctx, input); errorCode(err) != "response_not_found" {
		t.Fatalf("Responses(model mismatch) error = %v", err)
	}

	missingRevision := binding
	missingRevision.PublicResponseID, missingRevision.ModelAlias, missingRevision.Revision = "resp_missing_revision", "response-model", 999
	if err := fixture.store.PutBinding(ctx, missingRevision); err != nil {
		t.Fatal(err)
	}
	input.PreviousID = missingRevision.PublicResponseID
	if _, err := fixture.service.Responses(ctx, input); errorCode(err) != "not_found" {
		t.Fatalf("Responses(missing revision) error = %v", err)
	}
}

func TestArchivedResponseConfigFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		body []byte
	}{
		{"malformed", []byte(`not-json`)},
		{"invalid", []byte(`{}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			ctx := context.Background()
			revision, err := fixture.store.SaveConfig(ctx, 1, test.body, "test", "req")
			if err != nil {
				t.Fatal(err)
			}
			binding := model.ResponseBinding{
				PublicResponseID: "resp_archived_" + test.name, GatewayTokenID: fixture.token.Token.ID,
				BackendID: "openai-main", Provider: "openai", ModelAlias: "response-model",
				UpstreamResponseID: "resp_up", Revision: revision.Revision,
				ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), State: "active",
			}
			if err := fixture.store.PutBinding(ctx, binding); err != nil {
				t.Fatal(err)
			}
			_, err = fixture.service.Responses(ctx, CallInput{
				Token: fixture.token.Token, RequestID: "req_archived_" + test.name,
				ModelAlias: "response-model", PreviousID: binding.PublicResponseID, Body: []byte(`{"input":"hi"}`),
			})
			if errorCode(err) != "response_backend_unavailable" {
				t.Fatalf("Responses() error = %v", err)
			}
		})
	}
}

func TestChatAndResponseStreams(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	chat, err := fixture.service.ChatStream(ctx, CallInput{
		Token: fixture.token.Token, RequestID: "req_chat_stream", ModelAlias: "chat-model",
		Body: []byte(`{"messages":[]}`), Stream: true,
	})
	if err != nil || chat.AuditID() == "" || chat.Revision() != 1 || chat.BackendID() != "openai-main" || chat.ResponseID() != "" {
		t.Fatalf("ChatStream() = %#v, %v", chat, err)
	}
	if !chat.Next() {
		t.Fatalf("ChatStream.Next() error = %v", chat.Err())
	}
	assertJSONField(t, chat.Current().Data, "model", "chat-model")
	if chat.Next() || chat.Err() != nil {
		t.Fatalf("ChatStream end error = %v", chat.Err())
	}
	assertAudit(t, fixture.store, chat.AuditID(), "succeeded", "chat.completions")
	if err := chat.Close(); err != nil {
		t.Fatalf("ChatStream.Close() error = %v", err)
	}

	responses, err := fixture.service.ResponsesStream(ctx, CallInput{
		Token: fixture.token.Token, RequestID: "req_response_stream", ModelAlias: "response-model",
		Body: []byte(`{"input":"hi"}`), Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !responses.Next() {
		t.Fatalf("ResponsesStream.Next() error = %v", responses.Err())
	}
	event := responses.Current()
	if !event.Terminal || responses.ResponseID() == "" || !strings.Contains(string(event.Data), responses.ResponseID()) ||
		strings.Contains(string(event.Data), "resp_upstream") || !strings.Contains(string(event.Data), "response-model") {
		t.Fatalf("response stream event = %#v, responseID=%q", event, responses.ResponseID())
	}
	if responses.Next() || responses.Err() != nil {
		t.Fatalf("ResponsesStream end error = %v", responses.Err())
	}
	assertAudit(t, fixture.store, responses.AuditID(), "succeeded", "responses.create")
	if err := responses.Close(); err != nil {
		t.Fatalf("ResponsesStream.Close() error = %v", err)
	}
}

func TestDashScopeStreams(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	if _, err := fixture.service.SwitchBackend(ctx, 1, "chat-route", "dashscope-main", "req"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SwitchBackend(ctx, 2, "responses-route", "dashscope-main", "req"); err != nil {
		t.Fatal(err)
	}
	chat, err := fixture.service.ChatStream(ctx, CallInput{
		Token: fixture.token.Token, RequestID: "req_dash_chat_stream", ModelAlias: "chat-model", Body: []byte(`{}`), Stream: true,
	})
	if err != nil || !chat.Next() || chat.Current().Data == nil {
		t.Fatalf("dash chat stream = %#v, %v", chat, err)
	}
	_ = chat.Close()
	responses, err := fixture.service.ResponsesStream(ctx, CallInput{
		Token: fixture.token.Token, RequestID: "req_dash_response_stream", ModelAlias: "response-model", Body: []byte(`{}`), Stream: true,
	})
	if err != nil || !responses.Next() || responses.ResponseID() == "" {
		t.Fatalf("dash responses stream = %#v, %v", responses, err)
	}
	if err := responses.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamFailuresAndClose(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"500", "malformed", "transport-error"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newFixture(t)
			fixture.transport.setScenario(scenario)
			stream, err := fixture.service.ChatStream(context.Background(), CallInput{
				Token: fixture.token.Token, RequestID: "req_stream_" + scenario,
				ModelAlias: "chat-model", Body: []byte(`{}`), Stream: true,
			})
			if err != nil {
				t.Fatalf("ChatStream() error = %v", err)
			}
			if stream.Next() || stream.Err() == nil {
				t.Fatalf("Next() 应失败, error=%v", stream.Err())
			}
			assertAudit(t, fixture.store, stream.AuditID(), "failed", "chat.completions")
			if err := stream.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		})
	}

	fixture := newFixture(t)
	stream, err := fixture.service.ChatStream(context.Background(), CallInput{
		Token: fixture.token.Token, RequestID: "req_early_close", ModelAlias: "chat-model", Body: []byte(`{}`), Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	assertAudit(t, fixture.store, stream.AuditID(), "failed", "chat.completions")

	cancelFixture := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancelled, err := cancelFixture.service.ChatStream(ctx, CallInput{
		Token: cancelFixture.token.Token, RequestID: "req_cancel", ModelAlias: "chat-model", Body: []byte(`{}`), Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := cancelled.Close(); err != nil {
		t.Fatal(err)
	}
	assertAudit(t, cancelFixture.store, cancelled.AuditID(), "cancelled", "chat.completions")
}

func TestStreamCreationFailures(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	if _, err := fixture.service.ChatStream(ctx, CallInput{
		Token: fixture.token.Token, RequestID: "req_missing_model_stream", ModelAlias: "missing", Body: []byte(`{}`), Stream: true,
	}); errorCode(err) != "model_not_found" {
		t.Fatalf("ChatStream(missing model) error = %v", err)
	}
	if _, err := fixture.service.ChatStream(ctx, CallInput{
		Token: fixture.token.Token, RequestID: "req_invalid_chat_stream", ModelAlias: "chat-model", Body: []byte(`{`), Stream: true,
	}); errorCode(err) != "invalid_request" {
		t.Fatalf("ChatStream(invalid) error = %v", err)
	}
	if _, err := fixture.service.ResponsesStream(ctx, CallInput{
		Token: fixture.token.Token, RequestID: "req_invalid_response_stream", ModelAlias: "response-model", Body: []byte(`{`), Stream: true,
	}); errorCode(err) != "invalid_request" {
		t.Fatalf("ResponsesStream(invalid) error = %v", err)
	}
	if _, err := fixture.service.ResponsesStream(ctx, CallInput{
		Token: fixture.token.Token, RequestID: "req_missing_previous_stream", ModelAlias: "response-model",
		PreviousID: "resp_missing", Body: []byte(`{}`), Stream: true,
	}); errorCode(err) != "response_not_found" {
		t.Fatalf("ResponsesStream(missing previous) error = %v", err)
	}

	missingCredential := newFixture(t)
	if err := missingCredential.store.DeleteCredential(ctx, "openai-main"); err != nil {
		t.Fatal(err)
	}
	if _, err := missingCredential.service.ChatStream(ctx, CallInput{
		Token: missingCredential.token.Token, RequestID: "req_missing_cred_chat_stream", ModelAlias: "chat-model", Body: []byte(`{}`), Stream: true,
	}); errorCode(err) != "not_found" {
		t.Fatalf("ChatStream(missing credential) error = %v", err)
	}
	if _, err := missingCredential.service.ResponsesStream(ctx, CallInput{
		Token: missingCredential.token.Token, RequestID: "req_missing_cred_response_stream", ModelAlias: "response-model", Body: []byte(`{}`), Stream: true,
	}); errorCode(err) != "not_found" {
		t.Fatalf("ResponsesStream(missing credential) error = %v", err)
	}
}

func TestDirectCallStreamBranches(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	input := CallInput{
		Token: fixture.token.Token, RequestID: "req_direct_invalid", ModelAlias: "chat-model", Body: []byte(`{}`), Stream: true,
	}
	state, err := fixture.service.startCall(ctx, input, "chat", "chat.completions")
	if err != nil {
		t.Fatal(err)
	}
	stream := newCallStream(fixture.service, state, &sliceStream{
		events: []provider.Event{{Data: []byte(`not-json`)}},
	}, input, "chat")
	if stream.Next() || stream.Err() == nil {
		t.Fatalf("invalid event 应失败: %v", stream.Err())
	}
	assertAudit(t, fixture.store, stream.AuditID(), "failed", "chat.completions")

	input.RequestID = "req_direct_failed_event"
	input.ModelAlias = "response-model"
	state, err = fixture.service.startCall(ctx, input, "responses", "responses.create")
	if err != nil {
		t.Fatal(err)
	}
	failed := newCallStream(fixture.service, state, &sliceStream{events: []provider.Event{{
		Type: "response.failed", Data: []byte(`{"type":"response.failed","id":"resp_up","model":"gpt-response"}`),
		UpstreamID: "resp_up", Terminal: true,
	}}}, input, "responses")
	if !failed.Next() {
		t.Fatalf("failed terminal event 应可读取: %v", failed.Err())
	}
	assertAudit(t, fixture.store, failed.AuditID(), "failed", "responses.create")

	input.RequestID = "req_direct_upstream_error"
	input.ModelAlias = "chat-model"
	state, err = fixture.service.startCall(ctx, input, "chat", "chat.completions")
	if err != nil {
		t.Fatal(err)
	}
	upstreamError := newCallStream(fixture.service, state, &sliceStream{err: errors.New("stream error")}, input, "chat")
	if upstreamError.Next() || upstreamError.Err() == nil {
		t.Fatalf("upstream error 未传播: %v", upstreamError.Err())
	}

	input.RequestID = "req_direct_close_error"
	state, err = fixture.service.startCall(ctx, input, "chat", "chat.completions")
	if err != nil {
		t.Fatal(err)
	}
	closeStream := newCallStream(fixture.service, state, &sliceStream{closeErr: errors.New("close")}, input, "chat")
	if err := closeStream.Close(); err == nil || !strings.Contains(err.Error(), "关闭 provider stream") {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestDirectFailureAndBindingBranches(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	input := CallInput{Token: fixture.token.Token, RequestID: "req_direct_fail", ModelAlias: "chat-model", Body: []byte(`{}`)}
	state, err := fixture.service.startCall(ctx, input, "chat", "chat.completions")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.failCall(state, errors.New("internal")); errorCode(err) != "internal_error" {
		t.Fatalf("failCall(internal) error = %v", err)
	}

	route, err := fixture.manager.Current().Resolve("responses", "response-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.bindResponse(ctx, fixture.token.Token.ID, "response-model", route, ""); errorCode(err) != "invalid_upstream_response" {
		t.Fatalf("bindResponse(empty ID) error = %v", err)
	}
	if _, err := fixture.service.bindResponse(ctx, "missing-token", "response-model", route, "resp_up"); errorCode(err) != "revision_conflict" {
		t.Fatalf("bindResponse(missing token) error = %v", err)
	}

	finishFixture := newFixture(t)
	finishInput := CallInput{Token: finishFixture.token.Token, RequestID: "req_failed_finish", ModelAlias: "chat-model", Body: []byte(`{}`)}
	finishState, err := finishFixture.service.startCall(ctx, finishInput, "chat", "chat.completions")
	if err != nil {
		t.Fatal(err)
	}
	if err := finishFixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := finishFixture.service.failCall(finishState, errors.New("internal")); errorCode(err) != "audit_unavailable" || finishFixture.recorder.auditFailures != 1 {
		t.Fatalf("failCall(finish failure) error = %v", err)
	}
}

func TestStreamAuditFinishFailure(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	input := CallInput{Token: fixture.token.Token, RequestID: "req_stream_finish_fail", ModelAlias: "chat-model", Body: []byte(`{}`), Stream: true}
	state, err := fixture.service.startCall(ctx, input, "chat", "chat.completions")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	stream := newCallStream(fixture.service, state, &sliceStream{events: []provider.Event{{
		Data: []byte(`{"id":"up","model":"gpt-up"}`), Terminal: true,
	}}}, input, "chat")
	if !stream.Next() || errorCode(stream.Err()) != "audit_unavailable" || fixture.recorder.auditFailures != 1 {
		t.Fatalf("stream finish failure: next/current/error = %#v / %v", stream.Current(), stream.Err())
	}
}

func TestQueryAndStorageFailures(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	missing := fixture.token.Token
	missing.ID = "missing"
	if _, err := fixture.service.Models(context.Background(), missing, "req_missing_models"); errorCode(err) != "audit_unavailable" {
		t.Fatalf("Models(missing token) error = %v", err)
	}
	if _, err := fixture.service.CurrentToken(context.Background(), missing, "req_missing_token"); errorCode(err) != "audit_unavailable" {
		t.Fatalf("CurrentToken(missing token) error = %v", err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Tokens(context.Background()); errorCode(err) != "storage_unavailable" {
		t.Fatalf("Tokens(closed) error = %v", err)
	}
	if _, err := fixture.service.Credentials(context.Background()); errorCode(err) != "storage_unavailable" {
		t.Fatalf("Credentials(closed) error = %v", err)
	}
	if _, err := fixture.service.ConfigRevisions(context.Background(), 10); errorCode(err) != "storage_unavailable" {
		t.Fatalf("ConfigRevisions(closed) error = %v", err)
	}
}

func callChatError(fixture *fixture, requestID string) error {
	_, err := fixture.service.Chat(context.Background(), CallInput{
		Token: fixture.token.Token, RequestID: requestID, ModelAlias: "chat-model", Body: []byte(`{"messages":[]}`),
	})
	return err
}

func assertJSONField(t *testing.T, data []byte, key string, want any) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", data, err)
	}
	if object[key] != want {
		t.Fatalf("field %q = %#v, want %#v; body=%s", key, object[key], want, data)
	}
}

func assertAudit(t *testing.T, store *sqlite.Store, id, status, operation string) model.Audit {
	t.Helper()
	audit, err := store.Audit(context.Background(), id)
	if err != nil {
		t.Fatalf("Audit(%q) error = %v", id, err)
	}
	if audit.Status != status || audit.Operation != operation {
		t.Fatalf("audit = %#v", audit)
	}
	return audit
}

type sliceStream struct {
	events   []provider.Event
	index    int
	current  provider.Event
	err      error
	closeErr error
}

func (stream *sliceStream) Next() bool {
	if stream.index >= len(stream.events) {
		return false
	}
	stream.current = stream.events[stream.index]
	stream.index++
	return true
}

func (stream *sliceStream) Current() provider.Event { return stream.current }
func (stream *sliceStream) Err() error              { return stream.err }
func (stream *sliceStream) Close() error            { return stream.closeErr }
