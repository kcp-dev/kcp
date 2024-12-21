package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// GenerateSecret generates a random string of specified length.
func GenerateSecret(length int) (string, error) {
	// Allocate a byte slice for the random bytes
	bytes := make([]byte, length)

	// Read random bytes into the slice
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Encode the bytes to a base64 string to make it printable
	return base64.URLEncoding.EncodeToString(bytes)[:length], nil
}
