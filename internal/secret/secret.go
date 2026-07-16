// Package secret 负责 Provider Key 和 Gateway Token 的加密与摘要。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

var (
	ErrInvalidKey          = errors.New("secret: invalid master key")
	ErrDecrypt             = errors.New("secret: decrypt failed")
	ErrInvalidGatewayToken = errors.New("secret: invalid gateway token")
)

type Cipher struct {
	aead       cipher.AEAD
	keyVersion int
}

func NewCipher(key []byte, keyVersion int) (*Cipher, error) {
	if len(key) != 32 || keyVersion < 1 {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("创建 aes cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("创建 aes-gcm: %w", err)
	}
	return &Cipher{aead: aead, keyVersion: keyVersion}, nil
}

func LoadMasterKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 master key: %w", err)
	}
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 32 {
		return data, nil
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(string(data))
	if decodeErr != nil || len(decoded) != 32 {
		clear(data)
		clear(decoded)
		return nil, ErrInvalidKey
	}
	clear(data)
	return decoded, nil
}

func (c *Cipher) Encrypt(resourceType, resourceID, provider string, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("secret: plaintext is empty")
	}
	aad := c.aad(resourceType, resourceID, provider)
	return c.aead.Seal(nil, nil, plaintext, aad), nil
}

func (c *Cipher) Decrypt(resourceType, resourceID, provider string, ciphertext []byte) ([]byte, error) {
	plaintext, err := c.aead.Open(nil, nil, ciphertext, c.aad(resourceType, resourceID, provider))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

func (c *Cipher) KeyVersion() int {
	return c.keyVersion
}

func (c *Cipher) aad(resourceType, resourceID, provider string) []byte {
	return fmt.Appendf(nil, "ai-gateway:v1:%s:%s:%s:%d", resourceType, resourceID, provider, c.keyVersion)
}

func NewGatewayToken() string {
	return "agw_" + rand.Text()
}

func Digest(value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(value))
}

func MatchesDigest(value string, expected []byte) bool {
	actual := Digest(value)
	return len(expected) == sha256.Size && subtle.ConstantTimeCompare(actual[:], expected) == 1
}

func ValidateGatewayToken(value string) error {
	if !strings.HasPrefix(value, "agw_") || len(value) < 24 || len(value) > 256 {
		return ErrInvalidGatewayToken
	}
	return nil
}
