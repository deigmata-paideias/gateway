package observe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/model"
)

func TestMetricsRecordAndHTTPMiddleware(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	metrics, err := NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	handler := metrics.HTTPMiddleware("data", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.Pattern = "GET /v1/models"
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("ok"))
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d", response.Code)
	}

	input, cached, output, reasoning := int64(3), int64(1), int64(4), int64(2)
	metrics.RecordProviderCall(context.Background(), "openai", "chat.completions", "chat-model", "succeeded", 25*time.Millisecond, model.Usage{
		InputTokens: &input, CachedInputTokens: &cached, OutputTokens: &output, ReasoningOutputTokens: &reasoning,
	})
	metrics.RecordProviderCall(context.Background(), "dashscope", "images.generate", "image-model", "failed", time.Millisecond, model.Usage{})
	metrics.RecordAuditFailure(context.Background(), "chat.completions")

	var resourceMetrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &resourceMetrics); err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, scope := range resourceMetrics.ScopeMetrics {
		for _, item := range scope.Metrics {
			names[item.Name] = true
		}
	}
	for _, name := range []string{
		"ai_gateway.http.server.request.count", "ai_gateway.http.server.request.duration",
		"ai_gateway.http.server.active_requests", "ai_gateway.provider.request.count",
		"gen_ai.client.operation.duration", "gen_ai.client.token.usage", "ai_gateway.audit.write.failure",
	} {
		if !names[name] {
			t.Errorf("未采集指标 %q，已有 %#v", name, names)
		}
	}
}

func TestNewMetricsInstrumentErrors(t *testing.T) {
	t.Parallel()

	for failAt := 1; failAt <= 7; failAt++ {
		meter := &failingMeter{Meter: metricnoop.NewMeterProvider().Meter("test"), failAt: failAt}
		if metrics, err := NewMetrics(meter); err == nil || metrics != nil {
			t.Fatalf("failAt=%d: NewMetrics() = %#v, %v", failAt, metrics, err)
		}
	}
	if metrics := newNoopMetrics(); metrics == nil {
		t.Fatal("newNoopMetrics() = nil")
	}
}

func TestStatusWriter(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	writer := &statusWriter{ResponseWriter: response, status: http.StatusOK}
	writer.WriteHeader(http.StatusAccepted)
	writer.WriteHeader(http.StatusInternalServerError)
	if _, err := writer.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	writer.Flush()
	if writer.status != http.StatusAccepted || response.Code != http.StatusAccepted || writer.Unwrap() != response {
		t.Fatalf("statusWriter = %#v, response=%d", writer, response.Code)
	}

	plain := &basicWriter{header: make(http.Header)}
	plainStatus := &statusWriter{ResponseWriter: plain, status: http.StatusOK}
	plainStatus.Flush()
	if plain.status != http.StatusOK {
		t.Fatalf("Flush() status = %d", plain.status)
	}
	implicit := &statusWriter{ResponseWriter: &basicWriter{header: make(http.Header)}, status: http.StatusOK}
	if _, err := implicit.Write([]byte("x")); err != nil || implicit.status != http.StatusOK {
		t.Fatalf("implicit Write() = %d, %v", implicit.status, err)
	}
}

func TestTelemetryDisabled(t *testing.T) {
	t.Parallel()

	telemetry, err := Setup(context.Background(), config.OTel{Enabled: false})
	if err != nil || telemetry.Metrics() == nil || telemetry.isEnabled {
		t.Fatalf("Setup(disabled) = %#v, %v", telemetry, err)
	}
	base := roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") })
	got := telemetry.Transport(base)
	if _, ok := got.(roundTripperFunc); !ok {
		t.Fatal("disabled Transport() 不应包装 base")
	}
	if got := telemetry.Transport(nil); got != http.DefaultTransport {
		t.Fatal("disabled Transport(nil) 应返回 DefaultTransport")
	}
	handler := telemetry.WrapHTTP("operations", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("WrapHTTP() status = %d", response.Code)
	}
	if err := telemetry.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown(disabled) error = %v", err)
	}
}

func TestTelemetryEnabledSetup(t *testing.T) {
	// Setup 会更新 OTel 全局 Provider，故本测试不并行。
	cfg := config.OTel{
		Enabled: true, ServiceName: "ai-gateway-test", ServiceVersion: "test",
		OTLPHTTPEndpoint: "http://127.0.0.1:1/", Insecure: true,
		MetricExportInterval: config.Duration(time.Hour), TraceSampleRatio: 0,
	}
	telemetry, err := Setup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Setup(enabled) error = %v", err)
	}
	if !telemetry.isEnabled || telemetry.Metrics() == nil || telemetry.Transport(nil) == http.DefaultTransport {
		t.Fatalf("Setup(enabled) = %#v", telemetry)
	}
	wrapped := telemetry.WrapHTTP("data", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if wrapped == nil {
		t.Fatal("WrapHTTP(enabled) = nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = telemetry.Shutdown(ctx)
}

func TestTelemetryInvalidEndpoint(t *testing.T) {
	t.Parallel()

	_, err := Setup(context.Background(), config.OTel{
		Enabled: true, ServiceName: "test", OTLPHTTPEndpoint: "://bad",
		MetricExportInterval: config.Duration(time.Hour), TraceSampleRatio: 1,
	})
	if err == nil {
		t.Fatal("Setup(invalid endpoint) 应失败")
	}
}

func TestNormalizedRoute(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"/v1/chat/completions":              "/v1/chat/completions",
		"/v1/responses":                     "/v1/responses",
		"/v1/images/generations":            "/v1/images/generations",
		"/v1/models":                        "/v1/models",
		"/v1/token":                         "/v1/token",
		"/livez":                            "/livez",
		"/readyz":                           "/readyz",
		"/healthz":                          "/healthz",
		"/admin/v1/tokens":                  "/admin/v1/tokens",
		"/admin/v1/tokens/token-123":        "/admin/v1/tokens/{id}",
		"/admin/v1/tokens/token-123/rotate": "/admin/v1/tokens/{id}/rotate",
		"/admin/v1/routes/chat-route/active-backend/extra": "/admin/v1/routes/{id}/active-backend",
		"/unknown/path": "unknown",
	}
	for path, want := range tests {
		if got := normalizedRoute(path); got != want {
			t.Errorf("normalizedRoute(%q) = %q, want %q", path, got, want)
		}
	}

	metrics := newNoopMetrics()
	handler := metrics.HTTPMiddleware("data", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/some/random/id", nil))
}

type failingMeter struct {
	metric.Meter
	step   int
	failAt int
}

func (meter *failingMeter) fail() error {
	meter.step++
	if meter.step == meter.failAt {
		return errors.New("instrument failed")
	}
	return nil
}

func (meter *failingMeter) Int64Counter(name string, options ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	if err := meter.fail(); err != nil {
		return nil, err
	}
	return meter.Meter.Int64Counter(name, options...)
}

func (meter *failingMeter) Float64Histogram(name string, options ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	if err := meter.fail(); err != nil {
		return nil, err
	}
	return meter.Meter.Float64Histogram(name, options...)
}

func (meter *failingMeter) Int64UpDownCounter(name string, options ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	if err := meter.fail(); err != nil {
		return nil, err
	}
	return meter.Meter.Int64UpDownCounter(name, options...)
}

type basicWriter struct {
	header http.Header
	body   strings.Builder
	status int
}

func (writer *basicWriter) Header() http.Header { return writer.header }
func (writer *basicWriter) Write(data []byte) (int, error) {
	return writer.body.Write(data)
}
func (writer *basicWriter) WriteHeader(status int) { writer.status = status }

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
