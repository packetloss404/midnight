// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// stderrLeakingRunner simulates a git binary that echoes a credential in its
// stderr and then fails, exercising the redaction path.
type stderrLeakingRunner struct {
	leak string
}

func (r stderrLeakingRunner) Run(_ context.Context, _ string, _ []string, _ io.Writer, stderr io.Writer) error {
	_, _ = io.WriteString(stderr, "fatal: could not read Authorization: Bearer "+r.leak+" for repo\n")
	return errors.New("exit status 128")
}

func TestGitCheckoutRedactsTokenInError(t *testing.T) {
	const token = "SUPERSECRET-git-token-value"
	planner := SourceCheckoutPlanner{
		Root:   t.TempDir(),
		Token:  token,
		Runner: stderrLeakingRunner{leak: token},
	}

	_, err := planner.Checkout(context.Background(), "svc-x", SourceRef{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
	})
	if err == nil {
		t.Fatal("Checkout() expected an error from the failing clone")
	}
	msg := err.Error()
	if strings.Contains(msg, token) {
		t.Fatalf("error text leaked the token: %q", msg)
	}
	if !strings.Contains(msg, redactedValue) {
		t.Fatalf("error text = %q, want it to contain %q", msg, redactedValue)
	}
}

func TestGitCheckoutRejectsEmbeddedCredentials(t *testing.T) {
	planner := SourceCheckoutPlanner{
		Root:   t.TempDir(),
		Runner: stderrLeakingRunner{leak: "unused"},
	}

	_, err := planner.Checkout(context.Background(), "svc-x", SourceRef{
		Repo: "https://user:supersecret@github.com/example/repo.git",
		Ref:  "main",
	})
	if !errors.Is(err, ErrEmbeddedCredentials) {
		t.Fatalf("Checkout() err = %v, want ErrEmbeddedCredentials", err)
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("rejection error leaked the embedded credential: %q", err.Error())
	}
}

func TestSecretValueRendersRedacted(t *testing.T) {
	secret := secretValue("top-secret")
	if got := secret.String(); got != redactedValue {
		t.Fatalf("secretValue.String() = %q, want %q", got, redactedValue)
	}
	if got := secret.raw(); got != "top-secret" {
		t.Fatalf("secretValue.raw() = %q, want the raw value", got)
	}
}

func TestGitAuthEnvInjectsHeaderNotURL(t *testing.T) {
	env := gitAuthEnv("tok123")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=http.extraHeader") {
		t.Fatalf("gitAuthEnv() = %v, want http.extraHeader injection", env)
	}
	if !strings.Contains(joined, "GIT_CONFIG_VALUE_0=Authorization: Bearer tok123") {
		t.Fatalf("gitAuthEnv() = %v, want bearer header value", env)
	}
	if empty := gitAuthEnv("  "); len(empty) != 1 || empty[0] != "GIT_TERMINAL_PROMPT=0" {
		t.Fatalf("gitAuthEnv(empty) = %v, want only the terminal-prompt guard", empty)
	}
}
