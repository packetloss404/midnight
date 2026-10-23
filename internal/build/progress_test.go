package build

import (
	"testing"
	"time"
)

func TestBuildProgressEventTerminal(t *testing.T) {
	if (BuildProgressEvent{Status: BuildProgressRunning}).Terminal() {
		t.Fatal("running event reported terminal")
	}
	if !(BuildProgressEvent{Status: BuildProgressCompleted}).Terminal() {
		t.Fatal("completed event did not report terminal")
	}
	if !(BuildProgressEvent{Status: BuildProgressFailed}).Terminal() {
		t.Fatal("failed event did not report terminal")
	}
}

func TestBuildProgressEventsFromOperationPlan(t *testing.T) {
	startedAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	events := BuildProgressEventsFromOperationPlan("build-1", DefaultBuildOperationPlan(), startedAt)

	if len(events) != 6 {
		t.Fatalf("events length = %d, want 6", len(events))
	}
	if events[0].Vertex != "queued" || events[0].Status != BuildProgressQueued {
		t.Fatalf("first event = %#v", events[0])
	}
	if events[1].Vertex != "building" || events[1].Status != BuildProgressRunning {
		t.Fatalf("second event = %#v", events[1])
	}
	if events[5].Vertex != "deploy" || events[5].Timestamp != startedAt.Add(5*time.Second) {
		t.Fatalf("last event = %#v", events[5])
	}
	for _, event := range events {
		if event.ID != "build-1" || event.Total != int64(len(events)) {
			t.Fatalf("event contract = %#v", event)
		}
	}
}
