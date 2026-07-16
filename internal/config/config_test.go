package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDuration(t *testing.T) {
	t.Parallel()

	var duration Duration
	if err := duration.UnmarshalText([]byte("1.5s")); err != nil {
		t.Fatalf("UnmarshalText() error = %v", err)
	}
	if got := duration.Value(); got != 1500*time.Millisecond {
		t.Fatalf("Value() = %v", got)
	}
	text, err := duration.MarshalText()
	if err != nil || string(text) != "1.5s" {
		t.Fatalf("MarshalText() = %q, %v", text, err)
	}
	data, err := json.Marshal(duration)
	if err != nil || string(data) != `"1.5s"` {
		t.Fatalf("MarshalJSON() = %s, %v", data, err)
	}
	if err := json.Unmarshal([]byte(`"2m"`), &duration); err != nil || duration.Value() != 2*time.Minute {
		t.Fatalf("UnmarshalJSON() duration = %v, error = %v", duration.Value(), err)
	}
	if err := duration.UnmarshalText([]byte("wrong")); err == nil {
		t.Fatal("UnmarshalText() 应拒绝非法 duration")
	}
	if err := json.Unmarshal([]byte(`1`), &duration); err == nil {
		t.Fatal("UnmarshalJSON() 应拒绝非字符串")
	}
}

func TestLoadBootstrapDefaultsAndValidation(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	masterKey := filepath.Join(directory, "master.key")
	gatewayPath := filepath.Join(directory, "gateway.yaml")
	path := filepath.Join(directory, "bootstrap.yaml")
	content := strings.Join([]string{
		"storage:",
		"  path: " + filepath.Join(directory, "gateway.db"),
		"secrets:",
		"  master_key_file: " + masterKey,
		"initial_gateway_config: " + gatewayPath,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadBootstrap(path)
	if err != nil {
		t.Fatalf("LoadBootstrap() error = %v", err)
	}
	if cfg.APIVersion != APIVersion || cfg.Kind != KindBootstrap || cfg.Listeners.Data != "0.0.0.0:8080" {
		t.Fatalf("默认值未应用: %#v", cfg)
	}
	if cfg.Storage.Driver != "sqlite" || cfg.Storage.BusyTimeout.Value() != 5*time.Second ||
		cfg.Storage.MaxOpenConnections != 8 || cfg.Storage.JournalMode != "WAL" || cfg.Storage.Synchronous != "FULL" {
		t.Fatalf("SQLite 默认值不正确: %#v", cfg.Storage)
	}
	if cfg.Secrets.MasterKeyVersion != 1 || cfg.Health.ReadyTimeout.Value() != 500*time.Millisecond ||
		cfg.Health.ShutdownGracePeriod.Value() != 30*time.Second {
		t.Fatalf("启动默认值不正确: %#v %#v", cfg.Secrets, cfg.Health)
	}
	if cfg.Observability.OTel.ServiceName != "ai-gateway" ||
		cfg.Observability.OTel.MetricExportInterval.Value() != 10*time.Second ||
		cfg.Observability.OTel.CardinalityLimit != 2000 {
		t.Fatalf("OTel 默认值不正确: %#v", cfg.Observability.OTel)
	}
	if _, err := LoadBootstrap(filepath.Join(directory, "missing")); err == nil {
		t.Fatal("LoadBootstrap() 应返回读取错误")
	}
}

func TestStrictYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"syntax", "["},
		{"unknown", validGatewayYAML() + "unknown: true\n"},
		{"duplicate", strings.Replace(validGatewayYAML(), "kind: GatewayConfig", "kind: GatewayConfig\nkind: GatewayConfig", 1)},
		{"documents", validGatewayYAML() + "---\n{}\n"},
		{"anchor", strings.Replace(validGatewayYAML(), "backends:", "backends: &backends", 1)},
		{"alias", "api_version: gateway.ai/v1alpha2\nkind: GatewayConfig\nbackends: &b []\nroutes: *b\n"},
		{"tag", strings.Replace(validGatewayYAML(), "kind: GatewayConfig", "kind: !custom GatewayConfig", 1)},
		{"non-string-key", strings.Replace(validGatewayYAML(), "kind: GatewayConfig", "1: GatewayConfig", 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeGateway([]byte(test.data))
			if !errors.Is(err, ErrInvalidYAML) {
				t.Fatalf("DecodeGateway() error = %v, want ErrInvalidYAML", err)
			}
		})
	}
}

func TestLoadGatewayAndDefaults(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "gateway.yaml")
	if err := os.WriteFile(path, []byte(validGatewayYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGateway(path)
	if err != nil {
		t.Fatalf("LoadGateway() error = %v", err)
	}
	if cfg.APIVersion != APIVersion || cfg.Kind != KindGateway || cfg.Audit.Retention.Value() != 30*24*time.Hour {
		t.Fatalf("默认值未应用: %#v", cfg)
	}
	if cfg.Limits.RequestBodyBytes != 2<<20 || cfg.Limits.ChatConcurrency != 128 ||
		cfg.Limits.ResponsesConcurrency != 128 || cfg.Limits.ImageConcurrency != 4 ||
		cfg.Limits.ImagesPerRequest != 4 || cfg.Limits.ImageRawBytesPerResponse != 32<<20 {
		t.Fatalf("limits 默认值错误: %#v", cfg.Limits)
	}
	if cfg.Backends[0].Timeouts.Request.Value() != 120*time.Second ||
		cfg.Backends[0].Timeouts.StreamIdle.Value() != 30*time.Second {
		t.Fatalf("backend timeout 默认值错误: %#v", cfg.Backends[0].Timeouts)
	}
	if _, err := LoadGateway(filepath.Join(directory, "missing")); err == nil {
		t.Fatal("LoadGateway() 应返回读取错误")
	}
}

func TestValidateBootstrapFailures(t *testing.T) {
	t.Parallel()

	valid := validBootstrap()
	tests := []struct {
		name   string
		mutate func(*Bootstrap)
	}{
		{"identity", func(c *Bootstrap) { c.Kind = "wrong" }},
		{"listener", func(c *Bootstrap) { c.Listeners.Admin = "wrong" }},
		{"driver", func(c *Bootstrap) { c.Storage.Driver = "memory" }},
		{"path", func(c *Bootstrap) { c.Storage.Path = "" }},
		{"connections-low", func(c *Bootstrap) { c.Storage.MaxOpenConnections = 0 }},
		{"connections-high", func(c *Bootstrap) { c.Storage.MaxOpenConnections = 33 }},
		{"busy-timeout", func(c *Bootstrap) { c.Storage.BusyTimeout = 0 }},
		{"journal", func(c *Bootstrap) { c.Storage.JournalMode = "DELETE" }},
		{"synchronous", func(c *Bootstrap) { c.Storage.Synchronous = "NORMAL" }},
		{"master-file", func(c *Bootstrap) { c.Secrets.MasterKeyFile = "" }},
		{"master-version", func(c *Bootstrap) { c.Secrets.MasterKeyVersion = 0 }},
		{"initial-config", func(c *Bootstrap) { c.InitialGatewayConfig = "" }},
		{"ready-timeout", func(c *Bootstrap) { c.Health.ReadyTimeout = 0 }},
		{"shutdown-timeout", func(c *Bootstrap) { c.Health.ShutdownGracePeriod = 0 }},
		{"sample-low", func(c *Bootstrap) { c.Observability.OTel.TraceSampleRatio = -0.1 }},
		{"sample-high", func(c *Bootstrap) { c.Observability.OTel.TraceSampleRatio = 1.1 }},
		{"import-id", func(c *Bootstrap) { c.CredentialImports[0].CredentialID = "Bad" }},
		{"import-provider", func(c *Bootstrap) { c.CredentialImports[0].Provider = "other" }},
		{"import-source", func(c *Bootstrap) { c.CredentialImports[0].SourceFile = "" }},
		{"import-policy", func(c *Bootstrap) { c.CredentialImports[0].ImportPolicy = "always" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			cfg.CredentialImports = append([]CredentialImport(nil), valid.CredentialImports...)
			test.mutate(&cfg)
			if err := ValidateBootstrap(cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("ValidateBootstrap() error = %v", err)
			}
		})
	}
}

func TestValidateGatewayFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Gateway)
	}{
		{"identity", func(c *Gateway) { c.APIVersion = "v0" }},
		{"no-backends", func(c *Gateway) { c.Backends = nil }},
		{"too-many-backends", func(c *Gateway) { c.Limits.MaxBackends = 1 }},
		{"no-routes", func(c *Gateway) { c.Routes = nil }},
		{"too-many-routes", func(c *Gateway) { c.Limits.MaxRoutes = 2 }},
		{"backend-id", func(c *Gateway) { c.Backends[0].ID = "Bad" }},
		{"credential-id", func(c *Gateway) { c.Backends[0].CredentialID = "Bad" }},
		{"provider", func(c *Gateway) { c.Backends[0].Provider = "other" }},
		{"base-url", func(c *Gateway) { c.Backends[0].BaseURL = "://" }},
		{"deprecated-dashscope-url", func(c *Gateway) { c.Backends[1].BaseURL = "https://example.com/api/v2/apps/protocols/openai" }},
		{"no-capabilities", func(c *Gateway) { c.Backends[0].Capabilities = nil }},
		{"capability", func(c *Gateway) { c.Backends[0].Capabilities[0] = "audio" }},
		{"duplicate-capability", func(c *Gateway) { c.Backends[0].Capabilities = []string{"chat", "chat"} }},
		{"backend-timeout", func(c *Gateway) { c.Backends[0].Timeouts.Request = 0 }},
		{"duplicate-backend", func(c *Gateway) { c.Backends[1].ID = c.Backends[0].ID }},
		{"route-id", func(c *Gateway) { c.Routes[0].ID = "Bad" }},
		{"operation", func(c *Gateway) { c.Routes[0].Operation = "audio" }},
		{"alias", func(c *Gateway) { c.Routes[0].ModelAlias = "" }},
		{"no-targets", func(c *Gateway) { c.Routes[0].Targets = nil }},
		{"missing-backend", func(c *Gateway) { c.Routes[0].Targets[0].BackendID = "missing" }},
		{"duplicate-target", func(c *Gateway) { c.Routes[0].Targets = append(c.Routes[0].Targets, c.Routes[0].Targets[0]) }},
		{"empty-upstream", func(c *Gateway) { c.Routes[0].Targets[0].UpstreamModel = "" }},
		{"incompatible", func(c *Gateway) { c.Backends[0].Capabilities = []string{"image"} }},
		{"inactive-target", func(c *Gateway) { c.Routes[0].ActiveBackend = "missing" }},
		{"duplicate-route-id", func(c *Gateway) { c.Routes[1].ID = c.Routes[0].ID }},
		{"duplicate-route-key", func(c *Gateway) {
			c.Routes[1].Operation = c.Routes[0].Operation
			c.Routes[1].ModelAlias = c.Routes[0].ModelAlias
		}},
		{"missing-operation", func(c *Gateway) {
			c.Routes[2].Operation = "chat"
			c.Routes[2].ID = "chat-two"
			c.Routes[2].ModelAlias = "chat-two"
		}},
		{"body-limit", func(c *Gateway) { c.Limits.RequestBodyBytes = 100 }},
		{"image-count", func(c *Gateway) { c.Limits.ImagesPerRequest = 0 }},
		{"image-bytes", func(c *Gateway) { c.Limits.ImageRawBytesPerResponse = 100 }},
		{"chat-concurrency", func(c *Gateway) { c.Limits.ChatConcurrency = 0 }},
		{"response-concurrency", func(c *Gateway) { c.Limits.ResponsesConcurrency = 0 }},
		{"image-concurrency", func(c *Gateway) { c.Limits.ImageConcurrency = 0 }},
		{"audit-retention", func(c *Gateway) { c.Audit.Retention = 0 }},
		{"cleanup-interval", func(c *Gateway) { c.Audit.CleanupInterval = 0 }},
		{"abandoned-after", func(c *Gateway) { c.Audit.AbandonedAfter = 0 }},
		{"cleanup-batch", func(c *Gateway) { c.Audit.CleanupBatchSize = 0 }},
		{"binding-retention", func(c *Gateway) { c.Responses.BindingRetention = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validGateway()
			test.mutate(&cfg)
			if err := ValidateGateway(cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("ValidateGateway() error = %v", err)
			}
		})
	}
}

func TestSnapshotManagerAndSwitch(t *testing.T) {
	t.Parallel()

	cfg := validGateway()
	snapshot, err := NewSnapshot(7, cfg)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	if _, err := NewSnapshot(0, cfg); err == nil {
		t.Fatal("NewSnapshot() 应拒绝 revision 0")
	}
	if _, err := NewSnapshot(1, Gateway{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	resolved, err := snapshot.Resolve("chat", "chat-model")
	if err != nil || resolved.Revision != 7 || resolved.Backend.ID != "openai-main" {
		t.Fatalf("Resolve() = %#v, %v", resolved, err)
	}
	resolved.Backend.Capabilities[0] = "changed"
	resolved.Route.Targets[0].UpstreamModel = "changed"
	again, _ := snapshot.Resolve("chat", "chat-model")
	if again.Backend.Capabilities[0] != "chat" || again.Target.UpstreamModel != "gpt-test" {
		t.Fatal("Resolve() 泄露了快照内部切片")
	}
	if _, err := snapshot.Resolve("missing", "chat-model"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("Resolve() error = %v", err)
	}
	if _, err := snapshot.ResolveBackend("chat", "chat-model", "missing"); !errors.Is(err, ErrBackendNotFound) {
		t.Fatalf("ResolveBackend() error = %v", err)
	}
	if _, err := snapshot.ResolveBackend("missing", "chat-model", "openai-main"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("ResolveBackend() error = %v", err)
	}
	if got, err := snapshot.ResolveBackend("chat", "chat-model", "dashscope-main"); err != nil || got.Target.UpstreamModel != "qwen-test" {
		t.Fatalf("ResolveBackend() = %#v, %v", got, err)
	}

	models := snapshot.Models()
	if len(models) != 3 || models[0].ModelAlias != "chat-model" || models[1].ModelAlias != "image-model" {
		t.Fatalf("Models() = %#v", models)
	}
	models[0].Targets[0].UpstreamModel = "changed"
	copyConfig := snapshot.Config()
	copyConfig.Backends[0].Capabilities[0] = "changed"
	if snapshot.Config().Backends[0].Capabilities[0] != "chat" || snapshot.Models()[0].Targets[0].UpstreamModel == "changed" {
		t.Fatal("Config()/Models() 泄露了内部状态")
	}

	manager := NewManager(snapshot)
	if manager.Current().Revision() != 7 {
		t.Fatal("Manager.Current() revision 错误")
	}
	next, _ := NewSnapshot(8, cfg)
	manager.Store(next)
	if manager.Current().Revision() != 8 {
		t.Fatal("Manager.Store() 未更新")
	}

	updated, err := SwitchActive(cfg, "chat-route", "dashscope-main")
	if err != nil || updated.Routes[0].ActiveBackend != "dashscope-main" || cfg.Routes[0].ActiveBackend != "openai-main" {
		t.Fatalf("SwitchActive() = %#v, %v", updated.Routes[0], err)
	}
	if _, err := SwitchActive(cfg, "chat-route", "missing"); !errors.Is(err, ErrBackendNotFound) {
		t.Fatalf("SwitchActive() error = %v", err)
	}
	if _, err := SwitchActive(cfg, "missing", "openai-main"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("SwitchActive() error = %v", err)
	}
}

func validBootstrap() Bootstrap {
	return Bootstrap{
		APIVersion: APIVersion,
		Kind:       KindBootstrap,
		Listeners: Listeners{
			Data: "127.0.0.1:8080", Admin: "127.0.0.1:9090", Operations: "127.0.0.1:8081",
		},
		Storage: Storage{
			Driver: "sqlite", Path: "gateway.db", MaxOpenConnections: 8,
			BusyTimeout: Duration(time.Second), JournalMode: "WAL", Synchronous: "FULL",
		},
		Secrets: Secrets{MasterKeyFile: "master.key", MasterKeyVersion: 1},
		CredentialImports: []CredentialImport{{
			CredentialID: "openai-main", Provider: "openai", SourceFile: "openai.key", ImportPolicy: "if_missing",
		}},
		InitialGatewayConfig: "gateway.yaml",
		Observability: Observability{OTel: OTel{
			TraceSampleRatio: 0.1,
		}},
		Health: Health{ReadyTimeout: Duration(time.Second), ShutdownGracePeriod: Duration(time.Second)},
	}
}

func validGateway() Gateway {
	backend := func(id, provider string) Backend {
		return Backend{
			ID: id, Provider: provider, BaseURL: "https://example.com/v1", CredentialID: id,
			Capabilities: []string{"chat", "responses", "image"},
			Timeouts:     BackendTimeouts{Request: Duration(time.Second), StreamIdle: Duration(time.Second)},
		}
	}
	route := func(id, operation, alias string) Route {
		return Route{
			ID: id, Operation: operation, ModelAlias: alias, ActiveBackend: "openai-main",
			Targets: []Target{
				{BackendID: "openai-main", UpstreamModel: map[string]string{"chat": "gpt-test", "responses": "gpt-response", "image": "gpt-image"}[operation]},
				{BackendID: "dashscope-main", UpstreamModel: map[string]string{"chat": "qwen-test", "responses": "qwen-response", "image": "wanx-test"}[operation]},
			},
		}
	}
	return Gateway{
		APIVersion: APIVersion,
		Kind:       KindGateway,
		Backends:   []Backend{backend("openai-main", "openai"), backend("dashscope-main", "dashscope")},
		Routes: []Route{
			route("chat-route", "chat", "chat-model"),
			route("responses-route", "responses", "response-model"),
			route("image-route", "image", "image-model"),
		},
		Audit: Audit{
			Retention: Duration(time.Hour), CleanupInterval: Duration(time.Minute),
			AbandonedAfter: Duration(time.Minute), CleanupBatchSize: 10,
		},
		Responses: Responses{BindingRetention: Duration(time.Hour)},
		Limits: Limits{
			RequestBodyBytes: 1024, MaxBackends: 10, MaxRoutes: 10,
			ChatConcurrency: 1, ResponsesConcurrency: 1, ImageConcurrency: 1,
			ImagesPerRequest: 1, ImageRawBytesPerResponse: 1024,
		},
	}
}

func validGatewayYAML() string {
	return `api_version: gateway.ai/v1alpha2
kind: GatewayConfig
backends:
  - id: openai-main
    provider: openai
    base_url: https://example.com/v1
    credential_id: openai-main
    capabilities: [chat, responses, image]
routes:
  - id: chat-route
    operation: chat
    model_alias: chat-model
    active_backend: openai-main
    targets: [{backend_id: openai-main, upstream_model: gpt-test}]
  - id: responses-route
    operation: responses
    model_alias: response-model
    active_backend: openai-main
    targets: [{backend_id: openai-main, upstream_model: gpt-test}]
  - id: image-route
    operation: image
    model_alias: image-model
    active_backend: openai-main
    targets: [{backend_id: openai-main, upstream_model: gpt-image}]
`
}
