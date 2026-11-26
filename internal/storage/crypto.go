package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type SecretBox struct {
	key []byte
}

func OpenSecretBox(dataDir string) (*SecretBox, error) {
	if rawEnv := strings.TrimSpace(os.Getenv("MW_ECHO_MASTER_KEY")); rawEnv != "" {
		sum := sha256.Sum256([]byte(rawEnv))
		return &SecretBox{key: sum[:]}, nil
	}
	keyPath := filepath.Join(dataDir, ".master.key")
	if existing, err := os.ReadFile(keyPath); err == nil && len(existing) == 32 {
		return &SecretBox{key: existing}, nil
	}
	generated := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, generated); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, generated, 0o600); err != nil {
		return nil, fmt.Errorf("persist master key: %w", err)
	}
	return &SecretBox{key: generated}, nil
}

func (b *SecretBox) Encrypt(plaintext []byte) (string, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (b *SecretBox) Decrypt(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce := raw[:gcm.NonceSize()]
	ciphertext := raw[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
