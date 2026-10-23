package router

import (
	"crypto/sha256"
	"encoding/hex"
)

func RenderedConfigHash(rendered string) string {
	sum := sha256.Sum256([]byte(rendered))
	return hex.EncodeToString(sum[:])
}
