package crypto

import (
	"fmt"
)

// KeyStore provides a platform-specific way to load or create the master encryption key.
type KeyStore interface {
	LoadOrCreate() ([]byte, error)
}

// masterKey is the 32-byte AES-256 key loaded at startup.
var masterKey []byte

// Init initializes the master encryption key from the platform-specific key store.
// Must be called before any Encrypt/Decrypt operations.
func Init() error {
	ks := newKeyStore()
	key, err := ks.LoadOrCreate()
	if err != nil {
		return fmt.Errorf("init keystore: %w", err)
	}
	masterKey = key
	return nil
}

// GetKey returns the loaded master encryption key, or nil if Init has not been called.
func GetKey() []byte {
	return masterKey
}
