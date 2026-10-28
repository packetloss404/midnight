package identity

import (
	"bytes"
	"errors"
	"testing"
)

func TestInvitationOnboardingVerifierRequiresCanonicalTokenAndSeparateCredential(t *testing.T) {
	service, err := NewInvitationService(InvitationServiceConfig{Store: NewMemoryAccessStore(), MACKey: bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", InvitationTokenPrefix + "short", "midn_user_" + string(bytes.Repeat([]byte{'a'}, 43))} {
		if _, err := service.PrepareOnboarding(token); !errors.Is(err, ErrInvalidInvitation) {
			t.Fatal("malformed bearer accepted")
		}
	}
	token, err := service.newToken()
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := service.PrepareOnboarding(token)
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier) != 32 || bytes.Contains(verifier, []byte(token)) || !bytes.Equal(verifier, service.tokenMAC(token)) {
		t.Fatal("onboarding verifier differs from invitation authority")
	}
}
