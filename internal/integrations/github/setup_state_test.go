package github

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSetupStateCodecSignsBindsAndExpires(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	codec, err := NewSetupStateCodec(
		bytes.Repeat([]byte{0x42}, 32),
		WithSetupClock(func() time.Time { return now }),
		WithSetupRandom(func(dst []byte) error {
			for index := range dst {
				dst[index] = byte(index + 1)
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	token, issued, err := codec.Issue("wrk_one", "usr_one", "ses_one")
	if err != nil {
		t.Fatal(err)
	}
	verified, err := codec.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Version != issued.Version || verified.Nonce != issued.Nonce ||
		verified.IssuedAt != issued.IssuedAt || verified.ExpiresAt != issued.ExpiresAt ||
		verified.WorkspaceID != "" || verified.UserID != "" || verified.SessionID != "" ||
		verified.ExpiresTime().Sub(verified.IssuedTime()) != DefaultSetupTTL {
		t.Fatalf("verified state = %#v, issued = %#v", verified, issued)
	}
	if len(verified.NonceHash()) != 32 {
		t.Fatalf("nonce hash length = %d", len(verified.NonceHash()))
	}
	payloadPart, _, _ := strings.Cut(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"wrk_one", "usr_one", "ses_one"} {
		if bytes.Contains(payload, []byte(identity)) {
			t.Fatalf("signed browser state exposed server-side binding %q: %s", identity, payload)
		}
	}

	tampered := []byte(token)
	tampered[4] ^= 1
	if _, err := codec.Verify(string(tampered)); !errors.Is(err, ErrSetupStateInvalid) {
		t.Fatalf("tampered state error = %v", err)
	}
	now = issued.ExpiresTime()
	if _, err := codec.Verify(token); !errors.Is(err, ErrSetupStateInvalid) {
		t.Fatalf("expired state error = %v", err)
	}
}

func TestAppConfigInstallationURLRequiresCompleteHTTPSSetup(t *testing.T) {
	_, key := mustGenTestKey(t)
	cfg := AppConfig{
		AppID: 7, PrivateKeyPEM: key,
		ClientID: "Iv1.client", ClientSecret: "secret",
		AppSlug: "midnight-app", CallbackURL: "https://midnight.example/api/v1alpha/github/setup/callback",
	}
	installationURL, err := cfg.InstallationURL("signed.state")
	if err != nil {
		t.Fatal(err)
	}
	if installationURL != "https://github.com/apps/midnight-app/installations/new?state=signed.state" {
		t.Fatalf("installation URL = %q", installationURL)
	}
	cfg.CallbackURL = "http://midnight.example/callback"
	if _, err := cfg.InstallationURL("signed.state"); !errors.Is(err, ErrAppConfigInvalid) {
		t.Fatalf("insecure callback error = %v", err)
	}
	cfg.CallbackURL = "https://midnight.example/wrong/callback"
	if _, err := cfg.InstallationURL("signed.state"); !errors.Is(err, ErrAppConfigInvalid) {
		t.Fatalf("wrong callback path error = %v", err)
	}
	cfg.CallbackURL = "https://midnight.example/api/v1alpha/github/setup/callback"
	cfg.AppSlug = " midnight-app"
	if _, err := cfg.InstallationURL("signed.state"); !errors.Is(err, ErrAppConfigInvalid) {
		t.Fatalf("non-canonical app slug error = %v", err)
	}
}
