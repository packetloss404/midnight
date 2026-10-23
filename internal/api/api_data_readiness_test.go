package api

import (
	"context"
	"errors"
	"testing"

	"github.com/midnight/midnight/internal/db"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestBuildAPIDataReadinessSummaryRollsUpNeedsReview(t *testing.T) {
	store := NewMemoryOperationStore()
	if _, err := store.Create(context.Background(), orailtypes.Operation{ID: "op-1", State: orailtypes.OperationQueued}); err != nil {
		t.Fatal(err)
	}
	packet := db.ReleaseEvidencePacket{Status: db.ReleaseEvidenceStatusNeedsReview, IssueCodes: []string{db.ReleaseEvidenceIssuePendingMigration}}
	index := db.APISchemaEvidenceIndex{Status: db.APISchemaEvidenceIndexStatusNeedsReview}

	got := BuildAPIDataReadinessSummary(context.Background(), APIDataReadinessInput{
		ReleaseEvidence: packet,
		EvidenceIndex:   index,
		OpenAPIYAML:     []byte("openapi: 3.1.0\n"),
		OperationStore:  store,
	})

	if got.Status != APIDataReadinessStatusNeedsReview {
		t.Fatalf("status = %q, want %q", got.Status, APIDataReadinessStatusNeedsReview)
	}
	if got.OpenAPI.Status != APIDataReadinessStatusReady || !got.OpenAPI.Available || got.OpenAPI.ByteCount == 0 {
		t.Fatalf("openapi = %#v, want ready available", got.OpenAPI)
	}
	if got.OperationStore.Status != APIDataReadinessStatusReady || !got.OperationStore.ListAvailable || got.OperationStore.OperationCount != 1 {
		t.Fatalf("operation store = %#v, want ready count 1", got.OperationStore)
	}
	wantCodes := []string{APIDataReadinessIssueReleaseEvidence, APIDataReadinessIssueEvidenceIndex}
	if len(got.IssueCodes) != len(wantCodes) || got.IssueCodes[0] != wantCodes[0] || got.IssueCodes[1] != wantCodes[1] {
		t.Fatalf("issue codes = %#v, want %#v", got.IssueCodes, wantCodes)
	}
}

func TestBuildAPIDataReadinessSummaryBlocksOnUnavailableOpenAPIAndStore(t *testing.T) {
	got := BuildAPIDataReadinessSummary(context.Background(), APIDataReadinessInput{
		ReleaseEvidence: db.ReleaseEvidencePacket{Status: db.ReleaseEvidenceStatusReady},
		EvidenceIndex:   db.APISchemaEvidenceIndex{Status: db.APISchemaEvidenceIndexStatusReady},
		OpenAPIYAML:     []byte{},
		OperationStore:  failingOperationStore{},
	})

	if got.Status != APIDataReadinessStatusBlocked {
		t.Fatalf("status = %q, want %q", got.Status, APIDataReadinessStatusBlocked)
	}
	if got.OpenAPI.Status != APIDataReadinessStatusBlocked || got.OpenAPI.Available {
		t.Fatalf("openapi = %#v, want blocked unavailable", got.OpenAPI)
	}
	if got.OperationStore.Status != APIDataReadinessStatusBlocked || got.OperationStore.ListAvailable {
		t.Fatalf("operation store = %#v, want blocked unavailable", got.OperationStore)
	}
	wantCodes := []string{APIDataReadinessIssueOpenAPIEmpty, APIDataReadinessIssueOperationStore}
	if len(got.IssueCodes) != len(wantCodes) || got.IssueCodes[0] != wantCodes[0] || got.IssueCodes[1] != wantCodes[1] {
		t.Fatalf("issue codes = %#v, want %#v", got.IssueCodes, wantCodes)
	}
}

type failingOperationStore struct{}

func (failingOperationStore) Create(context.Context, orailtypes.Operation) (orailtypes.Operation, error) {
	return orailtypes.Operation{}, errors.New("unavailable")
}

func (failingOperationStore) Get(context.Context, orailtypes.ID) (orailtypes.Operation, error) {
	return orailtypes.Operation{}, errors.New("unavailable")
}

func (failingOperationStore) List(context.Context, OperationListFilter) ([]orailtypes.Operation, error) {
	return nil, errors.New("unavailable")
}

func (failingOperationStore) Transition(context.Context, orailtypes.ID, orailtypes.OperationState) (orailtypes.Operation, error) {
	return orailtypes.Operation{}, errors.New("unavailable")
}

func (failingOperationStore) Fail(context.Context, orailtypes.ID, orailtypes.FailureCode, string) (orailtypes.Operation, error) {
	return orailtypes.Operation{}, errors.New("unavailable")
}

func (failingOperationStore) AppendEvent(context.Context, OperationEvent) (OperationEvent, error) {
	return OperationEvent{}, errors.New("unavailable")
}

func (failingOperationStore) ListEvents(context.Context, orailtypes.ID) ([]OperationEvent, error) {
	return nil, errors.New("unavailable")
}
