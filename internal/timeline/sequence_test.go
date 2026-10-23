package timeline

import "testing"

func TestValidateEventSequenceAcceptsOrderedEventsPerRoute(t *testing.T) {
	events := []Event{
		{Type: EventIngressRouteRendered, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
		{Type: EventIngressRouteArtifactWritten, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
		{Type: EventACMEDNSPending, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
		{Type: EventIngressRouteRendered, TenantID: "tenant-a", RouteName: "api", Host: "api.example.test"},
		{Type: EventIngressRouteArtifactWritten, TenantID: "tenant-a", RouteName: "api", Host: "api.example.test"},
		{Type: EventACMECertificateReady, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
		{Type: EventIngressRouteActive, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
	}

	if err := ValidateEventSequence(events); err != nil {
		t.Fatalf("ValidateEventSequence returned error: %v", err)
	}
}

func TestValidateEventSequenceRejectsOutOfOrderEvents(t *testing.T) {
	events := []Event{
		{Type: EventACMECertificateReady, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
		{Type: EventACMEDNSPending, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
	}

	if err := ValidateEventSequence(events); err == nil {
		t.Fatal("expected out-of-order event error")
	}
}

func TestValidateEventSequenceRejectsUnknownEvent(t *testing.T) {
	if err := ValidateEventSequence([]Event{{Type: EventType("unknown")}}); err == nil {
		t.Fatal("expected unknown event error")
	}
}

func TestValidateEventSequenceRejectsSkippedEvents(t *testing.T) {
	events := []Event{
		{Type: EventIngressRouteRendered, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
		{Type: EventIngressRouteActive, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
	}

	if err := ValidateEventSequence(events); err == nil {
		t.Fatal("expected skipped event error")
	}
}

func TestValidateEventSequenceAllowsDuplicateEvents(t *testing.T) {
	events := []Event{
		{Type: EventIngressRouteRendered, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
		{Type: EventIngressRouteRendered, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
		{Type: EventIngressRouteArtifactWritten, TenantID: "tenant-a", RouteName: "app", Host: "app.example.test"},
	}

	if err := ValidateEventSequence(events); err != nil {
		t.Fatalf("ValidateEventSequence returned error: %v", err)
	}
}
