package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/deigmata-paideias/gateway/internal/identity"
	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/secret"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

type IssuedToken struct {
	Token  model.GatewayToken `json:"token_metadata"`
	Secret string             `json:"token"`
}

func (s *Service) CreateToken(ctx context.Context, name string, expiresAt *int64, requestID string) (IssuedToken, error) {
	if name == "" || len(name) > 128 {
		return IssuedToken{}, newError(http.StatusUnprocessableEntity, "invalid_token_name", "Token 名称无效", errors.New("名称为空或过长"))
	}
	now := time.Now().UnixMilli()
	if expiresAt != nil && *expiresAt <= now {
		return IssuedToken{}, newError(http.StatusUnprocessableEntity, "invalid_expiration", "过期时间必须晚于当前时间", errors.New("过期时间无效"))
	}
	id, err := identity.New("gtok_")
	if err != nil {
		return IssuedToken{}, err
	}
	raw := secret.NewGatewayToken()
	digest := secret.Digest(raw)
	ciphertext, err := s.cipher.Encrypt("token", id, "", []byte(raw))
	if err != nil {
		return IssuedToken{}, fmt.Errorf("加密 gateway token: %w", err)
	}
	token := model.GatewayToken{
		ID: id, Name: name, Status: "active", Digest: digest[:], Ciphertext: ciphertext,
		KeyVersion: s.cipher.KeyVersion(), CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt,
	}
	if err := s.store.CreateToken(ctx, token); err != nil {
		return IssuedToken{}, mapStoreError(err)
	}
	if err := s.recordAdminEvent(ctx, "token.created", "token", id, requestID); err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{Token: token, Secret: raw}, nil
}

func (s *Service) Token(ctx context.Context, id string) (model.GatewayToken, error) {
	token, err := s.store.Token(ctx, id)
	if err != nil {
		return model.GatewayToken{}, mapStoreError(err)
	}
	return token, nil
}

func (s *Service) Tokens(ctx context.Context) ([]model.GatewayToken, error) {
	tokens, err := s.store.Tokens(ctx)
	if err != nil {
		return nil, mapStoreError(err)
	}
	return tokens, nil
}

func (s *Service) TokenSecret(ctx context.Context, id, requestID string) (string, error) {
	token, err := s.Token(ctx, id)
	if err != nil {
		return "", err
	}
	plaintext, err := s.cipher.Decrypt("token", token.ID, "", token.Ciphertext)
	if err != nil {
		return "", newError(http.StatusServiceUnavailable, "token_unavailable", "Token Secret 不可用", err)
	}
	defer clear(plaintext)
	if err := s.recordAdminEvent(ctx, "token.secret_revealed", "token", id, requestID); err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (s *Service) RotateToken(ctx context.Context, id, requestID string) (IssuedToken, error) {
	token, err := s.Token(ctx, id)
	if err != nil {
		return IssuedToken{}, err
	}
	if token.Status != "active" {
		return IssuedToken{}, newError(http.StatusConflict, "token_inactive", "只有 Active Token 可以轮换", sqlite.ErrInactiveToken)
	}
	raw := secret.NewGatewayToken()
	digest := secret.Digest(raw)
	ciphertext, err := s.cipher.Encrypt("token", id, "", []byte(raw))
	if err != nil {
		return IssuedToken{}, fmt.Errorf("加密 gateway token: %w", err)
	}
	if err := s.store.RotateToken(ctx, id, digest[:], ciphertext, s.cipher.KeyVersion()); err != nil {
		return IssuedToken{}, mapStoreError(err)
	}
	if err := s.recordAdminEvent(ctx, "token.rotated", "token", id, requestID); err != nil {
		return IssuedToken{}, err
	}
	token.Digest = digest[:]
	token.Ciphertext = ciphertext
	token.UpdatedAt = time.Now().UnixMilli()
	return IssuedToken{Token: token, Secret: raw}, nil
}

func (s *Service) RevokeToken(ctx context.Context, id, requestID string) error {
	if err := s.store.RevokeToken(ctx, id); err != nil {
		return mapStoreError(err)
	}
	return s.recordAdminEvent(ctx, "token.revoked", "token", id, requestID)
}

func (s *Service) DeleteToken(ctx context.Context, id, requestID string) error {
	if err := s.store.DeleteToken(ctx, id); err != nil {
		mapped := mapStoreError(err)
		if errors.Is(err, sqlite.ErrConflict) {
			mapped.Code = "token_has_dependencies"
		}
		return mapped
	}
	return s.recordAdminEvent(ctx, "token.deleted", "token", id, requestID)
}
