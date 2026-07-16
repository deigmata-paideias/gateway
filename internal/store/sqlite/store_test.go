package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deigmata-paideias/gateway/internal/model"
)

func TestOpenInitializePingAndMigrationIntegrity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if _, err := Open(ctx, Options{}); err == nil {
		t.Fatal("Open() 应拒绝空 path")
	}
	store := openTestStore(t)
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	var migrationCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil || migrationCount != 1 {
		t.Fatalf("migration count = %d, error = %v", migrationCount, err)
	}
	path := storePath(t, store)
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := Open(ctx, Options{Path: path, MaxOpenConns: 1, BusyTimeout: time.Millisecond})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Ping(ctx); err == nil {
		t.Fatal("关闭后的 Ping() 应返回错误")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Open(cancelled, Options{Path: filepath.Join(t.TempDir(), "cancelled.db")}); err == nil {
		t.Fatal("Open() 应传播取消错误")
	}
	if _, err := Open(ctx, Options{Path: "file:memory-test?mode=memory&cache=shared"}); err == nil {
		t.Fatal("内存数据库无法满足 WAL/FULL，Open() 应失败")
	}
}

func TestDSNAndByteComparison(t *testing.T) {
	t.Parallel()

	dsn, err := makeDSN(filepath.Join(t.TempDir(), "a b.db"), 0)
	if err != nil || !strings.HasPrefix(dsn, "file:") || !strings.Contains(dsn, "busy_timeout(1)") || !strings.Contains(dsn, "journal_mode(WAL)") {
		t.Fatalf("makeDSN() = %q, %v", dsn, err)
	}
	dsn, err = makeDSN("file:test.db?mode=rwc", 1500*time.Millisecond)
	if err != nil || !strings.Contains(dsn, "mode=rwc&_pragma=") || !strings.Contains(dsn, "busy_timeout(1500)") {
		t.Fatalf("makeDSN(file) = %q, %v", dsn, err)
	}
	if !equalBytes([]byte{1, 2}, []byte{1, 2}) || equalBytes([]byte{1}, []byte{1, 2}) || equalBytes([]byte{1, 2}, []byte{1, 3}) {
		t.Fatal("equalBytes() 结果错误")
	}
}

func TestMigrationChecksumMismatch(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	path := storePath(t, store)
	if _, err := store.db.Exec("UPDATE schema_migrations SET checksum = X'00'"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), Options{Path: path}); err == nil || !strings.Contains(err.Error(), "校验和") {
		t.Fatalf("Open() error = %v", err)
	}
}

func TestConfigRevisionLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	if _, err := store.CurrentConfig(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CurrentConfig() error = %v", err)
	}
	if _, err := store.ConfigRevision(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ConfigRevision() error = %v", err)
	}

	data := []byte(`{"kind":"GatewayConfig"}`)
	first, err := store.SaveConfig(ctx, 0, data, "initialize", "req_1")
	if err != nil || first.Revision != 1 || first.Operation != "initialize" || first.RequestID != "req_1" {
		t.Fatalf("SaveConfig() = %#v, %v", first, err)
	}
	digest := sha256.Sum256(data)
	if !equalBytes(first.SHA256, digest[:]) || string(first.ConfigJSON) != string(data) {
		t.Fatalf("SaveConfig() digest/body 错误: %#v", first)
	}
	data[0] = '['
	if first.ConfigJSON[0] != '{' {
		t.Fatal("SaveConfig() 未复制输入")
	}
	if _, err := store.SaveConfig(ctx, 0, []byte(`{}`), "replace", "req_conflict"); !errors.Is(err, ErrConflict) {
		t.Fatalf("SaveConfig(conflict) error = %v", err)
	}
	second, err := store.SaveConfig(ctx, 1, []byte(`{"revision":2}`), "replace", "req_2")
	if err != nil || second.Revision != 2 {
		t.Fatalf("SaveConfig(second) = %#v, %v", second, err)
	}
	current, err := store.CurrentConfig(ctx)
	if err != nil || current.Revision != 2 {
		t.Fatalf("CurrentConfig() = %#v, %v", current, err)
	}
	loaded, err := store.ConfigRevision(ctx, 1)
	if err != nil || loaded.Revision != 1 || string(loaded.ConfigJSON) != `{"kind":"GatewayConfig"}` {
		t.Fatalf("ConfigRevision() = %#v, %v", loaded, err)
	}
	for _, limit := range []int{-1, 1, 501} {
		items, err := store.ConfigRevisions(ctx, limit)
		if err != nil || len(items) == 0 || (limit == 1 && len(items) != 1) || items[0].Revision != 2 {
			t.Fatalf("ConfigRevisions(%d) = %#v, %v", limit, items, err)
		}
	}

	if _, err := store.db.ExecContext(ctx, "UPDATE runtime_meta SET value = 'not-number' WHERE key = 'current_revision'"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveConfig(ctx, 2, []byte(`{}`), "replace", "req_3"); err == nil || !strings.Contains(err.Error(), "解析 current revision") {
		t.Fatalf("SaveConfig(corrupt meta) error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE runtime_meta SET value = '999' WHERE key = 'current_revision'"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CurrentConfig(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CurrentConfig(missing revision) error = %v", err)
	}
}

func TestCredentialLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	now := time.Now().UnixMilli()
	credential := model.Credential{
		ID: "openai-main", Provider: "openai", Name: "OpenAI", Status: "active",
		Ciphertext: []byte("cipher-1"), KeyVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateCredential(ctx, credential); err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}
	if err := store.CreateCredential(ctx, credential); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateCredential(duplicate) error = %v", err)
	}
	created, err := store.CreateCredentialIfMissing(ctx, credential)
	if err != nil || created {
		t.Fatalf("CreateCredentialIfMissing(existing) = %v, %v", created, err)
	}
	second := credential
	second.ID, second.Provider, second.Name = "dashscope-main", "dashscope", "DashScope"
	created, err = store.CreateCredentialIfMissing(ctx, second)
	if err != nil || !created {
		t.Fatalf("CreateCredentialIfMissing(new) = %v, %v", created, err)
	}
	loaded, err := store.Credential(ctx, credential.ID)
	if err != nil || loaded.ID != credential.ID || string(loaded.Ciphertext) != "cipher-1" || loaded.RotatedAt != nil {
		t.Fatalf("Credential() = %#v, %v", loaded, err)
	}
	items, err := store.Credentials(ctx)
	if err != nil || len(items) != 2 || items[0].ID != "dashscope-main" || items[1].ID != "openai-main" {
		t.Fatalf("Credentials() = %#v, %v", items, err)
	}
	if err := store.RotateCredential(ctx, credential.ID, []byte("cipher-2"), 2); err != nil {
		t.Fatalf("RotateCredential() error = %v", err)
	}
	loaded, _ = store.Credential(ctx, credential.ID)
	if loaded.KeyVersion != 2 || string(loaded.Ciphertext) != "cipher-2" || loaded.RotatedAt == nil {
		t.Fatalf("rotated credential = %#v", loaded)
	}
	if err := store.RotateCredential(ctx, "missing", nil, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RotateCredential(missing) error = %v", err)
	}
	if err := store.DeleteCredential(ctx, second.ID); err != nil {
		t.Fatalf("DeleteCredential() error = %v", err)
	}
	if err := store.DeleteCredential(ctx, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteCredential(missing) error = %v", err)
	}
	if _, err := store.Credential(ctx, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Credential(missing) error = %v", err)
	}
	invalid := credential
	invalid.ID, invalid.Provider = "invalid", "other"
	if err := store.CreateCredential(ctx, invalid); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateCredential(constraint) error = %v", err)
	}
}

func TestTokenLifecycleAndAuthentication(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	now := time.Now().UnixMilli()
	token := testToken("tok_active", []byte("digest-active"), now)
	if err := store.CreateToken(ctx, token); err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	if err := store.CreateToken(ctx, token); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateToken(duplicate) error = %v", err)
	}
	loaded, err := store.Token(ctx, token.ID)
	if err != nil || loaded.ID != token.ID || loaded.ExpiresAt != nil || loaded.LastUsedAt != nil {
		t.Fatalf("Token() = %#v, %v", loaded, err)
	}
	authenticated, err := store.TokenByDigest(ctx, token.Digest)
	if err != nil || authenticated.ID != token.ID {
		t.Fatalf("TokenByDigest() = %#v, %v", authenticated, err)
	}
	if _, err := store.TokenByDigest(ctx, []byte("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TokenByDigest(missing) error = %v", err)
	}

	expiredAt := now - 1
	expired := testToken("tok_expired", []byte("digest-expired"), now)
	expired.ExpiresAt = &expiredAt
	if err := store.CreateToken(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TokenByDigest(ctx, expired.Digest); !errors.Is(err, ErrInactiveToken) {
		t.Fatalf("TokenByDigest(expired) error = %v", err)
	}
	revokedAt := now
	revoked := testToken("tok_revoked", []byte("digest-revoked"), now)
	revoked.Status, revoked.RevokedAt = "revoked", &revokedAt
	if err := store.CreateToken(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TokenByDigest(ctx, revoked.Digest); !errors.Is(err, ErrInactiveToken) {
		t.Fatalf("TokenByDigest(revoked) error = %v", err)
	}

	items, err := store.Tokens(ctx)
	if err != nil || len(items) != 3 {
		t.Fatalf("Tokens() = %#v, %v", items, err)
	}
	if err := store.RotateToken(ctx, token.ID, []byte("digest-new"), []byte("cipher-new"), 2); err != nil {
		t.Fatalf("RotateToken() error = %v", err)
	}
	loaded, _ = store.Token(ctx, token.ID)
	if string(loaded.Digest) != "digest-new" || string(loaded.Ciphertext) != "cipher-new" || loaded.KeyVersion != 2 {
		t.Fatalf("rotated token = %#v", loaded)
	}
	if err := store.RotateToken(ctx, "missing", []byte("x"), []byte("x"), 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RotateToken(missing) error = %v", err)
	}
	if err := store.RotateToken(ctx, token.ID, expired.Digest, []byte("x"), 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("RotateToken(conflict) error = %v", err)
	}
	if err := store.DeleteToken(ctx, token.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteToken(active) error = %v", err)
	}
	if err := store.RevokeToken(ctx, token.ID); err != nil {
		t.Fatalf("RevokeToken() error = %v", err)
	}
	if err := store.RevokeToken(ctx, token.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RevokeToken(again) error = %v", err)
	}
	if _, err := store.TokenByDigest(ctx, []byte("digest-new")); !errors.Is(err, ErrInactiveToken) {
		t.Fatalf("TokenByDigest(after revoke) error = %v", err)
	}
	if err := store.RotateToken(ctx, token.ID, []byte("x"), []byte("x"), 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RotateToken(revoked) error = %v", err)
	}
	if err := store.DeleteToken(ctx, token.ID); err != nil {
		t.Fatalf("DeleteToken() error = %v", err)
	}
	if err := store.DeleteToken(ctx, token.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteToken(missing) error = %v", err)
	}
	if _, err := store.Token(ctx, token.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Token(missing) error = %v", err)
	}
}

func TestAuditLifecycleFiltersAndUsage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Second).UnixMilli()
	if err := store.CreateToken(ctx, testToken("tok_audit", []byte("digest-audit"), now)); err != nil {
		t.Fatal(err)
	}
	revision := int64(2)
	audit := model.Audit{
		ID: "aud_002", RequestID: "req_002", GatewayTokenID: "tok_audit", Operation: "chat",
		ModelAlias: "chat-model", BackendID: "openai-main", Provider: "openai", ConfigRevision: &revision,
		Stream: true, FallbackCount: 1, StartedAt: now, TraceID: "trace-1",
	}
	if err := store.StartAudit(ctx, audit); err != nil {
		t.Fatalf("StartAudit() error = %v", err)
	}
	token, _ := store.Token(ctx, "tok_audit")
	if token.LastUsedAt == nil || *token.LastUsedAt != now {
		t.Fatalf("last_used_at = %v", token.LastUsedAt)
	}
	if err := store.StartAudit(ctx, audit); err == nil {
		t.Fatal("StartAudit(duplicate) 应失败")
	}
	missing := audit
	missing.ID, missing.RequestID, missing.GatewayTokenID = "aud_missing", "req_missing", "tok_missing"
	if err := store.StartAudit(ctx, missing); err == nil {
		t.Fatal("StartAudit(missing token) 应失败")
	}

	input, cached, output, reasoning, total := int64(3), int64(1), int64(4), int64(2), int64(7)
	finish := model.AuditFinish{
		ID: audit.ID, Status: "succeeded", HTTPStatus: 200, UpstreamStatus: 200,
		PublicResponseID: "resp_public", Usage: model.Usage{
			InputTokens: &input, CachedInputTokens: &cached, OutputTokens: &output,
			ReasoningOutputTokens: &reasoning, TotalTokens: &total,
		},
		FinishedAt: now + 50, DurationMillis: 50, UpstreamDurationMillis: 40, TimeToFirstTokenMillis: 10,
	}
	if err := store.FinishAudit(ctx, finish); err != nil {
		t.Fatalf("FinishAudit() error = %v", err)
	}
	if err := store.FinishAudit(ctx, finish); !errors.Is(err, ErrAuditState) {
		t.Fatalf("FinishAudit(again) error = %v", err)
	}
	finish.ID = "missing"
	if err := store.FinishAudit(ctx, finish); !errors.Is(err, ErrAuditState) {
		t.Fatalf("FinishAudit(missing) error = %v", err)
	}
	loaded, err := store.Audit(ctx, audit.ID)
	if err != nil || loaded.Status != "succeeded" || loaded.PublicResponseID != "resp_public" ||
		loaded.HTTPStatus == nil || *loaded.HTTPStatus != 200 || loaded.UpstreamDurationMillis == nil ||
		loaded.TraceID != "trace-1" || loaded.Usage.TotalTokens == nil || *loaded.Usage.TotalTokens != total {
		t.Fatalf("Audit() = %#v, %v", loaded, err)
	}
	if _, err := store.Audit(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Audit(missing) error = %v", err)
	}

	second := model.Audit{
		ID: "aud_003", RequestID: "req_003", GatewayTokenID: "tok_audit", Operation: "image",
		ModelAlias: "image-model", BackendID: "dashscope-main", Provider: "dashscope", StartedAt: now + 100,
	}
	if err := store.StartAudit(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAudit(ctx, model.AuditFinish{
		ID: second.ID, Status: "failed", HTTPStatus: 502, UpstreamStatus: 500,
		ErrorCode: "upstream_error", FinishedAt: now + 120, DurationMillis: 20,
	}); err != nil {
		t.Fatal(err)
	}

	from, to := now, now+200
	filters := []model.AuditFilter{
		{},
		{TokenID: "tok_audit", Operation: "chat", ModelAlias: "chat-model", BackendID: "openai-main", Status: "succeeded", From: &from, To: &to, Limit: 1},
		{BeforeID: "aud_003", Limit: 501},
	}
	for index, filter := range filters {
		items, err := store.Audits(ctx, filter)
		if err != nil || len(items) == 0 || (index == 1 && (len(items) != 1 || items[0].ID != audit.ID)) {
			t.Fatalf("Audits(%d) = %#v, %v", index, items, err)
		}
	}
	for _, groupBy := range []string{"", "operation", "model", "day"} {
		groups, err := store.AggregateUsage(ctx, "tok_audit", groupBy, &from, &to)
		if err != nil || len(groups) == 0 {
			t.Fatalf("AggregateUsage(%q) = %#v, %v", groupBy, groups, err)
		}
		var requests int64
		for _, group := range groups {
			requests += group.Requests
		}
		if requests != 2 {
			t.Fatalf("AggregateUsage(%q) requests = %d", groupBy, requests)
		}
	}
	if _, err := store.AggregateUsage(ctx, "tok_audit", "invalid", nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("AggregateUsage(invalid) error = %v", err)
	}
}

func TestBindingsCleanupAndAbandoned(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	now := time.Now().UnixMilli()
	if err := store.CreateToken(ctx, testToken("tok_binding", []byte("digest-binding"), now)); err != nil {
		t.Fatal(err)
	}
	binding := model.ResponseBinding{
		PublicResponseID: "resp_public", GatewayTokenID: "tok_binding", BackendID: "openai-main",
		Provider: "openai", ModelAlias: "response-model", UpstreamResponseID: "resp_upstream",
		Revision: 2, ExpiresAt: now + 1000, State: "completed",
	}
	if err := store.PutBinding(ctx, binding); err != nil {
		t.Fatalf("PutBinding() error = %v", err)
	}
	if err := store.PutBinding(ctx, binding); !errors.Is(err, ErrConflict) {
		t.Fatalf("PutBinding(duplicate) error = %v", err)
	}
	loaded, err := store.ResolveBinding(ctx, binding.PublicResponseID, binding.GatewayTokenID)
	if err != nil || loaded.UpstreamResponseID != binding.UpstreamResponseID || loaded.Revision != 2 {
		t.Fatalf("ResolveBinding() = %#v, %v", loaded, err)
	}
	if _, err := store.ResolveBinding(ctx, binding.PublicResponseID, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveBinding(other token) error = %v", err)
	}

	expired := binding
	expired.PublicResponseID, expired.ExpiresAt = "resp_expired", now-1
	if err := store.PutBinding(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveBinding(ctx, expired.PublicResponseID, expired.GatewayTokenID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveBinding(expired) error = %v", err)
	}
	audit := model.Audit{
		ID: "aud_old", RequestID: "req_old", GatewayTokenID: "tok_binding", Operation: "responses", StartedAt: now - 1000,
	}
	if err := store.StartAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAudit(ctx, model.AuditFinish{ID: audit.ID, Status: "succeeded", HTTPStatus: 200, FinishedAt: now - 900, DurationMillis: 100}); err != nil {
		t.Fatal(err)
	}
	started := audit
	started.ID, started.RequestID = "aud_started", "req_started"
	if err := store.StartAudit(ctx, started); err != nil {
		t.Fatal(err)
	}
	affected, err := store.Cleanup(ctx, now, now, 0)
	if err != nil || affected != 2 {
		t.Fatalf("Cleanup() = %d, %v", affected, err)
	}
	if _, err := store.Audit(ctx, audit.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old audit 未清理: %v", err)
	}
	if _, err := store.Audit(ctx, started.ID); err != nil {
		t.Fatalf("started audit 不应清理: %v", err)
	}
	affected, err = store.MarkAbandoned(ctx, now)
	if err != nil || affected != 1 {
		t.Fatalf("MarkAbandoned() = %d, %v", affected, err)
	}
	loadedAudit, _ := store.Audit(ctx, started.ID)
	if loadedAudit.Status != "abandoned" || loadedAudit.ErrorCode != "gateway_restarted" {
		t.Fatalf("abandoned audit = %#v", loadedAudit)
	}
	if affected, err := store.MarkAbandoned(ctx, now); err != nil || affected != 0 {
		t.Fatalf("MarkAbandoned(second) = %d, %v", affected, err)
	}

	if err := store.RevokeToken(ctx, "tok_binding"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteToken(ctx, "tok_binding"); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteToken(FK) error = %v", err)
	}
}

func TestAdminEvents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	events := []AdminEvent{
		{ID: "evt_1", Action: "create", ResourceType: "token", ResourceID: "tok_1", RequestID: "req_1", Result: "succeeded", CreatedAt: 1},
		{ID: "evt_2", Action: "rotate", ResourceType: "credential", ResourceID: "cred_1", RequestID: "req_2", Result: "succeeded", TraceID: "trace", CreatedAt: 2},
	}
	for _, event := range events {
		if err := store.RecordAdminEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.AdminEvents(ctx, 1)
	if err != nil || len(items) != 1 || items[0].ID != "evt_2" || items[0].TraceID != "trace" {
		t.Fatalf("AdminEvents(1) = %#v, %v", items, err)
	}
	items, err = store.AdminEvents(ctx, 501)
	if err != nil || len(items) != 2 || items[1].TraceID != "" {
		t.Fatalf("AdminEvents(default) = %#v, %v", items, err)
	}
	if err := store.RecordAdminEvent(ctx, events[0]); err == nil {
		t.Fatal("RecordAdminEvent(duplicate) 应失败")
	}
}

func TestHelperFunctionsAndTransactionRollback(t *testing.T) {
	t.Parallel()

	if err := requireOne(fakeResult{rows: 1}); err != nil {
		t.Fatalf("requireOne(1) error = %v", err)
	}
	if err := requireOne(fakeResult{rows: 0}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("requireOne(0) error = %v", err)
	}
	if err := requireOne(fakeResult{err: errors.New("rows")}); err == nil {
		t.Fatal("requireOne(error) 应失败")
	}
	if isConstraint(nil) || !isConstraint(errors.New("UNIQUE failed")) || !isConstraint(errors.New("constraint failed")) || isConstraint(errors.New("other")) {
		t.Fatal("isConstraint() 结果错误")
	}
	valid := sql.NullInt64{Int64: 7, Valid: true}
	if nullableInt64(sql.NullInt64{}) != nil || nullableInt(sql.NullInt64{}) != nil ||
		nullableInt64(valid) == nil || *nullableInt64(valid) != 7 || nullableInt(valid) == nil || *nullableInt(valid) != 7 {
		t.Fatal("nullable helpers 结果错误")
	}
	if nullString("") != nil || nullString("x") != "x" || nullPositiveInt(0) != nil || nullPositiveInt(2) != 2 ||
		nullPositiveInt64(-1) != nil || nullPositiveInt64(3) != int64(3) {
		t.Fatal("null helpers 结果错误")
	}

	store := openTestStore(t)
	defer store.Close()
	rollback := errors.New("rollback")
	if err := store.withWriteTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO runtime_meta(key, value) VALUES ('rollback-test', '1')"); err != nil {
			t.Fatal(err)
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("withWriteTx() error = %v", err)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM runtime_meta WHERE key = 'rollback-test'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("事务未回滚: count=%d error=%v", count, err)
	}
}

func TestClosedDatabaseErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertError := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s 应返回数据库关闭错误", name)
		}
	}
	_, err := store.CurrentConfig(ctx)
	assertError("CurrentConfig", err)
	_, err = store.ConfigRevision(ctx, 1)
	assertError("ConfigRevision", err)
	_, err = store.ConfigRevisions(ctx, 1)
	assertError("ConfigRevisions", err)
	_, err = store.Credential(ctx, "id")
	assertError("Credential", err)
	_, err = store.Credentials(ctx)
	assertError("Credentials", err)
	_, err = store.Token(ctx, "id")
	assertError("Token", err)
	_, err = store.Tokens(ctx)
	assertError("Tokens", err)
	_, err = store.Audit(ctx, "id")
	assertError("Audit", err)
	_, err = store.Audits(ctx, model.AuditFilter{})
	assertError("Audits", err)
	_, err = store.AggregateUsage(ctx, "id", "operation", nil, nil)
	assertError("AggregateUsage", err)
	_, err = store.ResolveBinding(ctx, "id", "token")
	assertError("ResolveBinding", err)
	_, err = store.AdminEvents(ctx, 1)
	assertError("AdminEvents", err)
	assertError("CreateCredential", store.CreateCredential(ctx, model.Credential{}))
	assertError("CreateToken", store.CreateToken(ctx, model.GatewayToken{}))
	assertError("StartAudit", store.StartAudit(ctx, model.Audit{}))
	assertError("PutBinding", store.PutBinding(ctx, model.ResponseBinding{}))
}

func TestDroppedTableWriteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		table string
		run   func(context.Context, *Store) error
	}{
		{"save-config", "runtime_meta", func(ctx context.Context, store *Store) error {
			_, err := store.SaveConfig(ctx, 0, []byte(`{}`), "op", "req")
			return err
		}},
		{"create-credential", "provider_credentials", func(ctx context.Context, store *Store) error { return store.CreateCredential(ctx, model.Credential{}) }},
		{"import-credential", "provider_credentials", func(ctx context.Context, store *Store) error {
			_, err := store.CreateCredentialIfMissing(ctx, model.Credential{})
			return err
		}},
		{"rotate-credential", "provider_credentials", func(ctx context.Context, store *Store) error { return store.RotateCredential(ctx, "id", nil, 1) }},
		{"delete-credential", "provider_credentials", func(ctx context.Context, store *Store) error { return store.DeleteCredential(ctx, "id") }},
		{"create-token", "gateway_tokens", func(ctx context.Context, store *Store) error { return store.CreateToken(ctx, model.GatewayToken{}) }},
		{"rotate-token", "gateway_tokens", func(ctx context.Context, store *Store) error { return store.RotateToken(ctx, "id", nil, nil, 1) }},
		{"revoke-token", "gateway_tokens", func(ctx context.Context, store *Store) error { return store.RevokeToken(ctx, "id") }},
		{"delete-token", "gateway_tokens", func(ctx context.Context, store *Store) error { return store.DeleteToken(ctx, "id") }},
		{"start-audit", "request_audits", func(ctx context.Context, store *Store) error { return store.StartAudit(ctx, model.Audit{}) }},
		{"finish-audit", "request_audits", func(ctx context.Context, store *Store) error { return store.FinishAudit(ctx, model.AuditFinish{}) }},
		{"put-binding", "response_bindings", func(ctx context.Context, store *Store) error { return store.PutBinding(ctx, model.ResponseBinding{}) }},
		{"cleanup", "response_bindings", func(ctx context.Context, store *Store) error { _, err := store.Cleanup(ctx, 1, 1, 1); return err }},
		{"abandoned", "request_audits", func(ctx context.Context, store *Store) error { _, err := store.MarkAbandoned(ctx, 1); return err }},
		{"admin-event", "admin_events", func(ctx context.Context, store *Store) error { return store.RecordAdminEvent(ctx, AdminEvent{}) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			defer store.Close()
			if _, err := store.db.Exec("DROP TABLE " + test.table); err != nil {
				t.Fatal(err)
			}
			if err := test.run(context.Background(), store); err == nil {
				t.Fatal("操作应返回缺表错误")
			}
		})
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(context.Background(), Options{Path: path, MaxOpenConns: 4, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}

func storePath(t *testing.T, store *Store) string {
	t.Helper()
	var path string
	if err := store.db.QueryRow("PRAGMA database_list").Scan(new(int), new(string), &path); err != nil {
		t.Fatal(err)
	}
	return path
}

func testToken(id string, digest []byte, now int64) model.GatewayToken {
	return model.GatewayToken{
		ID: id, Name: id, Status: "active", Digest: digest, Ciphertext: []byte("cipher-" + id),
		KeyVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
}

type fakeResult struct {
	rows int64
	err  error
}

func (result fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (result fakeResult) RowsAffected() (int64, error) { return result.rows, result.err }

type errorScanner struct{ err error }

func (scanner errorScanner) Scan(...any) error { return scanner.err }

func TestScanErrorWrappers(t *testing.T) {
	t.Parallel()

	scanError := errors.New("scan")
	if _, err := scanConfigRevision(errorScanner{scanError}); err == nil || !strings.Contains(err.Error(), "config revision") {
		t.Fatalf("scanConfigRevision() error = %v", err)
	}
	if _, err := scanCredential(errorScanner{scanError}); err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("scanCredential() error = %v", err)
	}
	if _, err := scanToken(errorScanner{scanError}); err == nil || !strings.Contains(err.Error(), "gateway token") {
		t.Fatalf("scanToken() error = %v", err)
	}
	if _, err := scanAudit(errorScanner{scanError}); err == nil || !strings.Contains(err.Error(), "请求审计") {
		t.Fatalf("scanAudit() error = %v", err)
	}
	if _, err := scanConfigRevision(errorScanner{sql.ErrNoRows}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scanConfigRevision(no rows) error = %v", err)
	}
	if _, err := scanCredential(errorScanner{sql.ErrNoRows}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scanCredential(no rows) error = %v", err)
	}
	if _, err := scanToken(errorScanner{sql.ErrNoRows}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scanToken(no rows) error = %v", err)
	}
	if _, err := scanAudit(errorScanner{sql.ErrNoRows}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scanAudit(no rows) error = %v", err)
	}
}

func TestWriteTransactionCommitFailureSurface(t *testing.T) {
	// 关闭数据库覆盖 BeginTx 错误；此测试保留明确的错误文本断言，避免未来吞掉事务错误。
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	err := store.withWriteTx(context.Background(), func(*sql.Tx) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "开始 sqlite 事务") {
		t.Fatalf("withWriteTx() error = %v", err)
	}
}
