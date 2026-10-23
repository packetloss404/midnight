package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

var _ Store = (*PostgresStore)(nil)
var _ QueuedCancelStore = (*PostgresStore)(nil)

func TestQueueNamesUseStableWireValues(t *testing.T) {
	if Cleanup != "cleanup" || Deploy != "deploy" {
		t.Fatalf("unexpected queue names: %q %q", Cleanup, Deploy)
	}
}

func TestHeartbeatIntervalUsesLeaseTTLThird(t *testing.T) {
	got, err := HeartbeatInterval(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != 10*time.Second {
		t.Fatalf("HeartbeatInterval() = %s, want 10s", got)
	}
}

func TestHeartbeatIntervalRejectsInvalidTTL(t *testing.T) {
	if _, err := HeartbeatInterval(0); !errors.Is(err, ErrInvalidLeaseHeartbeat) {
		t.Fatalf("HeartbeatInterval() error = %v, want ErrInvalidLeaseHeartbeat", err)
	}
}

func TestHeartbeatLeaseExtendsLease(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	store := &heartbeatStore{}
	until, err := HeartbeatLease(context.Background(), store, Job{ID: "job_1", LeaseID: "lease_1"}, 45*time.Second, now)
	if err != nil {
		t.Fatal(err)
	}
	if until != now.Add(45*time.Second) || store.until != until {
		t.Fatalf("until = %s, stored = %s", until, store.until)
	}
	if store.jobID != "job_1" || store.leaseID != "lease_1" {
		t.Fatalf("heartbeat identifiers = %q/%q", store.jobID, store.leaseID)
	}
}

func TestParentOperationIDFromRequest(t *testing.T) {
	cases := []struct {
		name    string
		request EnqueueRequest
		want    string
	}{
		{
			name:    "explicit field",
			request: EnqueueRequest{ParentOperationID: "op_explicit", Payload: []byte(`{"operation_id":"op_payload"}`)},
			want:    "op_explicit",
		},
		{
			name:    "payload compatibility",
			request: EnqueueRequest{Payload: []byte(`{"operation_id":"op_payload"}`)},
			want:    "op_payload",
		},
		{
			name:    "missing metadata",
			request: EnqueueRequest{Payload: []byte(`{"kind":"deploy"}`)},
			want:    "",
		},
		{
			name:    "invalid json",
			request: EnqueueRequest{Payload: []byte(`not-json`)},
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parentOperationIDFromRequest(tc.request); got != tc.want {
				t.Fatalf("parentOperationIDFromRequest() = %q, want %q", got, tc.want)
			}
		})
	}
}

type heartbeatStore struct {
	jobID   string
	leaseID string
	until   time.Time
}

func (s *heartbeatStore) Enqueue(context.Context, EnqueueRequest) (Job, error) { return Job{}, nil }
func (s *heartbeatStore) Lease(context.Context, LeaseRequest) (Job, error)     { return Job{}, nil }
func (s *heartbeatStore) Heartbeat(_ context.Context, jobID string, leaseID string, until time.Time) error {
	s.jobID = jobID
	s.leaseID = leaseID
	s.until = until
	return nil
}
func (s *heartbeatStore) Complete(context.Context, string, string) error     { return nil }
func (s *heartbeatStore) Fail(context.Context, string, string, string) error { return nil }
