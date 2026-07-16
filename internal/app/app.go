// Package app 负责网关进程的依赖装配和生命周期。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/deigmata-paideias/gateway/internal/api"
	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/gateway"
	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/observe"
	"github.com/deigmata-paideias/gateway/internal/secret"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

type App struct {
	bootstrap config.Bootstrap
	service   *gateway.Service
	store     *sqlite.Store
	telemetry *observe.Telemetry
	logger    *slog.Logger
	servers   []*http.Server
}

func Build(ctx context.Context, bootstrapPath string, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	bootstrap, err := config.LoadBootstrap(bootstrapPath)
	if err != nil {
		return nil, err
	}
	masterKey, err := secret.LoadMasterKey(bootstrap.Secrets.MasterKeyFile)
	if err != nil {
		return nil, err
	}
	cipher, err := secret.NewCipher(masterKey, bootstrap.Secrets.MasterKeyVersion)
	clear(masterKey)
	if err != nil {
		return nil, err
	}
	store, err := sqlite.Open(ctx, sqlite.Options{
		Path:         bootstrap.Storage.Path,
		MaxOpenConns: bootstrap.Storage.MaxOpenConnections,
		BusyTimeout:  bootstrap.Storage.BusyTimeout.Value(),
	})
	if err != nil {
		return nil, err
	}
	closeOnError := func(buildErr error) (*App, error) {
		return nil, errors.Join(buildErr, store.Close())
	}
	for _, item := range bootstrap.CredentialImports {
		created, importErr := importCredential(ctx, store, cipher, item)
		if importErr != nil {
			return closeOnError(importErr)
		}
		if created {
			logger.InfoContext(ctx, "已导入 provider credential", "credential_id", item.CredentialID, "provider", item.Provider)
		}
	}
	snapshot, err := gateway.LoadSnapshot(ctx, store, bootstrap.InitialGatewayConfig)
	if err != nil {
		return closeOnError(err)
	}
	if err := validateCredentials(ctx, store, snapshot.Config()); err != nil {
		return closeOnError(err)
	}
	telemetry, err := observe.Setup(ctx, bootstrap.Observability.OTel)
	if err != nil {
		logger.WarnContext(ctx, "otel 初始化失败，使用 no-op provider", "error", err)
		telemetry, err = observe.Setup(ctx, config.OTel{Enabled: false})
		if err != nil {
			return closeOnError(err)
		}
	}
	service, err := gateway.New(
		store,
		cipher,
		config.NewManager(snapshot),
		telemetry.Transport(http.DefaultTransport),
		gateway.WithRecorder(telemetry.Metrics()),
	)
	if err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return nil, errors.Join(err, telemetry.Shutdown(shutdownCtx), store.Close())
	}
	if _, err := store.MarkAbandoned(ctx, time.Now().Add(-snapshot.Config().Audit.AbandonedAfter.Value()).UnixMilli()); err != nil {
		logger.WarnContext(ctx, "修复遗留 started 审计失败", "error", err)
	}
	app := &App{
		bootstrap: bootstrap,
		service:   service,
		store:     store,
		telemetry: telemetry,
		logger:    logger,
	}
	app.servers = app.makeServers()
	return app, nil
}

func (a *App) Run(ctx context.Context) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	errCh := make(chan error, len(a.servers))
	var waitGroup sync.WaitGroup
	for _, server := range a.servers {
		server := server
		waitGroup.Go(func() {
			a.logger.InfoContext(runCtx, "http listener 启动", "address", server.Addr)
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("监听 %s: %w", server.Addr, err)
			}
		})
	}
	waitGroup.Go(func() {
		a.runCleanup(runCtx)
	})
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errCh:
	}
	cancelRun()
	shutdownCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		a.bootstrap.Health.ShutdownGracePeriod.Value(),
	)
	defer cancel()
	shutdownErrors := make([]error, 0, len(a.servers)+2)
	for _, server := range a.servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("关闭 listener %s: %w", server.Addr, err))
		}
	}
	waitGroup.Wait()
	shutdownErrors = append(shutdownErrors, a.telemetry.Shutdown(shutdownCtx), a.store.Close())
	return errors.Join(append([]error{runErr}, shutdownErrors...)...)
}

func (a *App) makeServers() []*http.Server {
	dataHandler := a.telemetry.WrapHTTP("data", api.NewDataHandler(a.service))
	adminHandler := a.telemetry.WrapHTTP("admin", api.NewAdminHandler(a.service))
	operationsHandler := a.telemetry.WrapHTTP(
		"operations",
		api.NewOperationsHandler(a.service, a.bootstrap.Health.ReadyTimeout.Value()),
	)
	return []*http.Server{
		newHTTPServer(a.bootstrap.Listeners.Data, dataHandler),
		newHTTPServer(a.bootstrap.Listeners.Admin, adminHandler),
		newHTTPServer(a.bootstrap.Listeners.Operations, operationsHandler),
	}
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func (a *App) runCleanup(ctx context.Context) {
	interval := a.service.CurrentSnapshot().Config().Audit.CleanupInterval.Value()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gatewayConfig := a.service.CurrentSnapshot().Config()
			now := time.Now()
			_, err := a.store.Cleanup(
				ctx,
				now.Add(-gatewayConfig.Audit.Retention.Value()).UnixMilli(),
				now.UnixMilli(),
				gatewayConfig.Audit.CleanupBatchSize,
			)
			if err != nil && ctx.Err() == nil {
				a.logger.WarnContext(ctx, "清理历史数据失败", "error", err)
			}
		}
	}
}

func importCredential(
	ctx context.Context,
	store *sqlite.Store,
	cipher *secret.Cipher,
	item config.CredentialImport,
) (bool, error) {
	data, err := os.ReadFile(item.SourceFile)
	if err != nil {
		return false, fmt.Errorf("读取 credential import %q: %w", item.CredentialID, err)
	}
	data = []byte(strings.TrimSpace(string(data)))
	defer clear(data)
	if len(data) == 0 || len(data) > 16<<10 {
		return false, fmt.Errorf("credential import %q secret 大小无效", item.CredentialID)
	}
	ciphertext, err := cipher.Encrypt("credential", item.CredentialID, item.Provider, data)
	if err != nil {
		return false, fmt.Errorf("加密 credential import %q: %w", item.CredentialID, err)
	}
	now := time.Now().UnixMilli()
	return store.CreateCredentialIfMissing(ctx, model.Credential{
		ID: item.CredentialID, Provider: item.Provider, Name: item.CredentialID, Status: "active",
		Ciphertext: ciphertext, KeyVersion: cipher.KeyVersion(), CreatedAt: now, UpdatedAt: now,
	})
}

func validateCredentials(ctx context.Context, store *sqlite.Store, cfg config.Gateway) error {
	for _, backend := range cfg.Backends {
		credential, err := store.Credential(ctx, backend.CredentialID)
		if err != nil {
			return fmt.Errorf("backend %q credential %q 不可用: %w", backend.ID, backend.CredentialID, err)
		}
		if credential.Provider != backend.Provider || credential.Status != "active" {
			return fmt.Errorf("backend %q credential provider 或状态不匹配", backend.ID)
		}
	}
	return nil
}
