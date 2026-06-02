package crypto

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// fileKeyStore stores the master encryption key in ~/.stackr/.encryption-key
// with 0600 permissions. Used as primary store on non-Darwin and as fallback
// on Darwin when Keychain is unavailable.
type fileKeyStore struct{}

// keyFilePath returns the path to the encryption key file: ~/.stackr/.encryption-key
func keyFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".stackr", ".encryption-key")
}

func (k *fileKeyStore) LoadOrCreate() ([]byte, error) {
	path := keyFilePath()

	data, err := os.ReadFile(path)
	if err == nil {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat key file: %w", err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			return nil, fmt.Errorf("key file has insecure permissions %04o, expected 0600 — run: chmod 600 %s", perm, path)
		}
		return data, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read key file: %w", err)
	}

	key := make([]byte, keyLength)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return k.saveKey(key)
}

// saveKey persists an already-generated master key to the encrypted key file.
func (k *fileKeyStore) saveKey(key []byte) ([]byte, error) {
	path := keyFilePath()

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}

	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}

	return key, nil
}
