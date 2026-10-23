// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package router

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestNormalizeDurableRouteBackendSetsCopiesSortsAndRejectsDuplicates(t *testing.T) {
	input := []DurableRouteBackendSet{
		{
			RouteID: " rte-b ",
			Backends: []DurableRouteBackend{{
				ID: "backend-b",
			}},
		},
		{
			RouteID: "rte-a",
			Backends: []DurableRouteBackend{{
				ID: "backend-a",
			}},
		},
	}
	got, err := normalizeDurableRouteBackendSets(input)
	if err != nil {
		t.Fatalf("normalize durable route backend sets: %v", err)
	}
	if got[0].RouteID != "rte-a" || got[1].RouteID != "rte-b" {
		t.Fatalf("normalized order = %#v", got)
	}
	got[0].Backends[0].ID = "mutated"
	if input[1].Backends[0].ID != "backend-a" {
		t.Fatal("normalization aliased caller backend storage")
	}

	_, err = normalizeDurableRouteBackendSets([]DurableRouteBackendSet{
		{RouteID: "rte-a"},
		{RouteID: " rte-a "},
	})
	if !errors.Is(err, ErrDurableRouteInvalid) {
		t.Fatalf("duplicate route error = %v, want durable route invalid", err)
	}
	_, err = normalizeDurableRouteBackendSets([]DurableRouteBackendSet{{
		RouteID: " ",
	}})
	if !errors.Is(err, ErrDurableRouteInvalid) {
		t.Fatalf("blank route error = %v, want durable route invalid", err)
	}
}

func TestDurableRouteBackendSetsOwnRevisionRequiresEveryNonEmptySet(t *testing.T) {
	set := func(routeID string, revisions ...int64) DurableRouteBackendSet {
		backends := make([]DurableRouteBackend, len(revisions))
		for index, revision := range revisions {
			backends[index] = DurableRouteBackend{
				ID:              routeID + "-backend",
				DesiredRevision: revision,
			}
		}
		return DurableRouteBackendSet{RouteID: routeID, Backends: backends}
	}

	owned, err := durableRouteBackendSetsOwnRevision(
		[]DurableRouteBackendSet{set("rte-a", 7), set("rte-b", 7, 7)},
		7,
	)
	if err != nil || !owned {
		t.Fatalf("exact ownership = %t, %v", owned, err)
	}
	owned, err = durableRouteBackendSetsOwnRevision(
		[]DurableRouteBackendSet{set("rte-a", 7), set("rte-b")},
		7,
	)
	if err != nil || owned {
		t.Fatalf("empty set ownership = %t, %v, want false", owned, err)
	}
	owned, err = durableRouteBackendSetsOwnRevision(
		[]DurableRouteBackendSet{set("rte-a", 7), set("rte-b", 8)},
		7,
	)
	if err != nil || owned {
		t.Fatalf("moved set ownership = %t, %v, want false", owned, err)
	}
	_, err = durableRouteBackendSetsOwnRevision(
		[]DurableRouteBackendSet{set("rte-a", 7, 8)},
		7,
	)
	if !errors.Is(err, ErrDurableRouteInvalid) {
		t.Fatalf("mixed set revision error = %v, want durable route invalid", err)
	}
}

func TestValidateEligiblePublicRouteStateFailsClosedOnIntentChanges(t *testing.T) {
	scope := RouteScope{
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
	}
	routeValue := DurableHTTPRoute{
		ID: "rte", DomainID: "dom", RouteScope: scope, Public: true,
	}
	domainValue := DurableDomain{
		ID: "dom", RouteScope: scope, Kind: DomainKindGenerated,
		TLSState: DomainTLSDisabled,
	}
	if err := validateEligiblePublicRouteState(routeValue, domainValue); err != nil {
		t.Fatalf("generated HTTP route eligibility: %v", err)
	}

	legacyTLS := domainValue
	legacyTLS.Kind = DomainKindCustom
	legacyTLS.TLSDesired = true
	legacyTLS.TLSState = DomainTLSReady
	if err := validateEligiblePublicRouteState(routeValue, legacyTLS); err != nil {
		t.Fatalf("legacy custom TLS route eligibility: %v", err)
	}

	privateRoute := routeValue
	privateRoute.Public = false
	if err := validateEligiblePublicRouteState(privateRoute, domainValue); !errors.Is(err, ErrDurableRouteInvalid) {
		t.Fatalf("private route error = %v, want durable route invalid", err)
	}

	unreadyTLS := legacyTLS
	unreadyTLS.TLSState = DomainTLSUnavailable
	if err := validateEligiblePublicRouteState(routeValue, unreadyTLS); !errors.Is(err, ErrDurableRouteInvalid) {
		t.Fatalf("unready TLS error = %v, want durable route invalid", err)
	}
}

func TestHistoricalBackendPortPolicyAllowsOnlyPersistedOrCapturedPriorBackends(t *testing.T) {
	scope := RouteScope{
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
	}
	routeValue := DurableHTTPRoute{
		ID: "rte", DomainID: "dom", RouteScope: scope,
		Name: "app", PathPrefix: "/", TargetPort: 9090, Public: true,
	}
	domainValue := DurableDomain{
		ID: "dom", RouteScope: scope, Kind: DomainKindGenerated,
		NormalizedHostname: "app.example.test", TLSState: DomainTLSDisabled,
	}
	previous := DurableRouteBackend{
		ID: "old", RouteID: routeValue.ID, RouteScope: scope,
		DeploymentID: "dep-old", InstanceID: "dep-old-r0",
		ServiceURL: "http://10.0.0.1:8080", Ready: true,
		DesiredRevision: 7,
	}

	if allowed := allowHistoricalBackendPort(
		previous,
		[]DurableRouteBackend{previous},
		8,
		false,
	); !allowed {
		t.Fatal("exact persisted backend did not retain its historical port")
	}
	if err := validateBackendURLTarget(
		routeValue,
		domainValue,
		previous.ServiceURL,
		true,
	); err != nil {
		t.Fatalf("validate persisted historical backend: %v", err)
	}

	spoofed := previous
	spoofed.ServiceURL = "http://10.0.0.9:8080"
	if allowed := allowHistoricalBackendPort(
		spoofed,
		[]DurableRouteBackend{previous},
		8,
		false,
	); allowed {
		t.Fatal("changed address reused a persisted backend identity")
	}
	if err := validateBackendURLTarget(
		routeValue,
		domainValue,
		spoofed.ServiceURL,
		false,
	); !errors.Is(err, ErrDurableRouteInvalid) {
		t.Fatalf("spoofed historical backend error = %v, want durable route invalid", err)
	}

	candidate := DurableRouteBackend{
		ID: "new", RouteID: routeValue.ID, RouteScope: scope,
		DeploymentID: "dep-new", InstanceID: "dep-new-r0",
		ServiceURL: "http://10.0.0.2:8080", Ready: true,
	}
	if allowed := allowHistoricalBackendPort(
		candidate,
		[]DurableRouteBackend{previous},
		8,
		false,
	); allowed {
		t.Fatal("new candidate received historical-port permission")
	}
	if err := validateBackendURLTarget(
		routeValue,
		domainValue,
		candidate.ServiceURL,
		false,
	); !errors.Is(err, ErrDurableRouteInvalid) {
		t.Fatalf("wrong-port candidate error = %v, want durable route invalid", err)
	}
	candidate.ServiceURL = "http://10.0.0.2:9090"
	if err := validateBackendURLTarget(
		routeValue,
		domainValue,
		candidate.ServiceURL,
		false,
	); err != nil {
		t.Fatalf("current-port candidate: %v", err)
	}

	if allowed := allowHistoricalBackendPort(
		previous,
		nil,
		9,
		true,
	); !allowed {
		t.Fatal("captured prior backend was not restorable after commit")
	}
	candidate.DesiredRevision = 0
	if allowed := allowHistoricalBackendPort(
		candidate,
		nil,
		9,
		true,
	); allowed {
		t.Fatal("unstamped candidate was accepted as a captured prior backend")
	}
}

func TestDurableRouteEnvironmentMutationLockKeyIsStableAndScopeBound(t *testing.T) {
	first := durableRouteEnvironmentMutationLockKey("project-a", "environment-a")
	if first != durableRouteEnvironmentMutationLockKey(
		"project-a",
		"environment-a",
	) {
		t.Fatal("route environment mutation lock key is not deterministic")
	}
	for _, scope := range [][2]string{
		{"project-b", "environment-a"},
		{"project-a", "environment-b"},
		{"project-a:environment-a", ""},
	} {
		if got := durableRouteEnvironmentMutationLockKey(
			scope[0],
			scope[1],
		); got == first {
			t.Fatalf("scope %#v reused mutation lock key %d", scope, got)
		}
	}
}

func TestMultiRoutePersistenceSQLContract(t *testing.T) {
	source, err := os.ReadFile("durable_routes_postgres.go")
	if err != nil {
		t.Fatalf("read durable route postgres store: %v", err)
	}
	text := string(source)
	required := []string{
		"FROM service_domain_bindings b",
		"ON CONFLICT (deployment_id, route_id) DO NOTHING",
		"ManagedServiceRouteEligibilityPredicateSQL",
		"(d.tls_desired=FALSE AND d.tls_state='disabled')",
		"(d.tls_desired=TRUE AND d.tls_state='ready')",
		"dc.domain_id=d.id AND dc.state='verified'",
		"DELETE FROM route_backends WHERE route_id = $1",
	}
	for _, fragment := range required {
		if !strings.Contains(text, fragment) {
			t.Fatalf("multi-route persistence missing SQL contract %q", fragment)
		}
	}
	if strings.Contains(text, "ON CONFLICT (deployment_id) DO NOTHING") {
		t.Fatal("singular deployment route conflict target remains")
	}
	deploymentListStart := strings.Index(
		text,
		"func (s *PostgresDurableRouteStore) ListDeploymentPublicRouteStates",
	)
	serviceListStart := strings.Index(
		text,
		"func (s *PostgresDurableRouteStore) ListServiceGeneratedRouteStates",
	)
	if deploymentListStart < 0 ||
		serviceListStart <= deploymentListStart {
		t.Fatal("deployment/service route list boundaries are missing")
	}
	deploymentListEnd := strings.Index(text[deploymentListStart:], "\nfunc ")
	if deploymentListEnd <= 0 {
		t.Fatal("deployment-bound route read function boundary is missing")
	}
	deploymentList := text[deploymentListStart : deploymentListStart+deploymentListEnd]
	if !strings.Contains(deploymentList, "managedRouteOwnershipEligibilityPredicateSQL") ||
		!strings.Contains(deploymentList, "r.public_enabled=TRUE") {
		t.Fatal("deployment-bound route read does not retain public TLS intent under centralized ownership eligibility")
	}
	if strings.Contains(deploymentList, "ManagedServiceRouteEligibilityPredicateSQL") {
		t.Fatal("deployment-bound route read hides TLS-pending intent before typed fail-closed validation")
	}
	for _, method := range []string{
		"ReplaceEligibleRouteGroupBackendsCAS",
		"RestoreEligibleRouteGroupBackendsWithOwnershipCAS",
	} {
		if !strings.Contains(text, method) {
			t.Fatalf("multi-route CAS boundary missing %s", method)
		}
	}
	for _, function := range []string{
		"PutDomainInTx",
		"DeleteDomain",
		"EnsureDomainLifecycleInTx",
		"MarkDomainTLSReady",
		"PutHTTPRouteInTx",
		"BindServiceDomainInTx",
		"BindServiceGeneratedRoutesToDeploymentInTx",
		"BindDeploymentRouteIntentInTx",
		"DeleteHTTPRoute",
		"replaceRouteGroupBackendsCAS",
		"replaceRouteBackends",
		"RecordRouteApply",
	} {
		functionStart := strings.Index(
			text,
			"func (s *PostgresDurableRouteStore) "+function+"(",
		)
		if functionStart < 0 {
			t.Fatalf("durable route mutation function %s is missing", function)
		}
		nextFunction := strings.Index(text[functionStart:], "\nfunc ")
		functionEnd := len(text)
		if nextFunction > 0 {
			functionEnd = functionStart + nextFunction
		}
		if !strings.Contains(
			text[functionStart:functionEnd],
			"AcquireRouteEnvironmentMutationLock(",
		) {
			t.Fatalf(
				"durable route mutation %s does not acquire the environment lock",
				function,
			)
		}
	}
}

func TestManagedRouteRepairRetryIsBoundedAndPreservesTerminalErrors(t *testing.T) {
	attempts := 0
	_, _, _, err := retryManagedRouteRepair(func() (int64, bool, error) {
		attempts++
		return 0, false, ErrDurableRouteRevisionMoved
	})
	if !errors.Is(err, ErrDurableRouteRevisionMoved) || attempts != managedRouteRepairMaxAttempts {
		t.Fatalf("retry error=%v attempts=%d, want revision-moved after %d", err, attempts, managedRouteRepairMaxAttempts)
	}

	want := errors.New("repair read failed")
	attempts = 0
	_, _, _, err = retryManagedRouteRepair(func() (int64, bool, error) {
		attempts++
		return 0, false, want
	})
	if !errors.Is(err, want) || attempts != 1 {
		t.Fatalf("terminal error=%v attempts=%d, want one attempt preserving %v", err, attempts, want)
	}
}

func TestDurableRouteInputsCollapsesBackendsSharingOneAddress(t *testing.T) {
	group := DurableRouteGroup{
		Routes: []DurableDesiredRoute{{
			Route:  DurableHTTPRoute{Name: "route-a", ID: "rte-a", Public: true, PathPrefix: "/"},
			Domain: DurableDomain{NormalizedHostname: "app.example.test"},
			// A redeploy leaves a backend per deployment, and the replacement
			// container routinely reuses the address the removed one had.
			Backends: []DurableRouteBackend{
				{ID: "backend-old", ServiceURL: "http://172.18.0.4:8000", Ready: true},
				{ID: "backend-new", ServiceURL: "http://172.18.0.4:8000", Ready: true},
				{ID: "backend-other", ServiceURL: "http://172.18.0.5:8000", Ready: true},
				{ID: "backend-unready", ServiceURL: "http://172.18.0.6:8000"},
			},
		}},
	}

	routes, blocked, publicCount := durableRouteInputs(group)
	if blocked != nil {
		t.Fatalf("unexpected block: %+v", blocked)
	}
	if publicCount != 1 || len(routes) != 1 {
		t.Fatalf("publicCount = %d, routes = %d", publicCount, len(routes))
	}
	want := []string{"http://172.18.0.4:8000", "http://172.18.0.5:8000"}
	if len(routes[0].ServiceURLs) != len(want) {
		t.Fatalf("service URLs = %#v, want %#v", routes[0].ServiceURLs, want)
	}
	for i, url := range want {
		if routes[0].ServiceURLs[i] != url {
			t.Fatalf("service URLs = %#v, want %#v", routes[0].ServiceURLs, want)
		}
	}
	// The renderer refuses a duplicated URL, so the collapse has to happen
	// before validation rather than being caught by it.
	if err := ValidateRouteGroup("durable-group"); err != nil {
		t.Fatalf("route group name: %v", err)
	}
	if _, err := RenderTraefikDynamicYAML(routes); err != nil {
		t.Fatalf("render: %v", err)
	}
}
