package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestOperationListPageSQLUsesFixedIndexablePredicates(t *testing.T) {
	running := orailtypes.OperationRunning
	for _, test := range []struct {
		name       string
		filter     OperationListFilter
		predicate  string
		wantArgs   int
		limitParam int
	}{
		{name: "none", predicate: "", wantArgs: 1, limitParam: 1},
		{name: "kind", filter: OperationListFilter{Kind: "deploy"}, predicate: "WHERE kind = $1", wantArgs: 2, limitParam: 2},
		{name: "state", filter: OperationListFilter{Status: &running}, predicate: "WHERE state = $1", wantArgs: 2, limitParam: 2},
		{name: "both", filter: OperationListFilter{Kind: "deploy", Status: &running}, predicate: "WHERE kind = $1 AND state = $2", wantArgs: 3, limitParam: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			query, args := operationListPageSQL(test.filter, nil, 25)
			if test.predicate != "" && !strings.Contains(query, test.predicate) {
				t.Fatalf("query = %q, want predicate %q", query, test.predicate)
			}
			if strings.Contains(query, " OR ") {
				t.Fatalf("query contains optional-OR predicate: %q", query)
			}
			if !strings.Contains(query, fmt.Sprintf("LIMIT $%d", test.limitParam)) || len(args) != test.wantArgs || args[len(args)-1] != 26 {
				t.Fatalf("query/args = %q %#v", query, args)
			}
		})
	}
}

func TestOperationListPageSQLAddsExclusiveKeysetAfterFilters(t *testing.T) {
	running := orailtypes.OperationRunning
	after := &OperationListCursor{
		CreatedAt: time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC),
		ID:        "op_after",
	}
	query, args := operationListPageSQL(OperationListFilter{Kind: "deploy", Status: &running}, after, 10)
	if !strings.Contains(query, "kind = $1 AND state = $2 AND (created_at, id) > ($3, $4)") ||
		!strings.Contains(query, "LIMIT $5") {
		t.Fatalf("query = %q", query)
	}
	if len(args) != 5 || args[2] != after.CreatedAt || args[3] != string(after.ID) || args[4] != 11 {
		t.Fatalf("args = %#v", args)
	}
}
