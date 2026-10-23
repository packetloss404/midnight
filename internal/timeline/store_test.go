package timeline

import "testing"

func TestMemoryStoreAppendsAndListsByDeploymentID(t *testing.T) {
	store := NewMemoryStore()
	if err := store.Append(Event{DeploymentID: "dep-a", EventSeq: 1, Type: EventIngressRouteRendered}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(Event{DeploymentID: "dep-b", EventSeq: 1, Type: EventIngressRouteRendered}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(Event{DeploymentID: "dep-a", EventSeq: 2, Type: EventIngressRouteArtifactWritten}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	events := store.List("dep-a")
	if len(events) != 2 {
		t.Fatalf("List returned %d events, want 2", len(events))
	}
	if events[0].DeploymentID != "dep-a" || events[1].EventSeq != 2 {
		t.Fatalf("unexpected events: %#v", events)
	}

	events[0].DeploymentID = "changed"
	if got := store.List("dep-a")[0].DeploymentID; got != "dep-a" {
		t.Fatalf("List did not return a copy, got deployment ID %q", got)
	}
}

func TestMemoryStoreRejectsNonMonotonicEventSeq(t *testing.T) {
	store := NewMemoryStore()
	if err := store.Append(Event{DeploymentID: "dep-a", EventSeq: 10}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(Event{DeploymentID: "dep-a", EventSeq: 10}); err == nil {
		t.Fatal("expected duplicate event_seq error")
	}
	if err := store.Append(Event{DeploymentID: "dep-a", EventSeq: 9}); err == nil {
		t.Fatal("expected decreasing event_seq error")
	}
}

func TestMemoryStoreRequiresDeploymentIDAndPositiveEventSeq(t *testing.T) {
	store := NewMemoryStore()
	if err := store.Append(Event{EventSeq: 1}); err == nil {
		t.Fatal("expected deployment ID error")
	}
	if err := store.Append(Event{DeploymentID: "dep-a"}); err == nil {
		t.Fatal("expected event_seq error")
	}
}
