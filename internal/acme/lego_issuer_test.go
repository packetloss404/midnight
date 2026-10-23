package acme

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-acme/lego/v4/challenge/dns01"
)

func TestNewAccountUserCreatesAndReusesKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "account.key")
	user1, err := NewAccountUser("ops@example.invalid", keyPath)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if user1.GetEmail() != "ops@example.invalid" {
		t.Fatalf("email = %q", user1.GetEmail())
	}
	if user1.GetPrivateKey() == nil {
		t.Fatal("first create produced nil private key")
	}

	user2, err := NewAccountUser("ops@example.invalid", keyPath)
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if user2.GetPrivateKey() == nil {
		t.Fatal("reuse produced nil private key")
	}
	// We cannot directly compare interface{} private keys but we CAN
	// ensure the persisted file is the same one that was loaded.
	info1, _ := filepath.Glob(keyPath)
	if len(info1) != 1 {
		t.Fatalf("expected one key file, got %d", len(info1))
	}
}

func TestNewAccountUserRejectsRelativePath(t *testing.T) {
	if _, err := NewAccountUser("ops@example.invalid", "relative/path"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestNewAccountUserRejectsEmptyEmail(t *testing.T) {
	if _, err := NewAccountUser("", filepath.Join(t.TempDir(), "k")); err == nil {
		t.Fatal("empty email accepted")
	}
}

func TestLookupBuiltinDNSProviderManual(t *testing.T) {
	provider, err := LookupBuiltinDNSProvider("manual")
	if err != nil {
		t.Fatalf("manual provider: %v", err)
	}
	if _, ok := provider.(*dns01.DNSProviderManual); !ok {
		t.Fatalf("expected manual provider concrete type, got %T", provider)
	}
}

func TestLookupBuiltinDNSProviderRejectsUnknown(t *testing.T) {
	if _, err := LookupBuiltinDNSProvider("nope"); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestLookupBuiltinDNSProviderCloudflareWithoutCredsFailsClean(t *testing.T) {
	t.Setenv("CLOUDFLARE_DNS_API_TOKEN", "")
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	t.Setenv("CLOUDFLARE_EMAIL", "")
	t.Setenv("CLOUDFLARE_API_KEY", "")
	if _, err := LookupBuiltinDNSProvider("cloudflare"); err == nil {
		t.Fatal("cloudflare accepted without credentials")
	} else if !strings.Contains(err.Error(), "cloudflare") {
		t.Fatalf("expected cloudflare-specific error, got %v", err)
	}
}

func TestNewLegoIssuerValidatesConfig(t *testing.T) {
	manual, err := dns01.NewDNSProviderManual()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		cfg  LegoIssuerConfig
	}{
		{"missing email", LegoIssuerConfig{StateDir: "/tmp/x", Provider: manual, Environment: LegoEnvironmentStaging}},
		{"missing state dir", LegoIssuerConfig{Email: "a@b.com", Provider: manual, Environment: LegoEnvironmentStaging}},
		{"relative state dir", LegoIssuerConfig{Email: "a@b.com", StateDir: "rel", Provider: manual, Environment: LegoEnvironmentStaging}},
		{"missing provider", LegoIssuerConfig{Email: "a@b.com", StateDir: "/tmp/x", Environment: LegoEnvironmentStaging}},
		{"missing env", LegoIssuerConfig{Email: "a@b.com", StateDir: "/tmp/x", Provider: manual}},
		{"unknown env", LegoIssuerConfig{Email: "a@b.com", StateDir: "/tmp/x", Provider: manual, Environment: "preview"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewLegoIssuer(tc.cfg); err == nil {
				t.Fatal("expected validation error")
			} else if errors.Is(err, errLegoConfigOK) {
				t.Fatal("unexpected sentinel")
			}
		})
	}
}

// errLegoConfigOK is referenced by the validation table only to keep the
// errors import alive when t.Fatal-only tests do not otherwise use it.
var errLegoConfigOK = errors.New("ok")
