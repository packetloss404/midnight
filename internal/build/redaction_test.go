// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"strings"
	"testing"
)

func TestRedactorRedactsAssignmentsAndFlags(t *testing.T) {
	redactor := NewRedactor(nil)

	cases := []struct {
		name    string
		input   string
		leaked  string
		wantHas string
	}{
		{
			name:    "secret assignment",
			input:   "build progress: exporting SECRET=hunter2packed value",
			leaked:  "hunter2",
			wantHas: "SECRET=<redacted>",
		},
		{
			name:    "prefixed token assignment",
			input:   "GITHUB_TOKEN=ghp_abcdef123456 was printed",
			leaked:  "ghp_abcdef123456",
			wantHas: "GITHUB_TOKEN=<redacted>",
		},
		{
			name:    "build-arg flag",
			input:   "buildctl ... --build-arg TOKEN=s3cr3t-value --opt foo",
			leaked:  "s3cr3t-value",
			wantHas: "--build-arg TOKEN=<redacted>",
		},
		{
			name:    "secret mount flag",
			input:   "buildctl ... --secret id=npm,src=/run/secrets/npmrc next",
			leaked:  "/run/secrets/npmrc",
			wantHas: "src=<redacted>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactor.RedactText(tc.input)
			if strings.Contains(got, tc.leaked) {
				t.Fatalf("RedactText() = %q still leaks %q", got, tc.leaked)
			}
			if !strings.Contains(got, tc.wantHas) {
				t.Fatalf("RedactText() = %q, want it to contain %q", got, tc.wantHas)
			}
		})
	}
}

func TestRedactorRedactsKnownValuesLongestFirst(t *testing.T) {
	redactor := NewRedactor([]string{"abc", "abc123def"})
	got := redactor.RedactText("value is abc123def and abc")
	if strings.Contains(got, "abc123def") {
		t.Fatalf("RedactText() = %q still leaks the longer secret", got)
	}
	if strings.Count(got, redactedValue) != 2 {
		t.Fatalf("RedactText() = %q, want two redactions", got)
	}
}
