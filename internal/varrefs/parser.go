package varrefs

import (
	"strings"
)

// ReferenceKind classifies one of the four reference shapes supported
// by the template language.
type ReferenceKind string

const (
	// RefServiceVar is `${{ServiceName.VAR}}` — read another service.
	RefServiceVar ReferenceKind = "service_var"
	// RefShared is `${{shared.KEY}}` — read a project shared variable.
	RefShared ReferenceKind = "shared"
	// RefLocal is `${{VAR}}` — read another variable on the same service.
	RefLocal ReferenceKind = "local"
	// RefBuiltin is `${{this.*}}` — read an auto-injected built-in.
	RefBuiltin ReferenceKind = "builtin"
)

// sharedSentinel is the magic service name that turns into RefShared.
const sharedSentinel = "shared"

// builtinSentinel is the magic service name that turns into RefBuiltin.
const builtinSentinel = "this"

// Reference is one parsed reference token. The combination
// (Kind, Service, Name) uniquely identifies the resolution target.
//
// Position fields capture the byte range in the original template the
// reference spans (used by the resolver when reporting cycles tied to a
// particular reference).
type Reference struct {
	Kind    ReferenceKind
	Service string // empty for RefShared / RefLocal / RefBuiltin
	Name    string
	Start   int // byte offset of the opening `$`
	End     int // byte offset of the byte after the closing `}}`
}

// Segment is one piece of a tokenized template. Exactly one of Literal /
// Reference is populated.
type Segment struct {
	Literal string
	Ref     *Reference
}

// Template is the parsed form of a value string.
type Template struct {
	Raw      string
	Segments []Segment
	Refs     []Reference // flattened, in declaration order
}

// Parse tokenizes the supplied value into a Template. Invalid templates
// return ErrInvalidSyntax pointing at the offending offset.
//
// The grammar is intentionally permissive on the literal side — anything
// outside `${{...}}` is passed through unchanged.
func Parse(value string) (Template, error) {
	t := Template{Raw: value}
	i := 0
	literalStart := 0
	for i < len(value) {
		// Look for the next `${{`
		idx := strings.Index(value[i:], "${{")
		if idx < 0 {
			break
		}
		idx += i
		// Flush any literal accumulated before the marker.
		if idx > literalStart {
			t.Segments = append(t.Segments, Segment{Literal: value[literalStart:idx]})
		}
		// Find the matching `}}`. We do not nest, so the first one wins.
		closeRel := strings.Index(value[idx+3:], "}}")
		if closeRel < 0 {
			return Template{}, ErrInvalidSyntax{Pos: idx, Reason: "missing closing }}"}
		}
		close := idx + 3 + closeRel
		body := value[idx+3 : close]
		ref, err := parseReferenceBody(body, idx+3)
		if err != nil {
			return Template{}, err
		}
		ref.Start = idx
		ref.End = close + 2
		t.Segments = append(t.Segments, Segment{Ref: &ref})
		t.Refs = append(t.Refs, ref)
		i = ref.End
		literalStart = ref.End
	}
	if literalStart < len(value) {
		t.Segments = append(t.Segments, Segment{Literal: value[literalStart:]})
	}
	return t, nil
}

// parseReferenceBody parses the text inside a `${{ ... }}` expression.
// bodyStart is the byte offset of the first character of body in the
// original input (used for accurate ErrInvalidSyntax.Pos values).
func parseReferenceBody(body string, bodyStart int) (Reference, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return Reference{}, ErrInvalidSyntax{Pos: bodyStart, Reason: "empty reference expression"}
	}
	// We don't accept any whitespace within identifiers — TrimSpace handles
	// `${{  FOO  }}` deliberately, but disallows `${{ FOO BAR }}`.
	if strings.ContainsAny(trimmed, " \t\r\n") {
		return Reference{}, ErrInvalidSyntax{Pos: bodyStart, Reason: "whitespace inside reference"}
	}
	if idx := strings.IndexByte(trimmed, '.'); idx >= 0 {
		service := trimmed[:idx]
		name := trimmed[idx+1:]
		if !validIdent(service) {
			return Reference{}, ErrInvalidSyntax{Pos: bodyStart, Reason: "invalid service identifier"}
		}
		if name == "" {
			return Reference{}, ErrInvalidSyntax{Pos: bodyStart + idx + 1, Reason: "empty variable name"}
		}
		// Reject further dots — we only support one-segment service refs.
		if strings.ContainsRune(name, '.') {
			return Reference{}, ErrInvalidSyntax{Pos: bodyStart + idx + 1, Reason: "nested dots not allowed"}
		}
		if !validIdent(name) {
			return Reference{}, ErrInvalidSyntax{Pos: bodyStart + idx + 1, Reason: "invalid variable name"}
		}
		switch service {
		case sharedSentinel:
			return Reference{Kind: RefShared, Name: name}, nil
		case builtinSentinel:
			return Reference{Kind: RefBuiltin, Name: name}, nil
		default:
			return Reference{Kind: RefServiceVar, Service: service, Name: name}, nil
		}
	}
	// Single-segment reference -> RefLocal.
	if !validIdent(trimmed) {
		return Reference{}, ErrInvalidSyntax{Pos: bodyStart, Reason: "invalid identifier"}
	}
	return Reference{Kind: RefLocal, Name: trimmed}, nil
}

// validIdent reports whether the supplied string is a permitted identifier
// for service names and variable names. The rules are intentionally
// stricter than env-var lexing: ASCII alnum plus `_` and `-`. The first
// byte must not be a digit so we keep room for future numeric prefix
// reservations.
func validIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		case c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// IsLiteral reports whether the template contains no references.
func (t Template) IsLiteral() bool {
	for _, ref := range t.Refs {
		_ = ref
		return false
	}
	return true
}
