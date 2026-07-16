package gateway

import (
	"context"
	"errors"
	"net/http"

	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/secret"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

func (s *Service) Authenticate(ctx context.Context, rawToken string) (model.GatewayToken, error) {
	if err := secret.ValidateGatewayToken(rawToken); err != nil {
		return model.GatewayToken{}, newError(http.StatusUnauthorized, "invalid_gateway_token", "Gateway Token 无效", err)
	}
	digest := secret.Digest(rawToken)
	token, err := s.store.TokenByDigest(ctx, digest[:])
	if err != nil {
		if errors.Is(err, sqlite.ErrNotFound) || errors.Is(err, sqlite.ErrInactiveToken) {
			return model.GatewayToken{}, newError(http.StatusUnauthorized, "invalid_gateway_token", "Gateway Token 无效", err)
		}
		return model.GatewayToken{}, mapStoreError(err)
	}
	if !secret.MatchesDigest(rawToken, token.Digest) {
		return model.GatewayToken{}, newError(http.StatusUnauthorized, "invalid_gateway_token", "Gateway Token 无效", errors.New("token digest 不匹配"))
	}
	return token, nil
}
