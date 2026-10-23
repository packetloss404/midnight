package v1alpha

import _ "embed"

// MidnightYAML is the v1alpha OpenAPI contract served by the HTTP API.
//
//go:generate go run ../../../tools/api-contract-metadata --source midnight.yaml --manifest audiences.yaml
//go:generate go run ../../../tools/api-contract-views --source midnight.yaml --manifest audiences.yaml --output views --stable-output ../v1/midnight.yaml
//go:embed midnight.yaml
var MidnightYAML []byte
