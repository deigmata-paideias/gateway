package api

import (
	"github.com/deigmata-paideias/gateway/internal/model"
)

type tokenResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Token      string `json:"token,omitempty"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	ExpiresAt  *int64 `json:"expires_at"`
	RevokedAt  *int64 `json:"revoked_at,omitempty"`
	LastUsedAt *int64 `json:"last_used_at,omitempty"`
}

func tokenView(token model.GatewayToken) tokenResponse {
	return tokenResponse{
		ID: token.ID, Name: token.Name, Status: token.Status, CreatedAt: token.CreatedAt,
		UpdatedAt: token.UpdatedAt, ExpiresAt: token.ExpiresAt, RevokedAt: token.RevokedAt,
		LastUsedAt: token.LastUsedAt,
	}
}

type credentialResponse struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	RotatedAt *int64 `json:"rotated_at,omitempty"`
}

func credentialView(credential model.Credential) credentialResponse {
	return credentialResponse{
		ID: credential.ID, Provider: credential.Provider, Name: credential.Name,
		Status: credential.Status, CreatedAt: credential.CreatedAt, UpdatedAt: credential.UpdatedAt,
		RotatedAt: credential.RotatedAt,
	}
}
