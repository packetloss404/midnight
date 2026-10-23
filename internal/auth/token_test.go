package auth

import (
	"errors"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestParseBearerTokenAcceptsMidnightBearer(t *testing.T) {
	token, err := ParseBearerToken("Bearer midn_agent_abcdefghijklmnopqrstuvwxyz")
	if err != nil {
		t.Fatal(err)
	}
	if token.Raw != "midn_agent_abcdefghijklmnopqrstuvwxyz" || token.Class != TokenClassAgent {
		t.Fatalf("unexpected token: %#v", token)
	}
}

func TestParseBearerTokenAcceptsAllowedPublicClasses(t *testing.T) {
	for _, class := range []TokenClass{TokenClassAdmin, TokenClassUser, TokenClassSA, TokenClassAgent, TokenClassMCP} {
		if _, err := ParseBearerToken("Bearer midn_" + string(class) + "_abcdefghijklmnopqrstuvwxyz"); err != nil {
			t.Fatalf("class %q rejected: %v", class, err)
		}
	}
}

func TestParseBearerTokenRejectsUnsupportedTransport(t *testing.T) {
	_, err := ParseBearerToken("Basic midn_agent_abcdefghijklmnopqrstuvwxyz")
	if err == nil {
		t.Fatal("expected error")
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %T", err)
	}
	if authErr.Code != failurecodes.AuthTokenHeaderInvalid {
		t.Fatalf("code = %q, want %q", authErr.Code, failurecodes.AuthTokenHeaderInvalid)
	}
}

func TestParseBearerTokenRejectsNonMidnightToken(t *testing.T) {
	if _, err := ParseBearerToken("Bearer other_token"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseBearerTokenRejectsUnknownClass(t *testing.T) {
	_, err := ParseBearerToken("Bearer midn_root_abcdefghijklmnopqrstuvwxyz")
	if err == nil {
		t.Fatal("expected error")
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %T", err)
	}
	if authErr.Code != failurecodes.AuthTokenHeaderInvalid {
		t.Fatalf("code = %q, want %q", authErr.Code, failurecodes.AuthTokenHeaderInvalid)
	}
}

func TestParseBearerTokenRejectsMissingClass(t *testing.T) {
	if _, err := ParseBearerToken("Bearer midn_abcdefghijklmnopqrstuvwxyz"); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateOperatorRecoveryTokenRejectsRecognizedNonAdminClasses(t *testing.T) {
	for _, token := range []string{
		"midn_user_recovery-secret",
		"midn_sa_recovery-secret",
		"midn_agent_recovery-secret",
		"midn_mcp_recovery-secret",
	} {
		t.Run(token, func(t *testing.T) {
			err := ValidateOperatorRecoveryToken(token)
			if !errors.Is(err, ErrOperatorRecoveryTokenClass) {
				t.Fatalf("ValidateOperatorRecoveryToken(%q) = %v", token, err)
			}
			if strings.Contains(err.Error(), "recovery-secret") {
				t.Fatalf("validation error leaked bearer material: %v", err)
			}
		})
	}
}

func TestValidateOperatorRecoveryTokenPreservesLegacyAndAdminCompatibility(t *testing.T) {
	for _, token := range []string{
		"",
		"legacy-operator-secret",
		"midn_admin_recovery-secret",
		"midn_unknown_legacy-secret",
	} {
		t.Run(token, func(t *testing.T) {
			if err := ValidateOperatorRecoveryToken(token); err != nil {
				t.Fatalf("ValidateOperatorRecoveryToken(%q) = %v", token, err)
			}
		})
	}
}
