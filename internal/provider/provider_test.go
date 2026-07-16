package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deigmata-paideias/gateway/internal/model"
)

func TestError(t *testing.T) {
	t.Parallel()

	cause := errors.New("cause")
	err := &Error{Status: 429, Code: "rate_limit", Retryable: true, Err: cause}
	if !strings.Contains(err.Error(), "429") || !errors.Is(err, cause) {
		t.Fatalf("Error() = %q, Unwrap() = %v", err.Error(), err.Unwrap())
	}
}

func TestJSONHelpers(t *testing.T) {
	t.Parallel()

	rewritten, err := RewriteTopLevel([]byte(`{"id":"old","nested":{"id":"old"}}`), map[string]string{
		"id": "new", "model": "alias", "ignored": "",
	})
	if err != nil || string(rewritten) != `{"id":"new","model":"alias","nested":{"id":"old"}}` {
		t.Fatalf("RewriteTopLevel() = %s, %v", rewritten, err)
	}
	if _, err := RewriteTopLevel([]byte(`{`), nil); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("RewriteTopLevel() error = %v", err)
	}

	data := []byte(`{"id":"old","items":["old",{"value":"old"}],"keep":"x"}`)
	rewritten, err = RewriteString(data, "old", "new")
	if err != nil || string(rewritten) != `{"id":"new","items":["new",{"value":"new"}],"keep":"x"}` {
		t.Fatalf("RewriteString() = %s, %v", rewritten, err)
	}
	for _, test := range []struct{ old, new string }{{"", "new"}, {"same", "same"}} {
		copyData, err := RewriteString(data, test.old, test.new)
		if err != nil || string(copyData) != string(data) || &copyData[0] == &data[0] {
			t.Fatalf("RewriteString(%q,%q) 未返回独立副本", test.old, test.new)
		}
	}
	if _, err := RewriteString([]byte(`{`), "old", "new"); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("RewriteString() error = %v", err)
	}

	prepared, err := PrepareRequest([]byte(`{"model":"alias","stream":false}`), "upstream", "prev_1", true)
	if err != nil || string(prepared) != `{"model":"upstream","previous_response_id":"prev_1","stream":true}` {
		t.Fatalf("PrepareRequest() = %s, %v", prepared, err)
	}
	prepared, err = PrepareRequest([]byte(`{}`), "upstream", "", false)
	if err != nil || string(prepared) != `{"model":"upstream","stream":false}` {
		t.Fatalf("PrepareRequest() = %s, %v", prepared, err)
	}
	if _, err := PrepareRequest([]byte(`{`), "model", "", false); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
}

func TestExtractIDAndUsage(t *testing.T) {
	t.Parallel()

	if got := ExtractID([]byte(`{"id":"direct","response":{"id":"nested"}}`)); got != "direct" {
		t.Fatalf("ExtractID() = %q", got)
	}
	if got := ExtractID([]byte(`{"response":{"id":"nested"}}`)); got != "nested" {
		t.Fatalf("ExtractID() = %q", got)
	}
	if got := ExtractID([]byte(`{`)); got != "" {
		t.Fatalf("ExtractID(invalid) = %q", got)
	}

	usage := ExtractUsage([]byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`))
	assertUsage(t, usage, 3, 2, 4, 1, 7)
	usage = ExtractUsage([]byte(`{"response":{"usage":{"input_tokens":5,"output_tokens":6,"total_tokens":11,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}`))
	assertUsage(t, usage, 5, 3, 6, 2, 11)
	for _, data := range []string{`{`, `{}`, `{"response":{"usage":null}}`} {
		usage = ExtractUsage([]byte(data))
		if usage.InputTokens != nil || usage.TotalTokens != nil {
			t.Fatalf("ExtractUsage(%s) = %#v", data, usage)
		}
	}

	for eventType, want := range map[string]bool{
		"response.completed": true, "response.failed": true, "response.incomplete": true,
		"error": true, "response.output_text.delta": false,
	} {
		if got := IsTerminalResponseEvent(eventType); got != want {
			t.Errorf("IsTerminalResponseEvent(%q) = %v", eventType, got)
		}
	}
}

func TestSSEReader(t *testing.T) {
	t.Parallel()

	body := io.NopCloser(strings.NewReader(
		": comment\r\n" +
			"event: response.output_text.delta\r\n" +
			"data: {\"id\":\"resp_1\",\r\n" +
			"data: \"usage\":{\"input_tokens\":1}}\r\n\r\n" +
			"event: response.completed\n" +
			"data: {\"response\":{\"id\":\"resp_1\",\"usage\":{\"total_tokens\":2}}}\n\n" +
			"data: [DONE]\n\n",
	))
	reader := NewSSEReader(body, 1024)
	if !reader.Next() {
		t.Fatalf("first Next() = false, error = %v", reader.Err())
	}
	event := reader.Current()
	if event.Type != "response.output_text.delta" || event.UpstreamID != "resp_1" || event.Terminal ||
		event.Usage.InputTokens == nil || *event.Usage.InputTokens != 1 {
		t.Fatalf("first event = %#v", event)
	}
	if !reader.Next() {
		t.Fatalf("second Next() = false, error = %v", reader.Err())
	}
	event = reader.Current()
	if !event.Terminal || event.UpstreamID != "resp_1" || event.Usage.TotalTokens == nil || *event.Usage.TotalTokens != 2 {
		t.Fatalf("second event = %#v", event)
	}
	if reader.Next() || reader.Err() != nil || reader.Next() {
		t.Fatalf("DONE 后状态错误: current=%#v err=%v", reader.Current(), reader.Err())
	}
	if err := reader.Close(); err != nil || reader.Close() != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestSSEReaderFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body io.ReadCloser
	}{
		{"invalid-json", io.NopCloser(strings.NewReader("data: not-json\n\n"))},
		{"too-large", io.NopCloser(strings.NewReader("data: {\"value\":\"" + strings.Repeat("x", 1100) + "\"}\n\n"))},
		{"read", &testReadCloser{readErr: errors.New("read failed")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := NewSSEReader(test.body, 1024)
			if reader.Next() || reader.Err() == nil || reader.Next() {
				t.Fatalf("Next() 应失败, error = %v", reader.Err())
			}
		})
	}
	reader := NewSSEReader(&testReadCloser{closeErr: errors.New("close failed")}, 0)
	if err := reader.Close(); err == nil {
		t.Fatal("Close() 应传播错误")
	}
	reader = NewSSEReader(io.NopCloser(strings.NewReader("\n")), 0)
	if reader.Next() || reader.Err() != nil {
		t.Fatalf("空 EOF 状态错误: %v", reader.Err())
	}
}

func TestNormalizeImages(t *testing.T) {
	t.Parallel()

	raw := []byte("image-data")
	b64 := base64.StdEncoding.EncodeToString(raw)
	result, err := NormalizeImages(context.Background(), []byte(`{"created":1,"data":[{"b64_json":"`+b64+`","url":"https://ignored"}],"usage":{"total_tokens":3}}`), http.DefaultClient, "https://provider.example", 1, 1024)
	if err != nil {
		t.Fatalf("NormalizeImages() error = %v", err)
	}
	if result.ImageCount != 1 || result.RawImageBytes != int64(len(raw)) || result.HTTPStatus != http.StatusOK ||
		result.Usage.ImageCount == nil || *result.Usage.ImageCount != 1 ||
		result.Usage.RawImageBytes == nil || *result.Usage.RawImageBytes != int64(len(raw)) ||
		strings.Contains(string(result.Body), `"url"`) {
		t.Fatalf("NormalizeImages() = %#v body=%s", result, result.Body)
	}

	imageClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader(string(raw))),
			Request:    request,
		}, nil
	})}
	result, err = NormalizeImages(context.Background(), []byte(`{"data":[{"url":"http://provider.test/image"}]}`), imageClient, "http://provider.test/v1", 1, 1024)
	if err != nil || !strings.Contains(string(result.Body), b64) {
		t.Fatalf("NormalizeImages(URL) = %s, %v", result.Body, err)
	}
}

func TestNormalizeImageFailures(t *testing.T) {
	t.Parallel()

	b64 := base64.StdEncoding.EncodeToString([]byte("abc"))
	tests := []struct {
		name  string
		data  string
		max   int
		bytes int64
		err   error
	}{
		{"json", `{`, 1, 10, ErrInvalidResponse},
		{"empty", `{"data":[]}`, 1, 10, ErrInvalidResponse},
		{"too-many", `{"data":[{"b64_json":"` + b64 + `"},{"b64_json":"` + b64 + `"}]}`, 1, 10, ErrInvalidResponse},
		{"bad-base64", `{"data":[{"b64_json":"%%%"}]}`, 1, 10, ErrInvalidResponse},
		{"missing", `{"data":[{}]}`, 1, 10, ErrInvalidResponse},
		{"large", `{"data":[{"b64_json":"` + b64 + `"}]}`, 1, 2, ErrImageTooLarge},
		{"no-space", `{"data":[{"url":"https://example.com/image"}]}`, 1, 0, ErrImageTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeImages(context.Background(), []byte(test.data), http.DefaultClient, "https://provider.example", test.max, test.bytes)
			if !errors.Is(err, test.err) {
				t.Fatalf("NormalizeImages() error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestImageDownloadValidationAndFailures(t *testing.T) {
	t.Parallel()

	if parsed, err := validateImageURL("https://provider.example", "https://cdn.example/image.png"); err != nil || parsed.Host != "cdn.example" {
		t.Fatalf("validateImageURL(https) = %v, %v", parsed, err)
	}
	if parsed, err := validateImageURL("http://provider.example:8080/v1", "http://provider.example:9090/image.png"); err != nil || parsed == nil {
		t.Fatalf("validateImageURL(mock http) = %v, %v", parsed, err)
	}
	for _, rawURL := range []string{"relative.png", "https://user:pass@example.com/image", "http://other.example/image"} {
		if _, err := validateImageURL("http://provider.example", rawURL); !errors.Is(err, ErrUnsupportedImage) {
			t.Errorf("validateImageURL(%q) error = %v", rawURL, err)
		}
	}

	baseClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}
		switch request.URL.Path {
		case "/status":
			response.StatusCode = http.StatusNotFound
		case "/type":
			response.Header.Set("Content-Type", "text/plain")
			response.Body = io.NopCloser(strings.NewReader("not image"))
		case "/bad-type":
			response.Header.Set("Content-Type", "%%%")
		case "/large":
			response.Header.Set("Content-Type", "image/png")
			response.Body = io.NopCloser(strings.NewReader("12345"))
		case "/redirect":
			response.StatusCode = http.StatusFound
			response.Header.Set("Location", "/redirect")
		}
		return response, nil
	})}
	client := ImageHTTPClient(baseClient, "http://provider.test")
	for _, test := range []struct {
		path string
		err  error
	}{
		{"/status", ErrInvalidResponse},
		{"/type", ErrInvalidResponse},
		{"/bad-type", ErrInvalidResponse},
		{"/large", ErrImageTooLarge},
	} {
		if _, err := downloadImage(context.Background(), client, "http://provider.test", "http://provider.test"+test.path, 3); !errors.Is(err, test.err) {
			t.Errorf("downloadImage(%s) error = %v", test.path, err)
		}
	}
	if _, err := downloadImage(context.Background(), client, "http://provider.test", "http://provider.test/redirect", 10); err == nil {
		t.Fatal("downloadImage() 应拒绝过多重定向")
	}

	errorClient := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport failed")
	})}
	if _, err := downloadImage(context.Background(), errorClient, "https://provider.example", "https://provider.example/image", 10); err == nil {
		t.Fatal("downloadImage() 应传播 transport 错误")
	}
	readErrorClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       &testReadCloser{readErr: errors.New("read failed")},
			Request:    request,
		}, nil
	})}
	if _, err := downloadImage(context.Background(), readErrorClient, "https://provider.example", "https://provider.example/image", 10); err == nil {
		t.Fatal("downloadImage() 应传播读取错误")
	}
}

func assertUsage(t *testing.T, usage model.Usage, input, cached, output, reasoning, total int64) {
	t.Helper()
	if usage.InputTokens == nil || *usage.InputTokens != input ||
		usage.CachedInputTokens == nil || *usage.CachedInputTokens != cached ||
		usage.OutputTokens == nil || *usage.OutputTokens != output ||
		usage.ReasoningOutputTokens == nil || *usage.ReasoningOutputTokens != reasoning ||
		usage.TotalTokens == nil || *usage.TotalTokens != total {
		t.Fatalf("usage = %#v", usage)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type testReadCloser struct {
	readErr  error
	closeErr error
}

func (reader *testReadCloser) Read([]byte) (int, error) { return 0, reader.readErr }
func (reader *testReadCloser) Close() error             { return reader.closeErr }
