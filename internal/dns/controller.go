// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package dns

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const DefaultReconcileInterval = 30 * time.Second

// DomainIntent is the provider-safe projection of one service-managed domain.
type DomainIntent struct {
	DomainID       string
	Scope          Scope
	Kind           string
	Hostname       string
	AppsDomain     string
	TXTRecordName  string
	TXTRecordValue string
}

type ManagedOwner struct {
	OwnerKind string
	OwnerID   string
	Scope     Scope
}

type DomainIntentStore interface {
	ListDomainIntents(context.Context) ([]DomainIntent, error)
	ListManagedOwners(context.Context, string, string) ([]ManagedOwner, error)
}

type ControllerConfig struct {
	ZoneID      string
	ZoneName    string
	RouteTarget string
	IPv4        netip.Addr
	IPv6        netip.Addr
	TTL         int
	DryRun      bool
}

type Controller struct {
	Reconciler *Reconciler
	Intents    DomainIntentStore
	Config     ControllerConfig
}

// RunController performs an immediate reconciliation and repeats until the
// worker context ends. A failed owner never prevents later owners from being
// inspected during the same pass.
func RunController(ctx context.Context, controller *Controller, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = DefaultReconcileInterval
	}
	run := func() {
		if err := controller.ReconcileAll(ctx); err != nil && !errors.Is(err, context.Canceled) && onError != nil {
			onError(err)
		}
	}
	run()
	// Dry-run is a startup preflight, not a perpetual audit producer. The
	// operator restarts after changing provider state or enabling apply; only
	// apply mode needs periodic convergence.
	if controller != nil && controller.Config.DryRun {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (c *Controller) ReconcileAll(ctx context.Context) error {
	if c == nil || c.Reconciler == nil || c.Intents == nil {
		return errors.New("DNS controller requires reconciler and domain intent store")
	}
	config, err := normalizeControllerConfig(c.Config)
	if err != nil {
		return err
	}
	intents, err := c.Intents.ListDomainIntents(ctx)
	if err != nil {
		return fmt.Errorf("list DNS domain intents: %w", err)
	}
	provider := strings.ToLower(strings.TrimSpace(c.Reconciler.Provider.Name()))
	owners, err := c.Intents.ListManagedOwners(ctx, provider, config.ZoneID)
	if err != nil {
		return fmt.Errorf("list DNS managed owners: %w", err)
	}
	intentByOwner := make(map[string]DomainIntent, len(intents))
	for _, intent := range intents {
		intentByOwner[intent.DomainID] = intent
	}
	ownerByID := make(map[string]ManagedOwner, len(owners)+len(intents))
	for _, owner := range owners {
		if owner.OwnerKind == "domain" {
			ownerByID[owner.OwnerID] = owner
		}
	}
	for _, intent := range intents {
		ownerByID[intent.DomainID] = ManagedOwner{OwnerKind: "domain", OwnerID: intent.DomainID, Scope: intent.Scope}
	}
	ownerIDs := make([]string, 0, len(ownerByID))
	for ownerID := range ownerByID {
		ownerIDs = append(ownerIDs, ownerID)
	}
	sort.Strings(ownerIDs)
	var errs []error
	for _, ownerID := range ownerIDs {
		owner := ownerByID[ownerID]
		desired := []DesiredRecord{}
		if intent, exists := intentByOwner[ownerID]; exists {
			desired, err = desiredDomainRecords(config, intent)
			if err != nil {
				errs = append(errs, fmt.Errorf("domain %s: %w", ownerID, err))
				continue
			}
		}
		_, reconcileErr := c.Reconciler.Reconcile(ctx, Request{
			ZoneID: config.ZoneID, OwnerKind: "domain", OwnerID: ownerID,
			Scope: owner.Scope, Desired: desired, DryRun: config.DryRun,
		})
		if reconcileErr != nil {
			errs = append(errs, fmt.Errorf("domain %s: %w", ownerID, reconcileErr))
		}
	}
	return errors.Join(errs...)
}

func normalizeControllerConfig(config ControllerConfig) (ControllerConfig, error) {
	config.ZoneID = strings.TrimSpace(config.ZoneID)
	var err error
	config.ZoneName, err = normalizeRecordName(config.ZoneName, false)
	if err != nil || config.ZoneID == "" {
		return ControllerConfig{}, fmt.Errorf("%w: DNS zone id and valid zone name are required", ErrInvalidRequest)
	}
	if strings.TrimSpace(config.RouteTarget) != "" {
		config.RouteTarget, err = normalizeRecordName(config.RouteTarget, false)
		if err != nil {
			return ControllerConfig{}, fmt.Errorf("%w: invalid DNS route target", ErrInvalidRequest)
		}
	}
	if config.TTL == 0 {
		config.TTL = 300
	}
	if config.TTL != 1 && (config.TTL < 60 || config.TTL > 86400) {
		return ControllerConfig{}, fmt.Errorf("%w: invalid DNS controller ttl", ErrInvalidRequest)
	}
	if config.IPv4.IsValid() && !config.IPv4.Is4() {
		return ControllerConfig{}, fmt.Errorf("%w: DNS IPv4 target must be an IPv4 address", ErrInvalidRequest)
	}
	if config.IPv6.IsValid() && (!config.IPv6.Is6() || config.IPv6.Is4In6()) {
		return ControllerConfig{}, fmt.Errorf("%w: DNS IPv6 target must be an IPv6 address", ErrInvalidRequest)
	}
	return config, nil
}

func desiredDomainRecords(config ControllerConfig, intent DomainIntent) ([]DesiredRecord, error) {
	hostname, err := normalizeRecordName(intent.Hostname, false)
	if err != nil || !isInZone(hostname, config.ZoneName) {
		return nil, fmt.Errorf("%w: hostname is outside the configured authoritative zone", ErrInvalidRequest)
	}
	desired := []DesiredRecord{}
	if intent.Kind == "custom" {
		if strings.TrimSpace(intent.TXTRecordName) == "" || strings.TrimSpace(intent.TXTRecordValue) == "" {
			return nil, fmt.Errorf("%w: custom domain has no ownership TXT challenge", ErrInvalidRequest)
		}
		txtName, txtErr := normalizeRecordName(intent.TXTRecordName, true)
		if txtErr != nil || !isInZone(txtName, config.ZoneName) {
			return nil, fmt.Errorf("%w: ownership TXT name is outside the configured authoritative zone", ErrInvalidRequest)
		}
		desired = append(desired, DesiredRecord{Type: TypeTXT, Name: txtName, Content: intent.TXTRecordValue, TTL: config.TTL})
	} else if intent.Kind != "generated" {
		return nil, fmt.Errorf("%w: unsupported domain kind", ErrInvalidRequest)
	}

	if hostname != config.ZoneName && config.RouteTarget != "" && hostname != config.RouteTarget {
		desired = append(desired, DesiredRecord{Type: TypeCNAME, Name: hostname, Content: config.RouteTarget, TTL: config.TTL})
		return desired, nil
	}
	if config.IPv4.IsValid() {
		desired = append(desired, DesiredRecord{Type: TypeA, Name: hostname, Content: config.IPv4.String(), TTL: config.TTL})
	}
	if config.IPv6.IsValid() {
		desired = append(desired, DesiredRecord{Type: TypeAAAA, Name: hostname, Content: config.IPv6.String(), TTL: config.TTL})
	}
	if len(desired) == 0 || (intent.Kind == "custom" && len(desired) == 1) {
		return nil, fmt.Errorf("%w: no safe routing target is configured for domain", ErrInvalidRequest)
	}
	return desired, nil
}

func isInZone(name, zone string) bool {
	return name == zone || strings.HasSuffix(name, "."+zone)
}
