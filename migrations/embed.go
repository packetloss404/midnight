// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package migrations exposes the on-disk migration SQL + manifest as an
// embedded fs.FS. The Postgres migration runner consumes this filesystem
// directly so the same `migrations/` directory feeds the binary, the
// integration test, and the human-facing audit/release-evidence
// generators.
package migrations

import "embed"

//go:embed manifest.yaml
//go:embed *.up.sql
//go:embed *.down.sql
var FS embed.FS
