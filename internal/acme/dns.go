package acme

import (
	"context"
	"errors"
)

var ErrManualDNSRequired = errors.New("manual DNS update required")

// DNSProvider is the DNS-01 challenge contract. Implementations must not block
// past ctx while publishing or removing challenge records.
type DNSProvider interface {
	Present(ctx context.Context, fqdn, value string) error
	CleanUp(ctx context.Context, fqdn, value string) error
}

// ManualDNSProvider documents the pending manual workflow without performing IO.
type ManualDNSProvider struct{}

func (ManualDNSProvider) Present(context.Context, string, string) error {
	return ErrManualDNSRequired
}

func (ManualDNSProvider) CleanUp(context.Context, string, string) error {
	return ErrManualDNSRequired
}
