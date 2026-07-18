package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"

	"github.com/deigmata-paideias/gateway/internal/provider"
)

func TestClientSyncOperations(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{}
	client := newTestClient(t, transport)
	ctx := context.Background()

	chat, err := client.Chat(ctx, provider.Request{Body: json.RawMessage(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`), UpstreamModel: "gpt-up"})
	if err != nil || chat.UpstreamID != "chatcmpl_1" || chat.Usage.TotalTokens == nil || *chat.Usage.TotalTokens != 3 {
		t.Fatalf("Chat() = %#v, %v", chat, err)
	}
	assertRecorded(t, transport.last(), "/v1/chat/completions", "gpt-up", false, "")

	response, err := client.Responses(ctx, provider.Request{
		Body: json.RawMessage(`{"model":"alias","input":"hi","background":true}`), UpstreamModel: "gpt-resp", PreviousResponseID: "resp_previous",
	})
	if err != nil || response.UpstreamID != "resp_up" || response.Usage.InputTokens == nil || *response.Usage.InputTokens != 1 {
		t.Fatalf("Responses() = %#v, %v", response, err)
	}
	assertRecorded(t, transport.last(), "/v1/responses", "gpt-resp", false, "resp_previous")
	var responseBody map[string]any
	if err := json.Unmarshal(transport.last().body, &responseBody); err != nil || responseBody["background"] != nil {
		t.Fatalf("Responses() 不应转发 background: %s", transport.last().body)
	}

	image, err := client.Image(ctx, provider.Request{
		Body: json.RawMessage(`{"model":"alias","prompt":"a cat","response_format":"url"}`), UpstreamModel: "gpt-image",
	})
	if err != nil || image.ImageCount != 1 || string(image.Body) == "" {
		t.Fatalf("Image() = %#v, %v", image, err)
	}
	record := transport.last()
	assertRecorded(t, record, "/v1/images/generations", "gpt-image", false, "")
	var imageBody map[string]any
	if err := json.Unmarshal(record.body, &imageBody); err != nil || imageBody["response_format"] != "b64_json" {
		t.Fatalf("Image() response_format = %#v, body=%s", imageBody["response_format"], record.body)
	}
	if record.authorization != "Bearer test-key" {
		t.Fatalf("Authorization = %q", record.authorization)
	}
	if record.userAgent != userAgent {
		t.Fatalf("User-Agent = %q, want %q", record.userAgent, userAgent)
	}
}

func TestClientStreams(t *testing.T) {
	t.Parallel()

	transport := &recordingTransport{}
	client := newTestClient(t, transport)

	chat, err := client.ChatStream(context.Background(), provider.Request{
		Body: json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`), UpstreamModel: "gpt-up",
	})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if !chat.Next() {
		t.Fatalf("ChatStream.Next() = false, error = %v", chat.Err())
	}
	if event := chat.Current(); event.UpstreamID != "chatcmpl_stream" || event.Usage.TotalTokens == nil || *event.Usage.TotalTokens != 2 {
		t.Fatalf("chat event = %#v", event)
	}
	if chat.Next() || chat.Err() != nil {
		t.Fatalf("chat stream terminal error = %v", chat.Err())
	}
	if err := chat.Close(); err != nil {
		t.Fatalf("ChatStream.Close() error = %v", err)
	}
	assertRecorded(t, transport.last(), "/v1/chat/completions", "gpt-up", true, "")

	responses, err := client.ResponsesStream(context.Background(), provider.Request{
		Body: json.RawMessage(`{"input":"hi"}`), UpstreamModel: "gpt-resp", PreviousResponseID: "resp_previous",
	})
	if err != nil {
		t.Fatalf("ResponsesStream() error = %v", err)
	}
	if !responses.Next() {
		t.Fatalf("ResponsesStream.Next() = false, error = %v", responses.Err())
	}
	event := responses.Current()
	if event.Type != "response.completed" || !event.Terminal || event.UpstreamID != "resp_stream" ||
		event.Usage.TotalTokens == nil || *event.Usage.TotalTokens != 3 {
		t.Fatalf("response event = %#v", event)
	}
	if responses.Next() || responses.Err() != nil {
		t.Fatalf("response stream terminal error = %v", responses.Err())
	}
	if err := responses.Close(); err != nil {
		t.Fatalf("ResponsesStream.Close() error = %v", err)
	}
	assertRecorded(t, transport.last(), "/v1/responses", "gpt-resp", true, "resp_previous")
}

func TestClientValidationAndErrors(t *testing.T) {
	t.Parallel()

	valid := Options{
		BaseURL: "https://api.test/v1", APIKey: "key", HTTPClient: &http.Client{Transport: &recordingTransport{}},
		MaxImages: 1, MaxImageBytes: 1024,
	}
	for _, mutate := range []func(*Options){
		func(options *Options) { options.BaseURL = "" },
		func(options *Options) { options.APIKey = "" },
		func(options *Options) { options.HTTPClient = nil },
		func(options *Options) { options.MaxImages = 0 },
		func(options *Options) { options.MaxImageBytes = 0 },
	} {
		options := valid
		mutate(&options)
		if _, err := New(options); err == nil {
			t.Fatal("New() 应拒绝非法参数")
		}
	}

	client := newTestClient(t, &recordingTransport{})
	invalid := provider.Request{Body: json.RawMessage(`{`), UpstreamModel: "model"}
	if _, err := client.Chat(context.Background(), invalid); !errors.Is(err, provider.ErrInvalidRequest) {
		t.Fatalf("Chat() error = %v", err)
	}
	if _, err := client.ChatStream(context.Background(), invalid); !errors.Is(err, provider.ErrInvalidRequest) {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if _, err := client.Responses(context.Background(), invalid); !errors.Is(err, provider.ErrInvalidRequest) {
		t.Fatalf("Responses() error = %v", err)
	}
	if _, err := client.ResponsesStream(context.Background(), invalid); !errors.Is(err, provider.ErrInvalidRequest) {
		t.Fatalf("ResponsesStream() error = %v", err)
	}
	if _, err := client.Image(context.Background(), invalid); !errors.Is(err, provider.ErrInvalidRequest) {
		t.Fatalf("Image() error = %v", err)
	}

	for status, retryable := range map[int]bool{http.StatusUnauthorized: false, http.StatusTooManyRequests: true, http.StatusInternalServerError: true} {
		client := newTestClient(t, &recordingTransport{status: status})
		_, err := client.Chat(context.Background(), provider.Request{Body: json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`), UpstreamModel: "gpt-up"})
		var providerError *provider.Error
		if !errors.As(err, &providerError) || providerError.Status != status || providerError.Retryable != retryable {
			t.Fatalf("status %d: error = %#v", status, err)
		}
	}
	transportError := errors.New("transport failed")
	client = newTestClient(t, &recordingTransport{err: transportError})
	_, err := client.Chat(context.Background(), provider.Request{Body: json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`), UpstreamModel: "gpt-up"})
	var providerError *provider.Error
	if !errors.As(err, &providerError) || !providerError.Retryable || providerError.Status != 0 {
		t.Fatalf("transport error = %#v", err)
	}

	fallback := mapError(errors.New("plain"))
	if !errors.As(fallback, &providerError) || providerError.Code != "upstream_unavailable" {
		t.Fatalf("mapError() = %#v", fallback)
	}
	apiError := &openaisdk.Error{StatusCode: http.StatusBadRequest}
	mapped := mapError(apiError)
	if !errors.As(mapped, &providerError) || providerError.Status != http.StatusBadRequest || providerError.Retryable {
		t.Fatalf("mapError(API) = %#v", mapped)
	}
}

func newTestClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := New(Options{
		BaseURL: "https://api.test/v1/", APIKey: "test-key", HTTPClient: &http.Client{Transport: transport},
		MaxImages: 2, MaxImageBytes: 1024,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

type requestRecord struct {
	path          string
	body          []byte
	authorization string
	userAgent     string
}

type recordingTransport struct {
	mu      sync.Mutex
	records []requestRecord
	status  int
	err     error
}

func (transport *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, readErr := io.ReadAll(request.Body)
	if readErr != nil {
		return nil, readErr
	}
	transport.mu.Lock()
	transport.records = append(transport.records, requestRecord{
		path: request.URL.Path, body: body, authorization: request.Header.Get("Authorization"),
		userAgent: request.Header.Get("User-Agent"),
	})
	transport.mu.Unlock()
	if transport.err != nil {
		return nil, transport.err
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	responseBody, contentType := transport.response(request.URL.Path, body, status)
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(responseBody)),
		Request:    request,
	}, nil
}

func (transport *recordingTransport) response(path string, body []byte, status int) (string, string) {
	if status >= 300 {
		return `{"error":{"message":"mock error","type":"mock_error","code":"mock"}}`, "application/json"
	}
	var request map[string]any
	_ = json.Unmarshal(body, &request)
	stream, _ := request["stream"].(bool)
	switch path {
	case "/v1/chat/completions":
		payload := `{"id":"chatcmpl_1","object":"chat.completion","created":1,"model":"gpt-up","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
		if stream {
			payload = strings.Join([]string{
				`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":1,"model":"gpt-up","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
				"", "data: [DONE]", "", "",
			}, "\n")
			return payload, "text/event-stream"
		}
		return payload, "application/json"
	case "/v1/responses":
		payload := `{"id":"resp_up","object":"response","created_at":1,"status":"completed","model":"gpt-resp","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`
		if stream {
			payload = strings.Join([]string{
				"event: response.completed",
				`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_stream","object":"response","created_at":1,"status":"completed","model":"gpt-resp","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
				"", "",
			}, "\n")
			return payload, "text/event-stream"
		}
		return payload, "application/json"
	case "/v1/images/generations":
		return `{"created":1,"data":[{"b64_json":"aW1n"}]}`, "application/json"
	default:
		return `{}`, "application/json"
	}
}

func (transport *recordingTransport) last() requestRecord {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.records[len(transport.records)-1]
}

func assertRecorded(t *testing.T, record requestRecord, path, model string, stream bool, previous string) {
	t.Helper()
	if record.path != path {
		t.Fatalf("path = %q, want %q", record.path, path)
	}
	var body map[string]any
	if err := json.Unmarshal(record.body, &body); err != nil {
		t.Fatalf("request body = %s: %v", record.body, err)
	}
	if body["model"] != model {
		t.Fatalf("model = %#v, want %q, body=%s", body["model"], model, record.body)
	}
	if got, _ := body["stream"].(bool); got != stream {
		t.Fatalf("stream = %v, want %v, body=%s", got, stream, record.body)
	}
	if previous == "" {
		if _, exists := body["previous_response_id"]; exists {
			t.Fatalf("unexpected previous_response_id, body=%s", record.body)
		}
	} else if body["previous_response_id"] != previous {
		t.Fatalf("previous_response_id = %#v, want %q", body["previous_response_id"], previous)
	}
}
