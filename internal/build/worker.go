package build

import (
	"context"
	"fmt"
	"time"
)

type DiskUsage struct {
	TotalBytes int64
	FreeBytes  int64
}

type DiskUsageProbe func(context.Context) (DiskUsage, error)

type BuildProgressSink interface {
	Emit(BuildProgressEvent) error
}

type BuildWorker struct {
	OperationID   string
	DiskUsage     DiskUsageProbe
	OperationPlan BuildOperationPlan
	Now           func() time.Time
}

type BuildWorkerExecutionPlan struct {
	OperationID   string
	Events        []BuildProgressEvent
	SnapshotDraft DeploymentSnapshot
}

func (w BuildWorker) Execute(ctx context.Context, plan BuildPlan, sink BuildProgressSink) (DeploymentSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if w.DiskUsage == nil {
		return DeploymentSnapshot{}, fmt.Errorf("build worker disk usage probe is required")
	}

	disk, err := w.DiskUsage(ctx)
	if err != nil {
		return DeploymentSnapshot{}, fmt.Errorf("probe build worker disk usage: %w", err)
	}

	execution, err := PlanBuildWorkerExecution(w.operationID(), plan, disk, w.operationPlan(), w.now())
	if err != nil {
		return DeploymentSnapshot{}, err
	}

	for _, event := range execution.Events {
		if err := ctx.Err(); err != nil {
			return DeploymentSnapshot{}, err
		}
		if sink == nil {
			continue
		}
		if err := sink.Emit(event); err != nil {
			return DeploymentSnapshot{}, fmt.Errorf("emit build progress: %w", err)
		}
	}

	return execution.SnapshotDraft, nil
}

func PlanBuildWorkerExecution(operationID string, plan BuildPlan, disk DiskUsage, operationPlan BuildOperationPlan, startedAt time.Time) (BuildWorkerExecutionPlan, error) {
	if operationID == "" {
		operationID = "build"
	}
	if err := plan.Validate(); err != nil {
		return BuildWorkerExecutionPlan{}, err
	}
	if err := validateDiskThresholds(plan.Worker, disk); err != nil {
		return BuildWorkerExecutionPlan{}, err
	}
	if len(operationPlan.Steps) == 0 {
		operationPlan = DefaultBuildOperationPlan()
	}
	if err := operationPlan.Validate(); err != nil {
		return BuildWorkerExecutionPlan{}, err
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}

	events := BuildProgressEventsFromOperationPlan(operationID, operationPlan, startedAt)
	final := BuildProgressEvent{
		ID:        operationID,
		Vertex:    string(BuildOperationSnapshot),
		Name:      "snapshot draft",
		Status:    BuildProgressCompleted,
		Timestamp: startedAt.Add(time.Duration(len(events)) * time.Second),
		Current:   int64(len(events)),
		Total:     int64(len(events) + 1),
	}
	for i := range events {
		events[i].Total = final.Total
	}
	events = append(events, final)

	snapshot := plan.SnapshotDraft
	if snapshot.SchemaVersion == "" {
		snapshot.SchemaVersion = DeploymentSnapshotSchemaVersion
	}
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = startedAt
	}

	return BuildWorkerExecutionPlan{OperationID: operationID, Events: events, SnapshotDraft: snapshot}, nil
}

func validateDiskThresholds(config WorkerConfig, disk DiskUsage) error {
	if disk.FreeBytes < 0 || disk.TotalBytes < 0 || disk.FreeBytes > disk.TotalBytes {
		return fmt.Errorf("build worker disk usage is invalid")
	}
	if disk.FreeBytes < config.MinFreeDiskBytes {
		return fmt.Errorf("build worker free disk bytes %d below minimum %d", disk.FreeBytes, config.MinFreeDiskBytes)
	}
	if config.MaxDiskUsagePercent > 0 {
		if disk.TotalBytes == 0 {
			return fmt.Errorf("build worker total disk bytes is required")
		}
		usedPercent := int(((disk.TotalBytes - disk.FreeBytes) * 100) / disk.TotalBytes)
		if usedPercent > config.MaxDiskUsagePercent {
			return fmt.Errorf("build worker disk usage percent %d above maximum %d", usedPercent, config.MaxDiskUsagePercent)
		}
	}

	return nil
}

func (w BuildWorker) operationID() string {
	if w.OperationID != "" {
		return w.OperationID
	}
	return "build"
}

func (w BuildWorker) operationPlan() BuildOperationPlan {
	if len(w.OperationPlan.Steps) > 0 {
		return w.OperationPlan
	}
	return DefaultBuildOperationPlan()
}

func (w BuildWorker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}
