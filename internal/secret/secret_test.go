package secret

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCipherRoundTripAndAAD(t *testing.T) {
	t.Parallel()

	key := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := NewCipher(key, 2)
	if err != nil {
		t.Fatalf("NewCipher() error = %v", err)
	}
	if cipher.KeyVersion() != 2 {
		t.Fatalf("KeyVersion() = %d", cipher.KeyVersion())
	}
	plaintext := []byte("sensitive-provider-key")
	ciphertext, err := cipher.Encrypt("credential", "openai-main", "openai", plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if string(ciphertext) == string(plaintext) {
		t.Fatal("ciphertext 不应等于明文")
	}
	decrypted, err := cipher.Decrypt("credential", "openai-main", "openai", ciphertext)
	if err != nil || string(decrypted) != string(plaintext) {
		t.Fatalf("Decrypt() = %q, %v", decrypted, err)
	}
	if _, err := cipher.Decrypt("credential", "other", "openai", ciphertext); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("错误 AAD 的 Decrypt() error = %v", err)
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 1
	if _, err := cipher.Decrypt("credential", "openai-main", "openai", tampered); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("篡改 ciphertext 的 Decrypt() error = %v", err)
	}
	if _, err := cipher.Encrypt("credential", "id", "openai", nil); err == nil {
		t.Fatal("Encrypt() 应拒绝空明文")
	}
}

func TestNewCipherValidation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		key     []byte
		version int
	}{
		{make([]byte, 31), 1},
		{make([]byte, 32), 0},
	} {
		if _, err := NewCipher(test.key, test.version); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("NewCipher() error = %v", err)
		}
	}
}

func TestLoadMasterKey(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef")
	write := func(name string, data []byte) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	for _, test := range []struct {
		name string
		data []byte
	}{
		{"raw", append(append([]byte(nil), key...), '\n')},
		{"base64", []byte(base64.StdEncoding.EncodeToString(key) + "\n")},
	} {
		loaded, err := LoadMasterKey(write(test.name, test.data))
		if err != nil || string(loaded) != string(key) {
			t.Fatalf("LoadMasterKey(%s) = %q, %v", test.name, loaded, err)
		}
	}
	for _, data := range [][]byte{[]byte("short"), []byte("%%%invalid%%%"), []byte(base64.StdEncoding.EncodeToString([]byte("short")))} {
		if _, err := LoadMasterKey(write("invalid-"+strings.ReplaceAll(string(data[:min(len(data), 2)]), "%", "x"), data)); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("LoadMasterKey(invalid) error = %v", err)
		}
	}
	if _, err := LoadMasterKey(filepath.Join(directory, "missing")); err == nil {
		t.Fatal("LoadMasterKey() 应返回读取错误")
	}
}

func TestGatewayTokenAndDigest(t *testing.T) {
	t.Parallel()

	token := NewGatewayToken()
	if err := ValidateGatewayToken(token); err != nil {
		t.Fatalf("ValidateGatewayToken() error = %v", err)
	}
	digest := Digest(token)
	if !MatchesDigest(token, digest[:]) || MatchesDigest(token+"x", digest[:]) || MatchesDigest(token, digest[:4]) {
		t.Fatal("MatchesDigest() 结果不正确")
	}
	for _, invalid := range []string{"", "wrong_abcdefghijklmnopqrstuvwxyz", "agw_short", "agw_" + strings.Repeat("a", 300)} {
		if err := ValidateGatewayToken(invalid); !errors.Is(err, ErrInvalidGatewayToken) {
			t.Fatalf("ValidateGatewayToken(%q) error = %v", invalid, err)
		}
	}
}
