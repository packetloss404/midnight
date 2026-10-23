package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/sourceartifact"
)

func TestCleanupLocalUploadRemovesOnlyOwnedCheckout(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "midnight-up-src-123")
	if err := os.Mkdir(owned, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := cleanupLocalUpload("local://"+owned, root); err != nil {
		t.Fatalf("cleanupLocalUpload: %v", err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned checkout still exists: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "midnight-up-src-456")
	if err := os.Mkdir(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := cleanupLocalUpload("local://"+outside, root); err == nil {
		t.Fatal("cleanupLocalUpload removed a checkout outside the configured root")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside checkout was changed: %v", err)
	}
}

func TestCleanupLocalUploadRejectsUnexpectedName(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "operator-data")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := cleanupLocalUpload("local://"+path, root); err == nil {
		t.Fatal("cleanupLocalUpload accepted an unowned directory name")
	}
}

func TestBuildWorkerChecksOutArtifactProviderFromSharedRoot(t *testing.T) {
	ctx := context.Background()
	artifactRoot := t.TempDir()
	store, err := sourceartifact.NewFSStore(artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	payload := workerArtifactArchive(t, "Dockerfile", "FROM scratch\n")
	staged, err := store.Stage(ctx, bytes.NewReader(payload), sourceartifact.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Commit(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	identity := &sourceArtifactCheckoutIdentity{
		OperationID:       "op-up",
		DeploymentID:      "dep-up",
		ProjectID:         "prj-up",
		ServiceID:         "svc-web",
		EnvironmentID:     "env-up",
		Queue:             queue.Build,
		ParentOperationID: "op-up",
	}
	references := &recordingSourceArtifactStore{reference: sourceartifact.Reference{
		OperationID:   identity.OperationID,
		DeploymentID:  identity.DeploymentID,
		Digest:        artifact.Digest,
		ProjectID:     identity.ProjectID,
		ServiceID:     identity.ServiceID,
		EnvironmentID: identity.EnvironmentID,
	}}
	executor := &buildJobExecutor{
		cfg: workerConfig{
			CheckoutRoot: t.TempDir(),
			UploadRoot:   artifactRoot,
		},
		sourceArtifacts: references,
	}
	descriptor := &buildJobSourceIntent{Provider: "artifact", Repo: artifact.URI, Ref: artifact.Digest}
	checkout, err := executor.checkoutSource(
		ctx,
		"svc-web",
		descriptor,
		build.SourceRef{Repo: artifact.URI, Ref: artifact.Digest},
		identity,
	)
	if err != nil {
		t.Fatalf("checkoutSource: %v", err)
	}
	if _, err := os.Stat(filepath.Join(checkout.WorkDir, "Dockerfile")); err != nil {
		t.Fatalf("artifact checkout: %v", err)
	}
	if err := checkout.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	blobs, err := store.ListBlobs(ctx)
	if err != nil || len(blobs) != 1 {
		t.Fatalf("durable artifact changed by worker cleanup: blobs=%v err=%v", blobs, err)
	}
}

type recordingSourceArtifactStore struct {
	reconcileCalls int
	reconcileCount int64
	reference      sourceartifact.Reference
	found          bool
	missing        bool
	err            error
}

func (r *recordingSourceArtifactStore) GetReference(context.Context, string) (sourceartifact.Reference, bool, error) {
	if r.missing {
		return sourceartifact.Reference{}, false, r.err
	}
	found := r.found
	if !found && r.reference.OperationID != "" {
		found = true
	}
	return r.reference, found, r.err
}

func (r *recordingSourceArtifactStore) ReleaseTerminalReferences(context.Context, time.Time) (int64, error) {
	r.reconcileCalls++
	return r.reconcileCount, r.err
}

func TestBuildWorkerReleasesArtifactReferenceOnTerminalNotification(t *testing.T) {
	digest := "sha256:" + string(bytes.Repeat([]byte{'a'}, 64))
	releaser := &recordingSourceArtifactStore{reference: sourceartifact.Reference{
		OperationID:   "op-artifact",
		DeploymentID:  "dep-artifact",
		Digest:        digest,
		ProjectID:     "prj-artifact",
		ServiceID:     "svc-artifact",
		EnvironmentID: "env-artifact",
	}}
	executor := &buildJobExecutor{sourceArtifacts: releaser}
	artifactURI, err := sourceartifact.URI(digest)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(buildJobPayload{
		SchemaVersion: buildJobPayloadSchema,
		OperationID:   "op-artifact",
		ProjectID:     "prj-artifact",
		ServiceID:     "svc-artifact",
		EnvironmentID: "env-artifact",
		Request: buildJobRequest{Source: &buildJobSourceIntent{
			Provider: "artifact",
			Repo:     artifactURI,
			Ref:      digest,
		}, DeploymentID: "dep-artifact"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.OnBuildJobTerminalized(context.Background(), queue.Job{
		Queue:             queue.Build,
		ParentOperationID: "op-artifact",
		Payload:           payload,
	}); err != nil {
		t.Fatalf("OnBuildJobTerminalized: %v", err)
	}
	if releaser.reconcileCalls != 1 {
		t.Fatalf("reconcile calls = %d", releaser.reconcileCalls)
	}
}

func TestBuildWorkerTerminalNotificationNeverReleasesMismatchedArtifactAuthority(t *testing.T) {
	digest := "sha256:" + string(bytes.Repeat([]byte{'e'}, 64))
	uri, err := sourceartifact.URI(digest)
	if err != nil {
		t.Fatal(err)
	}
	reference := sourceartifact.Reference{
		OperationID:   "op",
		DeploymentID:  "dep",
		Digest:        digest,
		ProjectID:     "prj",
		ServiceID:     "svc",
		EnvironmentID: "env",
	}
	basePayload := buildJobPayload{
		SchemaVersion: buildJobPayloadSchema,
		OperationID:   "op",
		ProjectID:     "prj",
		ServiceID:     "svc",
		EnvironmentID: "env",
		Request: buildJobRequest{
			DeploymentID: "dep",
			Source: &buildJobSourceIntent{
				Provider: "artifact",
				Repo:     uri,
				Ref:      digest,
			},
		},
	}
	tests := map[string]func(*queue.Job, *buildJobPayload){
		"queue": func(job *queue.Job, _ *buildJobPayload) {
			job.Queue = queue.Deploy
		},
		"parent": func(job *queue.Job, _ *buildJobPayload) {
			job.ParentOperationID = "other"
		},
		"project": func(_ *queue.Job, payload *buildJobPayload) {
			payload.ProjectID = "other"
		},
		"ref": func(_ *queue.Job, payload *buildJobPayload) {
			payload.Request.Source.Ref = "main"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			payload := basePayload
			source := *basePayload.Request.Source
			payload.Request.Source = &source
			job := queue.Job{Queue: queue.Build, ParentOperationID: "op"}
			mutate(&job, &payload)
			job.Payload, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			store := &recordingSourceArtifactStore{reference: reference}
			executor := &buildJobExecutor{sourceArtifacts: store}
			if err := executor.OnBuildJobTerminalized(context.Background(), job); err == nil {
				t.Fatal("mismatched terminal artifact authority was accepted")
			}
			if store.reconcileCalls != 0 {
				t.Fatalf("mismatched terminal path reconciled %d times", store.reconcileCalls)
			}
		})
	}
}

func TestBuildJobLineageRejectsEveryEnvelopeAndDuplicateMismatch(t *testing.T) {
	base := buildJobPayload{
		SchemaVersion: buildJobPayloadSchema,
		OperationID:   "op",
		ProjectID:     "prj",
		ServiceID:     "svc",
		EnvironmentID: "env",
		Request: buildJobRequest{
			DeploymentID:  "dep",
			EnvironmentID: "env",
		},
		Candidate: buildJobCandidate{
			DeploymentID:  "dep",
			ProjectID:     "prj",
			ServiceID:     "svc",
			EnvironmentID: "env",
		},
	}
	baseJob := queue.Job{Queue: queue.Build, ParentOperationID: "op"}
	tests := map[string]func(*queue.Job, *buildJobPayload){
		"schema":    func(_ *queue.Job, payload *buildJobPayload) { payload.SchemaVersion = "other" },
		"queue":     func(job *queue.Job, _ *buildJobPayload) { job.Queue = queue.Deploy },
		"parent":    func(job *queue.Job, _ *buildJobPayload) { job.ParentOperationID = "other" },
		"operation": func(_ *queue.Job, payload *buildJobPayload) { payload.OperationID = "" },
		"deployment": func(_ *queue.Job, payload *buildJobPayload) {
			payload.Candidate.DeploymentID = "other"
		},
		"project": func(_ *queue.Job, payload *buildJobPayload) { payload.Candidate.ProjectID = "other" },
		"service": func(_ *queue.Job, payload *buildJobPayload) { payload.Candidate.ServiceID = "other" },
		"environment": func(_ *queue.Job, payload *buildJobPayload) {
			payload.Candidate.EnvironmentID = "other"
		},
		"request_environment": func(_ *queue.Job, payload *buildJobPayload) {
			payload.Request.EnvironmentID = "other"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			job, payload := baseJob, base
			mutate(&job, &payload)
			if _, err := validateBuildJobLineage(job, payload); !errors.Is(err, errSourceArtifactOwnership) {
				t.Fatalf("lineage error=%v, want ownership failure", err)
			}
		})
	}
}

type recordingSourceArtifactReconciler struct {
	calls int
	count int64
	err   error
}

func (r *recordingSourceArtifactReconciler) ReleaseTerminalReferences(context.Context, time.Time) (int64, error) {
	r.calls++
	return r.count, r.err
}

func TestBuildWorkerReconcilesTerminalArtifactReferencesWithoutLeakingErrors(t *testing.T) {
	var output bytes.Buffer
	reconciler := &recordingSourceArtifactReconciler{count: 2}
	reconcileTerminalSourceArtifactReferences(context.Background(), reconciler, time.Now().UTC(), &output)
	if reconciler.calls != 1 || !bytes.Contains(output.Bytes(), []byte("count=2")) {
		t.Fatalf("calls=%d output=%q", reconciler.calls, output.String())
	}

	output.Reset()
	reconciler.err = errors.New("postgres://user:secret@database/private")
	reconcileTerminalSourceArtifactReferences(context.Background(), reconciler, time.Now().UTC(), &output)
	if bytes.Contains(output.Bytes(), []byte("secret")) || !bytes.Contains(output.Bytes(), []byte("deferred")) {
		t.Fatalf("reconciliation error output was not generic: %q", output.String())
	}
}

func TestBuildWorkerGenericGitRejectsEveryInternalOrHelperTransport(t *testing.T) {
	local := t.TempDir()
	digest := "sha256:" + string(bytes.Repeat([]byte{'b'}, 64))
	artifactURI, err := sourceartifact.URI(digest)
	if err != nil {
		t.Fatal(err)
	}
	executor := &buildJobExecutor{cfg: workerConfig{
		CheckoutRoot: t.TempDir(),
		UploadRoot:   t.TempDir(),
	}}
	for _, provider := range []string{"", "git"} {
		for _, repo := range []string{
			artifactURI,
			"local://" + local,
			local,
			"file://" + local,
			"ssh://git@example.com/acme/repo.git",
			"git@example.com:acme/repo.git",
			"git://example.com/acme/repo.git",
			"ext::sh -c exploit",
			"https://127.0.0.1/repo.git",
			"https://localhost/repo.git",
		} {
			t.Run(provider+"/"+repo, func(t *testing.T) {
				descriptor := &buildJobSourceIntent{Provider: provider, Repo: repo, Ref: "main"}
				if _, err := executor.checkoutSource(
					context.Background(),
					"svc",
					descriptor,
					build.SourceRef{Repo: repo, Ref: "main"},
					nil,
				); err == nil {
					t.Fatalf("generic provider accepted reserved repo %q", repo)
				}
			})
		}
	}
}

func TestBuildWorkerLegacyLocalProviderAcceptsOnlyOwnedDirectCheckout(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "midnight-up-src-owned")
	if err := os.Mkdir(owned, 0o750); err != nil {
		t.Fatal(err)
	}
	executor := &buildJobExecutor{cfg: workerConfig{
		CheckoutRoot: t.TempDir(),
		UploadRoot:   root,
	}}
	source := build.SourceRef{Repo: "local://" + owned, Ref: "legacy"}
	descriptor := &buildJobSourceIntent{
		Provider: api.DeploySourceProviderLegacyLocal,
		Repo:     source.Repo,
		Ref:      source.Ref,
	}
	checkout, err := executor.checkoutSource(
		context.Background(),
		"svc",
		descriptor,
		source,
		nil,
	)
	if err != nil {
		t.Fatalf("owned legacy local checkout rejected: %v", err)
	}
	if checkout.WorkDir != owned {
		t.Fatalf("legacy checkout workdir=%q, want %q", checkout.WorkDir, owned)
	}

	outside := filepath.Join(t.TempDir(), "midnight-up-src-outside")
	if err := os.Mkdir(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		provider string
		repo     string
	}{
		{api.DeploySourceProviderLegacyLocal, "local://" + outside},
		{api.DeploySourceProviderLegacyLocal, "local://" + filepath.Join(root, "not-owned")},
		{" legacy-local ", "local://" + owned},
		{"LEGACY-LOCAL", "local://" + owned},
	} {
		descriptor := &buildJobSourceIntent{Provider: test.provider, Repo: test.repo, Ref: "legacy"}
		if _, err := executor.checkoutSource(
			context.Background(),
			"svc",
			descriptor,
			build.SourceRef{Repo: test.repo, Ref: "legacy"},
			nil,
		); err == nil {
			t.Fatalf("legacy provider=%q repo=%q bypassed ownership", test.provider, test.repo)
		}
	}
}

func TestBuildWorkerArtifactOwnershipRejectsEveryMismatchedAuthorityField(t *testing.T) {
	digest := "sha256:" + string(bytes.Repeat([]byte{'c'}, 64))
	uri, err := sourceartifact.URI(digest)
	if err != nil {
		t.Fatal(err)
	}
	baseIdentity := sourceArtifactCheckoutIdentity{
		OperationID:       "op",
		DeploymentID:      "dep",
		ProjectID:         "prj",
		ServiceID:         "svc",
		EnvironmentID:     "env",
		Queue:             queue.Build,
		ParentOperationID: "op",
	}
	baseReference := sourceartifact.Reference{
		OperationID:   "op",
		DeploymentID:  "dep",
		Digest:        digest,
		ProjectID:     "prj",
		ServiceID:     "svc",
		EnvironmentID: "env",
	}
	descriptor := buildJobSourceIntent{Provider: "artifact", Repo: uri, Ref: digest}
	source := build.SourceRef{Repo: uri, Ref: digest}
	releasedAt := time.Now().UTC()
	tests := map[string]func(*sourceArtifactCheckoutIdentity, *sourceartifact.Reference, *buildJobSourceIntent, *build.SourceRef, *recordingSourceArtifactStore){
		"missing": func(_ *sourceArtifactCheckoutIdentity, _ *sourceartifact.Reference, _ *buildJobSourceIntent, _ *build.SourceRef, store *recordingSourceArtifactStore) {
			store.missing = true
		},
		"released": func(_ *sourceArtifactCheckoutIdentity, ref *sourceartifact.Reference, _ *buildJobSourceIntent, _ *build.SourceRef, _ *recordingSourceArtifactStore) {
			ref.ReleasedAt = &releasedAt
		},
		"operation": func(identity *sourceArtifactCheckoutIdentity, _ *sourceartifact.Reference, _ *buildJobSourceIntent, _ *build.SourceRef, _ *recordingSourceArtifactStore) {
			identity.OperationID = "other"
		},
		"deployment": func(_ *sourceArtifactCheckoutIdentity, ref *sourceartifact.Reference, _ *buildJobSourceIntent, _ *build.SourceRef, _ *recordingSourceArtifactStore) {
			ref.DeploymentID = "other"
		},
		"project": func(_ *sourceArtifactCheckoutIdentity, ref *sourceartifact.Reference, _ *buildJobSourceIntent, _ *build.SourceRef, _ *recordingSourceArtifactStore) {
			ref.ProjectID = "other"
		},
		"service": func(_ *sourceArtifactCheckoutIdentity, ref *sourceartifact.Reference, _ *buildJobSourceIntent, _ *build.SourceRef, _ *recordingSourceArtifactStore) {
			ref.ServiceID = "other"
		},
		"environment": func(_ *sourceArtifactCheckoutIdentity, ref *sourceartifact.Reference, _ *buildJobSourceIntent, _ *build.SourceRef, _ *recordingSourceArtifactStore) {
			ref.EnvironmentID = "other"
		},
		"digest": func(_ *sourceArtifactCheckoutIdentity, ref *sourceartifact.Reference, _ *buildJobSourceIntent, _ *build.SourceRef, _ *recordingSourceArtifactStore) {
			ref.Digest = "sha256:" + string(bytes.Repeat([]byte{'d'}, 64))
		},
		"uri": func(_ *sourceArtifactCheckoutIdentity, _ *sourceartifact.Reference, descriptor *buildJobSourceIntent, source *build.SourceRef, _ *recordingSourceArtifactStore) {
			descriptor.Repo = strings.ToUpper(descriptor.Repo)
			source.Repo = descriptor.Repo
		},
		"ref": func(_ *sourceArtifactCheckoutIdentity, _ *sourceartifact.Reference, descriptor *buildJobSourceIntent, source *build.SourceRef, _ *recordingSourceArtifactStore) {
			descriptor.Ref = "main"
			source.Ref = "main"
		},
		"queue": func(identity *sourceArtifactCheckoutIdentity, _ *sourceartifact.Reference, _ *buildJobSourceIntent, _ *build.SourceRef, _ *recordingSourceArtifactStore) {
			identity.Queue = queue.Deploy
		},
		"parent": func(identity *sourceArtifactCheckoutIdentity, _ *sourceartifact.Reference, _ *buildJobSourceIntent, _ *build.SourceRef, _ *recordingSourceArtifactStore) {
			identity.ParentOperationID = "other"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			identity := baseIdentity
			reference := baseReference
			descriptorCopy := descriptor
			sourceCopy := source
			store := &recordingSourceArtifactStore{reference: reference}
			mutate(&identity, &reference, &descriptorCopy, &sourceCopy, store)
			store.reference = reference
			executor := &buildJobExecutor{sourceArtifacts: store}
			if _, err := executor.validateSourceArtifactOwnership(
				context.Background(),
				"svc",
				&descriptorCopy,
				sourceCopy,
				&identity,
				true,
			); !errors.Is(err, errSourceArtifactOwnership) {
				t.Fatalf("validation error=%v, want fail-closed ownership error", err)
			}
		})
	}
}

func TestBuildWorkerJanitorConfigRejectsUnsafeValues(t *testing.T) {
	t.Setenv("MIDNIGHT_SOURCE_ARTIFACT_GC_INTERVAL", "not-a-duration")
	if _, err := parseWorkerConfig(nil); err == nil {
		t.Fatal("invalid janitor environment duration was accepted")
	}
	t.Setenv("MIDNIGHT_SOURCE_ARTIFACT_GC_INTERVAL", "")
	for _, args := range [][]string{
		{"--source-artifact-gc-max-items=1"},
		{"--source-artifact-gc-max-verify-bytes=1024"},
		{"--execution-timeout=2h", "--lease-ttl=1h", "--source-artifact-work-retention=3h"},
	} {
		if _, err := parseWorkerConfig(args); err == nil {
			t.Fatalf("unsafe janitor config %v was accepted", args)
		}
	}
}

type recordingWorkerJanitor struct {
	quotaCalls        int
	casCalls          int
	legacyCalls       int
	casErr            error
	legacyErr         error
	quotaErr          error
	called            chan struct{}
	consumeCASContext bool
	legacyContextErr  error
}

func (r *recordingWorkerJanitor) ReconcileExpiredUploadReservations(
	context.Context,
	*sourceartifact.FSStore,
	time.Time,
	int,
) (int64, error) {
	r.quotaCalls++
	return 0, r.quotaErr
}

func (r *recordingWorkerJanitor) RunJanitor(ctx context.Context, _ *sourceartifact.FSStore, _ time.Time, _ sourceartifact.JanitorConfig) (sourceartifact.JanitorResult, error) {
	r.casCalls++
	if r.consumeCASContext {
		<-ctx.Done()
		return sourceartifact.JanitorResult{}, ctx.Err()
	}
	return sourceartifact.JanitorResult{}, r.casErr
}

func (r *recordingWorkerJanitor) RunLegacyJanitor(ctx context.Context, _ *sourceartifact.FSStore, _ time.Time, _ sourceartifact.JanitorConfig) (sourceartifact.LegacyJanitorResult, error) {
	r.legacyCalls++
	r.legacyContextErr = ctx.Err()
	if r.called != nil {
		select {
		case r.called <- struct{}{}:
		default:
		}
	}
	return sourceartifact.LegacyJanitorResult{}, r.legacyErr
}

func TestBuildWorkerAutomaticJanitorRunsBothPassesAndRedactsErrors(t *testing.T) {
	filesystem, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	janitor := &recordingWorkerJanitor{
		casErr:    errors.New("postgres://user:secret@db/private"),
		legacyErr: errors.New("/private/tenant/path"),
	}
	cfg := workerConfig{
		SourceArtifactGCTimeout: time.Second,
		SourceArtifactJanitor:   sourceartifact.DefaultJanitorConfig,
	}
	var output bytes.Buffer
	runSourceArtifactJanitor(context.Background(), janitor, filesystem, cfg, time.Now(), &output)
	if janitor.quotaCalls != 0 || janitor.casCalls != 1 || janitor.legacyCalls != 1 {
		t.Fatalf("janitor calls quota=%d cas=%d legacy=%d", janitor.quotaCalls, janitor.casCalls, janitor.legacyCalls)
	}
	if strings.Contains(output.String(), "secret") ||
		strings.Contains(output.String(), "/private/tenant") {
		t.Fatalf("janitor log leaked error details: %q", output.String())
	}

	janitor.casErr = nil
	janitor.legacyErr = nil
	janitor.consumeCASContext = true
	cfg.SourceArtifactGCTimeout = 5 * time.Millisecond
	runSourceArtifactJanitor(context.Background(), janitor, filesystem, cfg, time.Now(), &output)
	if janitor.legacyContextErr != nil {
		t.Fatalf("CAS timeout canceled the separate legacy pass context: %v", janitor.legacyContextErr)
	}

	janitor.consumeCASContext = false
	janitor.casErr = nil
	janitor.legacyErr = nil
	janitor.quotaErr = errors.New("postgres://quota:secret@db/private")
	runSourceArtifactJanitor(context.Background(), janitor, filesystem, cfg, time.Now(), &output)
	if janitor.quotaCalls != 1 ||
		!strings.Contains(output.String(), "quota reconciliation deferred") ||
		strings.Contains(output.String(), "quota:secret") {
		t.Fatalf("quota reconciliation calls=%d output=%q", janitor.quotaCalls, output.String())
	}
	janitor.quotaErr = nil
	janitor.called = make(chan struct{}, 1)
	cfg.SourceArtifactGCInterval = time.Millisecond
	loopCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSourceArtifactJanitorLoop(loopCtx, janitor, filesystem, cfg, &output)
	}()
	select {
	case <-janitor.called:
		cancel()
		<-done
	case <-time.After(time.Second):
		t.Fatal("automatic janitor ticker did not run")
	}
}

func workerArtifactArchive(t *testing.T, name, body string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o640,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
