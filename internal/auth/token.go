// Package auth contains authentication contract helpers.
package auth

import (
	"errors"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
)

const TokenPrefix = "midn_"

var ErrInvalidAuthorization = errors.New("invalid authorization header")
var ErrOperatorRecoveryTokenClass = errors.New("operator recovery token uses a non-admin Midnight credential class")

type TokenClass string

const (
	TokenClassAdmin TokenClass = "admin"
	TokenClassUser  TokenClass = "user"
	TokenClassSA    TokenClass = "sa"
	TokenClassAgent TokenClass = "agent"
	TokenClassMCP   TokenClass = "mcp"
)

// TODO(schema-drift): keep this explicit public class allowlist mapped to the
// canonical API token schema once generated contract types land.
var allowedPublicTokenClasses = map[TokenClass]struct{}{
	TokenClassAdmin: {},
	TokenClassUser:  {},
	TokenClassSA:    {},
	TokenClassAgent: {},
	TokenClassMCP:   {},
}

type Token struct {
	Raw   string
	Class TokenClass
}

// OperatorRecoveryTokenClassError reports a recognized Midnight credential
// class that cannot be promoted into installation-wide recovery authority.
// It never includes raw bearer material.
type OperatorRecoveryTokenClassError struct {
	Class TokenClass
}

func (e *OperatorRecoveryTokenClassError) Error() string {
	return ErrOperatorRecoveryTokenClass.Error() + ": " + string(e.Class)
}

func (e *OperatorRecoveryTokenClassError) Unwrap() error {
	return ErrOperatorRecoveryTokenClass
}

type AuthError struct {
	Code failurecodes.Code
	Err  error
}

func (e *AuthError) Error() string { return e.Err.Error() }
func (e *AuthError) Unwrap() error { return e.Err }

func ParseBearerToken(header string) (Token, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], TokenPrefix) {
		return Token{}, &AuthError{Code: failurecodes.AuthTokenHeaderInvalid, Err: ErrInvalidAuthorization}
	}

	token := Token{Raw: parts[1]}
	remainder := strings.TrimPrefix(parts[1], TokenPrefix)
	if class, _, ok := strings.Cut(remainder, "_"); ok {
		token.Class = TokenClass(class)
	}
	if !IsAllowedPublicTokenClass(token.Class) {
		return Token{}, &AuthError{Code: failurecodes.AuthTokenHeaderInvalid, Err: ErrInvalidAuthorization}
	}
	return token, nil
}

func IsAllowedPublicTokenClass(class TokenClass) bool {
	_, ok := allowedPublicTokenClasses[class]
	return ok
}

// ValidateOperatorRecoveryToken preserves arbitrary legacy operator secrets
// while preventing any recognized, non-admin Midnight credential class from
// inheriting installation-wide recovery authority. Empty is valid here because
// production-required handling is a separate configuration decision.
func ValidateOperatorRecoveryToken(raw string) error {
	class, ok := publicTokenClass(strings.TrimSpace(raw))
	if ok && class != TokenClassAdmin {
		return &OperatorRecoveryTokenClassError{Class: class}
	}
	return nil
}

func publicTokenClass(raw string) (TokenClass, bool) {
	if !strings.HasPrefix(raw, TokenPrefix) {
		return "", false
	}
	remainder := strings.TrimPrefix(raw, TokenPrefix)
	class, _, ok := strings.Cut(remainder, "_")
	if !ok {
		return "", false
	}
	tokenClass := TokenClass(class)
	return tokenClass, IsAllowedPublicTokenClass(tokenClass)
}
