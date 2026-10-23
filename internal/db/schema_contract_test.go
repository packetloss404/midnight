// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package db

import (
	"errors"
	"testing"
)

func TestValidateSchemaContractRejectsMissingColumn(t *testing.T) {
	evidence := SchemaEvidence{
		Tables: []TableEvidence{{
			Name:    "operations",
			Columns: []TableColumn{{Name: "id"}, {Name: "state"}},
		}},
	}
	contract := SchemaContract{
		Name:   "test-store",
		Tables: []TableContract{{Name: "operations", Columns: []string{"id", "kind", "state"}}},
	}

	if err := ValidateSchemaContract(evidence, contract); !errors.Is(err, ErrSchemaContractViolation) {
		t.Fatalf("ValidateSchemaContract() error = %v, want ErrSchemaContractViolation", err)
	}
}
