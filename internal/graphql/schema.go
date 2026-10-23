// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package graphql

import (
	_ "embed"
	"fmt"
	"sync"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// SchemaSDL is the SDL text consumed by both the executor (for query
// validation) and the GraphiQL playground (for completion/lint).
//
//go:embed schema.graphql
var SchemaSDL string

var (
	schemaOnce  sync.Once
	schemaCache *ast.Schema
	schemaErr   error
)

// LoadSchema parses and validates the SDL. It is memoized; subsequent
// callers receive the cached *ast.Schema.
func LoadSchema() (*ast.Schema, error) {
	schemaOnce.Do(func() {
		schema, err := gqlparser.LoadSchema(&ast.Source{
			Name:  "schemas/graphql/schema.graphql",
			Input: SchemaSDL,
		})
		if err != nil {
			schemaErr = fmt.Errorf("graphql: parse schema: %w", err)
			return
		}
		schemaCache = schema
	})
	return schemaCache, schemaErr
}
