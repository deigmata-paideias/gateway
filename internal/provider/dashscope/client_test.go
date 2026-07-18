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

	image, err := client.Image(context.Background(), provider.Request{
		Body:          json.RawMessage(`{"prompt":"cat","n":1,"size":"1024x1024","prompt_extend":false,"watermark":false,"seed":7}`),
		UpstreamModel: "qwen-image-up",
	})
	if err != nil || image.ImageCount != 1 || image.RawImageBytes != 3 || image.UpstreamID != "image_request" {
		t.Fatalf("Image() = %#v, %v", image, err)
	}
	imageRecord := transport.find("/api/v1" + imageGenerationPath)
	assertImageRequest(t, imageRecord, "qwen-image-up")
	if imageRecord.authorization != "Bearer dashscope-key" {
		t.Fatalf("Authorization = %q", imageRecord.authorization)
	}
	if imageRecord.accept != "application/json" {
		t.Fatalf("Accept = %q", imageRecord.accept)
	}
}

func TestNativeImageURL(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"https://dashscope.aliyuncs.com/compatible-mode/v1": "https://dashscope.aliyuncs.com/api/v1" + imageGenerationPath,
		"https://workspace.example/api/v1":                  "https://workspace.example/api/v1" + imageGenerationPath,
		"http://mock/dashscope/v1":                          "http://mock/dashscope/api/v1" + imageGenerationPath,
		"https://custom.example/root":                       "https://custom.example/root/api/v1" + imageGenerationPath,
	}
	for input, want := range tests {
		got, err := nativeImageURL(input)
		if err != nil || got != want {
			t.Errorf("nativeImageURL(%q) = %q, %v, want %q", input, got, err, want)
		}
	}
	if _, err := nativeImageURL("://"); err == nil {
		t.Fatal("nativeImageURL() 应拒绝非法 URL")
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
		func(options *Options) { options.BaseURL = "://" },
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
	for _, body := range []string{`{`, `{"prompt":""}`, `{"prompt":"cat","n":0}`} {
		_, err := client.Image(context.Background(), provider.Request{Body: json.RawMessage(body), UpstreamModel: "image-model"})
		if !errors.Is(err, provider.ErrInvalidRequest) {
			t.Errorf("Image(%s) error = %v", body, err)
		}
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
	if _, err := client.Image(context.Background(), provider.Request{Body: json.RawMessage(`{"prompt":"cat"}`), UpstreamModel: "model"}); !errors.Is(err, provider.ErrInvalidResponse) {
		t.Fatalf("Image(malformed) error = %v", err)
	}
	client = newClient(t, &mockTransport{body: `{}`})
	if _, err := client.Image(context.Background(), provider.Request{Body: json.RawMessage(`{"prompt":"cat"}`), UpstreamModel: "model"}); !errors.Is(err, provider.ErrInvalidResponse) {
		t.Fatalf("Image(missing image) error = %v", err)
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
	accept        string
}

type mockTransport struct {
	mu      sync.Mutex
	records []recordedRequest
	status  int
	body    string
	err     error
}

func (transport *mockTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
	}
	transport.mu.Lock()
	transport.records = append(transport.records, recordedRequest{
		path: request.URL.Path, body: body, authorization: request.Header.Get("Authorization"),
		accept: request.Header.Get("Accept"),
	})
	transport.mu.Unlock()
	if transport.err != nil {
		return nil, transport.err
	}
	if request.Method == http.MethodGet && request.URL.Path == "/generated.png" {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/png"}},
			Body: io.NopCloser(strings.NewReader("img")), Request: request,
		}, nil
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
	case "/api/v1/services/aigc/multimodal-generation/generation":
		return `{"request_id":"image_request","output":{"choices":[{"message":{"content":[{"image":"https://dash.test/generated.png"}]}}]},"usage":{"image_count":1}}`, "application/json"
	default:
		return `{}`, "application/json"
	}
}

func (transport *mockTransport) last() recordedRequest {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.records[len(transport.records)-1]
}

func (transport *mockTransport) find(path string) recordedRequest {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, record := range transport.records {
		if record.path == path {
			return record
		}
	}
	return recordedRequest{}
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

func assertImageRequest(t *testing.T, record recordedRequest, model string) {
	t.Helper()
	if record.path != "/api/v1"+imageGenerationPath {
		t.Fatalf("image path = %q", record.path)
	}
	var body struct {
		Model string `json:"model"`
		Input struct {
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		} `json:"input"`
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal(record.body, &body); err != nil {
		t.Fatalf("image body = %s: %v", record.body, err)
	}
	if body.Model != model || len(body.Input.Messages) != 1 || body.Input.Messages[0].Role != "user" ||
		len(body.Input.Messages[0].Content) != 1 || body.Input.Messages[0].Content[0].Text != "cat" ||
		body.Parameters["size"] != "1024*1024" || body.Parameters["n"] != float64(1) ||
		body.Parameters["prompt_extend"] != false || body.Parameters["watermark"] != false || body.Parameters["seed"] != float64(7) {
		t.Fatalf("image request body = %s", record.body)
	}
}
