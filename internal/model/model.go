// Package model 包含网关跨层共享的稳定领域值。
package model

type Credential struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Ciphertext []byte `json:"-"`
	KeyVersion int    `json:"key_version"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	RotatedAt  *int64 `json:"rotated_at,omitempty"`
}

type GatewayToken struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Digest     []byte `json:"-"`
	Ciphertext []byte `json:"-"`
	KeyVersion int    `json:"key_version"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	ExpiresAt  *int64 `json:"expires_at"`
	RevokedAt  *int64 `json:"revoked_at,omitempty"`
	LastUsedAt *int64 `json:"last_used_at,omitempty"`
}

type Usage struct {
	InputTokens           *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens     *int64 `json:"cached_input_tokens,omitempty"`
	OutputTokens          *int64 `json:"output_tokens,omitempty"`
	ReasoningOutputTokens *int64 `json:"reasoning_output_tokens,omitempty"`
	TotalTokens           *int64 `json:"total_tokens,omitempty"`
	ImageCount            *int64 `json:"image_count,omitempty"`
	RawImageBytes         *int64 `json:"raw_image_bytes,omitempty"`
}

type Audit struct {
	ID                     string `json:"id"`
	RequestID              string `json:"request_id"`
	GatewayTokenID         string `json:"gateway_token_id"`
	Operation              string `json:"operation"`
	ModelAlias             string `json:"model_alias,omitempty"`
	BackendID              string `json:"backend_id,omitempty"`
	Provider               string `json:"provider,omitempty"`
	ConfigRevision         *int64 `json:"config_revision,omitempty"`
	PublicResponseID       string `json:"public_response_id,omitempty"`
	Stream                 bool   `json:"stream"`
	Status                 string `json:"status"`
	HTTPStatus             *int   `json:"http_status,omitempty"`
	UpstreamStatus         *int   `json:"upstream_status,omitempty"`
	ErrorCode              string `json:"error_code,omitempty"`
	FallbackCount          int    `json:"fallback_count"`
	Usage                  Usage  `json:"usage"`
	StartedAt              int64  `json:"started_at"`
	FinishedAt             *int64 `json:"finished_at,omitempty"`
	DurationMillis         *int64 `json:"duration_ms,omitempty"`
	UpstreamDurationMillis *int64 `json:"upstream_duration_ms,omitempty"`
	TimeToFirstTokenMillis *int64 `json:"time_to_first_token_ms,omitempty"`
	TraceID                string `json:"trace_id,omitempty"`
}

type AuditFinish struct {
	ID                     string
	Status                 string
	HTTPStatus             int
	UpstreamStatus         int
	ErrorCode              string
	PublicResponseID       string
	Usage                  Usage
	FinishedAt             int64
	DurationMillis         int64
	UpstreamDurationMillis int64
	TimeToFirstTokenMillis int64
}

type ResponseBinding struct {
	PublicResponseID   string `json:"public_response_id"`
	GatewayTokenID     string `json:"gateway_token_id"`
	BackendID          string `json:"backend_id"`
	Provider           string `json:"provider"`
	ModelAlias         string `json:"model_alias"`
	UpstreamResponseID string `json:"upstream_response_id"`
	Revision           int64  `json:"revision"`
	ExpiresAt          int64  `json:"expires_at"`
	State              string `json:"state"`
}

type AuditFilter struct {
	TokenID    string
	Operation  string
	ModelAlias string
	BackendID  string
	Status     string
	From       *int64
	To         *int64
	Limit      int
	BeforeID   string
}

type UsageGroup struct {
	Key                    string `json:"key"`
	Requests               int64  `json:"requests"`
	Succeeded              int64  `json:"succeeded"`
	Failed                 int64  `json:"failed"`
	InputTokens            int64  `json:"input_tokens"`
	CachedInputTokens      int64  `json:"cached_input_tokens"`
	OutputTokens           int64  `json:"output_tokens"`
	ReasoningOutputTokens  int64  `json:"reasoning_output_tokens"`
	TotalTokens            int64  `json:"total_tokens"`
	Images                 int64  `json:"images"`
	UsageIncompleteRecords int64  `json:"usage_incomplete_records"`
}
