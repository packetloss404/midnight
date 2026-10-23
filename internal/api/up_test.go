// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func buildSampleTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "Dockerfile",
		Mode:     0o644,
		Size:     int64(len("FROM scratch\n")),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("FROM scratch\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     "app/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     "app/main.go",
		Mode:     0o644,
		Size:     int64(len("package main\n")),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("package main\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildEmptyEntryTarball(t *testing.T, count int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for index := range count {
		if err := tw.WriteHeader(&tar.Header{
			Name:     fmt.Sprintf("empty/%05d", index),
			Mode:     0o644,
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildSingleEntryTarball(t *testing.T, header *tar.Header, trailing []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if len(trailing) > 0 {
		if _, err := gz.Write(trailing); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestServiceUpAcceptsTarballAndCreatesOperation(t *testing.T) {
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", t.TempDir())
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{jobID: "job_up_1"}
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
		Clock:            func() time.Time { return now },
	})

	tarball := buildSampleTarball(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/up", bytes.NewReader(tarball))
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Idempotency-Key", "up-1")
	req.Header.Set("X-Midnight-Commit-Message", "feat: add Dockerfile")
	req.Header.Set("X-Midnight-Environment", "env_prod")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	var response upOperationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.OperationID == "" || response.DeploymentID == "" || response.Operation.Kind != "build" || response.Operation.State != orailtypes.OperationQueued {
		t.Fatalf("response = %#v, want queued build operation", response)
	}
	if len(queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d, want 1", len(queueStore.requests))
	}
	enqueued := queueStore.requests[0]
	if enqueued.Queue != queue.Build {
		t.Fatalf("queue = %q, want build", enqueued.Queue)
	}
	var payload deployJobPayload
	if err := json.Unmarshal(enqueued.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Request.Source == nil || payload.Request.Source.Repo == "" {
		t.Fatalf("source = %#v, want non-empty local source repo", payload.Request.Source)
	}
	if payload.Request.DeploymentID != response.DeploymentID ||
		payload.Candidate.DeploymentID != response.DeploymentID ||
		payload.Request.InstanceID != response.DeploymentID+"-r0" {
		t.Fatalf("payload canonical identity = request %#v candidate %#v response %#v", payload.Request, payload.Candidate, response)
	}
	if got := payload.Request.Source.Repo[:8]; got != "local://" {
		t.Fatalf("source.repo prefix = %q, want local://", got)
	}
	if got := payload.Request.Source.Provider; got != DeploySourceProviderLegacyLocal {
		t.Fatalf("source.provider = %q, want %q", got, DeploySourceProviderLegacyLocal)
	}
	checkoutPath := strings.TrimPrefix(payload.Request.Source.Repo, "local://")
	if info, err := os.Stat(checkoutPath); err != nil || !info.IsDir() {
		t.Fatalf("queued checkout path = %q stat=%v info=%v", checkoutPath, err, info)
	}
	matches, err := filepath.Glob(filepath.Join(upExtractionRoot(), "midnight-up-*.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("staged tarballs were not removed: %v", matches)
	}
}

func TestServiceUpReplaysStoredFailureAfterOperationAttachment(t *testing.T) {
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", t.TempDir())
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "up-replay"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	jobs := &recordingQueueStore{err: errors.New("queue unavailable")}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   NewMemoryOperationStore(),
		DeploymentStore:  NewMemoryDeploymentStore(nil),
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       jobs,
	})
	tarball := buildSampleTarball(t)
	doPost := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/up", bytes.NewReader(tarball))
		req.Header.Set("Content-Type", "application/gzip")
		req.Header.Set("Idempotency-Key", "up-attached-failure-replay")
		req.Header.Set("X-Midnight-Environment", "env_prod")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	first := doPost()
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d body=%s, want 500", first.Code, first.Body.String())
	}
	replay := doPost()
	if replay.Code != first.Code ||
		replay.Header().Get("Idempotent-Replayed") != "true" ||
		replay.Header().Get("Content-Type") != first.Header().Get("Content-Type") ||
		replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d/%q/%q/%s, want exact stored %d/%q/%s", replay.Code, replay.Header().Get("Idempotent-Replayed"), replay.Header().Get("Content-Type"), replay.Body.String(), first.Code, first.Header().Get("Content-Type"), first.Body.String())
	}
	if len(jobs.requests) != 1 {
		t.Fatalf("queue attempts = %d, want one", len(jobs.requests))
	}
}

func TestServiceUpReplaysExactSuccessWithoutDuplicateJob(t *testing.T) {
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", t.TempDir())
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "up-success-replay"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operations := NewMemoryOperationStore()
	jobs := &recordingQueueStore{jobID: "job_up_replay"}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operations,
		DeploymentStore:  NewMemoryDeploymentStore(nil),
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       jobs,
	})
	tarball := buildSampleTarball(t)
	doPost := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/up", bytes.NewReader(tarball))
		req.Header.Set("Content-Type", "application/gzip")
		req.Header.Set("Idempotency-Key", "up-exact-success-replay")
		req.Header.Set("X-Midnight-Environment", "env_prod")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	first := doPost()
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d body=%s, want 202", first.Code, first.Body.String())
	}
	var accepted upOperationResponse
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Transition(context.Background(), accepted.Operation.ID, orailtypes.OperationRunning); err != nil {
		t.Fatal(err)
	}
	replay := doPost()
	if replay.Code != first.Code ||
		replay.Header().Get("Idempotent-Replayed") != "true" ||
		replay.Header().Get("Content-Type") != first.Header().Get("Content-Type") ||
		replay.Header().Get("Location") != first.Header().Get("Location") ||
		replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d/%q/%q/%q/%s, want exact %d/%q/%q/%s", replay.Code, replay.Header().Get("Idempotent-Replayed"), replay.Header().Get("Content-Type"), replay.Header().Get("Location"), replay.Body.String(), first.Code, first.Header().Get("Content-Type"), first.Header().Get("Location"), first.Body.String())
	}
	if len(jobs.requests) != 1 {
		t.Fatalf("queue attempts = %d, want one", len(jobs.requests))
	}
}

func TestServiceUpIdempotencyBindsEnvironmentAndCommitMessageHeaders(t *testing.T) {
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", t.TempDir())
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(t.Context(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "headers"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(t.Context(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operations := NewMemoryOperationStore()
	jobs := &recordingQueueStore{jobID: "job_headers"}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operations,
		DeploymentStore:  NewMemoryDeploymentStore(nil),
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       jobs,
	})
	tarball := buildSampleTarball(t)
	request := func(key, environment, message string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1alpha/services/"+string(service.ID)+"/up",
			bytes.NewReader(tarball),
		)
		req.Header.Set("Content-Type", "application/gzip")
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("X-Midnight-Environment", environment)
		if message != "" {
			req.Header.Set("X-Midnight-Commit-Message", message)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}

	if response := request("up-header-environment", "env_prod", "deploy"); response.Code != http.StatusAccepted {
		t.Fatalf("initial environment request status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("up-header-environment", "env_stage", "deploy"); response.Code != http.StatusConflict {
		t.Fatalf("changed environment status=%d body=%s, want 409", response.Code, response.Body.String())
	}
	if response := request("up-header-message", "env_prod", "first"); response.Code != http.StatusAccepted {
		t.Fatalf("initial message request status=%d body=%s", response.Code, response.Body.String())
	}
	if response := request("up-header-message", "env_prod", "second"); response.Code != http.StatusConflict {
		t.Fatalf("changed message status=%d body=%s, want 409", response.Code, response.Body.String())
	}
	if len(jobs.requests) != 2 {
		t.Fatalf("queue requests=%d, want only two initial accepts", len(jobs.requests))
	}
	listed, err := operations.List(t.Context(), OperationListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("operations=%d, want only two initial accepts", len(listed))
	}
}

func TestServiceUpRejectsCompressedEmptyEntryBombAndCleansCheckout(t *testing.T) {
	extractionRoot := t.TempDir()
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", extractionRoot)
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(t.Context(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "bomb"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(t.Context(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operations := NewMemoryOperationStore()
	jobs := &recordingQueueStore{jobID: "must-not-enqueue"}
	durable := &faultRetryDeployIntake{}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:     operations,
		DeploymentStore:    NewMemoryDeploymentStore(nil),
		IdempotencyStore:   idempotency.NewMemoryStore(),
		ProjectStore:       resources,
		ServiceStore:       resources,
		QueueStore:         jobs,
		DeployIntakeOutbox: durable,
	})
	tarball := buildEmptyEntryTarball(t, sourceartifact.DefaultLimits.MaxEntries+1)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/services/"+string(service.ID)+"/up",
		bytes.NewReader(tarball),
	)
	request.Header.Set("Content-Type", "application/gzip")
	request.Header.Set("Idempotency-Key", "up-empty-entry-bomb")
	request.Header.Set("X-Midnight-Environment", "env_prod")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s, want 413", response.Code, response.Body.String())
	}
	listed, err := operations.List(t.Context(), OperationListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 || len(jobs.requests) != 0 || len(durable.commits) != 0 {
		t.Fatalf(
			"operations=%d queue=%d durable commits=%d, want no accepted work",
			len(listed),
			len(jobs.requests),
			len(durable.commits),
		)
	}
	entries, err := os.ReadDir(extractionRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("extraction root contains partial checkout/staging files: %#v", entries)
	}
}

func TestExtractGzippedTarballCapsCompleteExpandedStream(t *testing.T) {
	tests := map[string]*tar.Header{
		"empty regular entry": {
			Name:     "empty",
			Mode:     0o644,
			Typeflag: tar.TypeReg,
		},
		"directory entry": {
			Name:     "empty-dir/",
			Mode:     0o755,
			Typeflag: tar.TypeDir,
		},
		"pax metadata": {
			Name:       "empty",
			Mode:       0o644,
			Typeflag:   tar.TypeReg,
			Format:     tar.FormatPAX,
			PAXRecords: map[string]string{"comment": strings.Repeat("pax", 300)},
		},
	}
	for name, header := range tests {
		t.Run(name, func(t *testing.T) {
			err := extractGzippedTarball(
				bytes.NewReader(buildSingleEntryTarball(t, header, nil)),
				t.TempDir(),
				sourceartifact.Limits{
					MaxEntries:           10,
					MaxUncompressedBytes: 512,
				},
			)
			if !errors.Is(err, errUpArchiveLimitExceeded) {
				t.Fatalf("extract error=%v, want expanded-stream limit", err)
			}
		})
	}
}

func TestExtractGzippedTarballRejectsTrailingDecompressedData(t *testing.T) {
	err := extractGzippedTarball(
		bytes.NewReader(buildSingleEntryTarball(t, &tar.Header{
			Name:     "empty",
			Mode:     0o644,
			Typeflag: tar.TypeReg,
		}, []byte("data after the tar terminator"))),
		t.TempDir(),
		sourceartifact.Limits{
			MaxEntries:           10,
			MaxUncompressedBytes: 1 << 20,
		},
	)
	if err == nil || errors.Is(err, errUpArchiveLimitExceeded) || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("extract error=%v, want invalid trailing decompressed data", err)
	}
}

func TestServiceUpRequiresEnvironmentBeforeStaging(t *testing.T) {
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", t.TempDir())
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operations := NewMemoryOperationStore()
	jobs := &recordingQueueStore{jobID: "must-not-enqueue"}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operations,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       jobs,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/up", bytes.NewReader(buildSampleTarball(t)))
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Idempotency-Key", "up-missing-environment")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	listed, err := operations.List(context.Background(), OperationListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 || len(jobs.requests) != 0 {
		t.Fatalf("operations=%d jobs=%d, want no staging or queue side effects", len(listed), len(jobs.requests))
	}
}

func TestServiceUpRejectsOversizedBody(t *testing.T) {
	t.Setenv("MIDNIGHT_UP_BODY_LIMIT_BYTES", "16")
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", t.TempDir())
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   NewMemoryOperationStore(),
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       &recordingQueueStore{jobID: "job"},
	})

	tarball := buildSampleTarball(t) // > 16 bytes
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/up", bytes.NewReader(tarball))
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Idempotency-Key", "up-oversize")
	req.Header.Set("X-Midnight-Environment", "env_prod")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s, want 413", rr.Code, rr.Body.String())
	}
}

func TestServiceUpRejectsNonGzipBody(t *testing.T) {
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", t.TempDir())
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   NewMemoryOperationStore(),
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       &recordingQueueStore{jobID: "job"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/up", bytes.NewReader([]byte("not gzipped at all")))
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Idempotency-Key", "up-badgzip")
	req.Header.Set("X-Midnight-Environment", "env_prod")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
}

func TestServiceUpRequiresGzipContentType(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   NewMemoryOperationStore(),
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       &recordingQueueStore{jobID: "job"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/up", bytes.NewReader([]byte("anything")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "up-badct")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d body=%s, want 415", rr.Code, rr.Body.String())
	}
}

func TestAcceptableUpContentTypeUsesExactParsedMediaType(t *testing.T) {
	for _, contentType := range []string{
		"application/gzip",
		"application/x-gzip",
		"application/x-tar+gzip",
		"application/x-compressed-tar",
		"Application/GZip; charset=binary",
	} {
		if !isAcceptableUpContentType(contentType) {
			t.Errorf("content type %q was rejected", contentType)
		}
	}
	for _, contentType := range []string{
		"",
		"application/gzip-evil",
		"application/x-gzip.extra",
		"application/gzip; broken",
		"application/json",
	} {
		if isAcceptableUpContentType(contentType) {
			t.Errorf("near-miss content type %q was accepted", contentType)
		}
	}
}

func TestServiceUpRequiresIdempotencyKey(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   NewMemoryOperationStore(),
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       &recordingQueueStore{jobID: "job"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/up", bytes.NewReader([]byte{}))
	req.Header.Set("Content-Type", "application/gzip")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
}
