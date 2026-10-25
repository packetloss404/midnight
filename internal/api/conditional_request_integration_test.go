//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import "encoding/json"

// Lifecycle integration fixtures express CAS versions in their request bodies.
// Send the matching strong validator required by the public HTTP contract;
// explicit missing/malformed-header cases remain in conditional_request_test.go.
func integrationVersionHeaders(body string, headers map[string]string) map[string]string {
	var request struct {
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if json.Unmarshal([]byte(body), &request) == nil && request.ExpectedVersion != nil {
		headers["If-Match"] = strongVersionETag(*request.ExpectedVersion)
	}
	return headers
}
