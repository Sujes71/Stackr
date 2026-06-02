//go:build !darwin

package crypto

func newKeyStore() KeyStore {
	return &fileKeyStore{}
}
