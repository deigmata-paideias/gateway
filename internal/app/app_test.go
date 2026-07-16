package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/secret"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

func TestBuildAndServerAssembly(t *testing.T) {
	t.Parallel()

	files := writeAppFixture(t, fixtureOptions{})
	application, err := Build(context.Background(), files.bootstrapPath, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	t.Cleanup(func() {
		_ = application.telemetry.Shutdown(context.Background())
		_ = application.store.Close()
	})
	if application.service.CurrentSnapshot().Revision() != 1 || len(application.servers) != 3 {
		t.Fatalf("Build() app = %#v", application)
	}
	credential, err := application.store.Credential(context.Background(), "dashscope-main")
	if err != nil || credential.Provider != "dashscope" || len(credential.Ciphertext) == 0 {
		t.Fatalf("imported credential = %#v, %v", credential, err)
	}
	for _, server := range application.servers {
		if server.Handler == nil || server.ReadHeaderTimeout != 5*time.Second || server.IdleTimeout != 90*time.Second || server.MaxHeaderBytes != 1<<20 {
			t.Fatalf("server config = %#v", server)
		}
	}
	server := newHTTPServer("127.0.0.1:12345", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if server.Addr != "127.0.0.1:12345" || server.Handler == nil {
		t.Fatalf("newHTTPServer() = %#v", server)
	}
}

func TestBuildOTelFallback(t *testing.T) {
	// OTel Setup 会更新全局 Provider，本测试不并行。
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	files := writeAppFixture(t, fixtureOptions{invalidOTelEndpoint: true})
	application, err := Build(context.Background(), files.bootstrapPath, logger)
	if err != nil {
		t.Fatalf("Build(OTel fallback) error = %v", err)
	}
	defer application.store.Close()
	defer application.telemetry.Shutdown(context.Background())
	if !strings.Contains(logs.String(), "otel 初始化失败") {
		t.Fatalf("OTel fallback 未记录日志: %s", logs.String())
	}
}

func TestBuildFailures(t *testing.T) {
	t.Parallel()

	if _, err := Build(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), nil); err == nil {
		t.Fatal("Build(missing bootstrap) 应失败")
	}

	tests := []struct {
		name    string
		options fixtureOptions
	}{
		{"invalid-master-key", fixtureOptions{invalidMasterKey: true}},
		{"missing-import", fixtureOptions{missingImport: true}},
		{"empty-import", fixtureOptions{emptyImport: true}},
		{"large-import", fixtureOptions{largeImport: true}},
		{"missing-gateway", fixtureOptions{missingGateway: true}},
		{"missing-credential", fixtureOptions{omitImports: true}},
		{"provider-mismatch", fixtureOptions{credentialProvider: "openai"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := writeAppFixture(t, test.options)
			if _, err := Build(context.Background(), files.bootstrapPath, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
				t.Fatal("Build() 应失败")
			}
		})
	}
}

func TestRunListenerFailureAndCancelledContext(t *testing.T) {
	t.Parallel()

	files := writeAppFixture(t, fixtureOptions{})
	application, err := Build(context.Background(), files.bootstrapPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for index, server := range application.servers {
		server.Addr = "invalid-address-" + string(rune('a'+index))
	}
	if err := application.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "监听") {
		t.Fatalf("Run(invalid listener) error = %v", err)
	}

	files = writeAppFixture(t, fixtureOptions{})
	cancelledApp, err := Build(context.Background(), files.bootstrapPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	cancelledApp.servers = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cancelledApp.Run(ctx); err != nil {
		t.Fatalf("Run(cancelled) error = %v", err)
	}
}

func TestCleanupLoop(t *testing.T) {
	t.Parallel()

	files := writeAppFixture(t, fixtureOptions{shortRetention: true})
	application, err := Build(context.Background(), files.bootstrapPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer application.telemetry.Shutdown(context.Background())
	defer application.store.Close()
	issued, err := application.service.CreateToken(context.Background(), "cleanup", nil, "req_cleanup_token")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if err := application.store.PutBinding(context.Background(), model.ResponseBinding{
		PublicResponseID: "resp_expired", GatewayTokenID: issued.Token.ID, BackendID: "dashscope-main",
		Provider: "dashscope", ModelAlias: "response-model", UpstreamResponseID: "resp_up",
		Revision: 1, ExpiresAt: now - 1, State: "active",
	}); err != nil {
		t.Fatal(err)
	}
	audit := model.Audit{
		ID: "aud_cleanup", RequestID: "req_cleanup", GatewayTokenID: issued.Token.ID,
		Operation: "chat.completions", StartedAt: now - 1000,
	}
	if err := application.store.StartAudit(context.Background(), audit); err != nil {
		t.Fatal(err)
	}
	if err := application.store.FinishAudit(context.Background(), model.AuditFinish{
		ID: audit.ID, Status: "succeeded", HTTPStatus: 200, FinishedAt: now - 900, DurationMillis: 100,
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	application.runCleanup(ctx)
	if _, err := application.store.ResolveBinding(context.Background(), "resp_expired", issued.Token.ID); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("expired binding 未清理: %v", err)
	}
	if _, err := application.store.Audit(context.Background(), audit.ID); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("old audit 未清理: %v", err)
	}
}

func TestCleanupLoopHandlesStorageError(t *testing.T) {
	t.Parallel()

	files := writeAppFixture(t, fixtureOptions{shortRetention: true})
	var logs bytes.Buffer
	application, err := Build(context.Background(), files.bootstrapPath, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer application.telemetry.Shutdown(context.Background())
	if err := application.store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	application.runCleanup(ctx)
	if !strings.Contains(logs.String(), "清理历史数据失败") {
		t.Fatalf("cleanup error 未记录: %s", logs.String())
	}
}

func TestImportCredentialAndValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Options{Path: filepath.Join(t.TempDir(), "gateway.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cipher, err := secret.NewCipher([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "provider.key")
	if err := os.WriteFile(keyPath, []byte(" secret \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	item := config.CredentialImport{CredentialID: "dashscope-main", Provider: "dashscope", SourceFile: keyPath, ImportPolicy: "if_missing"}
	created, err := importCredential(ctx, store, cipher, item)
	if err != nil || !created {
		t.Fatalf("importCredential() = %v, %v", created, err)
	}
	created, err = importCredential(ctx, store, cipher, item)
	if err != nil || created {
		t.Fatalf("importCredential(existing) = %v, %v", created, err)
	}
	if err := validateCredentials(ctx, store, appGatewayConfig(false)); err != nil {
		t.Fatalf("validateCredentials() error = %v", err)
	}

	missing := appGatewayConfig(false)
	missing.Backends[0].CredentialID = "missing"
	if err := validateCredentials(ctx, store, missing); err == nil {
		t.Fatal("validateCredentials(missing) 应失败")
	}
	mismatch := appGatewayConfig(false)
	mismatch.Backends[0].Provider = "openai"
	if err := validateCredentials(ctx, store, mismatch); err == nil {
		t.Fatal("validateCredentials(mismatch) 应失败")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateCredentials(ctx, store, appGatewayConfig(false)); err == nil {
		t.Fatal("validateCredentials(closed) 应失败")
	}

	item.SourceFile = filepath.Join(directory, "missing")
	if _, err := importCredential(ctx, store, cipher, item); err == nil {
		t.Fatal("importCredential(missing) 应失败")
	}
}

type fixtureOptions struct {
	invalidMasterKey    bool
	missingImport       bool
	emptyImport         bool
	largeImport         bool
	missingGateway      bool
	omitImports         bool
	credentialProvider  string
	invalidOTelEndpoint bool
	shortRetention      bool
}

type appFixtureFiles struct {
	bootstrapPath string
}

func writeAppFixture(t *testing.T, options fixtureOptions) appFixtureFiles {
	t.Helper()
	directory := t.TempDir()
	masterPath := filepath.Join(directory, "master.key")
	master := []byte("0123456789abcdef0123456789abcdef")
	if options.invalidMasterKey {
		master = []byte("invalid")
	}
	if err := os.WriteFile(masterPath, master, 0o600); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(directory, "provider.key")
	credential := []byte("provider-secret")
	if options.emptyImport {
		credential = nil
	}
	if options.largeImport {
		credential = bytes.Repeat([]byte("x"), (16<<10)+1)
	}
	if !options.missingImport {
		if err := os.WriteFile(credentialPath, credential, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gatewayPath := filepath.Join(directory, "gateway.yaml")
	if !options.missingGateway {
		gatewayData, err := yaml.Marshal(appGatewayConfig(options.shortRetention))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(gatewayPath, gatewayData, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	providerName := options.credentialProvider
	if providerName == "" {
		providerName = "dashscope"
	}
	imports := []config.CredentialImport{{
		CredentialID: "dashscope-main", Provider: providerName, SourceFile: credentialPath, ImportPolicy: "if_missing",
	}}
	if options.omitImports {
		imports = nil
	}
	otel := config.OTel{Enabled: false}
	if options.invalidOTelEndpoint {
		otel = config.OTel{
			Enabled: true, ServiceName: "test", OTLPHTTPEndpoint: "://bad",
			MetricExportInterval: config.Duration(time.Hour), TraceSampleRatio: 0,
		}
	}
	bootstrap := config.Bootstrap{
		APIVersion: config.APIVersion, Kind: config.KindBootstrap,
		Listeners: config.Listeners{Data: "127.0.0.1:0", Admin: "127.0.0.1:0", Operations: "127.0.0.1:0"},
		Storage: config.Storage{
			Driver: "sqlite", Path: filepath.Join(directory, "gateway.db"), MaxOpenConnections: 4,
			BusyTimeout: config.Duration(time.Second), JournalMode: "WAL", Synchronous: "FULL",
		},
		Secrets:           config.Secrets{MasterKeyFile: masterPath, MasterKeyVersion: 1},
		CredentialImports: imports, InitialGatewayConfig: gatewayPath,
		Observability: config.Observability{OTel: otel},
		Health:        config.Health{ReadyTimeout: config.Duration(time.Second), ShutdownGracePeriod: config.Duration(time.Second)},
	}
	bootstrapData, err := yaml.Marshal(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapPath := filepath.Join(directory, "bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, bootstrapData, 0o600); err != nil {
		t.Fatal(err)
	}
	return appFixtureFiles{bootstrapPath: bootstrapPath}
}

func appGatewayConfig(shortRetention bool) config.Gateway {
	retention := time.Hour
	cleanupInterval := time.Minute
	if shortRetention {
		retention = time.Millisecond
		cleanupInterval = time.Millisecond
	}
	route := func(id, operation, alias, upstream string) config.Route {
		return config.Route{
			ID: id, Operation: operation, ModelAlias: alias, ActiveBackend: "dashscope-main",
			Targets: []config.Target{{BackendID: "dashscope-main", UpstreamModel: upstream}},
		}
	}
	return config.Gateway{
		APIVersion: config.APIVersion, Kind: config.KindGateway,
		Backends: []config.Backend{{
			ID: "dashscope-main", Provider: "dashscope", BaseURL: "https://dashscope.test/v1", CredentialID: "dashscope-main",
			Capabilities: []string{"chat", "responses", "image"},
			Timeouts:     config.BackendTimeouts{Request: config.Duration(time.Second), StreamIdle: config.Duration(time.Second)},
		}},
		Routes: []config.Route{
			route("chat-route", "chat", "chat-model", "qwen-chat"),
			route("response-route", "responses", "response-model", "qwen-response"),
			route("image-route", "image", "image-model", "wanx-image"),
		},
		Audit: config.Audit{
			Retention: config.Duration(retention), CleanupInterval: config.Duration(cleanupInterval),
			AbandonedAfter: config.Duration(time.Minute), CleanupBatchSize: 100,
		},
		Responses: config.Responses{BindingRetention: config.Duration(time.Hour)},
		Limits: config.Limits{
			RequestBodyBytes: 4096, MaxBackends: 10, MaxRoutes: 10,
			ChatConcurrency: 4, ResponsesConcurrency: 4, ImageConcurrency: 2,
			ImagesPerRequest: 2, ImageRawBytesPerResponse: 1 << 20,
		},
	}
}
