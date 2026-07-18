package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelectedOperations(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		want      string
		wantErr   bool
	}{
		{name: "chat", operation: "chat", want: "chat"},
		{name: "responses", operation: "responses", want: "responses"},
		{name: "image", operation: "image", want: "image"},
		{name: "all", operation: "all", want: "chat,responses,image"},
		{name: "invalid", operation: "audio", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectedOperations(test.operation)
			if (err != nil) != test.wantErr {
				t.Fatalf("selectedOperations() error = %v, wantErr %v", err, test.wantErr)
			}
			if strings.Join(got, ",") != test.want {
				t.Fatalf("selectedOperations() = %v, want %q", got, test.want)
			}
		})
	}
}

func TestRunChat(t *testing.T) {
	requests := make([]string, 0, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/admin/v1/tokens":
			writeTestJSON(w, `{"id":"tok_test","token":"agw_test"}`)
		case "/admin/v1/config":
			writeTestJSON(w, `{"revision":7,"config":{}}`)
		case "/admin/v1/routes/chat-default/active-backend":
			if got := r.Header.Get("If-Match"); got != "7" {
				t.Errorf("If-Match = %q, want 7", got)
			}
			writeTestJSON(w, `{"revision":8,"active_backend":"dashscope-quickstart"}`)
		case "/v1/models":
			assertTestAuthorization(t, r)
			writeTestJSON(w, `{"object":"list","data":[{"id":"chat-default"}]}`)
		case "/v1/chat/completions":
			assertTestAuthorization(t, r)
			writeTestJSON(w, `{"id":"chat_test","model":"chat-default","choices":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	var output bytes.Buffer
	err := run(t.Context(), options{
		adminURL:  server.URL,
		dataURL:   server.URL,
		provider:  "dashscope",
		operation: "chat",
		prompt:    "hello",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	wantRequests := []string{
		"POST /admin/v1/tokens",
		"GET /admin/v1/config",
		"PUT /admin/v1/routes/chat-default/active-backend",
		"GET /v1/models",
		"POST /v1/chat/completions",
	}
	if strings.Join(requests, "\n") != strings.Join(wantRequests, "\n") {
		t.Fatalf("requests = %v, want %v", requests, wantRequests)
	}
	if !strings.Contains(output.String(), "chat-default") || !strings.Contains(output.String(), "chat_test") {
		t.Fatalf("output = %s", output.String())
	}
}

func TestRunRejectsInvalidProvider(t *testing.T) {
	err := run(context.Background(), options{provider: "unknown", operation: "chat"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("run() error = nil")
	}
}

func TestPrintImageSummary(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("image"))
	var output bytes.Buffer
	if err := printImageSummary(&output, []byte(`{"data":[{"b64_json":"`+payload+`"}]}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "bytes=5") || !strings.Contains(output.String(), "sha256=") {
		t.Fatalf("output = %s", output.String())
	}
	for _, raw := range [][]byte{[]byte(`{"data":[]}`), []byte(`{"data":[{"b64_json":"***"}]}`)} {
		if err := printImageSummary(&bytes.Buffer{}, raw); err == nil {
			t.Fatalf("printImageSummary(%s) error = nil", raw)
		}
	}
}

func assertTestAuthorization(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer agw_test" {
		t.Errorf("Authorization = %q", got)
	}
}

func writeTestJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
