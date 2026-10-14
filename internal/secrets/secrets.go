// Package secrets encrypts variable values at rest and generates tokens.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

// Box seals values with AES-256-GCM using the master key in the data dir.
type Box struct {
	aead cipher.AEAD
}

// LoadOrCreate reads the 32-byte master key at path, creating it on first run.
// Losing this file makes every stored variable unreadable.
func LoadOrCreate(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		rand.Read(key)
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, fmt.Errorf("write master key: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("master key %s must be 32 bytes, got %d", path, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plain []byte) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	return b.aead.Seal(nonce, nonce, plain, nil)
}

func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("sealed value too short")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], nil)
}

// NewToken returns a random URL-safe token with the given prefix.
func NewToken(prefix string) string {
	b := make([]byte, 32)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// Hash is how tokens and session IDs are stored: never in plain text.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
