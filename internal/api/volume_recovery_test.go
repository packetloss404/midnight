// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/backup"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
)

func TestVolumeBackupAPIQueuesReferenceOnlyWork(t *testing.T) {
	store := &volumeRecoveryStoreStub{}
	router := NewRouterWithDataStack(DataStack{
		VolumeRecoveryStore:             store,
		VolumeRecoveryExecutorAvailable: true,
		IdempotencyStore:                idempotency.NewMemoryStore(),
		Clock: func() time.Time {
			return time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
		},
	})
	request := httptest.NewRequest(
		http.MethodPost,
		v1alphaPrefix+"/volumes/vol_1/backups",
		bytes.NewBufferString(`{"retention_days":7}`),
	)
	request.Header.Set("Idempotency-Key", "volume-backup-one")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if store.createBackup.VolumeID != "vol_1" ||
		!store.createBackup.RetentionUntil.Equal(time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC)) {
		t.Fatalf("create backup = %#v", store.createBackup)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("passphrase")) {
		t.Fatalf("response retained key material: %s", response.Body.String())
	}
	var accepted volumeBackupAccepted
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Run.ID != "vbk_1" || accepted.JobID != "job_1" {
		t.Fatalf("accepted = %#v", accepted)
	}
}

func TestVolumeBackupAPIFailsClosedWithoutDurableStore(t *testing.T) {
	router := NewRouterWithDataStack(DataStack{})
	request := httptest.NewRequest(http.MethodGet, v1alphaPrefix+"/volumes/vol_1/backups", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestVolumeBackupAPIFailsClosedWithoutConfiguredExecutor(t *testing.T) {
	router := NewRouterWithDataStack(DataStack{
		VolumeRecoveryStore: &volumeRecoveryStoreStub{},
		IdempotencyStore:    idempotency.NewMemoryStore(),
	})
	request := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/volumes/vol_1/backups", bytes.NewBufferString(`{}`))
	request.Header.Set("Idempotency-Key", "backup-unavailable")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestVolumeBackupPolicyAPIStoresScheduleAndRetention(t *testing.T) {
	store := &volumeRecoveryStoreStub{}
	router := NewRouterWithDataStack(DataStack{
		VolumeRecoveryStore:             store,
		VolumeRecoveryExecutorAvailable: true,
		IdempotencyStore:                idempotency.NewMemoryStore(),
	})
	request := httptest.NewRequest(
		http.MethodPost,
		v1alphaPrefix+"/volumes/vol_1/backup-policies",
		bytes.NewBufferString(`{"schedule_kind":"daily","retention_days":7}`),
	)
	request.Header.Set("Idempotency-Key", "daily-policy")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if store.createPolicy.VolumeID != "vol_1" ||
		store.createPolicy.ScheduleKind != "daily" ||
		store.createPolicy.RetentionSeconds != 7*24*60*60 {
		t.Fatalf("policy = %#v", store.createPolicy)
	}
}

func TestVolumeRestoreApplyFailsClosedWithoutAtomicCoordinator(t *testing.T) {
	router := NewRouterWithDataStack(DataStack{VolumeRecoveryStore: &volumeRecoveryStoreStub{}})
	request := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/volume-restores/vrs_1/apply", nil)
	request.Header.Set("Idempotency-Key", "restore-apply-unavailable")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable atomic apply status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestVolumeRestoreApplyReturnsAtomicRedeployDecision(t *testing.T) {
	coordinator := &volumeRecoveryApplyStub{
		result: VolumeRestoreApplyResult{
			Restore: backup.VolumeRestoreRun{ID: "vrs_1", State: backup.VolumeRestoreApplied},
			DeploymentAction: DeploymentActionResult{
				OutboxID: "wox_1", Created: true,
			},
			Created: true,
		},
	}
	router := NewRouterWithDataStack(DataStack{VolumeRecoveryApply: coordinator})
	request := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/volume-restores/vrs_1/apply", bytes.NewBufferString(`{}`))
	request.Header.Set("Idempotency-Key", "restore-apply-one")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if coordinator.restoreID != "vrs_1" ||
		coordinator.triggerKey != "rest:volume-restore-apply:vrs_1:restore-apply-one" {
		t.Fatalf("coordinator request = restore %q trigger %q", coordinator.restoreID, coordinator.triggerKey)
	}
}

type volumeRecoveryStoreStub struct {
	createBackup backup.CreateVolumeBackupRun
	createPolicy backup.CreateVolumeBackupPolicy
}

type volumeRecoveryApplyStub struct {
	restoreID  string
	triggerKey string
	result     VolumeRestoreApplyResult
	err        error
}

func (s *volumeRecoveryApplyStub) ApplyRestoreAndRedeploy(_ context.Context, restoreID, triggerKey string) (VolumeRestoreApplyResult, error) {
	s.restoreID = restoreID
	s.triggerKey = triggerKey
	return s.result, s.err
}

func (s *volumeRecoveryStoreStub) CreateBackupWork(_ context.Context, request backup.CreateVolumeBackupRun) (backup.VolumeBackupWork, error) {
	s.createBackup = request
	return backup.VolumeBackupWork{
		Run: backup.VolumeBackupRun{
			ID: "vbk_1", VolumeID: request.VolumeID, State: backup.VolumeBackupPending,
			RetentionUntil: request.RetentionUntil,
		},
		Job: queue.Job{ID: "job_1", Queue: queue.Cleanup, State: queue.JobQueued},
	}, nil
}
func (*volumeRecoveryStoreStub) ListBackups(context.Context, string, string, string, string) ([]backup.VolumeBackupRun, error) {
	return []backup.VolumeBackupRun{}, nil
}
func (*volumeRecoveryStoreStub) GetBackup(context.Context, string) (backup.VolumeBackupRun, error) {
	return backup.VolumeBackupRun{}, backup.ErrVolumeRunNotFound
}
func (*volumeRecoveryStoreStub) CreateRestoreWork(context.Context, backup.CreateVolumeRestoreRun) (backup.VolumeRestoreWork, error) {
	return backup.VolumeRestoreWork{}, errors.New("not used")
}
func (*volumeRecoveryStoreStub) GetRestore(context.Context, string) (backup.VolumeRestoreRun, error) {
	return backup.VolumeRestoreRun{}, backup.ErrVolumeRunNotFound
}
func (*volumeRecoveryStoreStub) ListRestores(context.Context, string) ([]backup.VolumeRestoreRun, error) {
	return []backup.VolumeRestoreRun{}, nil
}
func (*volumeRecoveryStoreStub) ApplyRestore(context.Context, string) (backup.VolumeRestoreRun, error) {
	return backup.VolumeRestoreRun{}, errors.New("unsafe apply must not be called")
}
func (s *volumeRecoveryStoreStub) CreatePolicy(_ context.Context, request backup.CreateVolumeBackupPolicy) (backup.VolumeBackupPolicy, error) {
	s.createPolicy = request
	return backup.VolumeBackupPolicy{
		ID: "vbp_1", VolumeID: request.VolumeID, ScheduleKind: request.ScheduleKind,
		RetentionSeconds: request.RetentionSeconds,
	}, nil
}
func (*volumeRecoveryStoreStub) ListPolicies(context.Context, string) ([]backup.VolumeBackupPolicy, error) {
	return nil, nil
}
func (*volumeRecoveryStoreStub) DeletePolicy(context.Context, string, string) error {
	return errors.New("not used")
}
