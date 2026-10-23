//go:build integration

package acme

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLegoIssuerStagingIntegration issues a wildcard certificate for the
// supplied apps-domain against Let's Encrypt staging and persists the
// bundle to disk.
//
// Prerequisites (set via environment):
//
//	MIDNIGHT_LE_STAGING        — set to "1" to acknowledge that this test
//	                             contacts the live LE staging API.
//	MIDNIGHT_LE_ACCOUNT_EMAIL  — operator email used to register the ACME account.
//	MIDNIGHT_LE_APPS_DOMAIN    — apex domain Lane 3 issues *.<domain> for.
//	MIDNIGHT_LE_DNS_PROVIDER   — one of "cloudflare", "route53", "manual".
//	                             Provider-specific credentials must already
//	                             be exported (e.g. CLOUDFLARE_DNS_API_TOKEN
//	                             or AWS_*).
//
// The test is intentionally NOT parallel and uses a long deadline because
// DNS-01 propagation on Cloudflare's free tier can take 30+ seconds.
func TestLegoIssuerStagingIntegration(t *testing.T) {
	if os.Getenv("MIDNIGHT_LE_STAGING") != "1" {
		t.Skip("set MIDNIGHT_LE_STAGING=1 plus MIDNIGHT_LE_* vars to run this integration test")
	}
	email := os.Getenv("MIDNIGHT_LE_ACCOUNT_EMAIL")
	domain := os.Getenv("MIDNIGHT_LE_APPS_DOMAIN")
	providerName := os.Getenv("MIDNIGHT_LE_DNS_PROVIDER")
	if email == "" || domain == "" || providerName == "" {
		t.Fatal("MIDNIGHT_LE_ACCOUNT_EMAIL, MIDNIGHT_LE_APPS_DOMAIN, and MIDNIGHT_LE_DNS_PROVIDER are required")
	}

	provider, err := LookupBuiltinDNSProvider(providerName)
	if err != nil {
		t.Fatalf("dns provider %q: %v", providerName, err)
	}

	stateDir := filepath.Join(t.TempDir(), "acme")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := LegoIssuerConfig{
		Environment:       LegoEnvironmentStaging,
		Email:             email,
		StateDir:          stateDir,
		Provider:          provider,
		ProviderID:        providerName,
		HTTPClientTimeout: 60 * time.Second,
	}
	issuer, err := NewLegoIssuer(cfg)
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := issuer.Issue(ctx, IssueRequest{Domains: []string{"*." + domain}, BundleCA: true})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if res.CertPath == "" || res.KeyPath == "" {
		t.Fatalf("missing artifact paths: %+v", res)
	}
	if data, err := os.ReadFile(res.CertPath); err != nil || len(data) == 0 {
		t.Fatalf("cert file not readable: %v", err)
	}
	if data, err := os.ReadFile(res.KeyPath); err != nil || len(data) == 0 {
		t.Fatalf("key file not readable: %v", err)
	}
	if res.NotAfter.Before(time.Now()) {
		t.Fatalf("issued cert already expired: %s", res.NotAfter)
	}
}
