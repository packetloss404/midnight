// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestTenantSourceUpValidationBindsConcreteRouteHeadersAndArtifact(t *testing.T) {
	coordinator := testTenantUpCoordinator(t)
	request := validTenantSourceUpRequest(t)
	if err := coordinator.validateSourceUpRequest(request); err != nil {
		t.Fatalf("valid request: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*TenantSourceUpRequest)
	}{
		{name: "wrong path", mutate: func(r *TenantSourceUpRequest) { r.Fingerprint.Path = tenantUpIdempotencyPath("svc_other") }},
		{name: "wrong digest fingerprint", mutate: func(r *TenantSourceUpRequest) { r.Fingerprint.BodySHA256 = strings.Repeat("0", 64) }},
		{name: "invalid artifact URI", mutate: func(r *TenantSourceUpRequest) { r.Artifact.URI = "file:///tmp/source" }},
		{name: "invalid storage key", mutate: func(r *TenantSourceUpRequest) { r.Artifact.StorageKey = "../source.tar.gz" }},
		{name: "empty archive", mutate: func(r *TenantSourceUpRequest) { r.Artifact.CompressedBytes = 0 }},
		{name: "empty entries", mutate: func(r *TenantSourceUpRequest) { r.Artifact.EntryCount = 0 }},
		{name: "message control", mutate: func(r *TenantSourceUpRequest) { r.CommitMessage = "bad\nmessage" }},
		{name: "message oversize", mutate: func(r *TenantSourceUpRequest) { r.CommitMessage = strings.Repeat("x", tenantUpMessageMaxBytes+1) }},
		{name: "wrong session", mutate: func(r *TenantSourceUpRequest) { r.Session.UserID = "usr_other" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := request
			tc.mutate(&candidate)
			if err := coordinator.validateSourceUpRequest(candidate); err == nil {
				t.Fatal("validation unexpectedly succeeded")
			}
		})
	}
}

func TestTenantUpAcceptedResponsePreservesCLIEnvelope(t *testing.T) {
	operation := orailtypes.Operation{
		ID:    "op_up_response",
		Kind:  "build",
		State: orailtypes.OperationQueued,
	}
	stored, err := tenantUpAcceptedStoredResponse(operation, "dep_up_response")
	if err != nil {
		t.Fatal(err)
	}
	if stored.StatusCode != http.StatusAccepted ||
		stored.Header.Get("Location") != v1alphaPrefix+"/operations/op_up_response" ||
		stored.Header.Get("X-Midnight-Delivery-State") != "pending" {
		t.Fatalf("stored=%#v", stored)
	}
	var response upOperationResponse
	if err := json.Unmarshal(stored.Body, &response); err != nil {
		t.Fatal(err)
	}
	if response.OperationID != "op_up_response" ||
		response.DeploymentID != "dep_up_response" ||
		response.Operation != operation ||
		response.Replayed {
		t.Fatalf("response=%#v", response)
	}
}

func TestTenantUpAuditNeverContainsCommitMessageOrStoragePath(t *testing.T) {
	message := "sensitive-ish local label"
	request := tenantDeployTransactionRequest{
		Principal: identity.Principal{
			Kind:       "user",
			UserID:     "usr_up",
			SessionID:  "ses_up",
			AuthMethod: "password",
		},
		RequestID:  "req_up",
		SourceIP:   "192.0.2.90:443",
		UserAgent:  "midnight-test",
		HasMessage: true,
		Message:    message,
		Artifact: &TenantSourceArtifact{
			Digest:            "sha256:" + strings.Repeat("a", 64),
			URI:               "sourceartifact://sha256:" + strings.Repeat("a", 64),
			StorageKey:        "blobs/sha256/aa/" + strings.Repeat("a", 64) + ".tar.gz",
			CompressedBytes:   123,
			UncompressedBytes: 456,
			EntryCount:        2,
		},
	}
	now := time.Date(2026, 7, 29, 1, 2, 3, 0, time.UTC)
	row := tenantUpAcceptedAuditRow(
		request,
		now,
		"wrk_up",
		"prj_up",
		identity.WorkspaceRoleMember,
		"",
		Service{ID: "svc_up", ProjectID: "prj_up"},
		DeployIntakeRequest{EnvironmentID: "env_up"},
		DeployIntakeCommitResult{
			Operation: orailtypes.Operation{ID: "op_up", Kind: "build", State: orailtypes.OperationQueued},
			Deployment: DeploymentRecord{
				ID:               "dep_up",
				ProjectID:        "prj_up",
				ServiceID:        "svc_up",
				EnvironmentID:    "env_up",
				Action:           deploy.ActionDeploySource,
				ExpectedActiveID: "dep_old",
			},
			OutboxID: "dox_up",
		},
	)
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), message) ||
		strings.Contains(string(encoded), request.Artifact.StorageKey) ||
		strings.Contains(string(encoded), request.Artifact.URI) {
		t.Fatalf("audit leaked message/storage identity: %s", encoded)
	}
	if row.After["has_client_commit_message"] != true ||
		row.After["source_artifact_digest"] != request.Artifact.Digest ||
		row.Attrs["capability"] != string(identity.CapabilityDeploymentTriggerCLI) {
		t.Fatalf("audit=%#v", row)
	}
}

func testTenantUpCoordinator(t *testing.T) *PostgresTenantDeployIntakeCoordinator {
	t.Helper()
	pool := &pg.Pool{}
	filesystem, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &PostgresTenantDeployIntakeCoordinator{
		Pool:                pool,
		Idempotency:         idempotency.NewPostgresStore(pool),
		Audit:               audit.NewPostgresChain(pool),
		Outbox:              NewPostgresDeployOutbox(pool),
		SourceArtifacts:     sourceartifact.NewPostgresStore(pool),
		SourceArtifactFS:    filesystem,
		SourceArtifactQuota: sourceartifact.NewQuotaStore(pool, sourceartifact.DefaultQuotaConfig),
		Routes:              router.NewPostgresDurableRouteStore(pool),
		Now:                 func() time.Time { return time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC) },
		OperationIDFactory:  func() orailtypes.ID { return "op_up" },
		DeploymentIDFactory: func() string { return "dep_up" },
		DomainIDFactory:     func() string { return "dom_up" },
		OutboxIDFactory:     func() string { return "dox_up" },
		EventIDFactory:      func() string { return "dev_up" },
	}
}

func validTenantSourceUpRequest(t *testing.T) TenantSourceUpRequest {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	uri, err := sourceartifact.URI(digest)
	if err != nil {
		t.Fatal(err)
	}
	storageKey, err := sourceartifact.StorageKey(digest)
	if err != nil {
		t.Fatal(err)
	}
	artifact := TenantSourceArtifact{
		Digest:            digest,
		URI:               uri,
		StorageKey:        storageKey,
		CompressedBytes:   123,
		UncompressedBytes: 456,
		EntryCount:        2,
	}
	fingerprint, err := tenantUpFingerprintDigest("svc_up", "env_up", "release", artifact)
	if err != nil {
		t.Fatal(err)
	}
	request := TenantSourceUpRequest{
		Principal: identity.Principal{
			Kind:       "user",
			UserID:     "usr_up",
			SessionID:  "ses_up",
			AuthMethod: "password",
		},
		Session: identity.Session{
			ID:                "ses_up",
			UserID:            "usr_up",
			SessionGeneration: 1,
		},
		ServiceID:     "svc_up",
		EnvironmentID: "env_up",
		CommitMessage: "release",
		Artifact:      artifact,
		Key:           "tenant-up-key",
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodPost,
			Path:       tenantUpIdempotencyPath("svc_up"),
			BodySHA256: fingerprint,
		},
	}
	intentHash, err := tenantUpPreflightIntentHash(TenantSourceUpPreflightRequest{
		Principal:       request.Principal,
		Session:         request.Session,
		ServiceID:       request.ServiceID,
		EnvironmentID:   request.EnvironmentID,
		CommitMessage:   request.CommitMessage,
		Key:             request.Key,
		ExpectedDigest:  request.Artifact.Digest,
		CompressedBytes: request.Artifact.CompressedBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	request.Admission = TenantSourceUpAdmission{
		ReservationID: "suar_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IntentHash:    intentHash,
	}
	return request
}
