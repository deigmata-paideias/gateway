CREATE TABLE IF NOT EXISTS runtime_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS config_revisions (
    revision INTEGER PRIMARY KEY,
    config_json TEXT NOT NULL,
    sha256 BLOB NOT NULL,
    operation TEXT NOT NULL,
    request_id TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS provider_credentials (
    credential_id TEXT PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider IN ('openai', 'dashscope')),
    name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
    cipher_version INTEGER NOT NULL DEFAULT 1,
    key_version INTEGER NOT NULL,
    secret_ciphertext BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    rotated_at INTEGER
);

CREATE TABLE IF NOT EXISTS gateway_tokens (
    token_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
    secret_sha256 BLOB NOT NULL,
    secret_ciphertext BLOB NOT NULL,
    cipher_version INTEGER NOT NULL DEFAULT 1,
    key_version INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER,
    revoked_at INTEGER,
    last_used_at INTEGER
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_gateway_tokens_secret_sha256
    ON gateway_tokens(secret_sha256);

CREATE TABLE IF NOT EXISTS response_bindings (
    public_response_id TEXT PRIMARY KEY,
    gateway_token_id TEXT NOT NULL REFERENCES gateway_tokens(token_id) ON DELETE RESTRICT,
    backend_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    model_alias TEXT NOT NULL,
    upstream_response_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    state TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_response_bindings_token_expires
    ON response_bindings(gateway_token_id, expires_at);
CREATE INDEX IF NOT EXISTS idx_response_bindings_expires
    ON response_bindings(expires_at);

CREATE TABLE IF NOT EXISTS request_audits (
    audit_id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    gateway_token_id TEXT NOT NULL REFERENCES gateway_tokens(token_id) ON DELETE RESTRICT,
    operation TEXT NOT NULL,
    model_alias TEXT,
    backend_id TEXT,
    provider TEXT,
    config_revision INTEGER,
    public_response_id TEXT,
    stream INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK (status IN ('started', 'succeeded', 'failed', 'cancelled', 'abandoned')),
    http_status INTEGER,
    upstream_status INTEGER,
    error_code TEXT,
    fallback_count INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER,
    cached_input_tokens INTEGER,
    output_tokens INTEGER,
    reasoning_output_tokens INTEGER,
    total_tokens INTEGER,
    image_count INTEGER,
    raw_image_bytes INTEGER,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    duration_ms INTEGER,
    upstream_duration_ms INTEGER,
    time_to_first_token_ms INTEGER,
    trace_id TEXT
);

CREATE INDEX IF NOT EXISTS idx_request_audits_token_started
    ON request_audits(gateway_token_id, started_at DESC, audit_id DESC);
CREATE INDEX IF NOT EXISTS idx_request_audits_started
    ON request_audits(started_at DESC, audit_id DESC);
CREATE INDEX IF NOT EXISTS idx_request_audits_operation_model_started
    ON request_audits(operation, model_alias, started_at DESC);

CREATE TABLE IF NOT EXISTS admin_events (
    event_id TEXT PRIMARY KEY,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    result TEXT NOT NULL,
    trace_id TEXT,
    created_at INTEGER NOT NULL
);

INSERT OR IGNORE INTO runtime_meta(key, value) VALUES ('store_version', '1');
