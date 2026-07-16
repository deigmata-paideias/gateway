// Package observe 初始化 OpenTelemetry，并提供低基数 HTTP 和 GenAI 指标。
package observe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"

	"github.com/deigmata-paideias/gateway/internal/config"
)

type Telemetry struct {
	traceProvider *sdktrace.TracerProvider
	meterProvider *sdkmetric.MeterProvider
	metrics       *Metrics
	isEnabled     bool
}

func Setup(ctx context.Context, cfg config.OTel) (*Telemetry, error) {
	if !cfg.Enabled {
		return &Telemetry{metrics: newNoopMetrics()}, nil
	}
	endpoint := strings.TrimRight(cfg.OTLPHTTPEndpoint, "/")
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Host == "" || (parsedEndpoint.Scheme != "http" && parsedEndpoint.Scheme != "https") {
		return nil, fmt.Errorf("OTLP HTTP endpoint 无效: %q", cfg.OTLPHTTPEndpoint)
	}
	traceOptions := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(endpoint + "/v1/traces")}
	metricOptions := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(endpoint + "/v1/metrics")}
	if cfg.Insecure {
		traceOptions = append(traceOptions, otlptracehttp.WithInsecure())
		metricOptions = append(metricOptions, otlpmetrichttp.WithInsecure())
	}
	traceExporter, err := otlptracehttp.New(ctx, traceOptions...)
	if err != nil {
		return nil, fmt.Errorf("创建 otlp trace exporter: %w", err)
	}
	metricExporter, err := otlpmetrichttp.New(ctx, metricOptions...)
	if err != nil {
		return nil, fmt.Errorf("创建 otlp metric exporter: %w", err)
	}
	res, err := resource.New(
		ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 otel resource: %w", err)
	}
	traceProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.TraceSampleRatio))),
	)
	reader := sdkmetric.NewPeriodicReader(
		metricExporter,
		sdkmetric.WithInterval(cfg.MetricExportInterval.Value()),
	)
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)
	otel.SetTracerProvider(traceProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	metrics, err := NewMetrics(meterProvider.Meter("github.com/deigmata-paideias/gateway"))
	if err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return nil, errors.Join(err, traceProvider.Shutdown(shutdownCtx), meterProvider.Shutdown(shutdownCtx))
	}
	return &Telemetry{
		traceProvider: traceProvider, meterProvider: meterProvider, metrics: metrics, isEnabled: true,
	}, nil
}

func (t *Telemetry) Metrics() *Metrics {
	return t.metrics
}

func (t *Telemetry) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if !t.isEnabled {
		return base
	}
	return otelhttp.NewTransport(base)
}

func (t *Telemetry) WrapHTTP(listener string, handler http.Handler) http.Handler {
	metricsHandler := t.metrics.HTTPMiddleware(listener, handler)
	if !t.isEnabled {
		return metricsHandler
	}
	return otelhttp.NewHandler(
		metricsHandler,
		"ai-gateway.http",
		otelhttp.WithSpanNameFormatter(func(_ string, request *http.Request) string {
			return listener + " " + request.Method + " " + normalizedRoute(request.URL.Path)
		}),
	)
}

func (t *Telemetry) Shutdown(ctx context.Context) error {
	if !t.isEnabled {
		return nil
	}
	return errors.Join(t.meterProvider.Shutdown(ctx), t.traceProvider.Shutdown(ctx))
}

func normalizedRoute(path string) string {
	if path == "/v1/chat/completions" || path == "/v1/responses" || path == "/v1/images/generations" ||
		path == "/v1/models" || path == "/v1/token" || path == "/livez" || path == "/readyz" || path == "/healthz" {
		return path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "admin" && parts[1] == "v1" {
		if len(parts) > 4 {
			return "/admin/v1/" + parts[2] + "/{id}/" + parts[4]
		}
		if len(parts) == 4 {
			return "/admin/v1/" + parts[2] + "/{id}"
		}
		return "/admin/v1/" + parts[2]
	}
	return "unknown"
}
