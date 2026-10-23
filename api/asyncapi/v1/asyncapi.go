// Package v1 embeds the canonical Midnight event contract.
package v1

import _ "embed"

// MidnightYAML is the AsyncAPI v1 contract served by the HTTP API.
//
//go:embed midnight.yaml
var MidnightYAML []byte
