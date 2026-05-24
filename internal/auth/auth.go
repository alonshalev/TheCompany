// Package auth handles API key creation, hashing, and validation.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	apiKeyPrefix = "sym_"
	apiKeyLength = 32 // bytes of random data
)

// GenerateAPIKey creates a new random API key.
// Returns the full plaintext key (shown once) and the hash+prefix for storage.
func GenerateAPIKey() (plaintext, hash, prefix string, err error) {
	raw := make([]byte, apiKeyLength)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("generate api key random bytes: %w", err)
	}

	encoded := hex.EncodeToString(raw)
	plaintext = apiKeyPrefix + encoded

	h := sha256.Sum256([]byte(plaintext))
	hash = hex.EncodeToString(h[:])

	// Prefix for display: "sym_" + first 8 chars
	prefix = plaintext[:len(apiKeyPrefix)+8]

	return plaintext, hash, prefix, nil
}

// HashAPIKey returns the SHA-256 hex hash of a plaintext API key.
func HashAPIKey(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}

// IsValidAPIKeyFormat checks that a key has the expected prefix and length.
func IsValidAPIKeyFormat(key string) bool {
	return strings.HasPrefix(key, apiKeyPrefix) && len(key) == len(apiKeyPrefix)+apiKeyLength*2
}
