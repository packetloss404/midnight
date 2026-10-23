// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"bytes"
	"strings"
	"testing"
)

func TestSecretRedactingWriterCoversArbitraryWriteBoundaries(t *testing.T) {
	const secret = "s3cr3t-value-with-prefix"
	var output bytes.Buffer
	writer := NewSecretRedactingWriter(&output, []string{secret, "s3cr3t"})
	chunks := []string{
		"before s3",
		"cr3t-value",
		"-with-",
		"prefix after and s3cr",
		"3t twice",
	}
	for _, chunk := range chunks {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.Contains(got, secret) || strings.Contains(got, "s3cr3t") {
		t.Fatalf("streaming output leaked a secret: %q", got)
	}
	if strings.Count(got, redactedValue) != 2 {
		t.Fatalf("streaming output = %q, want two redactions", got)
	}
}

func TestBuildSecretsHashAndIDsAreDeterministicWithoutPlaintext(t *testing.T) {
	first := []BuildSecret{
		{ID: "token_b", Value: []byte("bravo-private")},
		{ID: "token_a", Value: []byte("alpha-private")},
	}
	second := []BuildSecret{
		{ID: "token_a", Value: []byte("alpha-private")},
		{ID: "token_b", Value: []byte("bravo-private")},
	}
	hash := BuildSecretsHash(first)
	if hash == "" || hash != BuildSecretsHash(second) {
		t.Fatalf("BuildSecretsHash() = %q, want stable non-empty digest", hash)
	}
	if strings.Contains(hash, "alpha-private") || strings.Contains(hash, "bravo-private") {
		t.Fatalf("BuildSecretsHash() leaked plaintext: %q", hash)
	}
	if got := strings.Join(BuildSecretIDs(first), ","); got != "token_a,token_b" {
		t.Fatalf("BuildSecretIDs() = %q", got)
	}
}

func TestValidateBuildSecretsRejectsDelimiterAndDuplicateIDs(t *testing.T) {
	for _, secrets := range [][]BuildSecret{
		{{ID: "TOKEN,env=HOST", Value: []byte("value")}},
		{{ID: "9TOKEN", Value: []byte("value")}},
		{{ID: "TOKEN", Value: []byte("one")}, {ID: "TOKEN", Value: []byte("two")}},
	} {
		if err := ValidateBuildSecrets(secrets); err == nil {
			t.Fatalf("ValidateBuildSecrets(%#v) succeeded", secrets)
		}
	}
}

func TestEnvironmentWithOverridesRemovesShadowedHostValue(t *testing.T) {
	got := environmentWithOverrides(
		[]string{"PATH=/bin", buildSecretEnvPrefix + "0=host-leak"},
		[]string{buildSecretEnvPrefix + "0=ephemeral"},
	)
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "host-leak") {
		t.Fatalf("environment retained shadowed value: %q", joined)
	}
	if !strings.Contains(joined, buildSecretEnvPrefix+"0=ephemeral") {
		t.Fatalf("environment did not include override: %q", joined)
	}
}
