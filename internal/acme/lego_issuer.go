package acme

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/route53"
	"github.com/go-acme/lego/v4/registration"
)

// LiveIssuer is the contract production callers use to obtain a
// certificate end-to-end. It is intentionally narrow so the offline state
// machine in orchestrator.go can compose with either a real Lego client or
// a test fake.
type LiveIssuer interface {
	Issue(ctx context.Context, req IssueRequest) (IssuedCertificate, error)
}

// LegoEnvironment selects between LE staging and LE production. Phase 1
// Lane 3 defaults to staging; flipping to production is an explicit
// operator action gated by the panel-locked rate limit guard.
type LegoEnvironment string

const (
	// LegoEnvironmentStaging targets Let's Encrypt staging. Default.
	LegoEnvironmentStaging LegoEnvironment = "staging"
	// LegoEnvironmentProduction targets Let's Encrypt production. Selected
	// only after the operator has demonstrated a green issuance against
	// staging.
	LegoEnvironmentProduction LegoEnvironment = "production"
)

// IssueRequest identifies one ACME order. Domains[0] is the cert CN; the
// remainder become SANs. For wildcard certificates the domains slice
// contains a single entry of the form `*.<apps-domain>`.
type IssueRequest struct {
	Domains    []string
	BundleCA   bool
	OrderID    string
	Provider   string
	ProviderID string
}

// IssuedCertificate is the on-disk view of a successful issuance. The
// caller is responsible for moving the on-disk artefacts into Traefik's
// state directory; this type only records the paths and the cert metadata.
type IssuedCertificate struct {
	Domain         string
	IssuerURL      string
	CertPath       string
	KeyPath        string
	IssuerCertPath string
	NotBefore      time.Time
	NotAfter       time.Time
	SerialNumber   string
	Provider       string
}

// LegoIssuerConfig configures the live ACME issuer. All fields are
// required.
type LegoIssuerConfig struct {
	Environment LegoEnvironment
	Email       string
	StateDir    string // /var/lib/midnight/acme by default; the account key + issued certs live under here
	Provider    challenge.Provider
	ProviderID  string // human-readable provider id for evidence ("cloudflare", "manual", ...)
	// Resolvers are the public DNS resolvers used during DNS-01
	// propagation polling. ADR-007 mandates these are public resolvers,
	// not the provider's own API.
	Resolvers []string
	// HTTPClientTimeout caps every ACME-side HTTP call.
	HTTPClientTimeout time.Duration
}

// LegoIssuer is the production implementation of LiveIssuer. It wraps a
// lego.Client with on-disk persistence under StateDir.
type LegoIssuer struct {
	cfg    LegoIssuerConfig
	user   *AccountUser
	client *lego.Client
}

// NewLegoIssuer constructs a Lego-backed live issuer. It loads/creates the
// account key, registers with the ACME directory if needed, and binds the
// supplied DNS-01 challenge provider.
func NewLegoIssuer(cfg LegoIssuerConfig) (*LegoIssuer, error) {
	if err := validateLegoConfig(cfg); err != nil {
		return nil, err
	}
	user, err := NewAccountUser(cfg.Email, filepath.Join(cfg.StateDir, "account.key"))
	if err != nil {
		return nil, err
	}
	legoCfg := lego.NewConfig(user)
	legoCfg.CADirURL = directoryURL(cfg.Environment)
	if cfg.HTTPClientTimeout > 0 {
		legoCfg.HTTPClient.Timeout = cfg.HTTPClientTimeout
	}

	client, err := lego.NewClient(legoCfg)
	if err != nil {
		return nil, fmt.Errorf("lego client: %w", err)
	}

	resolvers := cfg.Resolvers
	if len(resolvers) == 0 {
		resolvers = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}
	}
	if err := client.Challenge.SetDNS01Provider(cfg.Provider, dns01.AddRecursiveNameservers(resolvers)); err != nil {
		return nil, fmt.Errorf("set DNS-01 provider: %w", err)
	}

	if err := loadOrRegisterAccount(client, user, cfg.StateDir); err != nil {
		return nil, err
	}

	return &LegoIssuer{cfg: cfg, user: user, client: client}, nil
}

// Issue obtains (or renews) a certificate for the supplied domains and
// persists the cert + key bundle under StateDir.
func (i *LegoIssuer) Issue(ctx context.Context, req IssueRequest) (IssuedCertificate, error) {
	if i == nil {
		return IssuedCertificate{}, errors.New("lego issuer is nil")
	}
	if len(req.Domains) == 0 {
		return IssuedCertificate{}, errors.New("ACME issue request must include at least one domain")
	}
	_ = ctx // lego does not yet propagate context; the cancellation bound is enforced via HTTPClientTimeout.

	resource, err := i.client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: req.Domains,
		Bundle:  req.BundleCA,
	})
	if err != nil {
		return IssuedCertificate{}, fmt.Errorf("ACME obtain: %w", err)
	}
	return persistIssuedCertificate(i.cfg.StateDir, i.cfg.ProviderID, resource)
}

// LookupBuiltinDNSProvider resolves a built-in lego provider by name.
// Lane 3 ships with the three providers ADR-007 panel-locks: cloudflare,
// route53, manual.
//
//   - "cloudflare" expects CLOUDFLARE_DNS_API_TOKEN (preferred) or
//     CLOUDFLARE_EMAIL + CLOUDFLARE_API_KEY in the process environment.
//   - "route53" expects standard AWS env vars (AWS_ACCESS_KEY_ID,
//     AWS_SECRET_ACCESS_KEY, AWS_REGION) or an attached instance role.
//   - "manual" prints the TXT instruction to stdout and waits for the
//     operator to publish the record before acknowledging propagation.
//
// Other providers are deliberately not registered. Adding one is a
// deliberate per-provider review (license, supply chain, idempotency
// contract) — not "import the universal lookup helper".
func LookupBuiltinDNSProvider(name string) (challenge.Provider, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "":
		return nil, errors.New("DNS provider name is required")
	case "cloudflare":
		provider, err := cloudflare.NewDNSProvider()
		if err != nil {
			return nil, fmt.Errorf("dns provider cloudflare: %w", err)
		}
		return provider, nil
	case "route53":
		provider, err := route53.NewDNSProvider()
		if err != nil {
			return nil, fmt.Errorf("dns provider route53: %w", err)
		}
		return provider, nil
	case "manual":
		provider, err := dns01.NewDNSProviderManual()
		if err != nil {
			return nil, fmt.Errorf("dns provider manual: %w", err)
		}
		return provider, nil
	default:
		return nil, fmt.Errorf("dns provider %q is not enabled in Lane 3 (allowed: cloudflare, route53, manual)", name)
	}
}

func validateLegoConfig(cfg LegoIssuerConfig) error {
	if strings.TrimSpace(cfg.Email) == "" {
		return errors.New("ACME issuer email is required")
	}
	if !filepath.IsAbs(cfg.StateDir) {
		return errors.New("ACME issuer state dir must be absolute")
	}
	if cfg.Provider == nil {
		return errors.New("ACME issuer challenge provider is required")
	}
	if cfg.Environment == "" {
		return errors.New("ACME issuer environment must be staging or production")
	}
	switch cfg.Environment {
	case LegoEnvironmentStaging, LegoEnvironmentProduction:
	default:
		return fmt.Errorf("ACME issuer environment %q is not supported", cfg.Environment)
	}
	return nil
}

func directoryURL(env LegoEnvironment) string {
	if env == LegoEnvironmentProduction {
		return lego.LEDirectoryProduction
	}
	return lego.LEDirectoryStaging
}

func loadOrRegisterAccount(client *lego.Client, user *AccountUser, stateDir string) error {
	regPath := filepath.Join(stateDir, "registration.json")
	if data, err := os.ReadFile(regPath); err == nil {
		var existing registration.Resource
		if err := jsonUnmarshalStrict(data, &existing); err == nil && existing.URI != "" {
			user.SetRegistration(&existing)
			return nil
		}
	}
	resource, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return fmt.Errorf("register ACME account: %w", err)
	}
	user.SetRegistration(resource)
	encoded, err := jsonMarshalIndent(resource)
	if err != nil {
		return err
	}
	return writeAtomic(regPath, encoded, 0o600)
}

func persistIssuedCertificate(stateDir, providerID string, res *certificate.Resource) (IssuedCertificate, error) {
	domain := strings.TrimPrefix(res.Domain, "*.")
	dir := filepath.Join(stateDir, "certs", domain)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return IssuedCertificate{}, fmt.Errorf("create ACME cert dir: %w", err)
	}
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	issuerPath := filepath.Join(dir, "issuer.pem")
	if err := writeAtomic(certPath, res.Certificate, 0o640); err != nil {
		return IssuedCertificate{}, err
	}
	if err := writeAtomic(keyPath, res.PrivateKey, 0o600); err != nil {
		return IssuedCertificate{}, err
	}
	if len(res.IssuerCertificate) > 0 {
		if err := writeAtomic(issuerPath, res.IssuerCertificate, 0o640); err != nil {
			return IssuedCertificate{}, err
		}
	}
	parsed, _ := decodeFirstCertificate(res.Certificate)
	out := IssuedCertificate{
		Domain:         res.Domain,
		IssuerURL:      res.CertStableURL,
		CertPath:       certPath,
		KeyPath:        keyPath,
		IssuerCertPath: issuerPath,
		Provider:       providerID,
	}
	if parsed != nil {
		out.NotBefore = parsed.NotBefore
		out.NotAfter = parsed.NotAfter
		out.SerialNumber = parsed.SerialNumber.String()
	}
	return out, nil
}

func decodeFirstCertificate(pemBytes []byte) (*x509.Certificate, error) {
	for {
		block, rest := pem.Decode(pemBytes)
		if block == nil {
			return nil, errors.New("no PEM CERTIFICATE block found")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
		pemBytes = rest
		if len(pemBytes) == 0 {
			return nil, errors.New("no PEM CERTIFICATE block found")
		}
	}
}
