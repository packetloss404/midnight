// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package serviceconfig

import (
	"context"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/db/pg"
)

func TestPostgresConfigTransactionSeamsRequireTransaction(t *testing.T) {
	store := NewStore(&pg.Pool{})

	if _, err := store.GetInTx(
		context.Background(),
		nil,
		"prj_1",
		"env_1",
		"svc_1",
	); err == nil || !strings.Contains(err.Error(), "transaction is required") {
		t.Fatalf("GetInTx nil transaction error = %v", err)
	}

	if _, err := store.PutInTx(
		context.Background(),
		nil,
		Default("prj_1", "svc_1", "env_1"),
		0,
	); err == nil || !strings.Contains(err.Error(), "transaction is required") {
		t.Fatalf("PutInTx nil transaction error = %v", err)
	}
}

func TestVariableRefsSchemaDistinguishesLegacyAliaslessSnapshots(t *testing.T) {
	legacy := []VariableVersionRef{{
		ID: "var_one", ServiceID: "svc_one", Name: "TOKEN", Version: 1,
	}}
	if got := variableRefsSchema(legacy); got != VariableRefsSchemaV1 {
		t.Fatalf("legacy schema=%q", got)
	}
	legacy[0].ServiceSlug = "api"
	legacy[0].ServiceDisplayName = "API"
	if got := variableRefsSchema(legacy); got != VariableRefsSchema {
		t.Fatalf("alias-pinned schema=%q", got)
	}
}
