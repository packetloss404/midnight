package acme

import "testing"

func TestParseCAARecordsHandlesCommentsAndCriticalFlag(t *testing.T) {
	input := `; this is a comment
0 issue "letsencrypt.org"
128 issuewild "letsencrypt.org"
0 iodef "mailto:security@example.test"
`
	records, err := ParseCAARecords(input)
	if err != nil {
		t.Fatalf("ParseCAARecords: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("record count = %d, want 3", len(records))
	}
	if records[0].Tag != "issue" || records[0].Value != "letsencrypt.org" || records[0].Critical {
		t.Fatalf("unexpected first record: %#v", records[0])
	}
	if records[1].Tag != "issuewild" || !records[1].Critical {
		t.Fatalf("unexpected second record: %#v", records[1])
	}
	if records[2].Tag != "iodef" {
		t.Fatalf("iodef should pass through, got %#v", records[2])
	}
}

func TestParseCAARecordsRejectsMalformed(t *testing.T) {
	tests := map[string]string{
		"missing tag":      "0\n",
		"non-numeric flag": `x issue "letsencrypt.org"`,
		"flag overflow":    `999 issue "letsencrypt.org"`,
		"empty value":      "0 issue",
	}
	for name, input := range tests {
		if _, err := ParseCAARecords(input); err == nil {
			t.Fatalf("%s: expected parse error", name)
		}
	}
}

func TestClassifyCAARecordsPermits(t *testing.T) {
	records, err := ParseCAARecords(`0 issue "letsencrypt.org"`)
	if err != nil {
		t.Fatalf("ParseCAARecords: %v", err)
	}
	if got := ClassifyCAARecords(records, "letsencrypt.org"); got != CAAClassificationPermits {
		t.Fatalf("classification = %q, want %q", got, CAAClassificationPermits)
	}
}

func TestClassifyCAARecordsForbids(t *testing.T) {
	records, err := ParseCAARecords(`0 issue ";"`)
	if err != nil {
		t.Fatalf("ParseCAARecords: %v", err)
	}
	if got := ClassifyCAARecords(records, "letsencrypt.org"); got != CAAClassificationForbids {
		t.Fatalf("classification = %q, want %q", got, CAAClassificationForbids)
	}

	records, err = ParseCAARecords(`0 issue "digicert.com"`)
	if err != nil {
		t.Fatalf("ParseCAARecords: %v", err)
	}
	if got := ClassifyCAARecords(records, "letsencrypt.org"); got != CAAClassificationForbids {
		t.Fatalf("other-CA classification = %q, want %q", got, CAAClassificationForbids)
	}
}

func TestClassifyCAARecordsAmbiguousOnUnknownCritical(t *testing.T) {
	records, err := ParseCAARecords(`128 unknown "value"`)
	if err != nil {
		t.Fatalf("ParseCAARecords: %v", err)
	}
	if got := ClassifyCAARecords(records, "letsencrypt.org"); got != CAAClassificationAmbiguous {
		t.Fatalf("classification = %q, want %q", got, CAAClassificationAmbiguous)
	}
}

func TestClassifyCAARecordsEmptyMeansPermits(t *testing.T) {
	if got := ClassifyCAARecords(nil, "letsencrypt.org"); got != CAAClassificationPermits {
		t.Fatalf("classification = %q, want %q", got, CAAClassificationPermits)
	}
}

func TestClassifyCAARecordsAccountURIBinding(t *testing.T) {
	const account = "https://acme.example/acct/12345"
	tests := []struct {
		name   string
		record string
		opts   []ClassifyCAAOption
		want   CAAClassification
	}{
		{
			name:   "matching accounturi authorizes",
			record: `0 issue "letsencrypt.org; accounturi=https://acme.example/acct/12345"`,
			opts:   []ClassifyCAAOption{WithACMEAccountURI(account)},
			want:   CAAClassificationPermits,
		},
		{
			name:   "non-matching accounturi does not authorize",
			record: `0 issue "letsencrypt.org; accounturi=https://acme.example/acct/99999"`,
			opts:   []ClassifyCAAOption{WithACMEAccountURI(account)},
			want:   CAAClassificationForbids,
		},
		{
			name:   "accounturi present but no account configured does not authorize",
			record: `0 issue "letsencrypt.org; accounturi=https://acme.example/acct/12345"`,
			opts:   nil,
			want:   CAAClassificationForbids,
		},
		{
			name:   "no params behaves as before (regression) permits",
			record: `0 issue "letsencrypt.org"`,
			opts:   []ClassifyCAAOption{WithACMEAccountURI(account)},
			want:   CAAClassificationPermits,
		},
		{
			name:   "no params with no options (regression) permits",
			record: `0 issue "letsencrypt.org"`,
			opts:   nil,
			want:   CAAClassificationPermits,
		},
		{
			name:   "accounturi is case-sensitive",
			record: `0 issue "letsencrypt.org; accounturi=https://acme.example/acct/ABC"`,
			opts:   []ClassifyCAAOption{WithACMEAccountURI("https://acme.example/acct/abc")},
			want:   CAAClassificationForbids,
		},
		{
			name:   "host still matched case-insensitively with matching accounturi",
			record: `0 issue "LetsEncrypt.ORG; accounturi=https://acme.example/acct/12345"`,
			opts:   []ClassifyCAAOption{WithACMEAccountURI(account)},
			want:   CAAClassificationPermits,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			records, err := ParseCAARecords(tc.record)
			if err != nil {
				t.Fatalf("ParseCAARecords: %v", err)
			}
			if got := ClassifyCAARecords(records, "letsencrypt.org", tc.opts...); got != tc.want {
				t.Fatalf("classification = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyCAARecordsValidationMethodsBinding(t *testing.T) {
	tests := []struct {
		name   string
		record string
		opts   []ClassifyCAAOption
		want   CAAClassification
	}{
		{
			name:   "declared method in list authorizes",
			record: `0 issue "letsencrypt.org; validationmethods=dns-01,http-01"`,
			opts:   []ClassifyCAAOption{WithACMEValidationMethod("http-01")},
			want:   CAAClassificationPermits,
		},
		{
			name:   "declared method absent from list does not authorize",
			record: `0 issue "letsencrypt.org; validationmethods=dns-01"`,
			opts:   []ClassifyCAAOption{WithACMEValidationMethod("http-01")},
			want:   CAAClassificationForbids,
		},
		{
			name:   "no method declared leaves validationmethods unenforced (offline contract)",
			record: `0 issue "letsencrypt.org; validationmethods=dns-01"`,
			opts:   nil,
			want:   CAAClassificationPermits,
		},
		{
			name:   "accounturi and validationmethods both must hold",
			record: `0 issue "letsencrypt.org; accounturi=https://acme.example/a/1; validationmethods=dns-01"`,
			opts: []ClassifyCAAOption{
				WithACMEAccountURI("https://acme.example/a/1"),
				WithACMEValidationMethod("http-01"),
			},
			want: CAAClassificationForbids,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			records, err := ParseCAARecords(tc.record)
			if err != nil {
				t.Fatalf("ParseCAARecords: %v", err)
			}
			if got := ClassifyCAARecords(records, "letsencrypt.org", tc.opts...); got != tc.want {
				t.Fatalf("classification = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCAAPreflightForwardsAccountURIOption(t *testing.T) {
	raw := `0 issue "letsencrypt.org; accounturi=https://acme.example/acct/1"`
	allowed, classification, err := CAAPreflight("app.example.test", "letsencrypt.org", raw, WithACMEAccountURI("https://acme.example/acct/1"))
	if err != nil {
		t.Fatalf("CAAPreflight: %v", err)
	}
	if classification != CAAClassificationPermits {
		t.Fatalf("classification = %q, want %q", classification, CAAClassificationPermits)
	}
	if allowed.Status != DomainCAAAllowed {
		t.Fatalf("status = %q, want %q", allowed.Status, DomainCAAAllowed)
	}

	blocked, classification, err := CAAPreflight("app.example.test", "letsencrypt.org", raw, WithACMEAccountURI("https://acme.example/acct/OTHER"))
	if err != nil {
		t.Fatalf("CAAPreflight (blocked): %v", err)
	}
	if classification != CAAClassificationForbids {
		t.Fatalf("classification = %q, want %q", classification, CAAClassificationForbids)
	}
	if blocked.Status != DomainCAABlocked {
		t.Fatalf("status = %q, want %q", blocked.Status, DomainCAABlocked)
	}
}

func TestCAAPreflightWiresIntoDomainDiagnostics(t *testing.T) {
	result, classification, err := CAAPreflight("APP.example.test", "letsencrypt.org", `0 issue "letsencrypt.org"`)
	if err != nil {
		t.Fatalf("CAAPreflight: %v", err)
	}
	if classification != CAAClassificationPermits {
		t.Fatalf("classification = %q, want %q", classification, CAAClassificationPermits)
	}
	if result.Status != DomainCAAAllowed {
		t.Fatalf("status = %q, want %q", result.Status, DomainCAAAllowed)
	}
	if result.Domain != "app.example.test" {
		t.Fatalf("domain = %q, want lower-cased", result.Domain)
	}

	// Wired into MergeDomainDiagnostics: blocked CAA produces a fix-caa action.
	blocked, _, err := CAAPreflight("app.example.test", "letsencrypt.org", `0 issue ";"`)
	if err != nil {
		t.Fatalf("CAAPreflight (blocked): %v", err)
	}
	summary := MergeDomainDiagnostics(DomainDiagnosticInputs{CAA: blocked})
	if summary.CAA != DomainCAABlocked {
		t.Fatalf("merged CAA = %q, want %q", summary.CAA, DomainCAABlocked)
	}
	foundFix := false
	for _, action := range summary.Actions {
		if action == DomainDiagnosticActionFixCAA {
			foundFix = true
		}
	}
	if !foundFix {
		t.Fatalf("expected fix-caa action in %v", summary.Actions)
	}
}
