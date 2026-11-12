// Package wallet encrypts deposit vault seeds at rest. Secrets exist in the
// database only encrypted with a master key supplied via environment
// (AES-256-GCM); the master key is never persisted.
package wallet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// EncryptSecret seals the secret with AES-256-GCM under the 32-byte master
// key. Output: nonce (12) || ciphertext.
func EncryptSecret(secret, masterKey []byte) ([]byte, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("master key must be 32 bytes")
	}
	aead, err := newAEAD(masterKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, secret, nil), nil
}

// DecryptSecret opens an encrypted secret.
func DecryptSecret(blob, masterKey []byte) ([]byte, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("master key must be 32 bytes")
	}
	aead, err := newAEAD(masterKey)
	if err != nil {
		return nil, err
	}
	if len(blob) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("encrypted secret too short")
	}
	nonce, ct := blob[:aead.NonceSize()], blob[aead.NonceSize():]
	return aead.Open(nil, nonce, ct, nil)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
