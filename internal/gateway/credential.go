package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/model"
	"github.com/deigmata-paideias/gateway/internal/store/sqlite"
)

type CredentialInput struct {
	ID       string
	Provider string
	Name     string
	Secret   []byte
}

func (s *Service) CreateCredential(ctx context.Context, input CredentialInput, requestID string) (model.Credential, error) {
	if err := validateCredentialInput(input); err != nil {
		return model.Credential{}, err
	}
	defer clear(input.Secret)
	ciphertext, err := s.cipher.Encrypt("credential", input.ID, input.Provider, input.Secret)
	if err != nil {
		return model.Credential{}, fmt.Errorf("加密 credential: %w", err)
	}
	now := time.Now().UnixMilli()
	credential := model.Credential{
		ID: input.ID, Provider: input.Provider, Name: input.Name, Status: "active",
		Ciphertext: ciphertext, KeyVersion: s.cipher.KeyVersion(), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateCredential(ctx, credential); err != nil {
		return model.Credential{}, mapStoreError(err)
	}
	if err := s.recordAdminEvent(ctx, "credential.created", "credential", input.ID, requestID); err != nil {
		return model.Credential{}, err
	}
	return credential, nil
}

func (s *Service) ImportCredential(ctx context.Context, item config.CredentialImport) (bool, error) {
	data, err := os.ReadFile(item.SourceFile)
	if err != nil {
		return false, fmt.Errorf("读取 credential import %q: %w", item.CredentialID, err)
	}
	data = []byte(strings.TrimSpace(string(data)))
	defer clear(data)
	input := CredentialInput{ID: item.CredentialID, Provider: item.Provider, Name: item.CredentialID, Secret: data}
	if err := validateCredentialInput(input); err != nil {
		return false, err
	}
	ciphertext, err := s.cipher.Encrypt("credential", input.ID, input.Provider, input.Secret)
	if err != nil {
		return false, fmt.Errorf("加密 credential import: %w", err)
	}
	now := time.Now().UnixMilli()
	return s.store.CreateCredentialIfMissing(ctx, model.Credential{
		ID: input.ID, Provider: input.Provider, Name: input.Name, Status: "active",
		Ciphertext: ciphertext, KeyVersion: s.cipher.KeyVersion(), CreatedAt: now, UpdatedAt: now,
	})
}

func (s *Service) Credential(ctx context.Context, id string) (model.Credential, error) {
	credential, err := s.store.Credential(ctx, id)
	if err != nil {
		return model.Credential{}, mapStoreError(err)
	}
	return credential, nil
}

func (s *Service) Credentials(ctx context.Context) ([]model.Credential, error) {
	credentials, err := s.store.Credentials(ctx)
	if err != nil {
		return nil, mapStoreError(err)
	}
	return credentials, nil
}

func (s *Service) RotateCredential(ctx context.Context, id string, plaintext []byte, requestID string) error {
	credential, err := s.Credential(ctx, id)
	if err != nil {
		return err
	}
	if len(plaintext) == 0 || len(plaintext) > 16<<10 {
		return newError(http.StatusUnprocessableEntity, "credential_invalid", "Credential Secret 无效", errors.New("secret 为空或过长"))
	}
	defer clear(plaintext)
	ciphertext, err := s.cipher.Encrypt("credential", id, credential.Provider, plaintext)
	if err != nil {
		return fmt.Errorf("加密 credential: %w", err)
	}
	if err := s.store.RotateCredential(ctx, id, ciphertext, s.cipher.KeyVersion()); err != nil {
		return mapStoreError(err)
	}
	return s.recordAdminEvent(ctx, "credential.rotated", "credential", id, requestID)
}

func (s *Service) DeleteCredential(ctx context.Context, id, requestID string) error {
	for _, backend := range s.configs.Current().Config().Backends {
		if backend.CredentialID == id {
			return newError(http.StatusConflict, "credential_in_use", "Credential 仍被 Backend 引用", sqlite.ErrConflict)
		}
	}
	if err := s.store.DeleteCredential(ctx, id); err != nil {
		return mapStoreError(err)
	}
	return s.recordAdminEvent(ctx, "credential.deleted", "credential", id, requestID)
}

func validateCredentialInput(input CredentialInput) error {
	validProvider := input.Provider == "openai" || input.Provider == "dashscope"
	if input.ID == "" || len(input.ID) > 63 || !validProvider || input.Name == "" || len(input.Secret) == 0 || len(input.Secret) > 16<<10 {
		return newError(http.StatusUnprocessableEntity, "credential_invalid", "Credential 参数无效", errors.New("id、provider、name 或 secret 无效"))
	}
	return nil
}
