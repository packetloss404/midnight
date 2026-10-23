package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type DomainLifecycleRecord struct {
	Domain      DurableDomain
	Challenge   *DomainDNSChallenge
	Certificate *DomainCertificateOrder
}

type DomainLifecycleStore interface {
	ListPendingDomainLifecycles(context.Context) ([]DomainLifecycleRecord, error)
	RecordDNSChallengeObservation(context.Context, DomainDNSChallenge) error
	RecordCertificateOrder(context.Context, DomainCertificateOrder) error
	MarkDomainTLSReady(context.Context, string, string, time.Time) error
}

type DNSOwnershipResolver interface {
	LookupTXT(context.Context, string) ([]string, error)
}

// CertificateIssuer returns references to operator-owned certificate
// material, never PEM/private-key bytes. Production deliberately leaves this
// interface unwired until an operator configures an issuer and DNS authority.
type CertificateIssuer interface {
	Name() string
	Issue(context.Context, DurableDomain) (CertificateIssueResult, error)
}

type CertificateIssueResult struct {
	OrderRef       string
	CertificateRef string
	ExpiresAt      time.Time
}

type DomainLifecycleController struct {
	Store    DomainLifecycleStore
	Resolver DNSOwnershipResolver
	Issuer   CertificateIssuer
	Now      func() time.Time
}

const DefaultDomainLifecycleReconcileInterval = 30 * time.Second
const domainLifecycleRetryBackoff = 5 * time.Minute
const domainLifecycleMaxRetryBackoff = time.Hour

// RunDomainLifecycleController performs an immediate startup reconciliation
// and then retries pending ownership/certificate work until the worker stops.
// The controller is deliberately opt-in at the command boundary; this
// runner only owns scheduling and never invents issuer or DNS credentials.
func RunDomainLifecycleController(ctx context.Context, controller *DomainLifecycleController, interval time.Duration, onError func(error)) {
	if controller == nil {
		if onError != nil {
			onError(errors.New("domain lifecycle controller is required"))
		}
		return
	}
	if interval <= 0 {
		interval = DefaultDomainLifecycleReconcileInterval
	}
	reconcile := func() {
		if err := controller.ReconcileAll(ctx); err != nil && !errors.Is(err, context.Canceled) && onError != nil {
			onError(err)
		}
	}
	reconcile()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func (c *DomainLifecycleController) ReconcileAll(ctx context.Context) error {
	if c == nil || c.Store == nil {
		return errors.New("domain lifecycle store is required")
	}
	values, err := c.Store.ListPendingDomainLifecycles(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, value := range values {
		if err := c.reconcile(ctx, value); err != nil {
			errs = append(errs, fmt.Errorf("domain %s: %w", value.Domain.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (c *DomainLifecycleController) reconcile(ctx context.Context, value DomainLifecycleRecord) error {
	if !value.Domain.TLSDesired {
		return nil
	}
	renewal := value.Domain.TLSState == DomainTLSReady
	if renewal && value.Certificate == nil {
		return nil
	}
	if value.Certificate != nil && (!renewal || value.Certificate.State == CertificateOrderFailed) && !certificateRetryDue(*value.Certificate, c.now()) {
		return nil
	}
	if value.Domain.Kind == DomainKindCustom {
		if value.Challenge == nil {
			return errors.New("custom TLS domain has no durable DNS ownership challenge")
		}
		if value.Challenge.State != DNSChallengeVerified {
			if c.Resolver == nil {
				return nil
			}
			observed, err := c.Resolver.LookupTXT(ctx, value.Challenge.RecordName)
			challenge := *value.Challenge
			challenge.Attempts++
			challenge.ObservedValues = append([]string(nil), observed...)
			now := c.now()
			challenge.CheckedAt = &now
			challenge.UpdatedAt = now
			if err != nil {
				challenge.State = DNSChallengeFailed
				challenge.LastError = err.Error()
			} else if containsExactTXT(observed, challenge.RecordValue) {
				challenge.State = DNSChallengeVerified
				challenge.LastError = ""
			} else {
				challenge.State = DNSChallengePending
				challenge.LastError = "expected TXT ownership value was not observed"
			}
			if recordErr := c.Store.RecordDNSChallengeObservation(ctx, challenge); recordErr != nil {
				return recordErr
			}
			if challenge.State != DNSChallengeVerified {
				return nil
			}
		}
	}
	if c.Issuer == nil {
		return nil
	}
	order := DomainCertificateOrder{DomainID: value.Domain.ID, RouteScope: value.Domain.RouteScope}
	if value.Certificate != nil {
		order = *value.Certificate
	}
	order.Issuer = strings.TrimSpace(c.Issuer.Name())
	if order.Issuer == "" {
		return errors.New("certificate issuer name is required")
	}
	order.State = CertificateOrderIssuing
	order.Attempts++
	order.UpdatedAt = c.now()
	if err := c.Store.RecordCertificateOrder(ctx, order); err != nil {
		return err
	}
	result, err := c.Issuer.Issue(ctx, value.Domain)
	if err != nil {
		order.State = CertificateOrderFailed
		order.LastError = err.Error()
		order.UpdatedAt = c.now()
		return errors.Join(err, c.Store.RecordCertificateOrder(ctx, order))
	}
	if strings.TrimSpace(result.OrderRef) == "" || strings.TrimSpace(result.CertificateRef) == "" || result.ExpiresAt.IsZero() {
		return errors.New("certificate issuer returned incomplete evidence references")
	}
	order.State = CertificateOrderReady
	order.IssuerOrderRef = result.OrderRef
	order.CertificateRef = result.CertificateRef
	order.ExpiresAt = &result.ExpiresAt
	order.LastError = ""
	order.UpdatedAt = c.now()
	if err := c.Store.RecordCertificateOrder(ctx, order); err != nil {
		return err
	}
	return c.Store.MarkDomainTLSReady(ctx, value.Domain.ID, result.CertificateRef, result.ExpiresAt)
}

// certificateRetryDue prevents a restarted worker from hot-looping a failed
// ACME order into a provider rate limit. A pending first attempt is eligible
// immediately; issuing/failed orders use bounded exponential backoff based on
// their durable attempt count.
func certificateRetryDue(order DomainCertificateOrder, now time.Time) bool {
	if order.State == CertificateOrderReady || order.Attempts <= 0 || order.UpdatedAt.IsZero() {
		return order.State != CertificateOrderReady
	}
	delay := domainLifecycleRetryBackoff
	for attempt := int64(1); attempt < order.Attempts && delay < domainLifecycleMaxRetryBackoff; attempt++ {
		delay *= 2
	}
	if delay > domainLifecycleMaxRetryBackoff {
		delay = domainLifecycleMaxRetryBackoff
	}
	return !now.Before(order.UpdatedAt.Add(delay))
}

func (c *DomainLifecycleController) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func containsExactTXT(values []string, expected string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == expected {
			return true
		}
	}
	return false
}
