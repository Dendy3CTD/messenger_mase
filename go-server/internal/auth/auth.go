package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// HashPassword returns the hex-encoded SHA-256 of the input.
// Matches the C++ server's Sha256Hex() implementation.
func HashPassword(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:])
}

// NewToken generates a 32-byte cryptographically random hex token.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// RandomDigits returns n random decimal digits as a string.
func RandomDigits(n int) string {
	digits := make([]byte, n)
	rand.Read(digits) //nolint:errcheck — rand.Read never fails on Linux
	result := make([]byte, n)
	for i, d := range digits {
		result[i] = '0' + (d % 10)
	}
	return string(result)
}

// NowSec returns Unix seconds.
func NowSec() int64 { return time.Now().Unix() }

// NowMilli returns Unix milliseconds.
func NowMilli() int64 { return time.Now().UnixMilli() }
