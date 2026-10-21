// Package secrets encrypts small secret values (game passwords, API keys) for storage.
//
// The key lives in a separate file (default DATA_DIR/secret.key, mode 0600). This keeps
// secrets out of the database file and its backups; it does not protect against someone
// who can read the whole data directory, where rendered game config files also hold
// game passwords in plaintext because the game servers need them.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

const prefix = "enc:v1:"

// Box seals and opens strings with AES-256-GCM.
type Box struct{ aead cipher.AEAD }

// Open loads the key at path, creating it (0600) when missing.
func Open(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key = make([]byte, 32)
		rand.Read(key)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create secret key: %w", err)
		}
		_, err = f.Write(key)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, fmt.Errorf("write secret key: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read secret key: %w", err)
	}
	return New(key)
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secret key must be 32 bytes")
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

// Seal encrypts plaintext into an "enc:v1:" string. Empty stays empty.
func (b *Box) Seal(plaintext string) string {
	if plaintext == "" {
		return ""
	}
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	return prefix + base64.RawStdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plaintext), nil))
}

// Open decrypts a Seal result. Values without the prefix are returned as-is, so data
// written before encryption was enabled keeps working.
func (b *Box) Open(s string) (string, error) {
	enc, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return s, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", errors.New("corrupt secret")
	}
	pt, err := b.aead.Open(nil, raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("secret cannot be decrypted (wrong secret.key?)")
	}
	return string(pt), nil
}
