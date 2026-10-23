package varrefs

import (
	"errors"
	"testing"
)

func TestParseLiteralPassthrough(t *testing.T) {
	tmpl, err := Parse("postgres://user:pass@host/db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tmpl.IsLiteral() {
		t.Fatalf("expected literal template; got refs=%d", len(tmpl.Refs))
	}
	if len(tmpl.Segments) != 1 || tmpl.Segments[0].Literal != "postgres://user:pass@host/db" {
		t.Fatalf("segments = %#v", tmpl.Segments)
	}
}

func TestParseServiceVar(t *testing.T) {
	tmpl, err := Parse("redis://${{cache.HOST}}:6379")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tmpl.Refs) != 1 {
		t.Fatalf("refs = %d, want 1", len(tmpl.Refs))
	}
	ref := tmpl.Refs[0]
	if ref.Kind != RefServiceVar || ref.Service != "cache" || ref.Name != "HOST" {
		t.Fatalf("ref = %#v", ref)
	}
	if len(tmpl.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(tmpl.Segments))
	}
	if tmpl.Segments[0].Literal != "redis://" || tmpl.Segments[2].Literal != ":6379" {
		t.Fatalf("segments boundaries = %#v", tmpl.Segments)
	}
}

func TestParseSharedAndLocal(t *testing.T) {
	tmpl, err := Parse("${{shared.SENTRY_DSN}}/${{LOG_PREFIX}}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tmpl.Refs) != 2 {
		t.Fatalf("refs = %d, want 2", len(tmpl.Refs))
	}
	if tmpl.Refs[0].Kind != RefShared || tmpl.Refs[0].Name != "SENTRY_DSN" {
		t.Fatalf("ref[0] = %#v", tmpl.Refs[0])
	}
	if tmpl.Refs[1].Kind != RefLocal || tmpl.Refs[1].Name != "LOG_PREFIX" {
		t.Fatalf("ref[1] = %#v", tmpl.Refs[1])
	}
}

func TestParseBuiltin(t *testing.T) {
	tmpl, err := Parse("https://${{this.MIDNIGHT_PUBLIC_DOMAIN}}/api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tmpl.Refs) != 1 || tmpl.Refs[0].Kind != RefBuiltin || tmpl.Refs[0].Name != "MIDNIGHT_PUBLIC_DOMAIN" {
		t.Fatalf("refs = %#v", tmpl.Refs)
	}
}

func TestParseInvalidSyntax(t *testing.T) {
	cases := []struct {
		name  string
		input string
		pos   int
	}{
		{"missing close", "prefix ${{NAME", 7},
		{"empty body", "${{}}", 3},
		{"whitespace inside", "${{FOO BAR}}", 3},
		{"nested dots", "${{svc.A.B}}", 7},
		{"leading digit", "${{1FOO}}", 3},
		{"empty service", "${{.NAME}}", 3},
		{"empty name", "${{svc.}}", 7},
		{"bad char", "${{svc@bad}}", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.input)
			var syn ErrInvalidSyntax
			if !errors.As(err, &syn) {
				t.Fatalf("err = %v, want ErrInvalidSyntax", err)
			}
			if syn.Pos != tc.pos {
				t.Fatalf("pos = %d, want %d (err=%v)", syn.Pos, tc.pos, syn)
			}
		})
	}
}

func TestParseMultipleAdjacentRefs(t *testing.T) {
	tmpl, err := Parse("${{A}}${{B}}${{C}}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tmpl.Refs) != 3 {
		t.Fatalf("refs = %d, want 3", len(tmpl.Refs))
	}
	for i, name := range []string{"A", "B", "C"} {
		if tmpl.Refs[i].Name != name || tmpl.Refs[i].Kind != RefLocal {
			t.Fatalf("ref[%d] = %#v", i, tmpl.Refs[i])
		}
	}
}
