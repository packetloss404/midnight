//go:build tools

// Package tools centralizes go.mod-tracked imports of build-time generators
// and scanners. The build tag keeps these dependencies out of the production
// binary set while letting `go mod tidy` see them as first-class graph nodes.
package tools

// Tool imports are added as implementation lanes wire generators and scanners.
