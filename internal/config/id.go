package config

import (
	"crypto/rand"
	"encoding/hex"
)

func NewID(prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return prefix + "-generated"
	}
	return prefix + "-" + hex.EncodeToString(b)
}
