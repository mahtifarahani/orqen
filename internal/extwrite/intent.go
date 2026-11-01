package extwrite

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"orqen/internal/canon"
)

// IntentHash is sha256 of the canonical JSON of the exact payload a user approves.
func IntentHash(payload any) string {
	sum := sha256.Sum256([]byte(canon.JSON(payload)))
	return hex.EncodeToString(sum[:])
}

// Marker is the hidden HTML comment that lets Orqen recognize its own writes after a lost response.
func Marker(run, intentKey string) string {
	sum := sha256.Sum256([]byte(intentKey))
	return fmt.Sprintf("<!-- orqen:run=%s intent=%s -->", run, hex.EncodeToString(sum[:])[:8])
}
