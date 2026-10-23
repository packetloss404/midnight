package varrefs

import (
	"fmt"
	"strings"
)

// ErrInvalidSyntax is returned when the parser cannot interpret a
// template. Pos is the zero-indexed byte offset where the parser gave up
// and Reason carries a short human-readable description ("missing
// closing }}" etc.).
type ErrInvalidSyntax struct {
	Pos    int
	Reason string
}

// Error implements error.
func (e ErrInvalidSyntax) Error() string {
	return fmt.Sprintf("varrefs: invalid reference syntax at offset %d: %s", e.Pos, e.Reason)
}

// IsInvalidVariableReferenceSyntax is the marker that the
// VariableReferenceErrorKind classifier recognises. The method name is
// exported so consumers outside this package can match it via errors.As
// against the matching interface in this package
// (see ErrorClassifier).
func (ErrInvalidSyntax) IsInvalidVariableReferenceSyntax() {}

// ErrUnknownReference is returned during resolution when a reference
// points at a variable that does not exist in the visible scope.
//
// Service is the service the reference points at ("" for shared and same-
// service references) and Name is the variable name being looked up.
type ErrUnknownReference struct {
	Kind    ReferenceKind
	Service string
	Name    string
}

// IsUnknownVariableReference is the marker that the
// VariableReferenceErrorKind classifier recognises. The method name is
// exported so consumers outside this package can match it via errors.As.
func (ErrUnknownReference) IsUnknownVariableReference() {}

// Error implements error.
func (e ErrUnknownReference) Error() string {
	switch e.Kind {
	case RefShared:
		return fmt.Sprintf("varrefs: unknown reference ${{shared.%s}}", e.Name)
	case RefLocal:
		return fmt.Sprintf("varrefs: unknown reference ${{%s}}", e.Name)
	case RefBuiltin:
		return fmt.Sprintf("varrefs: unknown reference ${{this.%s}}", e.Name)
	default:
		return fmt.Sprintf("varrefs: unknown reference ${{%s.%s}}", e.Service, e.Name)
	}
}

// ErrReferenceCycle is returned during resolution when the reference
// graph forms a cycle. Path is the deterministic visit stack starting at
// the variable we asked to resolve and ending at the first repeated
// entry, encoded as "service.name" tokens.
type ErrReferenceCycle struct {
	Path []string
}

// IsVariableReferenceCycle is the marker that the
// VariableReferenceErrorKind classifier recognises. The method name is
// exported so consumers outside this package can match it via errors.As.
func (ErrReferenceCycle) IsVariableReferenceCycle() {}

// Error implements error.
func (e ErrReferenceCycle) Error() string {
	return "varrefs: reference cycle detected: " + strings.Join(e.Path, " -> ")
}
