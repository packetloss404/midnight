package build

import "time"

type BuildProgressStatus string

const (
	BuildProgressQueued    BuildProgressStatus = "queued"
	BuildProgressRunning   BuildProgressStatus = "running"
	BuildProgressCompleted BuildProgressStatus = "completed"
	BuildProgressFailed    BuildProgressStatus = "failed"
)

// BuildProgressEvent is the stable control-plane shape for build progress.
type BuildProgressEvent struct {
	ID        string
	Vertex    string
	Name      string
	Status    BuildProgressStatus
	Timestamp time.Time
	Current   int64
	Total     int64
	Cached    bool
	Message   string
	Error     string
}

func (e BuildProgressEvent) Terminal() bool {
	return e.Status == BuildProgressCompleted || e.Status == BuildProgressFailed
}

func BuildProgressEventsFromOperationPlan(operationID string, plan BuildOperationPlan, startedAt time.Time) []BuildProgressEvent {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}

	events := make([]BuildProgressEvent, 0, len(plan.Steps))
	for i, step := range plan.Steps {
		status := BuildProgressRunning
		if step.State == BuildOperationQueued {
			status = BuildProgressQueued
		}
		events = append(events, BuildProgressEvent{
			ID:        operationID,
			Vertex:    string(step.State),
			Name:      step.Name,
			Status:    status,
			Timestamp: startedAt.Add(time.Duration(i) * time.Second),
			Current:   int64(i),
			Total:     int64(len(plan.Steps)),
		})
	}

	return events
}
