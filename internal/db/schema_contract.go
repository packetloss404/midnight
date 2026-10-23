// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package db

import (
	"errors"
	"fmt"
	"strings"
)

// ErrSchemaContractViolation indicates that migration-derived schema evidence
// does not provide a table, column, or index required by a DB-backed adapter.
var ErrSchemaContractViolation = errors.New("schema contract violation")

type SchemaContract struct {
	Name   string
	Tables []TableContract
}

type TableContract struct {
	Name    string
	Columns []string
	Indexes []IndexContract
}

type IndexContract struct {
	Name    string
	Columns []string
}

// OperationStoreSchemaContract captures the minimum migration shape needed for
// a SQL-backed implementation of the API OperationStore interface.
func OperationStoreSchemaContract() SchemaContract {
	return SchemaContract{
		Name: "api-operation-store",
		Tables: []TableContract{
			{
				Name:    "operations",
				Columns: []string{"id", "kind", "state", "code", "reason", "created_at", "updated_at", "completed_at"},
				Indexes: []IndexContract{
					{Name: "idx_operations_state", Columns: []string{"state"}},
					{Name: "idx_operations_kind_state", Columns: []string{"kind", "state"}},
				},
			},
			{
				Name:    "operation_events",
				Columns: []string{"id", "operation_id", "seq", "state", "message", "created_at"},
				Indexes: []IndexContract{
					{Name: "idx_operation_events_operation_seq", Columns: []string{"operation_id", "seq"}},
				},
			},
		},
	}
}

func ValidateSchemaContract(evidence SchemaEvidence, contract SchemaContract) error {
	tables := make(map[string]TableEvidence, len(evidence.Tables))
	for _, table := range evidence.Tables {
		tables[table.Name] = table
	}
	indexes := make(map[string]IndexEvidence, len(evidence.Indexes))
	for _, index := range evidence.Indexes {
		indexes[index.Name] = index
	}

	for _, requiredTable := range contract.Tables {
		table, ok := tables[requiredTable.Name]
		if !ok {
			return fmt.Errorf("%w: %s missing table %q", ErrSchemaContractViolation, contract.Name, requiredTable.Name)
		}
		columns := make(map[string]struct{}, len(table.Columns))
		for _, column := range table.Columns {
			columns[column.Name] = struct{}{}
		}
		for _, requiredColumn := range requiredTable.Columns {
			if _, ok := columns[requiredColumn]; !ok {
				return fmt.Errorf("%w: %s table %q missing column %q", ErrSchemaContractViolation, contract.Name, requiredTable.Name, requiredColumn)
			}
		}
		for _, requiredIndex := range requiredTable.Indexes {
			index, ok := indexes[requiredIndex.Name]
			if !ok {
				return fmt.Errorf("%w: %s missing index %q", ErrSchemaContractViolation, contract.Name, requiredIndex.Name)
			}
			if index.Table != requiredTable.Name {
				return fmt.Errorf("%w: %s index %q is on table %q, want %q", ErrSchemaContractViolation, contract.Name, requiredIndex.Name, index.Table, requiredTable.Name)
			}
			if !sameIdentifiers(index.Columns, requiredIndex.Columns) {
				return fmt.Errorf("%w: %s index %q columns = %s, want %s", ErrSchemaContractViolation, contract.Name, requiredIndex.Name, strings.Join(index.Columns, ","), strings.Join(requiredIndex.Columns, ","))
			}
		}
	}
	return nil
}

func sameIdentifiers(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
