package redaction

import (
	"errors"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestDefaultRedactorRedactsTokenEmailIPAndAWS(t *testing.T) {
	redactor := DefaultRedactor()

	cases := map[string]struct {
		input string
		match string
	}{
		"midn token":     {input: "session=midn_ABC12345xyz789 ok", match: "midn-token"},
		"email":          {input: "contact alice@example.test", match: "email"},
		"ipv4":           {input: "from 10.0.0.1", match: "ipv4"},
		"ipv6":           {input: "node 2001:db8:abcd:0012:0000:0000:0000:0001 down", match: "ipv6"},
		"aws access key": {input: "AKIAABCDEFGHIJKLMNOP claimed", match: "aws-access-key"},
	}

	for name, tc := range cases {
		got := redactor.RedactString(tc.input)
		want := "[REDACTED](" + tc.match + ")"
		if !strings.Contains(got, want) {
			t.Fatalf("%s: redacted = %q, want to contain %q", name, got, want)
		}
	}
}

func TestRedactMapPreservesSchema(t *testing.T) {
	redactor := DefaultRedactor()
	input := map[string]any{
		"plain":  "ok",
		"token":  "midn_ABCDEFGH",
		"nested": map[string]any{"email": "user@example.test"},
		"items":  []any{"AKIAABCDEFGHIJKLMNOP", 42},
		"number": 7,
	}
	out := redactor.RedactMap(input)

	for _, key := range []string{"plain", "token", "nested", "items", "number"} {
		if _, ok := out[key]; !ok {
			t.Fatalf("RedactMap dropped key %q", key)
		}
	}
	if got := out["plain"].(string); got != "ok" {
		t.Fatalf("plain mutated: %q", got)
	}
	if got := out["number"].(int); got != 7 {
		t.Fatalf("number mutated: %d", got)
	}
	nested := out["nested"].(map[string]any)
	if !strings.Contains(nested["email"].(string), "[REDACTED](email)") {
		t.Fatalf("nested email not redacted: %#v", nested)
	}
	items := out["items"].([]any)
	if !strings.Contains(items[0].(string), "[REDACTED](aws-access-key)") {
		t.Fatalf("items[0] not redacted: %#v", items)
	}
	if items[1].(int) != 42 {
		t.Fatalf("items[1] mutated: %#v", items)
	}
}

func TestBuilderRejectsInvalidRules(t *testing.T) {
	builder := NewBuilder()

	if err := builder.Add("", "x", ""); err == nil {
		t.Fatal("expected empty-name rejection")
	}
	if err := builder.Add("name", "", ""); err == nil {
		t.Fatal("expected empty-pattern rejection")
	}
	err := builder.Add("name", "(", "")
	if err == nil {
		t.Fatal("expected invalid-regexp rejection")
	}
	code, hint, ok := failurecodes.ReasonOf(err)
	if !ok || code != failurecodes.RedactionRuleInvalid || hint == "" {
		t.Fatalf("expected REDACTION_RULE_INVALID, got %q (hint %q ok %v)", code, hint, ok)
	}
	var failure *failurecodes.FailureError
	if !errors.As(err, &failure) {
		t.Fatal("expected *FailureError unwrap")
	}

	if err := builder.Add("dup", "a", ""); err != nil {
		t.Fatalf("first dup register: %v", err)
	}
	if err := builder.Add("dup", "b", ""); err == nil {
		t.Fatal("expected duplicate-name rejection")
	}
}

func TestRedactorGoldenVectors(t *testing.T) {
	redactor := DefaultRedactor()
	tests := []struct {
		name  string
		input string
		want  []string // substrings expected in output
	}{
		{
			name:  "multiple_in_one_line",
			input: "user alice@example.test from 10.0.0.1 token midn_ABCDEFGH AKIAABCDEFGHIJKLMNOP",
			want: []string{
				"[REDACTED](email)",
				"[REDACTED](ipv4)",
				"[REDACTED](midn-token)",
				"[REDACTED](aws-access-key)",
			},
		},
		{
			name:  "no_match",
			input: "plain message",
			want:  []string{"plain message"},
		},
	}
	for _, tc := range tests {
		got := redactor.RedactString(tc.input)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: missing %q in %q", tc.name, want, got)
			}
		}
	}
}

func TestRedactorRulesAreSorted(t *testing.T) {
	redactor := DefaultRedactor()
	rules := redactor.Rules()
	for i := 1; i < len(rules); i++ {
		if rules[i-1] > rules[i] {
			t.Fatalf("rules unsorted: %v", rules)
		}
	}
}
