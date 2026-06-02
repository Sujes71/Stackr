package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

const (
	// prefix identifies AES-256-GCM encrypted values in the config YAML.
	prefix    = "enc:AES256-GCM:"
	keyLength = 32 // AES-256
)

// Encrypt encrypts plaintext using AES-256-GCM with a random nonce.
// Returns a string in the format "enc:AES256-GCM:<base64url-encoded-ciphertext-with-nonce>".
func Encrypt(key []byte, plaintext string) (string, error) {
	if key == nil {
		return "", fmt.Errorf("encryption key not available")
	}
	if len(key) != keyLength {
		return "", fmt.Errorf("encryption key must be %d bytes, got %d", keyLength, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes: %w", err)
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	nonce := make([]byte, aesgcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// GCM Seal prepends the nonce to the ciphertext
	ciphertext := aesgcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.URLEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts an encoded value produced by Encrypt.
// If the value does not start with the encryption prefix, it is returned as-is
// (plaintext passthrough for migration compatibility).
func Decrypt(key []byte, encoded string) (string, error) {
	if !strings.HasPrefix(encoded, prefix) {
		// Not encrypted — return as-is (migration passthrough)
		return encoded, nil
	}

	if key == nil {
		return "", fmt.Errorf("encryption key not available")
	}

	data, err := base64.URLEncoding.DecodeString(encoded[len(prefix):])
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes: %w", err)
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	nonceSize := aesgcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short: %d bytes, need at least %d", len(data), nonceSize)
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// IsEncrypted reports whether s is an AES-256-GCM encrypted value.
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, prefix)
}
