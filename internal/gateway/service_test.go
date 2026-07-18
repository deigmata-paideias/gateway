package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.yaml.in/yaml/v3"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/provider"
	"github.com/deigmata-paideias/gateway/internal/secret"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

func TestNewServiceAndHelpers(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	if fixture.service.Store() != fixture.store || fixture.service.CurrentSnapshot().Revision() != 1 {
		t.Fatal("Service getter 返回值错误")
	}
	for _, test := range []struct {
		store   *sqlite.Store
		cipher  *secret.Cipher
		manager *config.Manager
	}{
		{nil, fixture.cipher, fixture.manager},
		{fixture.store, nil, fixture.manager},
		{fixture.store, fixture.cipher, nil},
		{fixture.store, fixture.cipher, config.NewManager(nil)},
	} {
		if _, err := New(test.store, test.cipher, test.manager, nil); err == nil {
			t.Fatal("New() 应拒绝空依赖")
		}
	}
	service, err := New(fixture.store, fixture.cipher, fixture.manager, nil, WithRecorder(nil))
	if err != nil || service.transport == nil {
		t.Fatalf("New(default transport) = %#v, %v", service, err)
	}

	underlying := errors.New("underlying")
	gatewayError := newError(418, "teapot", "message", underlying)
	if !strings.Contains(gatewayError.Error(), "teapot") || !errors.Is(gatewayError, underlying) || gatewayError.Unwrap() != underlying {
		t.Fatalf("Error() = %q", gatewayError.Error())
	}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	}))
	if traceID(ctx) == "" || traceID(context.Background()) != "" {
		t.Fatal("traceID() 结果错误")
	}
	if id, err := newInternalID("test_"); err != nil || !strings.HasPrefix(id, "test_") {
		t.Fatalf("newInternalID() = %q, %v", id, err)
	}
	finishCtx, cancel := finishContext(context.Background())
	defer cancel()
	if _, ok := finishCtx.Deadline(); !ok {
		t.Fatal("finishContext() 缺少 deadline")
	}
	noop := noopRecorder{}
	noop.RecordProviderCall(context.Background(), "openai", "chat", "model", "succeeded", 0, model.Usage{})
	noop.RecordAuditFailure(context.Background(), "chat")
}

func TestErrorMappings(t *testing.T) {
	t.Parallel()

	providerCases := []struct {
		err    error
		status int
		code   string
	}{
		{&provider.Error{Status: 429, Code: "rate_limit", Err: errors.New("x")}, 429, "rate_limit"},
		{&provider.Error{Status: 401, Code: "auth", Err: errors.New("x")}, 502, "auth"},
		{&provider.Error{Status: 500, Code: "upstream", Err: errors.New("x")}, 502, "upstream"},
		{provider.ErrInvalidRequest, 422, "invalid_request"},
		{provider.ErrImageTooLarge, 502, "image_too_large"},
		{provider.ErrInvalidResponse, 502, "invalid_upstream_response"},
		{provider.ErrUnsupportedImage, 502, "invalid_upstream_response"},
		{errors.New("network"), 502, "upstream_unavailable"},
	}
	for _, test := range providerCases {
		mapped := mapProviderError(test.err)
		if mapped.Status != test.status || mapped.Code != test.code {
			t.Errorf("mapProviderError(%v) = %#v", test.err, mapped)
		}
	}
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{sqlite.ErrNotFound, 404, "not_found"},
		{sqlite.ErrConflict, 409, "revision_conflict"},
		{errors.New("db"), 503, "storage_unavailable"},
	} {
		mapped := mapStoreError(test.err)
		if mapped.Status != test.status || mapped.Code != test.code {
			t.Errorf("mapStoreError(%v) = %#v", test.err, mapped)
		}
	}
	for _, err := range []error{config.ErrRouteNotFound, config.ErrBackendNotFound} {
		if mapped := mapRouteError(err); mapped.Status != 404 || mapped.Code != "model_not_found" {
			t.Errorf("mapRouteError(%v) = %#v", err, mapped)
		}
	}
	if mapped := mapRouteError(errors.New("invalid")); mapped.Status != 422 || mapped.Code != "invalid_configuration" {
		t.Fatalf("mapRouteError(invalid) = %#v", mapped)
	}
}

func TestTokenLifecycle(t *testing.T) {
	t.Parallel()

	fixture := newFixtureWithoutToken(t)
	ctx := context.Background()
	for _, name := range []string{"", strings.Repeat("x", 129)} {
		if _, err := fixture.service.CreateToken(ctx, name, nil, "req_invalid"); errorCode(err) != "invalid_token_name" {
			t.Fatalf("CreateToken(%q) error = %v", name, err)
		}
	}
	past := time.Now().Add(-time.Minute).UnixMilli()
	if _, err := fixture.service.CreateToken(ctx, "expired", &past, "req_invalid"); errorCode(err) != "invalid_expiration" {
		t.Fatalf("CreateToken(expired) error = %v", err)
	}
	future := time.Now().Add(time.Hour).UnixMilli()
	issued, err := fixture.service.CreateToken(ctx, "client", &future, "req_create")
	if err != nil || issued.Token.ID == "" || issued.Secret == "" || issued.Token.ExpiresAt == nil {
		t.Fatalf("CreateToken() = %#v, %v", issued, err)
	}
	loaded, err := fixture.service.Token(ctx, issued.Token.ID)
	if err != nil || loaded.Name != "client" {
		t.Fatalf("Token() = %#v, %v", loaded, err)
	}
	items, err := fixture.service.Tokens(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("Tokens() = %#v, %v", items, err)
	}
	revealed, err := fixture.service.TokenSecret(ctx, issued.Token.ID, "")
	if err != nil || revealed != issued.Secret {
		t.Fatalf("TokenSecret() = %q, %v", revealed, err)
	}
	for _, invalid := range []string{"bad", "agw_" + strings.Repeat("x", 30)} {
		if _, err := fixture.service.Authenticate(ctx, invalid); errorCode(err) != "invalid_gateway_token" {
			t.Fatalf("Authenticate(%q) error = %v", invalid, err)
		}
	}
	authenticated, err := fixture.service.Authenticate(ctx, issued.Secret)
	if err != nil || authenticated.ID != issued.Token.ID {
		t.Fatalf("Authenticate() = %#v, %v", authenticated, err)
	}
	rotated, err := fixture.service.RotateToken(ctx, issued.Token.ID, "req_rotate")
	if err != nil || rotated.Secret == issued.Secret {
		t.Fatalf("RotateToken() = %#v, %v", rotated, err)
	}
	if _, err := fixture.service.Authenticate(ctx, issued.Secret); errorCode(err) != "invalid_gateway_token" {
		t.Fatalf("旧 token 仍可鉴权: %v", err)
	}
	if _, err := fixture.service.Authenticate(ctx, rotated.Secret); err != nil {
		t.Fatalf("新 token 鉴权失败: %v", err)
	}
	if err := fixture.service.RevokeToken(ctx, issued.Token.ID, "req_revoke"); err != nil {
		t.Fatalf("RevokeToken() error = %v", err)
	}
	if _, err := fixture.service.Authenticate(ctx, rotated.Secret); errorCode(err) != "invalid_gateway_token" {
		t.Fatalf("被吊销 token 仍可鉴权: %v", err)
	}
	if _, err := fixture.service.RotateToken(ctx, issued.Token.ID, "req_rotate"); errorCode(err) != "token_inactive" {
		t.Fatalf("RotateToken(inactive) error = %v", err)
	}
	if err := fixture.service.DeleteToken(ctx, issued.Token.ID, "req_delete"); err != nil {
		t.Fatalf("DeleteToken() error = %v", err)
	}
	if _, err := fixture.service.Token(ctx, issued.Token.ID); errorCode(err) != "not_found" {
		t.Fatalf("Token(deleted) error = %v", err)
	}
	if err := fixture.service.DeleteToken(ctx, issued.Token.ID, "req_delete"); errorCode(err) != "not_found" {
		t.Fatalf("DeleteToken(missing) error = %v", err)
	}
}

func TestTokenSecretFailureAndDependency(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	token := fixture.token.Token
	if err := fixture.store.RotateToken(ctx, token.ID, token.Digest, []byte("tampered"), fixture.cipher.KeyVersion()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.TokenSecret(ctx, token.ID, "req"); errorCode(err) != "token_unavailable" {
		t.Fatalf("TokenSecret(tampered) error = %v", err)
	}
	if err := fixture.store.PutBinding(ctx, model.ResponseBinding{
		PublicResponseID: "resp_dependency", GatewayTokenID: token.ID, BackendID: "openai-main",
		Provider: "openai", ModelAlias: "response-model", UpstreamResponseID: "resp_up",
		Revision: 1, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), State: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RevokeToken(ctx, token.ID, "req"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DeleteToken(ctx, token.ID, "req"); errorCode(err) != "token_has_dependencies" {
		t.Fatalf("DeleteToken(dependency) error = %v", err)
	}
}

func TestCredentialLifecycleAndImport(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	tests := []CredentialInput{
		{},
		{ID: strings.Repeat("x", 64), Provider: "openai", Name: "x", Secret: []byte("key")},
		{ID: "valid", Provider: "other", Name: "x", Secret: []byte("key")},
		{ID: "valid", Provider: "openai", Name: "", Secret: []byte("key")},
		{ID: "valid", Provider: "openai", Name: "x", Secret: nil},
		{ID: "valid", Provider: "openai", Name: "x", Secret: make([]byte, (16<<10)+1)},
	}
	for _, input := range tests {
		if _, err := fixture.service.CreateCredential(ctx, input, "req"); errorCode(err) != "credential_invalid" {
			t.Fatalf("CreateCredential(%#v) error = %v", input, err)
		}
	}
	created, err := fixture.service.CreateCredential(ctx, CredentialInput{
		ID: "unused", Provider: "openai", Name: "Unused", Secret: []byte("secret"),
	}, "req_create")
	if err != nil || created.ID != "unused" || len(created.Ciphertext) == 0 {
		t.Fatalf("CreateCredential() = %#v, %v", created, err)
	}
	if _, err := fixture.service.CreateCredential(ctx, CredentialInput{
		ID: "unused", Provider: "openai", Name: "Unused", Secret: []byte("secret"),
	}, "req_create"); errorCode(err) != "revision_conflict" {
		t.Fatalf("CreateCredential(duplicate) error = %v", err)
	}
	loaded, err := fixture.service.Credential(ctx, "unused")
	if err != nil || loaded.Name != "Unused" {
		t.Fatalf("Credential() = %#v, %v", loaded, err)
	}
	items, err := fixture.service.Credentials(ctx)
	if err != nil || len(items) != 3 {
		t.Fatalf("Credentials() = %d, %v", len(items), err)
	}
	for _, value := range [][]byte{nil, make([]byte, (16<<10)+1)} {
		if err := fixture.service.RotateCredential(ctx, "unused", value, "req"); errorCode(err) != "credential_invalid" {
			t.Fatalf("RotateCredential(invalid) error = %v", err)
		}
	}
	if err := fixture.service.RotateCredential(ctx, "unused", []byte("new-secret"), "req_rotate"); err != nil {
		t.Fatalf("RotateCredential() error = %v", err)
	}
	if err := fixture.service.RotateCredential(ctx, "missing", []byte("x"), "req"); errorCode(err) != "not_found" {
		t.Fatalf("RotateCredential(missing) error = %v", err)
	}
	if err := fixture.service.DeleteCredential(ctx, "openai-main", "req"); errorCode(err) != "credential_in_use" {
		t.Fatalf("DeleteCredential(in-use) error = %v", err)
	}
	if err := fixture.service.DeleteCredential(ctx, "unused", "req_delete"); err != nil {
		t.Fatalf("DeleteCredential() error = %v", err)
	}
	if err := fixture.service.DeleteCredential(ctx, "unused", "req_delete"); errorCode(err) != "not_found" {
		t.Fatalf("DeleteCredential(missing) error = %v", err)
	}

	directory := t.TempDir()
	keyPath := filepath.Join(directory, "import.key")
	if err := os.WriteFile(keyPath, []byte(" imported-secret \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	item := config.CredentialImport{CredentialID: "imported", Provider: "dashscope", SourceFile: keyPath, ImportPolicy: "if_missing"}
	imported, err := fixture.service.ImportCredential(ctx, item)
	if err != nil || !imported {
		t.Fatalf("ImportCredential() = %v, %v", imported, err)
	}
	imported, err = fixture.service.ImportCredential(ctx, item)
	if err != nil || imported {
		t.Fatalf("ImportCredential(existing) = %v, %v", imported, err)
	}
	item.SourceFile = filepath.Join(directory, "missing")
	if _, err := fixture.service.ImportCredential(ctx, item); err == nil {
		t.Fatal("ImportCredential(missing) 应失败")
	}
	emptyPath := filepath.Join(directory, "empty.key")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	item.CredentialID, item.SourceFile = "empty", emptyPath
	if _, err := fixture.service.ImportCredential(ctx, item); errorCode(err) != "credential_invalid" {
		t.Fatalf("ImportCredential(empty) error = %v", err)
	}
}

func TestLoadSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Options{Path: filepath.Join(t.TempDir(), "gateway.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := LoadSnapshot(ctx, store, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("LoadSnapshot(missing) 应失败")
	}
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	data, err := yaml.Marshal(testGatewayConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := LoadSnapshot(ctx, store, path)
	if err != nil || snapshot.Revision() != 1 {
		t.Fatalf("LoadSnapshot() = %#v, %v", snapshot, err)
	}
	again, err := LoadSnapshot(ctx, store, filepath.Join(t.TempDir(), "unused"))
	if err != nil || again.Revision() != 1 {
		t.Fatalf("LoadSnapshot(persisted) = %#v, %v", again, err)
	}
}

func TestConfigLifecycle(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	revision, current := fixture.service.Config()
	if revision != 1 || len(current.Backends) != 2 {
		t.Fatalf("Config() = %d, %#v", revision, current)
	}
	if _, err := fixture.service.UpdateConfig(ctx, 1, config.Gateway{}, "replace", "req"); errorCode(err) != "invalid_configuration" {
		t.Fatalf("UpdateConfig(invalid) error = %v", err)
	}
	missingCredential := current
	missingCredential.Backends = append([]config.Backend(nil), current.Backends...)
	missingCredential.Backends[0].CredentialID = "missing"
	if _, err := fixture.service.UpdateConfig(ctx, 1, missingCredential, "replace", "req"); errorCode(err) != "credential_invalid" {
		t.Fatalf("UpdateConfig(missing credential) error = %v", err)
	}
	mismatch := current
	mismatch.Backends = append([]config.Backend(nil), current.Backends...)
	mismatch.Backends[0].CredentialID = "dashscope-main"
	if _, err := fixture.service.UpdateConfig(ctx, 1, mismatch, "replace", "req"); errorCode(err) != "credential_invalid" {
		t.Fatalf("UpdateConfig(mismatch) error = %v", err)
	}
	if _, err := fixture.service.SwitchBackend(ctx, 0, "chat-route", "dashscope-main", "req"); errorCode(err) != "revision_conflict" {
		t.Fatalf("SwitchBackend(conflict) error = %v", err)
	}
	if _, err := fixture.service.SwitchBackend(ctx, 1, "missing", "dashscope-main", "req"); errorCode(err) != "model_not_found" {
		t.Fatalf("SwitchBackend(missing route) error = %v", err)
	}
	if _, err := fixture.service.SwitchBackend(ctx, 1, "chat-route", "missing", "req"); errorCode(err) != "model_not_found" {
		t.Fatalf("SwitchBackend(missing backend) error = %v", err)
	}
	next, err := fixture.service.SwitchBackend(ctx, 1, "chat-route", "dashscope-main", "req_switch")
	if err != nil || next != 2 {
		t.Fatalf("SwitchBackend() = %d, %v", next, err)
	}
	_, switched := fixture.service.Config()
	if switched.Routes[0].ActiveBackend != "dashscope-main" {
		t.Fatalf("switched config = %#v", switched.Routes[0])
	}
	if _, err := fixture.service.UpdateConfig(ctx, 1, switched, "replace", "req"); errorCode(err) != "revision_conflict" {
		t.Fatalf("UpdateConfig(conflict) error = %v", err)
	}
	restored, err := fixture.service.RestoreConfig(ctx, 2, 1, "req_restore")
	if err != nil || restored != 3 {
		t.Fatalf("RestoreConfig() = %d, %v", restored, err)
	}
	_, restoredConfig := fixture.service.Config()
	if restoredConfig.Routes[0].ActiveBackend != "openai-main" {
		t.Fatalf("restored config = %#v", restoredConfig.Routes[0])
	}
	if _, err := fixture.service.RestoreConfig(ctx, 3, 999, "req"); errorCode(err) != "not_found" {
		t.Fatalf("RestoreConfig(missing) error = %v", err)
	}
	revisions, err := fixture.service.ConfigRevisions(ctx, 10)
	if err != nil || len(revisions) != 3 || revisions[0].Revision != 3 {
		t.Fatalf("ConfigRevisions() = %#v, %v", revisions, err)
	}
}

func TestServiceStorageFailureMappings(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	rawToken := fixture.token.Secret
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Authenticate(ctx, rawToken); errorCode(err) != "storage_unavailable" {
		t.Fatalf("Authenticate(closed) error = %v", err)
	}
	if _, err := fixture.service.Token(ctx, fixture.token.Token.ID); errorCode(err) != "storage_unavailable" {
		t.Fatalf("Token(closed) error = %v", err)
	}
	if _, err := fixture.service.TokenSecret(ctx, fixture.token.Token.ID, "req"); errorCode(err) != "storage_unavailable" {
		t.Fatalf("TokenSecret(closed) error = %v", err)
	}
	if _, err := fixture.service.CreateToken(ctx, "new", nil, "req"); errorCode(err) != "storage_unavailable" {
		t.Fatalf("CreateToken(closed) error = %v", err)
	}
	if err := fixture.service.RevokeToken(ctx, fixture.token.Token.ID, "req"); errorCode(err) != "storage_unavailable" {
		t.Fatalf("RevokeToken(closed) error = %v", err)
	}
	if _, err := fixture.service.Credential(ctx, "openai-main"); errorCode(err) != "storage_unavailable" {
		t.Fatalf("Credential(closed) error = %v", err)
	}
	if _, err := fixture.service.CreateCredential(ctx, CredentialInput{
		ID: "new-credential", Provider: "openai", Name: "New", Secret: []byte("secret"),
	}, "req"); errorCode(err) != "storage_unavailable" {
		t.Fatalf("CreateCredential(closed) error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ImportCredential(ctx, config.CredentialImport{
		CredentialID: "import-after-close", Provider: "openai", SourceFile: path, ImportPolicy: "if_missing",
	}); err == nil {
		t.Fatal("ImportCredential(closed) 应失败")
	}
	if err := fixture.service.recordAdminEvent(ctx, "test", "test", "id", "req"); errorCode(err) != "storage_unavailable" {
		t.Fatalf("recordAdminEvent(closed) error = %v", err)
	}
	if _, err := fixture.service.UpdateConfig(ctx, 1, testGatewayConfig(), "replace", "req"); errorCode(err) != "storage_unavailable" {
		t.Fatalf("UpdateConfig(closed) error = %v", err)
	}
}

func TestLoadAndRestoreCorruptConfig(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newFixture(t)
	bad, err := fixture.store.SaveConfig(ctx, 1, []byte(`not-json`), "test", "req")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(ctx, fixture.store, "unused"); err == nil || !strings.Contains(err.Error(), "解析持久化配置") {
		t.Fatalf("LoadSnapshot(corrupt) error = %v", err)
	}
	if _, err := fixture.service.RestoreConfig(ctx, 1, bad.Revision, "req"); errorCode(err) != "storage_unavailable" {
		t.Fatalf("RestoreConfig(corrupt) error = %v", err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(ctx, fixture.store, "unused"); err == nil || !strings.Contains(err.Error(), "加载当前配置") {
		t.Fatalf("LoadSnapshot(closed) error = %v", err)
	}
}

func TestNewProviderValidation(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	ctx := context.Background()
	route, _ := fixture.manager.Current().Resolve("chat", "chat-model")
	if _, err := fixture.service.newProvider(ctx, route); err != nil {
		t.Fatalf("newProvider(openai) error = %v", err)
	}
	route, _ = fixture.manager.Current().ResolveBackend("chat", "chat-model", "dashscope-main")
	if _, err := fixture.service.newProvider(ctx, route); err != nil {
		t.Fatalf("newProvider(dashscope) error = %v", err)
	}
	route.Backend.Provider, route.Backend.CredentialID = "openai", "missing"
	if _, err := fixture.service.newProvider(ctx, route); errorCode(err) != "not_found" {
		t.Fatalf("newProvider(missing credential) error = %v", err)
	}
	createStoredCredential(t, fixture.store, fixture.cipher, "disabled", "openai", "secret", "disabled")
	route.Backend.CredentialID = "disabled"
	if _, err := fixture.service.newProvider(ctx, route); errorCode(err) != "credential_unavailable" {
		t.Fatalf("newProvider(disabled) error = %v", err)
	}
	createStoredCredential(t, fixture.store, fixture.cipher, "mismatch-provider", "dashscope", "secret", "active")
	route.Backend.CredentialID = "mismatch-provider"
	if _, err := fixture.service.newProvider(ctx, route); errorCode(err) != "credential_unavailable" {
		t.Fatalf("newProvider(mismatch) error = %v", err)
	}
	now := time.Now().UnixMilli()
	if err := fixture.store.CreateCredential(ctx, model.Credential{
		ID: "tampered", Provider: "openai", Name: "tampered", Status: "active", Ciphertext: []byte("bad"),
		KeyVersion: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	route.Backend.CredentialID = "tampered"
	if _, err := fixture.service.newProvider(ctx, route); errorCode(err) != "credential_unavailable" {
		t.Fatalf("newProvider(tampered) error = %v", err)
	}
}

type fixture struct {
	store     *sqlite.Store
	cipher    *secret.Cipher
	manager   *config.Manager
	service   *Service
	transport *gatewayTransport
	recorder  *recordingRecorder
	token     IssuedToken
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fixture := newFixtureWithoutToken(t)
	issued, err := fixture.service.CreateToken(context.Background(), "test-client", nil, "req_fixture")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	fixture.token = issued
	return fixture
}

func newFixtureWithoutToken(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Options{
		Path: filepath.Join(t.TempDir(), "gateway.db"), MaxOpenConns: 4, BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cipher, err := secret.NewCipher([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testGatewayConfig()
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveConfig(ctx, 0, encoded, "bootstrap", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := config.NewSnapshot(1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager := config.NewManager(snapshot)
	createStoredCredential(t, store, cipher, "openai-main", "openai", "openai-key", "active")
	createStoredCredential(t, store, cipher, "dashscope-main", "dashscope", "dashscope-key", "active")
	transport := &gatewayTransport{}
	recorder := &recordingRecorder{}
	service, err := New(store, cipher, manager, transport, WithRecorder(recorder))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		store: store, cipher: cipher, manager: manager, service: service, transport: transport, recorder: recorder,
	}
}

func testGatewayConfig() config.Gateway {
	backend := func(id, providerName, baseURL string) config.Backend {
		return config.Backend{
			ID: id, Provider: providerName, BaseURL: baseURL, CredentialID: id,
			Capabilities: []string{"chat", "responses", "image"},
			Timeouts:     config.BackendTimeouts{Request: config.Duration(time.Second), StreamIdle: config.Duration(time.Second)},
		}
	}
	route := func(id, operation, alias string) config.Route {
		models := map[string][2]string{
			"chat": {"gpt-up", "qwen-up"}, "responses": {"gpt-response", "qwen-response"}, "image": {"gpt-image", "wanx-image"},
		}
		return config.Route{
			ID: id, Operation: operation, ModelAlias: alias, ActiveBackend: "openai-main",
			Targets: []config.Target{
				{BackendID: "openai-main", UpstreamModel: models[operation][0]},
				{BackendID: "dashscope-main", UpstreamModel: models[operation][1]},
			},
		}
	}
	return config.Gateway{
		APIVersion: config.APIVersion, Kind: config.KindGateway,
		Backends: []config.Backend{
			backend("openai-main", "openai", "https://openai.test/v1"),
			backend("dashscope-main", "dashscope", "https://dashscope.test/v1"),
		},
		Routes: []config.Route{
			route("chat-route", "chat", "chat-model"),
			route("responses-route", "responses", "response-model"),
			route("image-route", "image", "image-model"),
		},
		Audit: config.Audit{
			Retention: config.Duration(time.Hour), CleanupInterval: config.Duration(time.Minute),
			AbandonedAfter: config.Duration(time.Minute), CleanupBatchSize: 100,
		},
		Responses: config.Responses{BindingRetention: config.Duration(time.Hour)},
		Limits: config.Limits{
			RequestBodyBytes: 1 << 20, MaxBackends: 10, MaxRoutes: 10,
			ChatConcurrency: 8, ResponsesConcurrency: 8, ImageConcurrency: 2,
			ImagesPerRequest: 2, ImageRawBytesPerResponse: 1 << 20,
		},
	}
}

func createStoredCredential(
	t *testing.T,
	store *sqlite.Store,
	cipher *secret.Cipher,
	id,
	providerName,
	plaintext,
	status string,
) {
	t.Helper()
	ciphertext, err := cipher.Encrypt("credential", id, providerName, []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if err := store.CreateCredential(context.Background(), model.Credential{
		ID: id, Provider: providerName, Name: id, Status: status, Ciphertext: ciphertext,
		KeyVersion: cipher.KeyVersion(), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func errorCode(err error) string {
	var gatewayError *Error
	if errors.As(err, &gatewayError) {
		return gatewayError.Code
	}
	return ""
}

type providerRecord struct {
	host          string
	path          string
	authorization string
	body          []byte
}

type gatewayTransport struct {
	mu       sync.Mutex
	records  []providerRecord
	scenario string
	callback func()
}

func (transport *gatewayTransport) setScenario(scenario string) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.scenario = scenario
}

func (transport *gatewayTransport) setCallback(callback func()) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.callback = callback
}

func (transport *gatewayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.records = append(transport.records, providerRecord{
		host: request.URL.Host, path: request.URL.Path,
		authorization: request.Header.Get("Authorization"), body: body,
	})
	scenario := transport.scenario
	callback := transport.callback
	transport.callback = nil
	transport.mu.Unlock()
	if callback != nil {
		callback()
	}
	if scenario == "transport-error" {
		return nil, errors.New("mock transport error")
	}
	status := http.StatusOK
	switch scenario {
	case "401":
		status = http.StatusUnauthorized
	case "429":
		status = http.StatusTooManyRequests
	case "500":
		status = http.StatusInternalServerError
	}
	contentType := "application/json"
	responseBody := `{"error":{"message":"mock error","type":"mock","code":"mock"}}`
	if status == http.StatusOK {
		responseBody, contentType = gatewayResponse(request.URL.Path, body, scenario)
	}
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status),
		Header: http.Header{"Content-Type": []string{contentType}},
		Body:   io.NopCloser(strings.NewReader(responseBody)), Request: request,
	}, nil
}

func gatewayResponse(path string, body []byte, scenario string) (string, string) {
	if scenario == "malformed" {
		return "data: not-json\n\n", "text/event-stream"
	}
	var request map[string]any
	_ = json.Unmarshal(body, &request)
	stream, _ := request["stream"].(bool)
	switch path {
	case "/v1/chat/completions":
		if stream {
			return strings.Join([]string{
				`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","created":1,"model":"gpt-up","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
				"", "data: [DONE]", "", "",
			}, "\n"), "text/event-stream"
		}
		return `{"id":"chatcmpl_up","object":"chat.completion","created":1,"model":"gpt-up","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`, "application/json"
	case "/v1/responses":
		if stream {
			return strings.Join([]string{
				"event: response.completed",
				`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_upstream","object":"response","created_at":1,"status":"completed","model":"gpt-response","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
				"", "",
			}, "\n"), "text/event-stream"
		}
		id := "resp_upstream"
		if scenario == "no-response-id" {
			id = ""
		}
		return `{"id":"` + id + `","object":"response","created_at":1,"status":"completed","model":"gpt-response","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`, "application/json"
	case "/v1/images/generations", "/api/v1/services/aigc/multimodal-generation/generation":
		return `{"created":1,"data":[{"b64_json":"aW1n"}]}`, "application/json"
	default:
		return `{}`, "application/json"
	}
}

func (transport *gatewayTransport) last() providerRecord {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.records[len(transport.records)-1]
}

type recorderCall struct {
	provider  string
	operation string
	status    string
}

type recordingRecorder struct {
	mu            sync.Mutex
	calls         []recorderCall
	auditFailures int
}

func (recorder *recordingRecorder) RecordProviderCall(
	_ context.Context,
	providerName,
	operation,
	_ string,
	status string,
	_ time.Duration,
	_ model.Usage,
) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.calls = append(recorder.calls, recorderCall{provider: providerName, operation: operation, status: status})
}

func (recorder *recordingRecorder) RecordAuditFailure(context.Context, string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.auditFailures++
}
