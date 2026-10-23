// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/midnight/midnight/internal/idempotency"
)

// operationListCursorBinding prevents a cursor from being replayed under a
// different principal, tenant, filter, page size, collection, or ordering.
// Only server-derived identity namespaces are included; bearer material is
// never placed in a cursor payload or limiter key.
func operationListCursorBinding(ctx context.Context, filter OperationListFilter, limit int) string {
	status := ""
	if filter.Status != nil {
		status = string(*filter.Status)
	}
	raw := fmt.Sprintf(
		"collection=operations\x00order=created_at.asc,id.asc\x00actor=%s\x00tenant=%s\x00kind=%s\x00status=%s\x00limit=%d",
		idempotency.ActorFromContext(ctx),
		idempotency.TenantFromContext(ctx),
		filter.Kind,
		status,
		limit,
	)
	digest := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
