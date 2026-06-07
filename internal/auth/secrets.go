// Package auth implements authentication and authorisation (SPEC §11.2,
// §13): API tokens (argon2id at rest), RBAC with nested roles and
// scopes, OIDC code+PKCE SSO, UI sessions, and the AES-256-GCM secret
// store backing $SECRET:name$ references.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// SecretBox seals/opens secret values with the master key (SPEC §13.2).
type SecretBox struct {
	aead cipher.AEAD
}

// LoadMasterKey reads the 32-byte hex key file (created by init).
func LoadMasterKey(path string) (*SecretBox, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secret key file: %w", err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("secret key file must hold 64 hex chars (32 bytes)")
	}
	return NewSecretBox(key)
}

// NewSecretBox builds a box from raw key bytes.
func NewSecretBox(key []byte) (*SecretBox, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretBox{aead: aead}, nil
}

// GenerateMasterKey writes a fresh key file (0600).
func GenerateMasterKey(path string) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600)
}

// Seal encrypts value; output = nonce ‖ ciphertext.
func (sb *SecretBox) Seal(value string) ([]byte, error) {
	nonce := make([]byte, sb.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return sb.aead.Seal(nonce, nonce, []byte(value), nil), nil
}

// Open decrypts a sealed blob.
func (sb *SecretBox) Open(blob []byte) (string, error) {
	ns := sb.aead.NonceSize()
	if len(blob) < ns {
		return "", fmt.Errorf("sealed blob too short")
	}
	plain, err := sb.aead.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("secret decryption failed (wrong master key?)")
	}
	return string(plain), nil
}
