package observe

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/deigmata-paideias/gateway/internal/model"
)

type Metrics struct {
	httpRequests       metric.Int64Counter
	httpDuration       metric.Float64Histogram
	httpActive         metric.Int64UpDownCounter
	providerRequests   metric.Int64Counter
	providerDuration   metric.Float64Histogram
	tokenUsage         metric.Int64Counter
	auditWriteFailures metric.Int64Counter
}

func NewMetrics(meter metric.Meter) (*Metrics, error) {
	httpRequests, err := meter.Int64Counter(
		"ai_gateway.http.server.request.count",
		metric.WithDescription("网关处理的 HTTP 请求数"),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 http request counter: %w", err)
	}
	httpDuration, err := meter.Float64Histogram(
		"ai_gateway.http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("网关 HTTP 请求时延"),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 http duration histogram: %w", err)
	}
	httpActive, err := meter.Int64UpDownCounter(
		"ai_gateway.http.server.active_requests",
		metric.WithDescription("网关正在处理的 HTTP 请求数"),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 http active counter: %w", err)
	}
	providerRequests, err := meter.Int64Counter(
		"ai_gateway.provider.request.count",
		metric.WithDescription("Provider 调用次数"),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 provider counter: %w", err)
	}
	providerDuration, err := meter.Float64Histogram(
		"gen_ai.client.operation.duration",
		metric.WithUnit("s"),
		metric.WithDescription("GenAI Provider 调用时延"),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 provider duration histogram: %w", err)
	}
	tokenUsage, err := meter.Int64Counter(
		"gen_ai.client.token.usage",
		metric.WithUnit("{token}"),
		metric.WithDescription("Provider 返回的模型 Token 用量"),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 token usage counter: %w", err)
	}
	auditWriteFailures, err := meter.Int64Counter(
		"ai_gateway.audit.write.failure",
		metric.WithDescription("审计终态写入失败次数"),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 audit failure counter: %w", err)
	}
	return &Metrics{
		httpRequests: httpRequests, httpDuration: httpDuration, httpActive: httpActive,
		providerRequests: providerRequests, providerDuration: providerDuration,
		tokenUsage: tokenUsage, auditWriteFailures: auditWriteFailures,
	}, nil
}

func newNoopMetrics() *Metrics {
	metrics, err := NewMetrics(noop.NewMeterProvider().Meter("ai-gateway/noop"))
	if err != nil {
		panic(err)
	}
	return metrics
}

func (m *Metrics) HTTPMiddleware(listener string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		attributes := []attribute.KeyValue{
			attribute.String("server.listener", listener),
			attribute.String("http.request.method", r.Method),
		}
		m.httpActive.Add(r.Context(), 1, metric.WithAttributes(attributes...))
		defer m.httpActive.Add(r.Context(), -1, metric.WithAttributes(attributes...))
		writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(writer, r)
		route := r.Pattern
		if route == "" {
			route = normalizedRoute(r.URL.Path)
		}
		resultAttributes := append(attributes,
			attribute.String("http.route", route),
			attribute.String("http.response.status_code", strconv.Itoa(writer.status)),
		)
		m.httpRequests.Add(r.Context(), 1, metric.WithAttributes(resultAttributes...))
		m.httpDuration.Record(r.Context(), time.Since(started).Seconds(), metric.WithAttributes(resultAttributes...))
	})
}

func (m *Metrics) RecordProviderCall(
	ctx context.Context,
	providerName,
	operation,
	modelAlias,
	status string,
	duration time.Duration,
	usage model.Usage,
) {
	attributes := []attribute.KeyValue{
		attribute.String("gen_ai.provider.name", providerName),
		attribute.String("gen_ai.operation.name", operation),
		attribute.String("gen_ai.request.model", modelAlias),
		attribute.String("ai_gateway.result", status),
	}
	m.providerRequests.Add(ctx, 1, metric.WithAttributes(attributes...))
	m.providerDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attributes...))
	m.recordUsage(ctx, usage, attributes)
}

func (m *Metrics) RecordAuditFailure(ctx context.Context, operation string) {
	m.auditWriteFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("gen_ai.operation.name", operation)))
}

func (m *Metrics) recordUsage(ctx context.Context, usage model.Usage, base []attribute.KeyValue) {
	values := []struct {
		name  string
		value *int64
	}{
		{name: "input", value: usage.InputTokens},
		{name: "cached_input", value: usage.CachedInputTokens},
		{name: "output", value: usage.OutputTokens},
		{name: "reasoning_output", value: usage.ReasoningOutputTokens},
	}
	for _, item := range values {
		if item.value == nil {
			continue
		}
		attributes := append(base, attribute.String("gen_ai.token.type", item.name))
		m.tokenUsage.Add(ctx, *item.value, metric.WithAttributes(attributes...))
	}
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	isCommitted bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.isCommitted {
		return
	}
	w.status = status
	w.isCommitted = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.isCommitted {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) Flush() {
	if !w.isCommitted {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
