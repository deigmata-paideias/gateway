package dashscope

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/deigmata-paideias/gateway/internal/provider"
)

func TestClientOperations(t *testing.T) {
	t.Parallel()

	transport := &mockTransport{}
	client := newClient(t, transport)
	request := provider.Request{Body: json.RawMessage(`{"model":"alias","messages":[]}`), UpstreamModel: "qwen-up"}
	chat, err := client.Chat(context.Background(), request)
	if err != nil || chat.UpstreamID != "chat_up" || chat.HTTPStatus != http.StatusOK {
		t.Fatalf("Chat() = %#v, %v", chat, err)
	}
	assertRequest(t, transport.last(), "/v1/chat/completions", "qwen-up", false, "")

	request.PreviousResponseID = "resp_previous"
	response, err := client.Responses(context.Background(), request)
	if err != nil || response.UpstreamID != "resp_up" || response.Usage.TotalTokens == nil || *response.Usage.TotalTokens != 3 {
		t.Fatalf("Responses() = %#v, %v", response, err)
	}
	assertRequest(t, transport.last(), "/v1/responses", "qwen-up", false, "resp_previous")

	image, err := client.Image(context.Background(), provider.Request{Body: json.RawMessage(`{"prompt":"cat"}`), UpstreamModel: "wanx-up"})
	if err != nil || image.ImageCount != 1 || image.RawImageBytes != 3 {
		t.Fatalf("Image() = %#v, %v", image, err)
	}
	assertRequest(t, transport.last(), "/v1/images/generations", "wanx-up", false, "")
	if transport.last().authorization != "Bearer dashscope-key" {
		t.Fatalf("Authorization = %q", transport.last().authorization)
	}
}

func TestClientStreams(t *testing.T) {
	t.Parallel()

	transport := &mockTransport{}
	client := newClient(t, transport)
	for _, test := range []struct {
		name string
		path string
		open func() (provider.Stream, error)
	}{
		{"chat", "/v1/chat/completions", func() (provider.Stream, error) {
			return client.ChatStream(context.Background(), provider.Request{Body: json.RawMessage(`{}`), UpstreamModel: "qwen-up"})
		}},
		{"responses", "/v1/responses", func() (provider.Stream, error) {
			return client.ResponsesStream(context.Background(), provider.Request{Body: json.RawMessage(`{}`), UpstreamModel: "qwen-up"})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream, err := test.open()
			if err != nil {
				t.Fatalf("open stream error = %v", err)
			}
			if !stream.Next() || stream.Current().UpstreamID == "" {
				t.Fatalf("Next() = false, current=%#v error=%v", stream.Current(), stream.Err())
			}
			if stream.Next() || stream.Err() != nil {
				t.Fatalf("stream end error = %v", stream.Err())
			}
			if err := stream.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			assertRequest(t, transport.last(), test.path, "qwen-up", true, "")
		})
	}
}

func TestClientValidationAndFailures(t *testing.T) {
	t.Parallel()

	valid := Options{BaseURL: "https://dash.test/v1", APIKey: "key", HTTPClient: &http.Client{Transport: &mockTransport{}}, MaxImages: 1, MaxImageBytes: 10}
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
	client := newClient(t, &mockTransport{})
	invalid := provider.Request{Body: json.RawMessage(`{`), UpstreamModel: "model"}
	if _, err := client.Chat(context.Background(), invalid); !errors.Is(err, provider.ErrInvalidRequest) {
		t.Fatalf("Chat(invalid) error = %v", err)
	}
	if _, err := client.ChatStream(context.Background(), invalid); !errors.Is(err, provider.ErrInvalidRequest) {
		t.Fatalf("ChatStream(invalid) error = %v", err)
	}

	for status, retryable := range map[int]bool{401: false, 429: true, 500: true} {
		transport := &mockTransport{status: status}
		client := newClient(t, transport)
		_, err := client.Chat(context.Background(), provider.Request{Body: json.RawMessage(`{}`), UpstreamModel: "model"})
		var providerError *provider.Error
		if !errors.As(err, &providerError) || providerError.Status != status || providerError.Retryable != retryable {
			t.Fatalf("status %d error = %#v", status, err)
		}
		_, err = client.ChatStream(context.Background(), provider.Request{Body: json.RawMessage(`{}`), UpstreamModel: "model"})
		if !errors.As(err, &providerError) || providerError.Status != status {
			t.Fatalf("stream status %d error = %#v", status, err)
		}
	}
	client = newClient(t, &mockTransport{body: "not-json"})
	if _, err := client.Chat(context.Background(), provider.Request{Body: json.RawMessage(`{}`), UpstreamModel: "model"}); !errors.Is(err, provider.ErrInvalidResponse) {
		t.Fatalf("Chat(malformed) error = %v", err)
	}
	client = newClient(t, &mockTransport{err: errors.New("transport")})
	_, err := client.Chat(context.Background(), provider.Request{Body: json.RawMessage(`{}`), UpstreamModel: "model"})
	var providerError *provider.Error
	if !errors.As(err, &providerError) || !providerError.Retryable {
		t.Fatalf("transport error = %#v", err)
	}
}

func newClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := New(Options{
		BaseURL: "https://dash.test/v1/", APIKey: "dashscope-key", HTTPClient: &http.Client{Transport: transport},
		MaxImages: 2, MaxImageBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type recordedRequest struct {
	path          string
	body          []byte
	authorization string
}

type mockTransport struct {
	mu      sync.Mutex
	records []recordedRequest
	status  int
	body    string
	err     error
}

func (transport *mockTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.records = append(transport.records, recordedRequest{
		path: request.URL.Path, body: body, authorization: request.Header.Get("Authorization"),
	})
	transport.mu.Unlock()
	if transport.err != nil {
		return nil, transport.err
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	responseBody, contentType := transport.response(request.URL.Path, body)
	if transport.body != "" {
		responseBody = transport.body
	}
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}},
		Body: io.NopCloser(strings.NewReader(responseBody)), Request: request,
	}, nil
}

func (transport *mockTransport) response(path string, body []byte) (string, string) {
	var request map[string]any
	_ = json.Unmarshal(body, &request)
	stream, _ := request["stream"].(bool)
	if stream {
		return strings.Join([]string{
			"event: response.completed",
			`data: {"id":"stream_up","usage":{"total_tokens":2}}`,
			"", "data: [DONE]", "", "",
		}, "\n"), "text/event-stream"
	}
	switch path {
	case "/v1/chat/completions":
		return `{"id":"chat_up","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`, "application/json"
	case "/v1/responses":
		return `{"id":"resp_up","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`, "application/json"
	case "/v1/images/generations":
		return `{"data":[{"b64_json":"aW1n"}]}`, "application/json"
	default:
		return `{}`, "application/json"
	}
}

func (transport *mockTransport) last() recordedRequest {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.records[len(transport.records)-1]
}

func assertRequest(t *testing.T, record recordedRequest, path, model string, stream bool, previous string) {
	t.Helper()
	if record.path != path {
		t.Fatalf("path = %q, want %q", record.path, path)
	}
	var body map[string]any
	if err := json.Unmarshal(record.body, &body); err != nil {
		t.Fatalf("body = %s: %v", record.body, err)
	}
	if body["model"] != model || body["stream"] != stream {
		t.Fatalf("request body = %s", record.body)
	}
	if previous != "" && body["previous_response_id"] != previous {
		t.Fatalf("previous_response_id = %#v", body["previous_response_id"])
	}
}
