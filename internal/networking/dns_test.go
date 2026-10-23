// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package networking

import "testing"

func TestPlanInternalDNSResolverDefaultsPortAndNormalizesRecords(t *testing.T) {
	plan, err := PlanInternalDNSResolver(DNSResolverPlan{
		ListenAddress: "100.64.0.1",
		Zone:          "Svc.Midnight.Internal.",
		SearchDomains: []string{"svc.midnight.internal.", " svc.midnight.internal "},
		Upstreams:     []string{" 1.1.1.1 ", "1.1.1.1"},
		Records: []DNSRecord{
			{Name: "API.Svc.Midnight.Internal.", Address: "100.64.0.10", PeerID: "peer-api"},
			{Name: "DB.Svc.Midnight.Internal.", Address: "100.64.0.11", PeerID: "peer-db"},
		},
	})
	if err != nil {
		t.Fatalf("PlanInternalDNSResolver() error = %v", err)
	}
	if plan.Port != DefaultInternalDNSPort || plan.Zone != "svc.midnight.internal" {
		t.Fatalf("resolver defaults = %+v", plan)
	}
	if len(plan.SearchDomains) != 1 || len(plan.Upstreams) != 1 {
		t.Fatalf("deduped resolver lists = %+v", plan)
	}
	if plan.Records[0].Name != "api.svc.midnight.internal" || plan.Records[1].Name != "db.svc.midnight.internal" {
		t.Fatalf("records were not normalized/sorted: %+v", plan.Records)
	}
}

func TestPlanInternalDNSResolverRejectsDuplicateRecord(t *testing.T) {
	_, err := PlanInternalDNSResolver(DNSResolverPlan{
		ListenAddress: "100.64.0.1",
		Zone:          "svc.midnight.internal",
		Records: []DNSRecord{
			{Name: "api.svc.midnight.internal", Address: "100.64.0.10"},
			{Name: "API.SVC.MIDNIGHT.INTERNAL.", Address: "100.64.0.10"},
		},
	})
	if err == nil {
		t.Fatal("PlanInternalDNSResolver() error = nil, want duplicate record error")
	}
}

func TestCheckInternalDNSResolverReadinessRequiresRecords(t *testing.T) {
	readiness, err := CheckInternalDNSResolverReadiness(DNSResolverPlan{
		ListenAddress: "100.64.0.1",
		Zone:          "svc.midnight.internal",
	}, nil)
	if err != nil {
		t.Fatalf("CheckInternalDNSResolverReadiness() error = %v", err)
	}
	if readiness.Ready {
		t.Fatalf("readiness = %+v, want not ready", readiness)
	}
}

func TestCheckInternalDNSResolverReadinessChecksRequiredRecords(t *testing.T) {
	readiness, err := CheckInternalDNSResolverReadiness(DNSResolverPlan{
		ListenAddress: "100.64.0.1",
		Zone:          "svc.midnight.internal",
		Records: []DNSRecord{
			{Name: "api.svc.midnight.internal", Address: "100.64.0.10"},
		},
	}, []string{"API.SVC.MIDNIGHT.INTERNAL.", "db.svc.midnight.internal"})
	if err != nil {
		t.Fatalf("CheckInternalDNSResolverReadiness() error = %v", err)
	}
	if readiness.Ready {
		t.Fatalf("readiness = %+v, want missing required record", readiness)
	}
	foundMissing := false
	for _, check := range readiness.Checks {
		if check.Name == "record:db.svc.midnight.internal" && !check.Passed {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("readiness checks = %+v, want missing db record", readiness.Checks)
	}
}
