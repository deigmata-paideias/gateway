package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	ErrInvalidConfig = errors.New("config: invalid configuration")
	idPattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

func applyBootstrapDefaults(cfg *Bootstrap) {
	if cfg.APIVersion == "" {
		cfg.APIVersion = APIVersion
	}
	if cfg.Kind == "" {
		cfg.Kind = KindBootstrap
	}
	if cfg.Listeners.Data == "" {
		cfg.Listeners.Data = "0.0.0.0:8080"
	}
	if cfg.Listeners.Admin == "" {
		cfg.Listeners.Admin = "127.0.0.1:9090"
	}
	if cfg.Listeners.Operations == "" {
		cfg.Listeners.Operations = "0.0.0.0:8081"
	}
	if cfg.Storage.Driver == "" {
		cfg.Storage.Driver = "sqlite"
	}
	if cfg.Storage.MaxOpenConnections == 0 {
		cfg.Storage.MaxOpenConnections = 8
	}
	if cfg.Storage.BusyTimeout == 0 {
		cfg.Storage.BusyTimeout = Duration(5 * time.Second)
	}
	if cfg.Storage.JournalMode == "" {
		cfg.Storage.JournalMode = "WAL"
	}
	if cfg.Storage.Synchronous == "" {
		cfg.Storage.Synchronous = "FULL"
	}
	if cfg.Secrets.MasterKeyVersion == 0 {
		cfg.Secrets.MasterKeyVersion = 1
	}
	if cfg.Health.ReadyTimeout == 0 {
		cfg.Health.ReadyTimeout = Duration(500 * time.Millisecond)
	}
	if cfg.Health.ShutdownGracePeriod == 0 {
		cfg.Health.ShutdownGracePeriod = Duration(30 * time.Second)
	}
	if cfg.Observability.OTel.ServiceName == "" {
		cfg.Observability.OTel.ServiceName = "ai-gateway"
	}
	if cfg.Observability.OTel.MetricExportInterval == 0 {
		cfg.Observability.OTel.MetricExportInterval = Duration(10 * time.Second)
	}
	if cfg.Observability.OTel.CardinalityLimit == 0 {
		cfg.Observability.OTel.CardinalityLimit = 2000
	}
}

func applyGatewayDefaults(cfg *Gateway) {
	if cfg.APIVersion == "" {
		cfg.APIVersion = APIVersion
	}
	if cfg.Kind == "" {
		cfg.Kind = KindGateway
	}
	if cfg.Audit.Retention == 0 {
		cfg.Audit.Retention = Duration(30 * 24 * time.Hour)
	}
	if cfg.Audit.CleanupInterval == 0 {
		cfg.Audit.CleanupInterval = Duration(time.Hour)
	}
	if cfg.Audit.AbandonedAfter == 0 {
		cfg.Audit.AbandonedAfter = Duration(15 * time.Minute)
	}
	if cfg.Audit.CleanupBatchSize == 0 {
		cfg.Audit.CleanupBatchSize = 1000
	}
	if cfg.Responses.BindingRetention == 0 {
		cfg.Responses.BindingRetention = Duration(7 * 24 * time.Hour)
	}
	if cfg.Limits.RequestBodyBytes == 0 {
		cfg.Limits.RequestBodyBytes = 2 << 20
	}
	if cfg.Limits.MaxBackends == 0 {
		cfg.Limits.MaxBackends = 100
	}
	if cfg.Limits.MaxRoutes == 0 {
		cfg.Limits.MaxRoutes = 100
	}
	if cfg.Limits.ChatConcurrency == 0 {
		cfg.Limits.ChatConcurrency = 128
	}
	if cfg.Limits.ResponsesConcurrency == 0 {
		cfg.Limits.ResponsesConcurrency = 128
	}
	if cfg.Limits.ImageConcurrency == 0 {
		cfg.Limits.ImageConcurrency = 4
	}
	if cfg.Limits.ImagesPerRequest == 0 {
		cfg.Limits.ImagesPerRequest = 4
	}
	if cfg.Limits.ImageRawBytesPerResponse == 0 {
		cfg.Limits.ImageRawBytesPerResponse = 32 << 20
	}
	for i := range cfg.Backends {
		if cfg.Backends[i].Timeouts.Request == 0 {
			cfg.Backends[i].Timeouts.Request = Duration(120 * time.Second)
		}
		if cfg.Backends[i].Timeouts.StreamIdle == 0 {
			cfg.Backends[i].Timeouts.StreamIdle = Duration(30 * time.Second)
		}
	}
}

func ValidateBootstrap(cfg Bootstrap) error {
	if cfg.APIVersion != APIVersion || cfg.Kind != KindBootstrap {
		return invalid("api_version 或 kind 不受支持")
	}
	for name, address := range map[string]string{
		"data": cfg.Listeners.Data, "admin": cfg.Listeners.Admin, "operations": cfg.Listeners.Operations,
	} {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return invalid("listener %s 地址 %q 无效", name, address)
		}
	}
	if cfg.Storage.Driver != "sqlite" || cfg.Storage.Path == "" {
		return invalid("storage 必须使用 sqlite 且 path 非空")
	}
	if cfg.Storage.MaxOpenConnections < 1 || cfg.Storage.MaxOpenConnections > 32 {
		return invalid("max_open_connections 必须在 1 到 32 之间")
	}
	if cfg.Storage.BusyTimeout.Value() <= 0 || cfg.Storage.JournalMode != "WAL" || cfg.Storage.Synchronous != "FULL" {
		return invalid("sqlite 必须使用正 busy_timeout、WAL 和 FULL")
	}
	if cfg.Secrets.MasterKeyFile == "" || cfg.Secrets.MasterKeyVersion < 1 {
		return invalid("master key 文件和版本必须有效")
	}
	if cfg.InitialGatewayConfig == "" {
		return invalid("initial_gateway_config 不能为空")
	}
	if cfg.Health.ReadyTimeout.Value() <= 0 || cfg.Health.ShutdownGracePeriod.Value() <= 0 {
		return invalid("health timeout 必须为正数")
	}
	if cfg.Observability.OTel.TraceSampleRatio < 0 || cfg.Observability.OTel.TraceSampleRatio > 1 {
		return invalid("trace_sample_ratio 必须在 0 到 1 之间")
	}
	for _, item := range cfg.CredentialImports {
		if !idPattern.MatchString(item.CredentialID) || !validProvider(item.Provider) || item.SourceFile == "" {
			return invalid("credential import %q 无效", item.CredentialID)
		}
		if item.ImportPolicy != "if_missing" {
			return invalid("credential import %q 只支持 if_missing", item.CredentialID)
		}
	}
	return nil
}

func ValidateGateway(cfg Gateway) error {
	if cfg.APIVersion != APIVersion || cfg.Kind != KindGateway {
		return invalid("api_version 或 kind 不受支持")
	}
	if len(cfg.Backends) == 0 || len(cfg.Backends) > cfg.Limits.MaxBackends {
		return invalid("backend 数量必须在 1 到 %d 之间", cfg.Limits.MaxBackends)
	}
	if len(cfg.Routes) == 0 || len(cfg.Routes) > cfg.Limits.MaxRoutes {
		return invalid("route 数量必须在 1 到 %d 之间", cfg.Limits.MaxRoutes)
	}
	backends := make(map[string]Backend, len(cfg.Backends))
	for _, backend := range cfg.Backends {
		if err := validateBackend(backend); err != nil {
			return err
		}
		if _, ok := backends[backend.ID]; ok {
			return invalid("backend id %q 重复", backend.ID)
		}
		backends[backend.ID] = backend
	}
	routeIDs := make(map[string]struct{}, len(cfg.Routes))
	routeKeys := make(map[string]struct{}, len(cfg.Routes))
	operations := make(map[string]bool, 3)
	for _, route := range cfg.Routes {
		if err := validateRoute(route, backends); err != nil {
			return err
		}
		if _, ok := routeIDs[route.ID]; ok {
			return invalid("route id %q 重复", route.ID)
		}
		routeIDs[route.ID] = struct{}{}
		key := route.Operation + "\x00" + route.ModelAlias
		if _, ok := routeKeys[key]; ok {
			return invalid("operation/model_alias %q/%q 重复", route.Operation, route.ModelAlias)
		}
		routeKeys[key] = struct{}{}
		operations[route.Operation] = true
	}
	for _, operation := range []string{"chat", "responses", "image"} {
		if !operations[operation] {
			return invalid("至少需要一条 %s route", operation)
		}
	}
	if cfg.Limits.RequestBodyBytes < 1024 || cfg.Limits.ImagesPerRequest < 1 || cfg.Limits.ImageRawBytesPerResponse < 1024 {
		return invalid("limits 取值过小")
	}
	if cfg.Limits.ChatConcurrency < 1 || cfg.Limits.ResponsesConcurrency < 1 || cfg.Limits.ImageConcurrency < 1 {
		return invalid("concurrency 必须为正数")
	}
	if cfg.Audit.Retention.Value() <= 0 || cfg.Audit.CleanupInterval.Value() <= 0 || cfg.Audit.AbandonedAfter.Value() <= 0 {
		return invalid("audit duration 必须为正数")
	}
	if cfg.Audit.CleanupBatchSize < 1 || cfg.Responses.BindingRetention.Value() <= 0 {
		return invalid("cleanup batch 和 binding retention 必须为正数")
	}
	return nil
}

func validateBackend(backend Backend) error {
	if !idPattern.MatchString(backend.ID) || !idPattern.MatchString(backend.CredentialID) {
		return invalid("backend 或 credential id %q 无效", backend.ID)
	}
	if !validProvider(backend.Provider) {
		return invalid("backend %q provider 无效", backend.ID)
	}
	parsed, err := url.Parse(backend.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return invalid("backend %q base_url 无效", backend.ID)
	}
	if backend.Provider == "dashscope" && strings.Contains(parsed.Path, "/api/v2/apps/protocols/") {
		return invalid("backend %q 使用已废弃的 dashscope responses 路径", backend.ID)
	}
	if len(backend.Capabilities) == 0 {
		return invalid("backend %q capabilities 不能为空", backend.ID)
	}
	seen := make(map[string]struct{}, len(backend.Capabilities))
	for _, capability := range backend.Capabilities {
		if !validOperation(capability) {
			return invalid("backend %q capability %q 无效", backend.ID, capability)
		}
		if _, ok := seen[capability]; ok {
			return invalid("backend %q capability %q 重复", backend.ID, capability)
		}
		seen[capability] = struct{}{}
	}
	if backend.Timeouts.Request.Value() <= 0 || backend.Timeouts.StreamIdle.Value() <= 0 {
		return invalid("backend %q timeout 必须为正数", backend.ID)
	}
	return nil
}

func validateRoute(route Route, backends map[string]Backend) error {
	if !idPattern.MatchString(route.ID) || !validOperation(route.Operation) || route.ModelAlias == "" {
		return invalid("route %q 基础字段无效", route.ID)
	}
	if len(route.Targets) == 0 {
		return invalid("route %q targets 不能为空", route.ID)
	}
	activeFound := false
	seen := make(map[string]struct{}, len(route.Targets))
	for _, target := range route.Targets {
		backend, ok := backends[target.BackendID]
		if !ok {
			return invalid("route %q 引用不存在的 backend %q", route.ID, target.BackendID)
		}
		if _, ok := seen[target.BackendID]; ok {
			return invalid("route %q target %q 重复", route.ID, target.BackendID)
		}
		seen[target.BackendID] = struct{}{}
		if target.UpstreamModel == "" || !slices.Contains(backend.Capabilities, route.Operation) {
			return invalid("route %q target %q 与 operation 不兼容", route.ID, target.BackendID)
		}
		activeFound = activeFound || target.BackendID == route.ActiveBackend
	}
	if !activeFound {
		return invalid("route %q active_backend 不在 targets 中", route.ID)
	}
	return nil
}

func validProvider(provider string) bool {
	return provider == "openai" || provider == "dashscope"
}

func validOperation(operation string) bool {
	return operation == "chat" || operation == "responses" || operation == "image"
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}
