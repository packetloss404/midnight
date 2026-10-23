package build

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordingBuildProgressSink struct {
	events []BuildProgressEvent
}

func (s *recordingBuildProgressSink) Emit(event BuildProgressEvent) error {
	s.events = append(s.events, event)
	return nil
}

func TestBuildWorkerExecuteEmitsProgressAndReturnsSnapshotDraft(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	plan := validWorkerBuildPlan()
	plan.SnapshotDraft = DeploymentSnapshot{Build: BuildSnapshot{SourceRef: "refs/heads/main"}}
	sink := &recordingBuildProgressSink{}
	worker := BuildWorker{
		OperationID: "build-123",
		DiskUsage: func(context.Context) (DiskUsage, error) {
			return DiskUsage{TotalBytes: 1000, FreeBytes: 400}, nil
		},
		Now: func() time.Time { return now },
	}

	snapshot, err := worker.Execute(context.Background(), plan, sink)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if snapshot.SchemaVersion != DeploymentSnapshotSchemaVersion || snapshot.CreatedAt != now || snapshot.Build.SourceRef != "refs/heads/main" {
		t.Fatalf("snapshot draft = %#v", snapshot)
	}
	if len(sink.events) != len(DefaultBuildOperationPlan().Steps)+1 {
		t.Fatalf("events length = %d", len(sink.events))
	}
	last := sink.events[len(sink.events)-1]
	if last.ID != "build-123" || last.Status != BuildProgressCompleted || !last.Terminal() {
		t.Fatalf("last event = %#v", last)
	}
}

func TestPlanBuildWorkerExecutionRejectsDiskBelowThresholds(t *testing.T) {
	plan := validWorkerBuildPlan()
	plan.Worker.MinFreeDiskBytes = 500

	_, err := PlanBuildWorkerExecution("build-123", plan, DiskUsage{TotalBytes: 1000, FreeBytes: 499}, DefaultBuildOperationPlan(), time.Time{})
	if err == nil {
		t.Fatal("PlanBuildWorkerExecution() error = nil, want free disk rejection")
	}
}

func TestPlanBuildWorkerExecutionRejectsDiskUsageAboveThreshold(t *testing.T) {
	plan := validWorkerBuildPlan()
	plan.Worker.MaxDiskUsagePercent = 80

	_, err := PlanBuildWorkerExecution("build-123", plan, DiskUsage{TotalBytes: 1000, FreeBytes: 190}, DefaultBuildOperationPlan(), time.Time{})
	if err == nil {
		t.Fatal("PlanBuildWorkerExecution() error = nil, want disk usage rejection")
	}
}

func TestBuildWorkerExecuteReturnsSinkError(t *testing.T) {
	want := errors.New("sink failed")
	worker := BuildWorker{
		DiskUsage: func(context.Context) (DiskUsage, error) {
			return DiskUsage{TotalBytes: 1000, FreeBytes: 500}, nil
		},
	}

	_, err := worker.Execute(context.Background(), validWorkerBuildPlan(), failingBuildProgressSink{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("Execute() error = %v, want %v", err, want)
	}
}

type failingBuildProgressSink struct {
	err error
}

func (s failingBuildProgressSink) Emit(BuildProgressEvent) error {
	return s.err
}

func validWorkerBuildPlan() BuildPlan {
	return BuildPlan{
		Froms:  ParseDockerfileFroms("FROM alpine@sha256:base AS runtime\n"),
		Worker: WorkerConfig{Timeout: time.Minute, MaxConcurrency: 1, MinFreeDiskBytes: 100, MaxDiskUsagePercent: 90},
	}
}
