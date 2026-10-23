// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type recordingTenantUpCoordinator struct {
	mu              sync.Mutex
	preflights      []TenantSourceUpPreflightRequest
	requests        []TenantSourceUpRequest
	finishes        []bool
	preflightError  error
	preflightReplay bool
	finalizeError   error
	finalizeCalls   int
	publishCalls    int
	publishError    error
	result          TenantDeployIntakeResult
	err             error
	run             func(TenantSourceUpRequest) (TenantDeployIntakeResult, error)
}

func (c *recordingTenantUpCoordinator) PublishSourceUp(
	_ context.Context,
	request TenantSourceUpPublishRequest,
) (TenantSourceArtifact, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publishCalls++
	if c.publishError != nil {
		return TenantSourceArtifact{}, c.publishError
	}
	staged := request.Staged
	storageKey, err := sourceartifact.StorageKey(staged.Digest)
	if err != nil {
		return TenantSourceArtifact{}, err
	}
	uri, err := sourceartifact.URI(staged.Digest)
	if err != nil {
		return TenantSourceArtifact{}, err
	}
	return TenantSourceArtifact{
		Digest:            staged.Digest,
		URI:               uri,
		StorageKey:        storageKey,
		CompressedBytes:   staged.CompressedBytes,
		UncompressedBytes: staged.UncompressedBytes,
		EntryCount:        staged.EntryCount,
	}, nil
}

func (c *recordingTenantUpCoordinator) PreflightSourceUp(
	_ context.Context,
	request TenantSourceUpPreflightRequest,
) (TenantSourceUpAdmission, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.preflights = append(c.preflights, request)
	if c.preflightError != nil {
		return TenantSourceUpAdmission{}, c.preflightError
	}
	if c.preflightReplay {
		return TenantSourceUpAdmission{Replay: true}, nil
	}
	intentHash, err := tenantUpPreflightIntentHash(request)
	if err != nil {
		return TenantSourceUpAdmission{}, err
	}
	return TenantSourceUpAdmission{
		ReservationID: "suar_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IntentHash:    intentHash,
	}, nil
}

func (c *recordingTenantUpCoordinator) FinalizeSourceUpReplay(
	_ context.Context,
	_ TenantSourceUpPreflightRequest,
) (idempotency.StoredResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finalizeCalls++
	if c.finalizeError != nil {
		return idempotency.StoredResponse{}, c.finalizeError
	}
	return c.result.Response.Clone(), nil
}

func (c *recordingTenantUpCoordinator) DeploySourceArtifact(
	_ context.Context,
	request TenantSourceUpRequest,
) (TenantDeployIntakeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, request)
	if c.run != nil {
		return c.run(request)
	}
	return c.result, c.err
}

func (c *recordingTenantUpCoordinator) FinishSourceUpAdmission(
	_ context.Context,
	_ TenantSourceUpAdmission,
	published bool,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finishes = append(c.finishes, published)
	return nil
}

func (c *recordingTenantUpCoordinator) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.preflights), len(c.requests)
}

func (c *recordingTenantUpCoordinator) capturedRequest() TenantSourceUpRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		return TenantSourceUpRequest{}
	}
	return c.requests[len(c.requests)-1]
}

func (c *recordingTenantUpCoordinator) finishedPublications() []bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]bool(nil), c.finishes...)
}

func (c *recordingTenantUpCoordinator) replayActivity() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.finalizeCalls, c.publishCalls
}

type failingTenantUpArtifactStore struct {
	stageCalls int
	err        error
}

func (s *failingTenantUpArtifactStore) Stage(
	context.Context,
	io.Reader,
	sourceartifact.Limits,
) (sourceartifact.Staged, error) {
	s.stageCalls++
	return sourceartifact.Staged{}, s.err
}

func (*failingTenantUpArtifactStore) Commit(
	context.Context,
	sourceartifact.Staged,
) (sourceartifact.Artifact, error) {
	return sourceartifact.Artifact{}, errors.New("unexpected commit")
}

func (*failingTenantUpArtifactStore) Abort(sourceartifact.Staged) error { return nil }

type commitFailingTenantUpArtifactStore struct {
	*sourceartifact.FSStore
}

func (*commitFailingTenantUpArtifactStore) Commit(
	context.Context,
	sourceartifact.Staged,
) (sourceartifact.Artifact, error) {
	return sourceartifact.Artifact{}, errors.New("injected commit failure")
}

type abortFailingTenantUpArtifactStore struct {
	*sourceartifact.FSStore
}

func (*abortFailingTenantUpArtifactStore) Abort(sourceartifact.Staged) error {
	return errors.New("injected abort failure with sensitive path")
}

func TestCreateTenantServiceUpStagesCASAndReplaysExactEnvelope(t *testing.T) {
	store, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	response := idempotency.StoredResponse{
		StatusCode: http.StatusAccepted,
		Header: http.Header{
			"Content-Type":              []string{"application/json"},
			"Location":                  []string{v1alphaPrefix + "/operations/op_up"},
			"X-Midnight-Delivery-State": []string{"pending"},
		},
		Body: []byte("{\"operation_id\":\"op_up\",\"deployment_id\":\"dep_up\",\"operation\":{\"id\":\"op_up\",\"kind\":\"build\",\"state\":\"QUEUED\"}}\n"),
	}
	call := 0
	coordinator := &recordingTenantUpCoordinator{
		run: func(TenantSourceUpRequest) (TenantDeployIntakeResult, error) {
			call++
			return TenantDeployIntakeResult{Response: response, Replayed: call > 1}, nil
		},
	}
	body := buildSampleTarball(t)
	first := serveTenantUp(t, coordinator, store, body, tenantUpHeaders("up-key", "env_prod", "release, candidate"))
	second := serveTenantUp(t, coordinator, store, body, tenantUpHeaders("up-key", "env_prod", "release, candidate"))
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("statuses=%d/%d bodies=%s/%s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if first.Body.String() != string(response.Body) || second.Body.String() != string(response.Body) {
		t.Fatalf("stored response mismatch first=%q second=%q", first.Body.String(), second.Body.String())
	}
	if first.Header().Get("Idempotent-Replayed") != "" ||
		second.Header().Get("Idempotent-Replayed") != "true" ||
		second.Header().Get("Location") != response.Header.Get("Location") ||
		second.Header().Get("X-Midnight-Delivery-State") != "pending" {
		t.Fatalf("response headers first=%v second=%v", first.Header(), second.Header())
	}
	preflights, requests := coordinator.counts()
	if preflights != 2 || requests != 2 {
		t.Fatalf("coordinator calls preflight=%d final=%d", preflights, requests)
	}
	got := coordinator.capturedRequest()
	if got.Principal.UserID != "usr_up" ||
		got.Session.ID != "ses_up" ||
		got.ServiceID != "svc_up" ||
		got.EnvironmentID != "env_prod" ||
		got.CommitMessage != "release, candidate" ||
		got.Key != "up-key" {
		t.Fatalf("request=%#v", got)
	}
	if got.Artifact.Digest == "" ||
		got.Artifact.URI != "sourceartifact://"+got.Artifact.Digest ||
		got.Artifact.StorageKey == "" ||
		got.Artifact.CompressedBytes != int64(len(body)) ||
		got.Artifact.EntryCount != 3 {
		t.Fatalf("artifact=%#v", got.Artifact)
	}
	wantFingerprint, err := tenantUpFingerprintDigest(
		"svc_up",
		"env_prod",
		"release, candidate",
		got.Artifact,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint.Method != http.MethodPost ||
		got.Fingerprint.Path != tenantUpIdempotencyPath("svc_up") ||
		got.Fingerprint.BodySHA256 != wantFingerprint {
		t.Fatalf("fingerprint=%#v want digest=%s", got.Fingerprint, wantFingerprint)
	}
	if finishes := coordinator.finishedPublications(); len(finishes) != 0 {
		t.Fatalf("successful admissions unexpectedly finished out-of-band: %v", finishes)
	}
}

func TestCreateTenantServiceUpCoalescedReplayValidatesCompleteBodyWithoutPublishing(t *testing.T) {
	store, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stored := idempotency.StoredResponse{
		StatusCode: http.StatusAccepted,
		Header: http.Header{
			"Content-Type":              []string{"application/json"},
			"Location":                  []string{v1alphaPrefix + "/operations/op_up_replay"},
			"X-Midnight-Delivery-State": []string{"pending"},
		},
		Body: []byte("{\"operation_id\":\"op_up_replay\"}\n"),
	}
	coordinator := &recordingTenantUpCoordinator{
		preflightReplay: true,
		result:          TenantDeployIntakeResult{Response: stored},
	}
	body := buildSampleTarball(t)
	reader := &countingReader{Reader: bytes.NewReader(body)}
	headers := tenantUpHeaders("up-replay-follower", "env_prod", "")
	setTenantUpArchiveContract(headers, body)
	request := tenantUpRequest(reader, headers)
	request.ContentLength = int64(len(body))
	response := httptest.NewRecorder()
	createTenantServiceUp(response, request, "svc_up", coordinator, store)

	if response.Code != http.StatusAccepted ||
		response.Body.String() != string(stored.Body) ||
		response.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf(
			"status=%d replay=%q body=%s",
			response.Code,
			response.Header().Get("Idempotent-Replayed"),
			response.Body.String(),
		)
	}
	if reader.bytes != len(body) {
		t.Fatalf("replay body bytes read=%d, want %d", reader.bytes, len(body))
	}
	if _, finalRequests := coordinator.counts(); finalRequests != 0 {
		t.Fatalf("replay created %d deploy requests", finalRequests)
	}
	finalizations, publications := coordinator.replayActivity()
	if finalizations != 1 || publications != 0 {
		t.Fatalf(
			"replay finalizations/publications=%d/%d, want 1/0",
			finalizations,
			publications,
		)
	}
	if finishes := coordinator.finishedPublications(); len(finishes) != 0 {
		t.Fatalf("replay finished a leader quota reservation: %v", finishes)
	}
	entries, err := os.ReadDir(store.Root() + "/staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("replay staged temp entries=%d, want zero", len(entries))
	}
}

func TestCreateTenantServiceUpCoalescedReplayRejectsUnverifiedBody(t *testing.T) {
	store, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &recordingTenantUpCoordinator{
		preflightReplay: true,
		result: TenantDeployIntakeResult{Response: idempotency.StoredResponse{
			StatusCode: http.StatusAccepted,
			Body:       []byte("{\"operation_id\":\"must-not-replay\"}\n"),
		}},
	}
	advertised := buildSampleTarball(t)
	supplied := append([]byte(nil), advertised...)
	supplied[len(supplied)-1] ^= 0xff
	reader := &countingReader{Reader: bytes.NewReader(supplied)}
	headers := tenantUpHeaders("up-replay-invalid", "env_prod", "")
	setTenantUpArchiveContract(headers, advertised)
	request := tenantUpRequest(reader, headers)
	request.ContentLength = int64(len(supplied))
	response := httptest.NewRecorder()
	createTenantServiceUp(response, request, "svc_up", coordinator, store)

	if response.Code != http.StatusBadRequest ||
		strings.Contains(response.Body.String(), "must-not-replay") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if reader.bytes != len(supplied) {
		t.Fatalf("invalid replay body bytes read=%d, want %d", reader.bytes, len(supplied))
	}
	finalizations, publications := coordinator.replayActivity()
	if finalizations != 0 || publications != 0 {
		t.Fatalf(
			"invalid replay finalizations/publications=%d/%d, want 0/0",
			finalizations,
			publications,
		)
	}
}

func TestCreateTenantServiceUpCoalescedReplayRepeatsAuthorityAfterBody(t *testing.T) {
	store, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &recordingTenantUpCoordinator{
		preflightReplay: true,
		finalizeError:   identity.ErrAccessNotFound,
	}
	body := buildSampleTarball(t)
	reader := &countingReader{Reader: bytes.NewReader(body)}
	headers := tenantUpHeaders("up-replay-revoked", "env_prod", "")
	setTenantUpArchiveContract(headers, body)
	request := tenantUpRequest(reader, headers)
	request.ContentLength = int64(len(body))
	response := httptest.NewRecorder()
	createTenantServiceUp(response, request, "svc_up", coordinator, store)

	if response.Code != http.StatusNotFound ||
		response.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf(
			"status=%d replay=%q body=%s",
			response.Code,
			response.Header().Get("Idempotent-Replayed"),
			response.Body.String(),
		)
	}
	if reader.bytes != len(body) {
		t.Fatalf("revoked replay body bytes read=%d, want %d", reader.bytes, len(body))
	}
	finalizations, publications := coordinator.replayActivity()
	if finalizations != 1 || publications != 0 {
		t.Fatalf(
			"revoked replay finalizations/publications=%d/%d, want 1/0",
			finalizations,
			publications,
		)
	}
}

func TestCreateTenantServiceUpPreflightDenialDoesNotReadOrStageBody(t *testing.T) {
	coordinator := &recordingTenantUpCoordinator{preflightError: identity.ErrAuthorizationDenied}
	store := &failingTenantUpArtifactStore{err: errors.New("stage must not run")}
	rawBody := buildSampleTarball(t)
	body := &countingReader{Reader: bytes.NewReader(rawBody)}
	headers := tenantUpHeaders("up-key", "env_prod", "")
	setTenantUpArchiveContract(headers, rawBody)
	request := tenantUpRequest(body, headers)
	request.ContentLength = int64(len(rawBody))
	response := httptest.NewRecorder()
	createTenantServiceUp(response, request, "svc_up", coordinator, store)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if body.reads != 0 || store.stageCalls != 0 {
		t.Fatalf("preflight denial read=%d stage=%d", body.reads, store.stageCalls)
	}
	preflights, finals := coordinator.counts()
	if preflights != 1 || finals != 0 {
		t.Fatalf("coordinator calls preflight=%d final=%d", preflights, finals)
	}
}

func TestCreateTenantServiceUpRejectsLengthBeforeBodyOrPreflight(t *testing.T) {
	rawBody := buildSampleTarball(t)
	for _, test := range []struct {
		name          string
		contentLength int64
		transfer      []string
		wantStatus    int
	}{
		{name: "unknown", contentLength: -1, wantStatus: http.StatusLengthRequired},
		{name: "zero", contentLength: 0, wantStatus: http.StatusLengthRequired},
		{name: "chunked", contentLength: -1, transfer: []string{"chunked"}, wantStatus: http.StatusLengthRequired},
		{name: "over maximum", contentLength: sourceartifact.DefaultLimits.MaxCompressedBytes + 1, wantStatus: http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &countingReader{Reader: bytes.NewReader(rawBody)}
			headers := tenantUpHeaders("up-length", "env_prod", "")
			setTenantUpArchiveContract(headers, rawBody)
			request := tenantUpRequest(body, headers)
			request.ContentLength = test.contentLength
			request.TransferEncoding = test.transfer
			coordinator := &recordingTenantUpCoordinator{}
			store := &failingTenantUpArtifactStore{err: errors.New("stage must not run")}
			response := httptest.NewRecorder()
			createTenantServiceUp(response, request, "svc_up", coordinator, store)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			preflights, finals := coordinator.counts()
			if body.reads != 0 || store.stageCalls != 0 || preflights != 0 || finals != 0 {
				t.Fatalf("reads=%d stage=%d preflight=%d final=%d", body.reads, store.stageCalls, preflights, finals)
			}
		})
	}
}

func TestCreateTenantServiceUpFencesAdvertisedArchiveIdentityAndFinishesReservation(t *testing.T) {
	rawBody := buildSampleTarball(t)
	t.Run("digest mismatch", func(t *testing.T) {
		store, err := sourceartifact.NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		headers := tenantUpHeaders("up-digest-mismatch", "env_prod", "")
		headers.Set("X-Midnight-Archive-Digest", "sha256:"+strings.Repeat("f", 64))
		coordinator := &recordingTenantUpCoordinator{}
		response := serveTenantUp(t, coordinator, store, rawBody, headers)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		_, finals := coordinator.counts()
		if finals != 0 {
			t.Fatalf("final calls=%d", finals)
		}
		if finishes := coordinator.finishedPublications(); len(finishes) != 1 || finishes[0] {
			t.Fatalf("finishes=%v, want one pre-publication release", finishes)
		}
	})

	t.Run("length mismatch", func(t *testing.T) {
		store, err := sourceartifact.NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		headers := tenantUpHeaders("up-length-mismatch", "env_prod", "")
		setTenantUpArchiveContract(headers, rawBody)
		request := tenantUpRequest(bytes.NewReader(rawBody), headers)
		request.ContentLength = int64(len(rawBody) + 1)
		coordinator := &recordingTenantUpCoordinator{}
		response := httptest.NewRecorder()
		createTenantServiceUp(response, request, "svc_up", coordinator, store)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if finishes := coordinator.finishedPublications(); len(finishes) != 1 || finishes[0] {
			t.Fatalf("finishes=%v, want one pre-publication release", finishes)
		}
	})

	t.Run("mismatch abort failure preserves charge", func(t *testing.T) {
		filesystem, err := sourceartifact.NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store := &abortFailingTenantUpArtifactStore{FSStore: filesystem}
		headers := tenantUpHeaders("up-abort-failure", "env_prod", "")
		headers.Set("X-Midnight-Archive-Digest", "sha256:"+strings.Repeat("e", 64))
		coordinator := &recordingTenantUpCoordinator{}
		response := serveTenantUp(t, coordinator, store, rawBody, headers)
		if response.Code != http.StatusInternalServerError ||
			strings.Contains(response.Body.String(), "sensitive path") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if finishes := coordinator.finishedPublications(); len(finishes) != 1 || !finishes[0] {
			t.Fatalf("finishes=%v, want conservative physical-byte charge", finishes)
		}
	})
}

func TestCreateTenantServiceUpRejectsNonCanonicalHeadersBeforePreflight(t *testing.T) {
	valid := tenantUpHeaders("up-key", "env_prod", "")
	cases := []struct {
		name       string
		mutate     func(http.Header)
		wantStatus int
	}{
		{name: "missing authorization", mutate: func(h http.Header) { h.Del("Authorization") }, wantStatus: http.StatusUnauthorized},
		{name: "duplicate authorization", mutate: func(h http.Header) { h.Add("Authorization", "Bearer other") }, wantStatus: http.StatusUnauthorized},
		{name: "comma authorization", mutate: func(h http.Header) { h.Set("Authorization", "Bearer token, Bearer other") }, wantStatus: http.StatusUnauthorized},
		{name: "missing key", mutate: func(h http.Header) { h.Del("Idempotency-Key") }, wantStatus: http.StatusBadRequest},
		{name: "duplicate key", mutate: func(h http.Header) { h.Add("Idempotency-Key", "other") }, wantStatus: http.StatusBadRequest},
		{name: "comma key", mutate: func(h http.Header) { h.Set("Idempotency-Key", "up-key,other") }, wantStatus: http.StatusBadRequest},
		{name: "missing environment", mutate: func(h http.Header) { h.Del("X-Midnight-Environment") }, wantStatus: http.StatusBadRequest},
		{name: "duplicate environment", mutate: func(h http.Header) { h.Add("X-Midnight-Environment", "env_other") }, wantStatus: http.StatusBadRequest},
		{name: "comma environment", mutate: func(h http.Header) { h.Set("X-Midnight-Environment", "env_prod,env_other") }, wantStatus: http.StatusBadRequest},
		{name: "environment control", mutate: func(h http.Header) { h["X-Midnight-Environment"] = []string{"env\u007fprod"} }, wantStatus: http.StatusBadRequest},
		{name: "environment unicode control", mutate: func(h http.Header) { h["X-Midnight-Environment"] = []string{"env\u0085prod"} }, wantStatus: http.StatusBadRequest},
		{name: "duplicate message", mutate: func(h http.Header) {
			h.Add("X-Midnight-Commit-Message", "first")
			h.Add("X-Midnight-Commit-Message", "other")
		}, wantStatus: http.StatusBadRequest},
		{name: "message control", mutate: func(h http.Header) { h["X-Midnight-Commit-Message"] = []string{"bad\u0085message"} }, wantStatus: http.StatusBadRequest},
		{name: "message oversize", mutate: func(h http.Header) {
			h.Set("X-Midnight-Commit-Message", strings.Repeat("x", tenantUpMessageMaxBytes+1))
		}, wantStatus: http.StatusBadRequest},
		{name: "missing content type", mutate: func(h http.Header) { h.Del("Content-Type") }, wantStatus: http.StatusUnsupportedMediaType},
		{name: "duplicate content type", mutate: func(h http.Header) { h.Add("Content-Type", "application/x-gzip") }, wantStatus: http.StatusUnsupportedMediaType},
		{name: "invalid content type", mutate: func(h http.Header) { h.Set("Content-Type", "application/json") }, wantStatus: http.StatusUnsupportedMediaType},
		{name: "content type near miss", mutate: func(h http.Header) { h.Set("Content-Type", "application/gzip-evil") }, wantStatus: http.StatusUnsupportedMediaType},
		{name: "content encoding", mutate: func(h http.Header) { h.Set("Content-Encoding", "gzip") }, wantStatus: http.StatusUnsupportedMediaType},
		{name: "missing digest", mutate: func(h http.Header) { h.Del("X-Midnight-Archive-Digest") }, wantStatus: http.StatusBadRequest},
		{name: "duplicate digest", mutate: func(h http.Header) { h.Add("X-Midnight-Archive-Digest", "sha256:"+strings.Repeat("a", 64)) }, wantStatus: http.StatusBadRequest},
		{name: "noncanonical digest", mutate: func(h http.Header) { h.Set("X-Midnight-Archive-Digest", "sha256:"+strings.Repeat("A", 64)) }, wantStatus: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := valid.Clone()
			tc.mutate(headers)
			coordinator := &recordingTenantUpCoordinator{}
			store := &failingTenantUpArtifactStore{err: errors.New("stage must not run")}
			response := serveTenantUp(t, coordinator, store, buildSampleTarball(t), headers)
			if response.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			preflights, finals := coordinator.counts()
			if preflights != 0 || finals != 0 || store.stageCalls != 0 {
				t.Fatalf("calls preflight=%d final=%d stage=%d", preflights, finals, store.stageCalls)
			}
		})
	}
}

func TestCreateTenantServiceUpMapsStageAndCoordinatorErrorsWithoutLeaks(t *testing.T) {
	headers := tenantUpHeaders("up-key", "env_prod", "")
	stageCases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "archive", err: sourceartifact.ErrInvalidArchive, wantStatus: http.StatusBadRequest},
		{name: "limit", err: sourceartifact.ErrLimitExceeded, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "internal", err: errors.New("secret filesystem path"), wantStatus: http.StatusInternalServerError},
	}
	for _, tc := range stageCases {
		t.Run("stage "+tc.name, func(t *testing.T) {
			coordinator := &recordingTenantUpCoordinator{}
			store := &failingTenantUpArtifactStore{err: tc.err}
			response := serveTenantUp(t, coordinator, store, []byte("archive"), headers)
			if response.Code != tc.wantStatus || strings.Contains(response.Body.String(), "secret filesystem path") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if finishes := coordinator.finishedPublications(); len(finishes) != 1 || finishes[0] {
				t.Fatalf("finishes=%v, want pre-publication release", finishes)
			}
		})
	}
	t.Run("real compressed body limit", func(t *testing.T) {
		t.Setenv("MIDNIGHT_UP_BODY_LIMIT_BYTES", "32")
		realStore, err := sourceartifact.NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		coordinator := &recordingTenantUpCoordinator{}
		response := serveTenantUp(t, coordinator, realStore, buildSampleTarball(t), headers)
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		_, finals := coordinator.counts()
		if finals != 0 {
			t.Fatalf("final coordinator calls=%d", finals)
		}
	})
	t.Run("commit failure aborts staged ownership", func(t *testing.T) {
		realStore, err := sourceartifact.NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store := &commitFailingTenantUpArtifactStore{FSStore: realStore}
		coordinator := &recordingTenantUpCoordinator{
			publishError: errors.New("injected commit failure"),
		}
		response := serveTenantUp(t, coordinator, store, buildSampleTarball(t), headers)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		entries, err := os.ReadDir(realStore.Root() + "/staging")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("staged temp entries=%d, want zero", len(entries))
		}
		if finishes := coordinator.finishedPublications(); len(finishes) != 1 || !finishes[0] {
			t.Fatalf("finishes=%v, want conservative published charge", finishes)
		}
	})

	store, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalCases := []struct {
		name          string
		err           error
		wantStatus    int
		wantChallenge bool
		wantRetry     bool
	}{
		{name: "revoked", err: identity.ErrSessionInvalid, wantStatus: http.StatusUnauthorized, wantChallenge: true},
		{name: "concealed", err: identity.ErrAccessNotFound, wantStatus: http.StatusNotFound},
		{name: "denied", err: identity.ErrAuthorizationDenied, wantStatus: http.StatusForbidden},
		{name: "in progress", err: idempotency.ErrRequestInProgress, wantStatus: http.StatusConflict, wantRetry: true},
		{name: "different body", err: idempotency.ErrKeyReusedDifferentBody, wantStatus: http.StatusConflict},
		{name: "quota", err: sourceartifact.ErrQuotaExceeded, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "backpressure", err: sourceartifact.ErrUploadBackpressure, wantStatus: http.StatusTooManyRequests, wantRetry: true},
		{name: "expired reservation", err: sourceartifact.ErrReservationInvalid, wantStatus: http.StatusConflict},
		{name: "dependency", err: ErrTenantDeployIntakeUnavailable, wantStatus: http.StatusServiceUnavailable},
		{name: "internal", err: errors.New("secret postgres detail"), wantStatus: http.StatusInternalServerError},
	}
	for _, tc := range finalCases {
		t.Run("publication "+tc.name, func(t *testing.T) {
			coordinator := &recordingTenantUpCoordinator{publishError: tc.err}
			response := serveTenantUp(t, coordinator, store, buildSampleTarball(t), headers)
			if response.Code != tc.wantStatus ||
				strings.Contains(response.Body.String(), "secret postgres detail") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.wantChallenge != (response.Header().Get("WWW-Authenticate") == "Bearer") {
				t.Fatalf("WWW-Authenticate=%q", response.Header().Get("WWW-Authenticate"))
			}
			if tc.wantRetry != (response.Header().Get("Retry-After") == "1") {
				t.Fatalf("Retry-After=%q", response.Header().Get("Retry-After"))
			}
			if finishes := coordinator.finishedPublications(); len(finishes) != 1 || !finishes[0] {
				t.Fatalf("finishes=%v, want conservative published charge", finishes)
			}
		})
	}
}

func TestHumanServiceSubrouteBoundaryAdmitsUpOnlyWhenInstalled(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	for _, admitted := range [][]bool{
		nil,
		{true},
		{true, false},
		{false, true},
		{true, true},
	} {
		request := tenantDeployBoundaryRequest(http.MethodPost, tenantUpIdempotencyPath("svc_up"))
		response := httptest.NewRecorder()
		humanServiceDeploymentReadBoundaryMiddleware(next, admitted...).ServeHTTP(response, request)
		want := http.StatusForbidden
		if len(admitted) >= 2 && admitted[1] {
			want = http.StatusNoContent
		}
		if response.Code != want {
			t.Fatalf("admitted=%v status=%d body=%s", admitted, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{
		v1alphaPrefix + "/services/svc_up/github",
		v1alphaPrefix + "/services/svc_up/deployments/dep/actions/redeploy",
		v1alphaPrefix + "/services/svc_up/routes",
	} {
		response := httptest.NewRecorder()
		humanServiceDeploymentReadBoundaryMiddleware(next, true, true).ServeHTTP(
			response,
			tenantDeployBoundaryRequest(http.MethodPost, path),
		)
		if response.Code != http.StatusForbidden {
			t.Fatalf("closed path %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestRouterAdmitsHumanUpAndPreservesOperatorCompatibilityPath(t *testing.T) {
	stack, manager := productReadDataFixture(t)
	artifacts, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &recordingTenantUpCoordinator{
		result: TenantDeployIntakeResult{Response: idempotency.StoredResponse{
			StatusCode: http.StatusAccepted,
			Header: http.Header{
				"Content-Type":              []string{"application/json"},
				"Location":                  []string{v1alphaPrefix + "/operations/op_router_up"},
				"X-Midnight-Delivery-State": []string{"pending"},
			},
			Body: []byte("{\"operation_id\":\"op_router_up\",\"deployment_id\":\"dep_router_up\",\"operation\":{\"id\":\"op_router_up\",\"kind\":\"build\",\"state\":\"QUEUED\"}}\n"),
		}},
	}
	stack.TenantUpIntake = coordinator
	stack.SourceArtifactStore = artifacts
	handler := NewRouterWithDataStack(stack)
	alice := loginIdentityForTest(t, manager, "alice@example.com")

	request := httptest.NewRequest(
		http.MethodPost,
		tenantUpIdempotencyPath("svc_a"),
		bytes.NewReader(buildSampleTarball(t)),
	)
	request.Header = tenantUpHeaders("router-human-up", "env_a", "")
	setTenantUpArchiveContract(request.Header, buildSampleTarball(t))
	request.Header.Set("Authorization", "Bearer "+alice)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("human status=%d body=%s", response.Code, response.Body.String())
	}
	preflights, finals := coordinator.counts()
	if preflights != 1 || finals != 1 {
		t.Fatalf("human calls preflight=%d final=%d", preflights, finals)
	}

	operatorRequest := httptest.NewRequest(
		http.MethodPost,
		tenantUpIdempotencyPath("svc_a"),
		bytes.NewReader(buildSampleTarball(t)),
	)
	operatorRequest.Header = tenantUpHeaders("router-operator-up", "env_a", "")
	setTenantUpArchiveContract(operatorRequest.Header, buildSampleTarball(t))
	operatorRequest.Header.Set("Authorization", "Bearer operator-recovery-token")
	operatorResponse := httptest.NewRecorder()
	handler.ServeHTTP(operatorResponse, operatorRequest)
	if operatorResponse.Code != http.StatusServiceUnavailable ||
		!strings.Contains(operatorResponse.Body.String(), "up intake queue is not wired") {
		t.Fatalf("operator status=%d body=%s", operatorResponse.Code, operatorResponse.Body.String())
	}
	preflights, finals = coordinator.counts()
	if preflights != 1 || finals != 1 {
		t.Fatalf("operator reached human coordinator preflight=%d final=%d", preflights, finals)
	}
}

type countingReader struct {
	io.Reader
	reads int
	bytes int
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	r.reads++
	read, err := r.Reader.Read(buffer)
	r.bytes += read
	return read, err
}

func tenantUpHeaders(key, environmentID, message string) http.Header {
	header := make(http.Header)
	header.Set("Authorization", "Bearer human-session-token")
	header.Set("Content-Type", "application/gzip")
	header.Set("Idempotency-Key", key)
	header.Set("X-Midnight-Environment", environmentID)
	header.Set("X-Midnight-Archive-Digest", "sha256:"+strings.Repeat("0", 64))
	if message != "" {
		header.Set("X-Midnight-Commit-Message", message)
	}
	return header
}

func tenantUpRequest(body io.Reader, header http.Header) *http.Request {
	request := httptest.NewRequest(http.MethodPost, tenantUpIdempotencyPath("svc_up"), body)
	request.Header = header.Clone()
	request.RemoteAddr = "192.0.2.91:443"
	request.Header.Set("User-Agent", "midnight-up-test")
	principal := identity.Principal{Kind: "user", UserID: "usr_up", SessionID: "ses_up", AuthMethod: "password"}
	session := identity.Session{ID: "ses_up", UserID: "usr_up", SessionGeneration: 1}
	ctx := withProductPrincipal(request.Context(), productPrincipal{Kind: productPrincipalHuman, Human: principal})
	ctx = context.WithValue(ctx, identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	return request.WithContext(ctx)
}

func serveTenantUp(
	t *testing.T,
	coordinator TenantUpIntakeCoordinator,
	store TenantUpArtifactStore,
	body []byte,
	header http.Header,
) *httptest.ResponseRecorder {
	t.Helper()
	request := tenantUpRequest(bytes.NewReader(body), header)
	if values := request.Header.Values("X-Midnight-Archive-Digest"); len(values) == 1 && values[0] == "sha256:"+strings.Repeat("0", 64) {
		setTenantUpArchiveContract(request.Header, body)
	}
	response := httptest.NewRecorder()
	createTenantServiceUp(response, request, orailtypes.ID("svc_up"), coordinator, store)
	return response
}

func setTenantUpArchiveContract(header http.Header, body []byte) {
	header.Set("X-Midnight-Archive-Digest", fmt.Sprintf("sha256:%x", sha256.Sum256(body)))
}
