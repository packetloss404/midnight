// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package networking

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

const DefaultInternalDNSPort = 53

type DNSResolverPlan struct {
	ListenAddress string      `json:"listen_address"`
	Port          int         `json:"port"`
	Zone          string      `json:"zone"`
	SearchDomains []string    `json:"search_domains,omitempty"`
	Upstreams     []string    `json:"upstreams,omitempty"`
	Records       []DNSRecord `json:"records"`
}

type DNSRecord struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	PeerID  string `json:"peer_id,omitempty"`
}

type DNSResolverReadinessPlan struct {
	Ready  bool                `json:"ready"`
	Checks []DNSReadinessCheck `json:"checks"`
}

type DNSReadinessCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

func PlanInternalDNSResolver(plan DNSResolverPlan) (DNSResolverPlan, error) {
	plan.ListenAddress = strings.TrimSpace(plan.ListenAddress)
	plan.Zone = normalizeDNSName(plan.Zone)
	if plan.Port == 0 {
		plan.Port = DefaultInternalDNSPort
	}
	if _, err := netip.ParseAddr(plan.ListenAddress); err != nil {
		return DNSResolverPlan{}, fmt.Errorf("internal dns listen_address is invalid: %w", err)
	}
	if plan.Port <= 0 || plan.Port > 65535 {
		return DNSResolverPlan{}, errors.New("internal dns port must be between 1 and 65535")
	}
	if plan.Zone == "" {
		return DNSResolverPlan{}, errors.New("internal dns zone is required")
	}
	plan.SearchDomains = normalizeDNSNames(plan.SearchDomains)
	plan.Upstreams = normalizeStrings(plan.Upstreams)
	plan.Records = append([]DNSRecord(nil), plan.Records...)
	seenRecords := map[string]struct{}{}
	for i := range plan.Records {
		record, err := normalizeDNSRecord(plan.Records[i])
		if err != nil {
			return DNSResolverPlan{}, err
		}
		key := record.Name + "=" + record.Address
		if _, ok := seenRecords[key]; ok {
			return DNSResolverPlan{}, fmt.Errorf("internal dns record %q for %q must be unique", record.Name, record.Address)
		}
		seenRecords[key] = struct{}{}
		plan.Records[i] = record
	}
	sort.Slice(plan.Records, func(i, j int) bool {
		if plan.Records[i].Name == plan.Records[j].Name {
			return plan.Records[i].Address < plan.Records[j].Address
		}
		return plan.Records[i].Name < plan.Records[j].Name
	})
	return plan, nil
}

func CheckInternalDNSResolverReadiness(plan DNSResolverPlan, requiredRecordNames []string) (DNSResolverReadinessPlan, error) {
	plan, err := PlanInternalDNSResolver(plan)
	if err != nil {
		return DNSResolverReadinessPlan{}, err
	}
	readiness := DNSResolverReadinessPlan{Ready: true}
	addCheck := func(name string, passed bool, reason string) {
		if !passed {
			readiness.Ready = false
		} else {
			reason = ""
		}
		readiness.Checks = append(readiness.Checks, DNSReadinessCheck{Name: name, Passed: passed, Reason: reason})
	}

	addCheck("listen-address", plan.ListenAddress != "", "")
	addCheck("zone", plan.Zone != "", "")
	addCheck("records", len(plan.Records) > 0, "at least one internal dns record is required")

	recordNames := map[string]struct{}{}
	for _, record := range plan.Records {
		recordNames[record.Name] = struct{}{}
	}
	for _, required := range normalizeDNSNames(requiredRecordNames) {
		_, ok := recordNames[required]
		addCheck("record:"+required, ok, "required internal dns record is missing")
	}
	return readiness, nil
}

func normalizeDNSRecord(record DNSRecord) (DNSRecord, error) {
	record.Name = normalizeDNSName(record.Name)
	record.Address = strings.TrimSpace(record.Address)
	record.PeerID = strings.TrimSpace(record.PeerID)
	if record.Name == "" {
		return DNSRecord{}, errors.New("internal dns record name is required")
	}
	if _, err := netip.ParseAddr(record.Address); err != nil {
		return DNSRecord{}, fmt.Errorf("internal dns record address %q is invalid: %w", record.Address, err)
	}
	return record, nil
}

func normalizeDNSNames(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = normalizeDNSName(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizeDNSName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, ".")
	return value
}

func normalizeStrings(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized
}
