package acme

import (
	"context"
	"errors"
	"testing"
)

func TestManualDNSProviderRequiresManualAction(t *testing.T) {
	provider := ManualDNSProvider{}
	if err := provider.Present(context.Background(), "_acme-challenge.example.test.", "token"); !errors.Is(err, ErrManualDNSRequired) {
		t.Fatalf("Present error = %v, want ErrManualDNSRequired", err)
	}
	if err := provider.CleanUp(context.Background(), "_acme-challenge.example.test.", "token"); !errors.Is(err, ErrManualDNSRequired) {
		t.Fatalf("CleanUp error = %v, want ErrManualDNSRequired", err)
	}
}

func TestACMEBudgetsArePositive(t *testing.T) {
	if IssueTimeout <= 0 || DNSPollInterval <= 0 || DNSPropagateLimit <= 0 || RenewBefore <= 0 {
		t.Fatal("ACME budget constants must be positive")
	}
}
