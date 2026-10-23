package registry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestFilePinStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	store, err := NewFilePinStore(path)
	if err != nil {
		t.Fatal(err)
	}
	row := PinRow{
		Repository:   "midnight/demo",
		Digest:       "sha256:" + repeatHex64('a'),
		ServiceID:    "svc",
		DeploymentID: "dep-1",
		Reason:       PinReasonActive,
		CreatedAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := store.Insert(row); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFilePinStore(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := reopened.Rows()
	if len(rows) != 1 || rows[0].Digest != row.Digest {
		t.Fatalf("expected 1 row after reopen, got %d", len(rows))
	}
	if reopened.PinCount(row.Digest, time.Now().UTC()) != 1 {
		t.Fatal("expected pin count 1 after reopen")
	}
}

func TestFilePinStoreRejectsDuplicateInsert(t *testing.T) {
	store, err := NewFilePinStore(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	row := PinRow{
		Repository:   "midnight/demo",
		Digest:       "sha256:" + repeatHex64('a'),
		ServiceID:    "svc",
		DeploymentID: "dep-1",
		Reason:       PinReasonActive,
	}
	if err := store.Insert(row); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(row); err == nil {
		t.Fatal("duplicate insert accepted")
	}
}

func TestFilePinStoreDecrementRemovesRow(t *testing.T) {
	store, err := NewFilePinStore(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	row := PinRow{
		Repository:   "midnight/demo",
		Digest:       "sha256:" + repeatHex64('a'),
		ServiceID:    "svc",
		DeploymentID: "dep-1",
		Reason:       PinReasonActive,
	}
	if err := store.Insert(row); err != nil {
		t.Fatal(err)
	}
	if err := store.Decrement(row.Digest, row.ServiceID, row.DeploymentID, row.Reason); err != nil {
		t.Fatalf("decrement: %v", err)
	}
	if store.PinCount(row.Digest, time.Now().UTC()) != 0 {
		t.Fatal("expected pin count 0 after decrement")
	}
}

func TestGCOrchestratorRefusesProtectedDigests(t *testing.T) {
	store, err := NewFilePinStore(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	protectedDigest := "sha256:" + repeatHex64('a')
	freeDigest := "sha256:" + repeatHex64('b')
	if err := store.Insert(PinRow{
		Repository:   "midnight/demo",
		Digest:       protectedDigest,
		ServiceID:    "svc",
		DeploymentID: "dep-1",
		Reason:       PinReasonActive,
	}); err != nil {
		t.Fatal(err)
	}

	tags := []ImageTag{
		{Repository: "midnight/demo", Name: "v1", Digest: protectedDigest, SizeBytes: 100, PushedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Repository: "midnight/demo", Name: "v2", Digest: freeDigest, SizeBytes: 200, PushedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
	}
	orchestrator := GCOrchestrator{
		Tags:         tags,
		Rules:        []RetentionRule{{Repository: "midnight/demo", KeepLast: 0}},
		Probe:        store,
		ConfirmApply: true,
		GeneratedAt:  time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
	}
	plan, err := orchestrator.PlanGC()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(plan.Deletions()); got == 0 {
		t.Fatal("expected at least one candidate deletion")
	}
	evidence, err := orchestrator.ApplyGC(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range evidence.Reclaimed {
		if action.Digest == protectedDigest {
			t.Fatalf("protected digest %s reclaimed", protectedDigest)
		}
	}
	hasProtected := false
	for _, action := range evidence.Protected {
		if action.Digest == protectedDigest {
			hasProtected = true
		}
	}
	if !hasProtected {
		t.Fatal("expected protected digest to appear in evidence.Protected")
	}
}

type fakeWritePauseController struct {
	paused, resumed int
	pauseErr        error
}

func (f *fakeWritePauseController) PauseWrites(context.Context) error {
	f.paused++
	return f.pauseErr
}
func (f *fakeWritePauseController) ResumeWrites(context.Context) error {
	f.resumed++
	return nil
}

func TestGCOrchestratorPausesWritesAroundApply(t *testing.T) {
	pause := &fakeWritePauseController{}
	store, err := NewFilePinStore(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	tags := []ImageTag{
		{Repository: "midnight/demo", Name: "v1", Digest: "sha256:" + repeatHex64('a'), SizeBytes: 100, PushedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	orchestrator := GCOrchestrator{
		Tags:         tags,
		Probe:        store,
		WritePause:   pause,
		ConfirmApply: true,
		GeneratedAt:  time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
	}
	plan, err := orchestrator.PlanGC()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orchestrator.ApplyGC(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if pause.paused != 1 || pause.resumed != 1 {
		t.Fatalf("PauseWrites/ResumeWrites called %d/%d times, want 1/1", pause.paused, pause.resumed)
	}
}

func TestGCOrchestratorPropagatesPauseError(t *testing.T) {
	store, _ := NewFilePinStore(filepath.Join(t.TempDir(), "pins.json"))
	orchestrator := GCOrchestrator{
		Tags:         []ImageTag{{Repository: "demo", Name: "v1", Digest: "sha256:" + repeatHex64('a')}},
		Probe:        store,
		WritePause:   &fakeWritePauseController{pauseErr: errors.New("io")},
		ConfirmApply: true,
	}
	plan, err := orchestrator.PlanGC()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orchestrator.ApplyGC(context.Background(), plan); err == nil {
		t.Fatal("expected pause error to propagate")
	}
}

func repeatHex64(b byte) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = b
	}
	return string(out)
}
