package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/midnight/midnight/internal/api"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/webhook"
)

func writeServerGitHubKey(t *testing.T) string {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "github-app.pem")
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return keyPath
}

func TestConfigureGitHubAppWiresDiscoveryAndWebhookEndpoint(t *testing.T) {
	t.Setenv("MIDNIGHT_GITHUB_APP_ID", "7")
	t.Setenv("MIDNIGHT_GITHUB_PRIVATE_KEY_PATH", writeServerGitHubKey(t))
	t.Setenv("MIDNIGHT_GITHUB_WEBHOOK_SECRET", "webhook-secret")
	t.Setenv("MIDNIGHT_GITHUB_WEBHOOK_ENDPOINT_ID", "operator-app")

	tenantCoordinator := &api.PostgresTenantGitHubSourceLinkCoordinator{}
	recoveryCoordinator := &api.PostgresGitHubGrantRecoveryCoordinator{}
	setupCoordinator := &api.PostgresGitHubSetupCoordinator{}
	stack, err := configureGitHubApp(api.DataStack{
		TenantGitHubSourceLinks: tenantCoordinator,
		GitHubGrantRecovery:     recoveryCoordinator,
		GitHubSetup:             setupCoordinator,
	})
	if err != nil {
		t.Fatalf("configureGitHubApp() error = %v", err)
	}
	if stack.GitHubApp == nil || stack.GitHubAppConfig == nil {
		t.Fatal("GitHub App discovery client was not wired")
	}
	appClient, ok := stack.GitHubApp.(*githubapp.AppClient)
	if !ok {
		t.Fatalf("GitHubApp = %T, want *github.AppClient", stack.GitHubApp)
	}
	tenantVerifier, tenantOK := tenantCoordinator.Verifier.(*githubapp.AppClient)
	recoveryVerifier, recoveryOK := recoveryCoordinator.Verifier.(*githubapp.AppClient)
	if !tenantOK || !recoveryOK ||
		tenantVerifier != appClient ||
		recoveryVerifier != appClient ||
		setupCoordinator.Verifier != appClient {
		t.Fatal("GitHub App exact-source verifiers were not wired to product and recovery coordinators")
	}
	endpoint, err := stack.WebhookEndpoints.Resolve(context.Background(), "operator-app")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if endpoint.Provider != webhook.ProviderGitHub || len(endpoint.Keys) != 1 ||
		string(endpoint.Keys[0].Material) != "webhook-secret" {
		t.Fatalf("endpoint = %#v", endpoint)
	}
}

func TestConfigureGitHubAppWiresCompleteSetupAndRejectsPartialSetup(t *testing.T) {
	t.Setenv("MIDNIGHT_GITHUB_APP_ID", "7")
	t.Setenv("MIDNIGHT_GITHUB_PRIVATE_KEY_PATH", writeServerGitHubKey(t))
	t.Setenv("MIDNIGHT_GITHUB_WEBHOOK_SECRET", "webhook-secret")
	t.Setenv("MIDNIGHT_GITHUB_CLIENT_ID", "Iv1.client")
	t.Setenv("MIDNIGHT_GITHUB_CLIENT_SECRET", "client-secret")
	t.Setenv("MIDNIGHT_GITHUB_APP_SLUG", "midnight-app")
	t.Setenv("MIDNIGHT_GITHUB_CALLBACK_URL", "https://midnight.example/api/v1alpha/github/setup/callback")

	setup := &api.PostgresGitHubSetupCoordinator{}
	stack, err := configureGitHubApp(api.DataStack{GitHubSetup: setup})
	if err != nil {
		t.Fatalf("configure complete setup: %v", err)
	}
	setupClient, setupOK := setup.Verifier.(*githubapp.AppClient)
	appClient, appOK := stack.GitHubApp.(*githubapp.AppClient)
	if !setupOK || !appOK || setupClient != appClient || setup.Config.AppSlug != "midnight-app" {
		t.Fatalf("setup coordinator was not wired: %#v", setup.Config)
	}

	t.Setenv("MIDNIGHT_GITHUB_CLIENT_SECRET", "")
	if _, err := configureGitHubApp(api.DataStack{GitHubSetup: setup}); err == nil {
		t.Fatal("partial setup configuration was accepted")
	}
}

func TestConfigureGitHubAppRejectsPartialCredentials(t *testing.T) {
	t.Setenv("MIDNIGHT_GITHUB_APP_ID", "7")
	t.Setenv("MIDNIGHT_GITHUB_PRIVATE_KEY_PATH", "")
	t.Setenv("MIDNIGHT_GITHUB_WEBHOOK_SECRET", "")

	if _, err := configureGitHubApp(api.DataStack{}); err == nil {
		t.Fatal("configureGitHubApp() error = nil, want partial config refusal")
	}
}

func TestConfigureGitHubAppIsOptionalWhenFullyUnset(t *testing.T) {
	t.Setenv("MIDNIGHT_GITHUB_APP_ID", "")
	t.Setenv("MIDNIGHT_GITHUB_PRIVATE_KEY_PATH", "")
	t.Setenv("MIDNIGHT_GITHUB_WEBHOOK_SECRET", "")

	stack, err := configureGitHubApp(api.DataStack{})
	if err != nil {
		t.Fatalf("configureGitHubApp() error = %v", err)
	}
	if stack.GitHubApp != nil || stack.WebhookEndpoints != nil {
		t.Fatalf("optional GitHub configuration mutated stack: %#v", stack)
	}
}
