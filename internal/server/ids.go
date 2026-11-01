package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"github.com/oklog/ulid/v2"
	"strings"
)

func newID(prefix string) string { return prefix + "_" + ulid.Make().String() }
func validID(prefix, value string) bool {
	rest, ok := strings.CutPrefix(value, prefix+"_")
	if !ok || len(rest) != 26 {
		return false
	}
	for _, c := range rest {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", c) {
			return false
		}
	}
	return true
}
func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func sha256Hex(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
