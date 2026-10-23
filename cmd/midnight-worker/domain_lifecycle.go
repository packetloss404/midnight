package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/midnight/midnight/internal/acme"
	"github.com/midnight/midnight/internal/router"
)

// workerTXTResolver uses the host's recursive resolver for the ownership
// evidence lane. ACME issuance itself uses the explicitly configured public
// resolvers in LegoIssuerConfig; keeping these paths separate prevents a
// provider API response from being mistaken for public DNS evidence.
type workerTXTResolver struct {
	resolver *net.Resolver
}

func (r workerTXTResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	resolver := r.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return resolver.LookupTXT(ctx, name)
}

type workerCertificateIssuer struct {
	issuer *acme.LegoIssuer
	name   string
}

func (i workerCertificateIssuer) Name() string { return i.name }

func (i workerCertificateIssuer) Issue(ctx context.Context, domain router.DurableDomain) (router.CertificateIssueResult, error) {
	if i.issuer == nil {
		return router.CertificateIssueResult{}, errors.New("ACME issuer is not configured")
	}
	hostname := strings.TrimSpace(domain.NormalizedHostname)
	if hostname == "" {
		hostname = strings.TrimSpace(domain.Hostname)
	}
	if hostname == "" {
		return router.CertificateIssueResult{}, errors.New("ACME domain hostname is required")
	}
	issued, err := i.issuer.Issue(ctx, acme.IssueRequest{
		Domains:    []string{hostname},
		BundleCA:   true,
		OrderID:    domain.ID,
		Provider:   i.name,
		ProviderID: i.name,
	})
	if err != nil {
		return router.CertificateIssueResult{}, err
	}
	if strings.TrimSpace(issued.IssuerURL) == "" {
		return router.CertificateIssueResult{}, errors.New("ACME issuer returned no order reference")
	}
	if strings.TrimSpace(issued.CertPath) == "" || issued.NotAfter.IsZero() {
		return router.CertificateIssueResult{}, errors.New("ACME issuer returned no installed certificate evidence")
	}
	return router.CertificateIssueResult{
		OrderRef:       issued.IssuerURL,
		CertificateRef: issued.CertPath,
		ExpiresAt:      issued.NotAfter,
	}, nil
}

func newWorkerDomainLifecycleController(cfg workerConfig, store router.DomainLifecycleStore) (*router.DomainLifecycleController, error) {
	if !cfg.ACMEEnabled {
		return nil, nil
	}
	if store == nil {
		return nil, errors.New("ACME domain lifecycle store is required")
	}
	provider, err := acme.LookupBuiltinDNSProvider(cfg.ACMEDNSProvider)
	if err != nil {
		return nil, err
	}
	environment := acme.LegoEnvironment(cfg.ACMEEnvironment)
	issuer, err := acme.NewLegoIssuer(acme.LegoIssuerConfig{
		Environment:       environment,
		Email:             cfg.ACMEEmail,
		StateDir:          cfg.ACMEStateDir,
		Provider:          provider,
		ProviderID:        cfg.ACMEDNSProvider,
		Resolvers:         cfg.ACMEResolvers,
		HTTPClientTimeout: cfg.ACMEHTTPTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("construct ACME issuer: %w", err)
	}
	return &router.DomainLifecycleController{
		Store:    store,
		Resolver: workerTXTResolver{},
		Issuer: workerCertificateIssuer{
			issuer: issuer,
			name:   "lego-" + cfg.ACMEEnvironment + "/" + cfg.ACMEDNSProvider,
		},
	}, nil
}
