package storagecontrol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

// CredentialCipher encrypts profile credentials with AES-256-GCM. The output
// contains the random nonce followed by authenticated ciphertext.
type CredentialCipher struct {
	aead cipher.AEAD
}

func NewCredentialCipher(key []byte) (*CredentialCipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("credential encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create credential GCM: %w", err)
	}
	return &CredentialCipher{aead: aead}, nil
}

func (c *CredentialCipher) Encrypt(plaintext, additionalData []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate credential nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plaintext, additionalData), nil
}

func (c *CredentialCipher) Decrypt(ciphertext, additionalData []byte) ([]byte, error) {
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < nonceSize+c.aead.Overhead() {
		return nil, fmt.Errorf("decrypt credentials: invalid ciphertext")
	}
	plaintext, err := c.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], additionalData)
	if err != nil {
		return nil, fmt.Errorf("decrypt credentials: authentication failed")
	}
	return plaintext, nil
}
