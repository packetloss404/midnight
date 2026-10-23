// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package graphql

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorizeRejectsScopedMidnightRecoveryClasses(t *testing.T) {
	for _, token := range []string{
		"midn_user_recovery-secret",
		"midn_sa_recovery-secret",
		"midn_agent_recovery-secret",
		"midn_mcp_recovery-secret",
	} {
		t.Run(token, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/graphql/v2", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			if authorize(request, HandlerOptions{BearerToken: token}) {
				t.Fatalf("GraphQL accepted scoped token %q as recovery operator", token)
			}
		})
	}
}

func TestAuthorizePreservesLegacyAndAdminRecoveryCompatibility(t *testing.T) {
	for _, token := range []string{
		"legacy-operator-secret",
		"midn_admin_recovery-secret",
	} {
		t.Run(token, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/graphql/v2", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			if !authorize(request, HandlerOptions{BearerToken: token}) {
				t.Fatalf("GraphQL rejected compatible recovery token %q", token)
			}
		})
	}
}
