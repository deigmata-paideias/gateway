package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/identity"
	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/provider"
	dashscopeprovider "github.com/deigmata-paideias/gateway/internal/provider/dashscope"
	openaiProvider "github.com/deigmata-paideias/gateway/internal/provider/openai"
	"github.com/deigmata-paideias/gateway/internal/secret"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

type Service struct {
	store     *sqlite.Store
	cipher    *secret.Cipher
	configs   *config.Manager
	transport http.RoundTripper
	recorder  Recorder
}

type Recorder interface {
	RecordProviderCall(
		ctx context.Context,
		providerName,
		operation,
		modelAlias,
		status string,
		duration time.Duration,
		usage model.Usage,
	)
	RecordAuditFailure(ctx context.Context, operation string)
}

type Option func(*Service)

func WithRecorder(recorder Recorder) Option {
	return func(service *Service) {
		if recorder != nil {
			service.recorder = recorder
		}
	}
}

type noopRecorder struct{}

func (noopRecorder) RecordProviderCall(context.Context, string, string, string, string, time.Duration, model.Usage) {
}

func (noopRecorder) RecordAuditFailure(context.Context, string) {}

func New(
	store *sqlite.Store,
	cipher *secret.Cipher,
	configs *config.Manager,
	transport http.RoundTripper,
	options ...Option,
) (*Service, error) {
	if store == nil || cipher == nil || configs == nil || configs.Current() == nil {
		return nil, errors.New("gateway: 依赖不能为空")
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	service := &Service{
		store: store, cipher: cipher, configs: configs, transport: transport, recorder: noopRecorder{},
	}
	for _, option := range options {
		option(service)
	}
	return service, nil
}

func (s *Service) Store() *sqlite.Store {
	return s.store
}

func (s *Service) CurrentSnapshot() *config.Snapshot {
	return s.configs.Current()
}

func (s *Service) newProvider(ctx context.Context, route config.ResolvedRoute) (provider.Client, error) {
	credential, err := s.store.Credential(ctx, route.Backend.CredentialID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if credential.Status != "active" || credential.Provider != route.Backend.Provider {
		return nil, newError(http.StatusServiceUnavailable, "credential_unavailable", "Provider Credential 不可用", errors.New("credential 状态或 provider 不匹配"))
	}
	plaintext, err := s.cipher.Decrypt("credential", credential.ID, credential.Provider, credential.Ciphertext)
	if err != nil {
		return nil, newError(http.StatusServiceUnavailable, "credential_unavailable", "Provider Credential 不可用", err)
	}
	defer clear(plaintext)
	client := &http.Client{
		Transport: s.transport,
		Timeout:   route.Backend.Timeouts.Request.Value(),
	}
	options := struct {
		baseURL       string
		apiKey        string
		maxImages     int
		maxImageBytes int64
	}{
		baseURL: route.Backend.BaseURL, apiKey: string(plaintext),
		maxImages:     s.configs.Current().Config().Limits.ImagesPerRequest,
		maxImageBytes: s.configs.Current().Config().Limits.ImageRawBytesPerResponse,
	}
	switch route.Backend.Provider {
	case "openai":
		return openaiProvider.New(openaiProvider.Options{
			BaseURL: options.baseURL, APIKey: options.apiKey, HTTPClient: client,
			MaxImages: options.maxImages, MaxImageBytes: options.maxImageBytes,
		})
	case "dashscope":
		return dashscopeprovider.New(dashscopeprovider.Options{
			BaseURL: options.baseURL, APIKey: options.apiKey, HTTPClient: client,
			MaxImages: options.maxImages, MaxImageBytes: options.maxImageBytes,
		})
	default:
		return nil, newError(http.StatusUnprocessableEntity, "unsupported_provider", "Provider 不受支持", errors.New("未知 provider"))
	}
}

func traceID(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

func newInternalID(prefix string) (string, error) {
	value, err := identity.New(prefix)
	if err != nil {
		return "", fmt.Errorf("生成 %s id: %w", prefix, err)
	}
	return value, nil
}

func finishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
}
