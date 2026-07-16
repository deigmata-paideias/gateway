// Package config 定义网关的 YAML 配置及不可变运行时快照。
package config

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	APIVersion = "gateway.ai/v1alpha2"

	KindBootstrap = "BootstrapConfig"
	KindGateway   = "GatewayConfig"
)

type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("解析 duration: %w", err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("解析 duration json: %w", err)
	}
	return d.UnmarshalText([]byte(text))
}

func (d Duration) Value() time.Duration {
	return time.Duration(d)
}

type Bootstrap struct {
	APIVersion           string             `json:"api_version" yaml:"api_version"`
	Kind                 string             `json:"kind" yaml:"kind"`
	Listeners            Listeners          `json:"listeners" yaml:"listeners"`
	Storage              Storage            `json:"storage" yaml:"storage"`
	Secrets              Secrets            `json:"secrets" yaml:"secrets"`
	CredentialImports    []CredentialImport `json:"credential_imports" yaml:"credential_imports"`
	InitialGatewayConfig string             `json:"initial_gateway_config" yaml:"initial_gateway_config"`
	Observability        Observability      `json:"observability" yaml:"observability"`
	Health               Health             `json:"health" yaml:"health"`
}

type Listeners struct {
	Data       string `json:"data" yaml:"data"`
	Admin      string `json:"admin" yaml:"admin"`
	Operations string `json:"operations" yaml:"operations"`
}

type Storage struct {
	Driver             string   `json:"driver" yaml:"driver"`
	Path               string   `json:"path" yaml:"path"`
	MaxOpenConnections int      `json:"max_open_connections" yaml:"max_open_connections"`
	BusyTimeout        Duration `json:"busy_timeout" yaml:"busy_timeout"`
	JournalMode        string   `json:"journal_mode" yaml:"journal_mode"`
	Synchronous        string   `json:"synchronous" yaml:"synchronous"`
}

type Secrets struct {
	MasterKeyFile    string `json:"master_key_file" yaml:"master_key_file"`
	MasterKeyVersion int    `json:"master_key_version" yaml:"master_key_version"`
}

type CredentialImport struct {
	CredentialID string `json:"credential_id" yaml:"credential_id"`
	Provider     string `json:"provider" yaml:"provider"`
	SourceFile   string `json:"source_file" yaml:"source_file"`
	ImportPolicy string `json:"import_policy" yaml:"import_policy"`
}

type Observability struct {
	OTel OTel `json:"otel" yaml:"otel"`
}

type OTel struct {
	Enabled              bool     `json:"enabled" yaml:"enabled"`
	ServiceName          string   `json:"service_name" yaml:"service_name"`
	ServiceVersion       string   `json:"service_version" yaml:"service_version"`
	OTLPHTTPEndpoint     string   `json:"otlp_http_endpoint" yaml:"otlp_http_endpoint"`
	Insecure             bool     `json:"insecure" yaml:"insecure"`
	MetricExportInterval Duration `json:"metric_export_interval" yaml:"metric_export_interval"`
	TraceSampleRatio     float64  `json:"trace_sample_ratio" yaml:"trace_sample_ratio"`
	CardinalityLimit     int      `json:"cardinality_limit" yaml:"cardinality_limit"`
}

type Health struct {
	ReadyTimeout        Duration `json:"ready_timeout" yaml:"ready_timeout"`
	ShutdownGracePeriod Duration `json:"shutdown_grace_period" yaml:"shutdown_grace_period"`
}

type Gateway struct {
	APIVersion string    `json:"api_version" yaml:"api_version"`
	Kind       string    `json:"kind" yaml:"kind"`
	Backends   []Backend `json:"backends" yaml:"backends"`
	Routes     []Route   `json:"routes" yaml:"routes"`
	Audit      Audit     `json:"audit" yaml:"audit"`
	Responses  Responses `json:"responses" yaml:"responses"`
	Limits     Limits    `json:"limits" yaml:"limits"`
}

type Backend struct {
	ID           string          `json:"id" yaml:"id"`
	Provider     string          `json:"provider" yaml:"provider"`
	BaseURL      string          `json:"base_url" yaml:"base_url"`
	CredentialID string          `json:"credential_id" yaml:"credential_id"`
	Capabilities []string        `json:"capabilities" yaml:"capabilities"`
	Timeouts     BackendTimeouts `json:"timeouts" yaml:"timeouts"`
}

type BackendTimeouts struct {
	Request    Duration `json:"request" yaml:"request"`
	StreamIdle Duration `json:"stream_idle" yaml:"stream_idle"`
}

type Route struct {
	ID            string   `json:"id" yaml:"id"`
	Operation     string   `json:"operation" yaml:"operation"`
	ModelAlias    string   `json:"model_alias" yaml:"model_alias"`
	ActiveBackend string   `json:"active_backend" yaml:"active_backend"`
	Targets       []Target `json:"targets" yaml:"targets"`
}

type Target struct {
	BackendID     string `json:"backend_id" yaml:"backend_id"`
	UpstreamModel string `json:"upstream_model" yaml:"upstream_model"`
}

type Audit struct {
	Retention        Duration `json:"retention" yaml:"retention"`
	CleanupInterval  Duration `json:"cleanup_interval" yaml:"cleanup_interval"`
	AbandonedAfter   Duration `json:"abandoned_after" yaml:"abandoned_after"`
	CleanupBatchSize int      `json:"cleanup_batch_size" yaml:"cleanup_batch_size"`
}

type Responses struct {
	BindingRetention Duration `json:"binding_retention" yaml:"binding_retention"`
}

type Limits struct {
	RequestBodyBytes         int64 `json:"request_body_bytes" yaml:"request_body_bytes"`
	MaxBackends              int   `json:"max_backends" yaml:"max_backends"`
	MaxRoutes                int   `json:"max_routes" yaml:"max_routes"`
	ChatConcurrency          int   `json:"chat_concurrency" yaml:"chat_concurrency"`
	ResponsesConcurrency     int   `json:"responses_concurrency" yaml:"responses_concurrency"`
	ImageConcurrency         int   `json:"image_concurrency" yaml:"image_concurrency"`
	ImagesPerRequest         int   `json:"images_per_request" yaml:"images_per_request"`
	ImageRawBytesPerResponse int64 `json:"image_raw_bytes_per_response" yaml:"image_raw_bytes_per_response"`
}
