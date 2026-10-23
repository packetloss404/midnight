package multihost_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/scheduler"
	"github.com/midnight/midnight/internal/scheduler/multihost"
)

func TestMultiHostSchedulerSchedulePicksFirstFitDockerHost(t *testing.T) {
	hosts := &fakeHostRepo{records: []multihost.HostRecord{
		{ID: "host-b", Labels: map[string]string{"runtime": "docker"}},
		{ID: "host-a", Labels: map[string]string{"runtime": "podman"}},
		{ID: "host-c", Labels: map[string]string{"runtime": "docker"}},
	}}
	writer := &fakeDesiredWriter{}
	s := multihost.New(hosts, writer)

	placement, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{
		OperationID:     "op-1",
		ScheduleRequest: scheduleRequest(),
	})
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	if placement.PrimaryHostID() != "host-b" {
		t.Fatalf("primary host = %q, want host-b (first alphabetical docker host)", placement.PrimaryHostID())
	}
	if writer.agentID != "host-b" {
		t.Fatalf("desired state written for %q, want host-b", writer.agentID)
	}
	if writer.etag == "" {
		t.Fatalf("desired state etag is empty")
	}
	if placement.InstanceStatus.State != runtime.InstanceStateAllocated {
		t.Fatalf("placement instance state = %q, want allocated", placement.InstanceStatus.State)
	}
	// Verify the payload shape carries the runtime ScheduleRequest the
	// agent's reconciler will consume.
	var decoded struct {
		Instances []struct {
			DeploymentID string `json:"deployment_id"`
			InstanceID   string `json:"instance_id"`
			Image        string `json:"image"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(writer.payload, &decoded); err != nil {
		t.Fatalf("decode desired payload: %v", err)
	}
	if len(decoded.Instances) != 1 {
		t.Fatalf("desired instances = %#v, want one", decoded.Instances)
	}
	if decoded.Instances[0].DeploymentID != "dep-1" || decoded.Instances[0].Image != "image:sha256:abc" {
		t.Fatalf("desired instance = %#v, want dep-1 + image", decoded.Instances[0])
	}
}

func TestMultiHostSchedulerScheduleRejectsWhenNoHostMatches(t *testing.T) {
	hosts := &fakeHostRepo{records: []multihost.HostRecord{
		{ID: "host-a", Labels: map[string]string{"runtime": "podman"}},
	}}
	s := multihost.New(hosts, &fakeDesiredWriter{})

	_, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{ScheduleRequest: scheduleRequest()})
	if !errors.Is(err, multihost.ErrNoEligibleHost) {
		t.Fatalf("Schedule() error = %v, want ErrNoEligibleHost", err)
	}
}

func TestMultiHostSchedulerRespectsExplicitCapabilities(t *testing.T) {
	hosts := &fakeHostRepo{records: []multihost.HostRecord{
		{ID: "host-cpu", Labels: map[string]string{"runtime": "docker"}},
		{ID: "host-gpu", Labels: map[string]string{"runtime": "docker", "gpu": "true"}},
	}}
	writer := &fakeDesiredWriter{}
	s := multihost.New(hosts, writer)

	placement, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{
		ScheduleRequest:      scheduleRequest(),
		RequiredCapabilities: map[string]string{"gpu": "true"},
	})
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	if placement.PrimaryHostID() != "host-gpu" {
		t.Fatalf("primary host = %q, want host-gpu", placement.PrimaryHostID())
	}
}

func TestMultiHostSchedulerReconcileReturnsNotImplemented(t *testing.T) {
	s := multihost.New(&fakeHostRepo{}, &fakeDesiredWriter{})
	_, err := s.Reconcile(context.Background(), "dep-1")
	if !errors.Is(err, scheduler.ErrNotImplemented) {
		t.Fatalf("Reconcile() error = %v, want scheduler.ErrNotImplemented", err)
	}
}

func TestMultiHostSchedulerDrainReturnsNotImplemented(t *testing.T) {
	s := multihost.New(&fakeHostRepo{}, &fakeDesiredWriter{})
	err := s.Drain(context.Background(), "dep-1", 5*time.Second)
	if !errors.Is(err, scheduler.ErrNotImplemented) {
		t.Fatalf("Drain() error = %v, want scheduler.ErrNotImplemented", err)
	}
}

func TestMultiHostSchedulerStateReturnsNotImplemented(t *testing.T) {
	s := multihost.New(&fakeHostRepo{}, &fakeDesiredWriter{})
	_, err := s.State(context.Background(), "dep-1")
	if !errors.Is(err, scheduler.ErrNotImplemented) {
		t.Fatalf("State() error = %v, want scheduler.ErrNotImplemented", err)
	}
}

func TestMultiHostSchedulerImplementsSchedulerInterface(t *testing.T) {
	var _ scheduler.Scheduler = (*multihost.MultiHostScheduler)(nil)
}

func TestMultiHostSchedulerRequiresHostRepository(t *testing.T) {
	s := multihost.New(nil, &fakeDesiredWriter{})
	_, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{ScheduleRequest: scheduleRequest()})
	if !errors.Is(err, multihost.ErrHostRepositoryRequired) {
		t.Fatalf("Schedule() error = %v, want ErrHostRepositoryRequired", err)
	}
}

func TestMultiHostSchedulerRequiresDesiredStateWriter(t *testing.T) {
	s := multihost.New(&fakeHostRepo{records: []multihost.HostRecord{{ID: "host-a", Labels: map[string]string{"runtime": "docker"}}}}, nil)
	_, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{ScheduleRequest: scheduleRequest()})
	if !errors.Is(err, multihost.ErrDesiredStateWriterRequired) {
		t.Fatalf("Schedule() error = %v, want ErrDesiredStateWriterRequired", err)
	}
}

// --- fixtures -------------------------------------------------------------

func scheduleRequest() runtime.ScheduleRequest {
	return runtime.ScheduleRequest{
		DeploymentID:   "dep-1",
		InstanceID:     "dep-1-r0",
		ProjectID:      "prj-1",
		EnvironmentID:  "prod",
		ServiceID:      "api",
		Image:          "image:sha256:abc",
		IdempotencyKey: "op-1",
	}
}

type fakeHostRepo struct {
	records []multihost.HostRecord
	err     error
}

func (r *fakeHostRepo) HealthyHosts(_ context.Context, _ time.Duration) ([]multihost.HostRecord, error) {
	return r.records, r.err
}

type fakeDesiredWriter struct {
	agentID string
	etag    string
	payload []byte
	err     error
}

func (w *fakeDesiredWriter) WriteDesiredState(_ context.Context, agentID string, etag string, payload []byte) error {
	w.agentID = agentID
	w.etag = etag
	w.payload = append([]byte(nil), payload...)
	return w.err
}
