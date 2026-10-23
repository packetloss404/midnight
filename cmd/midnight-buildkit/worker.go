// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/registry"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/internal/varrefs"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	defaultBuildWorkerID           = "midnight-buildkit-worker"
	defaultBuildWorkerPoll         = 2 * time.Second
	defaultBuildWorkerReclaim      = 30 * time.Second
	defaultBuildWorkerLeaseTTL     = 10 * time.Minute
	defaultBuildExecutionTimeout   = 30 * time.Minute
	defaultBuildWorkerOutputRoot   = "/var/lib/midnight/build"
	defaultBuildCacheRoot          = "/var/lib/midnight/buildkit/cache"
	defaultBuildCheckoutRoot       = "/var/lib/midnight/build/checkout"
	defaultBuildUploadRoot         = sourceartifact.DefaultRoot
	buildJobPayloadSchema          = "midnight.dev/deploy-job/v1alpha1"
	defaultSourceArtifactGC        = 15 * time.Minute
	defaultSourceArtifactGCTimeout = 30 * time.Second
	sourceArtifactWorkSafetyMargin = time.Minute
)

type workerConfig struct {
	WorkerID                 string
	PostgresDSN              string
	PostgresDSNFile          string
	OutputRoot               string
	CheckoutRoot             string
	UploadRoot               string
	CacheRoot                string
	BuildctlPath             string
	BuildkitAddr             string
	RailpackPath             string
	RailpackFrontend         string
	GitPath                  string
	GitHubAppID              string
	GitHubKeyPath            string
	GitHubAPIBaseURL         string
	GitHubCloneHost          string
	Registry                 string
	RegistryInsec            bool
	PollInterval             time.Duration
	ReclaimInterval          time.Duration
	LeaseTTL                 time.Duration
	ExecutionTimeout         time.Duration
	SourceArtifactGCInterval time.Duration
	SourceArtifactGCTimeout  time.Duration
	SourceArtifactJanitor    sourceartifact.JanitorConfig
	SkipMigrations           bool
}

func runWorker(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, err := parseWorkerConfig(args)
	if err != nil {
		return err
	}
	return serveBuildWorker(ctx, cfg, stdout, stderr)
}

func parseWorkerConfig(args []string) (workerConfig, error) {
	gcInterval, err := strictEnvDuration(
		defaultSourceArtifactGC,
		"MIDNIGHT_SOURCE_ARTIFACT_GC_INTERVAL",
	)
	if err != nil {
		return workerConfig{}, err
	}
	gcTimeout, err := strictEnvDuration(
		defaultSourceArtifactGCTimeout,
		"MIDNIGHT_SOURCE_ARTIFACT_GC_TIMEOUT",
	)
	if err != nil {
		return workerConfig{}, err
	}
	janitorConfig := sourceartifact.DefaultJanitorConfig
	janitorConfig.BlobRetention, err = strictEnvDuration(
		janitorConfig.BlobRetention,
		"MIDNIGHT_SOURCE_ARTIFACT_RETENTION",
	)
	if err != nil {
		return workerConfig{}, err
	}
	janitorConfig.ReferenceRetention, err = strictEnvDuration(
		janitorConfig.ReferenceRetention,
		"MIDNIGHT_SOURCE_ARTIFACT_REFERENCE_RETENTION",
	)
	if err != nil {
		return workerConfig{}, err
	}
	janitorConfig.WorkRetention, err = strictEnvDuration(
		janitorConfig.WorkRetention,
		"MIDNIGHT_SOURCE_ARTIFACT_WORK_RETENTION",
	)
	if err != nil {
		return workerConfig{}, err
	}
	janitorConfig.QuarantineRetention, err = strictEnvDuration(
		janitorConfig.QuarantineRetention,
		"MIDNIGHT_SOURCE_ARTIFACT_QUARANTINE_RETENTION",
	)
	if err != nil {
		return workerConfig{}, err
	}
	janitorConfig.MaxItems, err = strictEnvPositiveInt(
		janitorConfig.MaxItems,
		"MIDNIGHT_SOURCE_ARTIFACT_GC_MAX_ITEMS",
	)
	if err != nil {
		return workerConfig{}, err
	}
	janitorConfig.MaxVerifyBytes, err = strictEnvPositiveInt64(
		janitorConfig.MaxVerifyBytes,
		"MIDNIGHT_SOURCE_ARTIFACT_GC_MAX_VERIFY_BYTES",
	)
	if err != nil {
		return workerConfig{}, err
	}
	cfg := workerConfig{
		WorkerID:                 envFirst(defaultBuildWorkerID, "MIDNIGHT_BUILDKIT_WORKER_ID"),
		PostgresDSN:              envFirst("", pg.EnvDSN, "MIDNIGHT_DATABASE_URL"),
		PostgresDSNFile:          envFirst(pg.DefaultDSNFile, pg.EnvDSNFile),
		OutputRoot:               envFirst(defaultBuildWorkerOutputRoot, "MIDNIGHT_BUILDKIT_OUTPUT_ROOT"),
		CheckoutRoot:             envFirst(defaultBuildCheckoutRoot, "MIDNIGHT_BUILDKIT_CHECKOUT_ROOT"),
		UploadRoot:               envFirst(defaultBuildUploadRoot, "MIDNIGHT_UP_EXTRACTION_ROOT"),
		CacheRoot:                envFirst(defaultBuildCacheRoot, "MIDNIGHT_BUILDKIT_CACHE_ROOT"),
		BuildctlPath:             envFirst("buildctl", "MIDNIGHT_BUILDCTL"),
		BuildkitAddr:             envFirst("", "MIDNIGHT_BUILDKIT_ADDR"),
		RailpackPath:             envFirst("railpack", "MIDNIGHT_RAILPACK_BIN"),
		RailpackFrontend:         envFirst(build.RequiredRailpackFrontend, "MIDNIGHT_RAILPACK_FRONTEND"),
		GitPath:                  envFirst("git", "MIDNIGHT_GIT_BIN"),
		GitHubAppID:              envFirst("", "MIDNIGHT_GITHUB_APP_ID"),
		GitHubKeyPath:            envFirst("", "MIDNIGHT_GITHUB_PRIVATE_KEY_PATH"),
		GitHubAPIBaseURL:         envFirst("", "MIDNIGHT_GITHUB_API_BASE_URL"),
		GitHubCloneHost:          envFirst("", "MIDNIGHT_GITHUB_CLONE_HOST"),
		Registry:                 envFirst("registry.midnight.local:5000", "MIDNIGHT_REGISTRY_ENDPOINT", "MIDNIGHT_REGISTRY_ADDR"),
		RegistryInsec:            envBool(true, "MIDNIGHT_REGISTRY_INSECURE"),
		PollInterval:             envDuration(defaultBuildWorkerPoll, "MIDNIGHT_BUILDKIT_POLL_INTERVAL"),
		ReclaimInterval:          envDuration(defaultBuildWorkerReclaim, "MIDNIGHT_BUILDKIT_RECLAIM_INTERVAL"),
		LeaseTTL:                 envDuration(defaultBuildWorkerLeaseTTL, "MIDNIGHT_BUILDKIT_LEASE_TTL"),
		ExecutionTimeout:         envDuration(defaultBuildExecutionTimeout, "MIDNIGHT_BUILDKIT_EXECUTION_TIMEOUT"),
		SourceArtifactGCInterval: gcInterval,
		SourceArtifactGCTimeout:  gcTimeout,
		SourceArtifactJanitor:    janitorConfig,
	}
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.StringVar(&cfg.WorkerID, "worker-id", cfg.WorkerID, "stable worker id")
	fs.StringVar(&cfg.PostgresDSN, "postgres-dsn", cfg.PostgresDSN, "Postgres DSN")
	fs.StringVar(&cfg.PostgresDSNFile, "postgres-dsn-file", cfg.PostgresDSNFile, "Postgres DSN file (default: $MIDNIGHT_DB_DSN_FILE or /etc/midnight/db.dsn)")
	fs.StringVar(&cfg.OutputRoot, "output-root", cfg.OutputRoot, "per-build output directory root")
	fs.StringVar(&cfg.CheckoutRoot, "checkout-root", cfg.CheckoutRoot, "source checkout directory root")
	fs.StringVar(&cfg.UploadRoot, "upload-root", cfg.UploadRoot, "shared root containing midnight up source checkouts")
	fs.StringVar(&cfg.CacheRoot, "cache-root", cfg.CacheRoot, "BuildKit cache root")
	fs.StringVar(&cfg.BuildctlPath, "buildctl", cfg.BuildctlPath, "path to pinned buildctl binary")
	fs.StringVar(&cfg.BuildkitAddr, "buildkit-addr", cfg.BuildkitAddr, "BuildKit daemon address (empty uses buildctl/BUILDKIT_HOST discovery)")
	fs.StringVar(&cfg.RailpackPath, "railpack", cfg.RailpackPath, "path to railpack binary")
	fs.StringVar(&cfg.RailpackFrontend, "railpack-frontend", cfg.RailpackFrontend, "digest-pinned Railpack BuildKit frontend image")
	fs.StringVar(&cfg.GitPath, "git", cfg.GitPath, "path to git binary")
	fs.StringVar(&cfg.GitHubAppID, "github-app-id", cfg.GitHubAppID, "GitHub App id used only to mint installation checkout tokens")
	fs.StringVar(&cfg.GitHubKeyPath, "github-private-key", cfg.GitHubKeyPath, "path to the GitHub App private key PEM")
	fs.StringVar(&cfg.GitHubAPIBaseURL, "github-api-base-url", cfg.GitHubAPIBaseURL, "GitHub API base URL (for GHES)")
	fs.StringVar(&cfg.GitHubCloneHost, "github-clone-host", cfg.GitHubCloneHost, "host allowed to receive GitHub installation credentials (required for GHES)")
	fs.StringVar(&cfg.Registry, "registry", cfg.Registry, "destination registry endpoint")
	fs.BoolVar(&cfg.RegistryInsec, "registry-insecure", cfg.RegistryInsec, "push to registry over plain HTTP")
	fs.DurationVar(&cfg.PollInterval, "poll-interval", cfg.PollInterval, "build queue poll interval")
	fs.DurationVar(&cfg.ReclaimInterval, "reclaim-interval", cfg.ReclaimInterval, "expired lease reclaim interval")
	fs.DurationVar(&cfg.LeaseTTL, "lease-ttl", cfg.LeaseTTL, "build job lease duration")
	fs.DurationVar(&cfg.ExecutionTimeout, "execution-timeout", cfg.ExecutionTimeout, "hard timeout for checkout and build execution")
	fs.DurationVar(&cfg.SourceArtifactGCInterval, "source-artifact-gc-interval", cfg.SourceArtifactGCInterval, "source artifact janitor interval")
	fs.DurationVar(&cfg.SourceArtifactGCTimeout, "source-artifact-gc-timeout", cfg.SourceArtifactGCTimeout, "source artifact janitor pass timeout")
	fs.DurationVar(&cfg.SourceArtifactJanitor.BlobRetention, "source-artifact-retention", cfg.SourceArtifactJanitor.BlobRetention, "minimum source artifact blob age before quarantine")
	fs.DurationVar(&cfg.SourceArtifactJanitor.ReferenceRetention, "source-artifact-reference-retention", cfg.SourceArtifactJanitor.ReferenceRetention, "minimum released reference age before removal")
	fs.DurationVar(&cfg.SourceArtifactJanitor.WorkRetention, "source-artifact-work-retention", cfg.SourceArtifactJanitor.WorkRetention, "minimum staging and checkout age before quarantine")
	fs.DurationVar(&cfg.SourceArtifactJanitor.QuarantineRetention, "source-artifact-quarantine-retention", cfg.SourceArtifactJanitor.QuarantineRetention, "minimum quarantine grace before final deletion")
	fs.IntVar(&cfg.SourceArtifactJanitor.MaxItems, "source-artifact-gc-max-items", cfg.SourceArtifactJanitor.MaxItems, "maximum source artifact janitor items per pass")
	fs.Int64Var(&cfg.SourceArtifactJanitor.MaxVerifyBytes, "source-artifact-gc-max-verify-bytes", cfg.SourceArtifactJanitor.MaxVerifyBytes, "maximum source artifact bytes hashed under the lifecycle lock per pass")
	fs.BoolVar(&cfg.SkipMigrations, "skip-migrations", false, "skip applying pending schema migrations at startup")
	if err := fs.Parse(args); err != nil {
		return workerConfig{}, err
	}
	if strings.TrimSpace(cfg.WorkerID) == "" {
		return workerConfig{}, errors.New("worker id is required")
	}
	if strings.TrimSpace(cfg.OutputRoot) == "" {
		return workerConfig{}, errors.New("output root is required")
	}
	if strings.TrimSpace(cfg.CheckoutRoot) == "" {
		return workerConfig{}, errors.New("checkout root is required")
	}
	if strings.TrimSpace(cfg.UploadRoot) == "" {
		return workerConfig{}, errors.New("upload root is required")
	}
	if strings.TrimSpace(cfg.Registry) == "" {
		return workerConfig{}, errors.New("registry endpoint is required")
	}
	if cfg.PollInterval <= 0 || cfg.ReclaimInterval <= 0 || cfg.LeaseTTL <= 0 || cfg.ExecutionTimeout <= 0 {
		return workerConfig{}, errors.New("poll, reclaim, lease, and execution timeout intervals must be positive")
	}
	if cfg.SourceArtifactGCInterval <= 0 || cfg.SourceArtifactGCTimeout <= 0 {
		return workerConfig{}, errors.New("source artifact janitor interval and timeout must be positive")
	}
	if err := cfg.SourceArtifactJanitor.Validate(); err != nil {
		return workerConfig{}, err
	}
	quotaConfig, err := sourceartifact.LoadQuotaConfig(
		os.LookupEnv,
		sourceartifact.DefaultLimits.MaxCompressedBytes,
		sourceartifact.DefaultUploadLease,
	)
	if err != nil {
		return workerConfig{}, err
	}
	if err := quotaConfig.ValidateRetentionCoverage(
		cfg.SourceArtifactJanitor.BlobRetention,
		cfg.SourceArtifactJanitor.QuarantineRetention,
		cfg.SourceArtifactGCInterval,
		cfg.SourceArtifactGCTimeout,
	); err != nil {
		return workerConfig{}, err
	}
	workHeadroom := cfg.SourceArtifactJanitor.WorkRetention - cfg.ExecutionTimeout
	if workHeadroom <= cfg.LeaseTTL ||
		workHeadroom-cfg.LeaseTTL <= sourceArtifactWorkSafetyMargin {
		return workerConfig{}, errors.New("source artifact work retention must exceed execution timeout plus lease TTL and safety margin")
	}
	return cfg, nil
}

func serveBuildWorker(ctx context.Context, cfg workerConfig, stdout, stderr io.Writer) error {
	dsn := strings.TrimSpace(cfg.PostgresDSN)
	if dsn == "" {
		loaded, err := (&pg.FileDSNLoader{
			EnvVar:    pg.EnvDSN,
			EnvVarRef: pg.EnvDSNFile,
			Path:      cfg.PostgresDSNFile,
		}).Load()
		if err != nil {
			return fmt.Errorf("build worker: postgres dsn is required: %w", err)
		}
		dsn = loaded.DSN
	}
	pool, err := pg.Open(ctx, pg.PoolConfig{
		DSN:            dsn,
		MaxConns:       4,
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres ping: %w", err)
	}
	if !cfg.SkipMigrations {
		if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
	}
	store := queue.NewPostgresStore(pool)
	operationStore := api.NewPostgresOperationStore(pool)
	deploymentStore := api.NewPostgresDeploymentStore(pool)
	stagedWorkOutbox := api.NewPostgresStagedWorkOutbox(pool)
	stagedWorkOutbox.WorkerID = cfg.WorkerID + "-handoff"
	observabilityStore := observability.NewPostgresStore(pool)
	sourceArtifacts := sourceartifact.NewPostgresStore(pool)
	sourceArtifactFilesystem, err := sourceartifact.NewFSStore(cfg.UploadRoot)
	if err != nil {
		return fmt.Errorf("build worker: initialize source artifact storage: %w", err)
	}
	variableKeyProvider, err := secrets.NewFileKeyProvider(secrets.FileKEKConfig{})
	if err != nil {
		return fmt.Errorf("build worker: load variable KEK: %w", err)
	}
	variableEnvelopeStore := secrets.NewPostgresEnvelopeStore(pool)
	variableVault := secrets.NewVaultWithStore(variableKeyProvider, secrets.DenyByDefault{}, variableEnvelopeStore)
	decryptAudit := secrets.NewPostgresDecryptAuditSink(
		audit.NewPostgresChain(pool),
		cfg.WorkerID,
	)
	executor := &buildJobExecutor{
		cfg:               cfg,
		operationStore:    operationStore,
		deploymentStore:   deploymentStore,
		queueStore:        store,
		handoffStore:      stagedWorkOutbox,
		sourceArtifacts:   sourceArtifacts,
		githubSourceLinks: githubapp.NewSourceLinkStore(pool),
		observability:     observabilityStore,
		stdout:            stdout,
		stderr:            stderr,
		redactor:          build.NewRedactor(nil),
		buildVariables: varrefs.NewResolver(
			secrets.NewAuditedBuildLookup(variableVault, variableEnvelopeStore, decryptAudit),
		),
		serviceConfig: serviceconfig.NewStore(pool),
		githubTokenMinter: &githubAppTokenMinter{
			appID:      cfg.GitHubAppID,
			keyPath:    cfg.GitHubKeyPath,
			apiBaseURL: cfg.GitHubAPIBaseURL,
			readFile:   os.ReadFile,
		},
	}
	runSourceArtifactJanitor(
		ctx,
		sourceArtifacts,
		sourceArtifactFilesystem,
		cfg,
		time.Now().UTC(),
		stderr,
	)
	go runSourceArtifactJanitorLoop(
		ctx,
		sourceArtifacts,
		sourceArtifactFilesystem,
		cfg,
		stderr,
	)
	fmt.Fprintf(stdout, "midnight-buildkit worker drained as %s registry=%s output_root=%s\n", cfg.WorkerID, cfg.Registry, cfg.OutputRoot)
	if _, err := stagedWorkOutbox.DispatchStagedWork(ctx, 32); err != nil {
		fmt.Fprintf(stderr, "staged work dispatch failed: %v\n", err)
	}
	if _, err := store.Reclaim(ctx, time.Now().UTC()); err != nil {
		return err
	}
	reclaimTicker := time.NewTicker(cfg.ReclaimInterval)
	defer reclaimTicker.Stop()
	pollTicker := time.NewTicker(cfg.PollInterval)
	defer pollTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-reclaimTicker.C:
			if _, err := store.Reclaim(ctx, now.UTC()); err != nil {
				return err
			}
		case now := <-pollTicker.C:
			if _, err := stagedWorkOutbox.DispatchStagedWork(ctx, 32); err != nil {
				fmt.Fprintf(stderr, "staged work dispatch failed: %v\n", err)
			}
			if _, err := processOneBuildJob(ctx, store, cfg.WorkerID, cfg.LeaseTTL, cfg.ExecutionTimeout, now.UTC(), executor); err != nil {
				fmt.Fprintf(stderr, "build job processing failed: %v\n", err)
			}
		}
	}
}

type buildJobExec interface {
	Execute(ctx context.Context, job queue.Job) error
}

// buildJobTerminalObserver is called only after the queue store has durably
// terminalized the lease. Source artifact ownership must remain active when
// Complete/Fail does not commit so a reclaimed attempt can still materialize.
type buildJobTerminalObserver interface {
	OnBuildJobTerminalized(ctx context.Context, job queue.Job) error
}

func processOneBuildJob(ctx context.Context, store queue.Store, workerID string, leaseTTL, executionTimeout time.Duration, now time.Time, executor buildJobExec) (bool, error) {
	if store == nil {
		return false, errors.New("build worker: queue store is required")
	}
	if executor == nil {
		return false, errors.New("build worker: executor is required")
	}
	job, err := store.Lease(ctx, queue.LeaseRequest{
		Queue:     queue.Build,
		WorkerID:  workerID,
		LeaseTime: leaseTTL,
		Now:       now,
	})
	if errors.Is(err, queue.ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	execCtx, stopHeartbeat, err := startBuildLeaseHeartbeat(ctx, store, job, leaseTTL)
	if err != nil {
		return true, err
	}
	execCtx, cancelExecution := context.WithTimeout(execCtx, executionTimeout)
	err = executor.Execute(execCtx, job)
	cancelExecution()
	heartbeatErr := stopHeartbeat()
	if err == nil && heartbeatErr != nil {
		err = heartbeatErr
	}
	if err != nil {
		if failErr := store.Fail(ctx, job.ID, job.LeaseID, string(failurecodes.InternalError)); failErr != nil {
			return true, errors.Join(err, heartbeatErr, failErr)
		}
		return true, errors.Join(err, heartbeatErr, notifyBuildJobTerminalized(ctx, executor, job))
	}
	if err := store.Complete(ctx, job.ID, job.LeaseID); err != nil {
		return true, err
	}
	return true, notifyBuildJobTerminalized(ctx, executor, job)
}

func notifyBuildJobTerminalized(ctx context.Context, executor buildJobExec, job queue.Job) error {
	observer, ok := executor.(buildJobTerminalObserver)
	if !ok {
		return nil
	}
	return observer.OnBuildJobTerminalized(ctx, job)
}

func startBuildLeaseHeartbeat(ctx context.Context, store queue.Store, job queue.Job, leaseTTL time.Duration) (context.Context, func() error, error) {
	interval, err := queue.HeartbeatInterval(leaseTTL)
	if err != nil {
		return nil, nil, err
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case now := <-ticker.C:
				if _, err := queue.HeartbeatLease(heartbeatCtx, store, job, leaseTTL, now.UTC()); err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	stop := func() error {
		cancel()
		<-done
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	}
	return heartbeatCtx, stop, nil
}

// buildJobPayload mirrors the deploy intake's deployJobPayload shape; the
// build worker only needs the operation id, the structured source object, and
// the service/project ids for routing.
type buildJobPayload struct {
	SchemaVersion  string                     `json:"schema_version"`
	OperationID    string                     `json:"operation_id"`
	ServiceID      string                     `json:"service_id"`
	ProjectID      string                     `json:"project_id,omitempty"`
	EnvironmentID  string                     `json:"environment_id,omitempty"`
	Request        buildJobRequest            `json:"request"`
	Candidate      buildJobCandidate          `json:"candidate,omitempty"`
	Route          *buildJobRouteIntent       `json:"route,omitempty"`
	PreviousActive *buildJobPreviousActiveRef `json:"previous_active,omitempty"`
	EnqueuedAt     time.Time                  `json:"enqueued_at"`
}

type buildJobRequest struct {
	EnvironmentID string                `json:"environment_id,omitempty"`
	Source        *buildJobSourceIntent `json:"source,omitempty"`
	DeploymentID  string                `json:"deployment_id,omitempty"`
	InstanceID    string                `json:"instance_id,omitempty"`
	Route         *buildJobRouteIntent  `json:"route,omitempty"`
}

// Keep worker decoding locked to the control plane's durable source contract.
// This alias prevents a newly added non-secret identity field from being
// silently dropped by a second hand-maintained JSON struct.
type buildJobSourceIntent = api.DeploySourceIntent

type buildJobCandidate struct {
	DeploymentID  string `json:"deployment_id,omitempty"`
	InstanceID    string `json:"instance_id,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	ServiceID     string `json:"service_id,omitempty"`
	Image         string `json:"image,omitempty"`
}

type buildJobRouteIntent struct {
	Name   string `json:"name,omitempty"`
	Group  string `json:"group,omitempty"`
	Host   string `json:"host,omitempty"`
	Path   string `json:"path,omitempty"`
	Public bool   `json:"public"`
	TLS    bool   `json:"tls"`
}

type buildJobPreviousActiveRef struct {
	DeploymentID string `json:"deployment_id,omitempty"`
	InstanceID   string `json:"instance_id,omitempty"`
}

type buildJobExecutor struct {
	cfg               workerConfig
	operationStore    api.OperationStore
	deploymentStore   api.DeploymentStore
	queueStore        queue.Store
	handoffStore      api.BuildDeployHandoffStore
	sourceArtifacts   sourceArtifactReferenceStore
	githubSourceLinks githubSourceAuthorityStore
	observability     observability.Store
	stdout            io.Writer
	stderr            io.Writer
	// redactor scrubs credential-shaped tokens out of build output and failure
	// reasons before they are persisted to the operation event log. It is
	// applied centrally in appendEvent so every persisted message is covered.
	redactor build.Redactor
	// githubTokenMinter exists only in the build worker. Durable jobs retain
	// the non-secret installation/repository identity; a token is minted into
	// this process immediately before checkout and is never re-enqueued.
	githubTokenMinter installationTokenMinter
	// buildVariables decrypts the service's encrypted variable templates with
	// the build_inject purpose immediately before the solve. It is never
	// serialized into a queue job or deploy handoff.
	buildVariables *varrefs.Resolver
	// serviceConfig resolves the same immutable deployment snapshot used by
	// the deploy worker. Build context and strategy never trust queue copies.
	serviceConfig serviceconfig.Resolver
}

type sourceArtifactReferenceStore interface {
	GetReference(context.Context, string) (sourceartifact.Reference, bool, error)
	sourceArtifactReferenceReconciler
}

type githubSourceAuthorityStore interface {
	GetServiceLink(context.Context, string, string, string) (githubapp.DurableServiceLink, error)
}

type sourceArtifactReferenceReconciler interface {
	ReleaseTerminalReferences(context.Context, time.Time) (int64, error)
}

type sourceArtifactJanitor interface {
	ReconcileExpiredUploadReservations(
		context.Context,
		*sourceartifact.FSStore,
		time.Time,
		int,
	) (int64, error)
	RunJanitor(
		context.Context,
		*sourceartifact.FSStore,
		time.Time,
		sourceartifact.JanitorConfig,
	) (sourceartifact.JanitorResult, error)
	RunLegacyJanitor(
		context.Context,
		*sourceartifact.FSStore,
		time.Time,
		sourceartifact.JanitorConfig,
	) (sourceartifact.LegacyJanitorResult, error)
}

func runSourceArtifactJanitor(
	ctx context.Context,
	janitor sourceArtifactJanitor,
	filesystem *sourceartifact.FSStore,
	cfg workerConfig,
	now time.Time,
	stderr io.Writer,
) {
	if janitor == nil || filesystem == nil {
		return
	}
	passCtx, cancel := context.WithTimeout(ctx, cfg.SourceArtifactGCTimeout)
	result, err := janitor.RunJanitor(
		passCtx,
		filesystem,
		now.UTC(),
		cfg.SourceArtifactJanitor,
	)
	cancel()
	casConverged := err == nil && !result.Skipped
	if err != nil {
		fmt.Fprintln(stderr, "build worker: source artifact janitor deferred")
	} else if result.Skipped {
		fmt.Fprintln(stderr, "build worker: source artifact janitor skipped because lifecycle work is active")
	} else if result.TerminalReferencesReleased+
		result.ReleasedReferencesRemoved+
		result.MetadataRemoved != 0 ||
		result.BlobsQuarantined+
			result.StagingQuarantined+
			result.CheckoutsQuarantined+
			result.QuarantineRestored+
			result.QuarantineFinalized != 0 {
		fmt.Fprintf(
			stderr,
			"build worker: source artifact janitor completed refs_released=%d refs_removed=%d metadata_removed=%d blobs_quarantined=%d staging_quarantined=%d checkouts_quarantined=%d quarantine_restored=%d quarantine_finalized=%d\n",
			result.TerminalReferencesReleased,
			result.ReleasedReferencesRemoved,
			result.MetadataRemoved,
			result.BlobsQuarantined,
			result.StagingQuarantined,
			result.CheckoutsQuarantined,
			result.QuarantineRestored,
			result.QuarantineFinalized,
		)
	}
	if casConverged {
		quotaCtx, quotaCancel := context.WithTimeout(ctx, cfg.SourceArtifactGCTimeout)
		quotaRemoved, quotaErr := janitor.ReconcileExpiredUploadReservations(
			quotaCtx,
			filesystem,
			now.UTC(),
			cfg.SourceArtifactJanitor.MaxItems,
		)
		quotaCancel()
		if quotaErr != nil {
			fmt.Fprintln(stderr, "build worker: source artifact quota reconciliation deferred")
		} else if quotaRemoved > 0 {
			fmt.Fprintf(
				stderr,
				"build worker: source artifact quota reconciliation completed accounting_records_removed=%d\n",
				quotaRemoved,
			)
		}
	}
	legacyCtx, legacyCancel := context.WithTimeout(ctx, cfg.SourceArtifactGCTimeout)
	defer legacyCancel()
	legacy, err := janitor.RunLegacyJanitor(
		legacyCtx,
		filesystem,
		now.UTC(),
		cfg.SourceArtifactJanitor,
	)
	if err != nil {
		fmt.Fprintln(stderr, "build worker: legacy source janitor deferred")
		return
	}
	if legacy.Skipped {
		fmt.Fprintln(stderr, "build worker: legacy source janitor skipped because another pass is active")
	} else if legacy.StagesQuarantined+
		legacy.CheckoutsQuarantined+
		legacy.QuarantineRestored+
		legacy.QuarantineFinalized != 0 {
		fmt.Fprintf(
			stderr,
			"build worker: legacy source janitor completed stages_quarantined=%d checkouts_quarantined=%d quarantine_restored=%d quarantine_finalized=%d candidates_examined=%d protected_checkouts=%d\n",
			legacy.StagesQuarantined,
			legacy.CheckoutsQuarantined,
			legacy.QuarantineRestored,
			legacy.QuarantineFinalized,
			legacy.CandidatesExamined,
			legacy.ProtectedCheckoutRefs,
		)
	}
}

func runSourceArtifactJanitorLoop(
	ctx context.Context,
	janitor sourceArtifactJanitor,
	filesystem *sourceartifact.FSStore,
	cfg workerConfig,
	stderr io.Writer,
) {
	ticker := time.NewTicker(cfg.SourceArtifactGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			runSourceArtifactJanitor(ctx, janitor, filesystem, cfg, now, stderr)
		}
	}
}

func reconcileTerminalSourceArtifactReferences(
	ctx context.Context,
	reconciler sourceArtifactReferenceReconciler,
	now time.Time,
	stderr io.Writer,
) {
	if reconciler == nil {
		return
	}
	released, err := reconciler.ReleaseTerminalReferences(ctx, now)
	if err != nil {
		fmt.Fprintln(stderr, "build worker: source artifact ownership reconciliation deferred")
		return
	}
	if released > 0 {
		fmt.Fprintf(stderr, "build worker: reconciled source artifact ownership references count=%d\n", released)
	}
}

// OnBuildJobTerminalized releases durable source ownership only after the
// queue attempt is irreversibly Complete or Failed. A crash or terminal-store
// error leaves the reference active for conservative replay/reconciliation.
func (e *buildJobExecutor) OnBuildJobTerminalized(ctx context.Context, job queue.Job) error {
	if e == nil || e.sourceArtifacts == nil {
		return nil
	}
	var payload buildJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("build worker: terminal source ownership payload parse: %w", err)
	}
	if payload.Request.Source == nil ||
		payload.Request.Source.Provider != "artifact" {
		return nil
	}
	deploymentID, err := validateBuildJobLineage(job, payload)
	if err != nil {
		return errSourceArtifactOwnership
	}
	identity := &sourceArtifactCheckoutIdentity{
		OperationID:       payload.OperationID,
		DeploymentID:      deploymentID,
		ProjectID:         payload.ProjectID,
		ServiceID:         payload.ServiceID,
		EnvironmentID:     payload.EnvironmentID,
		Queue:             job.Queue,
		ParentOperationID: job.ParentOperationID,
	}
	reference, err := e.validateSourceArtifactOwnership(
		ctx,
		payload.ServiceID,
		payload.Request.Source,
		build.SourceRef{Repo: payload.Request.Source.Repo, Ref: payload.Request.Source.Ref},
		identity,
		false,
	)
	if err != nil {
		return err
	}
	if reference.ReleasedAt != nil {
		return nil
	}
	if _, err := e.sourceArtifacts.ReleaseTerminalReferences(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("build worker: terminal source ownership reconciliation: %w", err)
	}
	return nil
}

func (e *buildJobExecutor) Execute(ctx context.Context, job queue.Job) error {
	var payload buildJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("build worker: payload parse: %w", err)
	}
	if payload.Request.Source == nil {
		return errors.New("build worker: payload missing source")
	}
	if err := validateBuildSourceTrustProfile(payload.Request.Source); err != nil {
		return err
	}
	buildPolicy := sourceBuildExecutionPolicy(payload.Request.Source, e.cfg.CacheRoot)
	deploymentID, err := validateBuildJobLineage(job, payload)
	if err != nil {
		return err
	}
	opID := orailtypes.ID(payload.OperationID)
	source := build.SourceRef{
		Repo:     payload.Request.Source.Repo,
		Ref:      payload.Request.Source.Ref,
		FetchRef: payload.Request.Source.FetchRef,
	}
	identity := &sourceArtifactCheckoutIdentity{
		OperationID:       payload.OperationID,
		DeploymentID:      deploymentID,
		ProjectID:         payload.ProjectID,
		ServiceID:         payload.ServiceID,
		EnvironmentID:     payload.EnvironmentID,
		Queue:             job.Queue,
		ParentOperationID: job.ParentOperationID,
	}
	if payload.Request.Source.Provider == "artifact" {
		if _, err := e.validateSourceArtifactOwnership(
			ctx,
			payload.ServiceID,
			payload.Request.Source,
			source,
			identity,
			true,
		); err != nil {
			return err
		}
	}
	if recovery, ok := e.handoffStore.(api.BuildDeployHandoffRecoveryStore); ok && deploymentID != "" {
		committed, found, err := recovery.GetBuildDeployHandoff(ctx, deploymentID)
		if err != nil {
			return fmt.Errorf("build worker: durable handoff recovery: %w", err)
		}
		if found {
			if committed.OperationID != payload.OperationID {
				return errors.New("build worker: durable handoff operation mismatch")
			}
			if build.IsLocalSourceURI(payload.Request.Source.Repo) {
				_ = cleanupLocalUpload(payload.Request.Source.Repo, e.cfg.UploadRoot)
			}
			// The previous execution atomically committed both the immutable
			// image result and deploy work. Completing this retried build job
			// is correct; rebuilding or re-enqueueing would be harmful.
			return nil
		}
	}
	effectiveConfig := serviceconfig.Default(payload.ProjectID, payload.ServiceID, payload.EnvironmentID)
	var pinnedBuildVariables *[]varrefs.VariableVersionRef
	if e.serviceConfig != nil {
		snapshot, err := e.serviceConfig.ResolveDeployment(
			ctx,
			deploymentID,
			payload.ProjectID,
			payload.EnvironmentID,
			payload.ServiceID,
		)
		if err != nil {
			e.failOperation(ctx, opID, err, "deployment config resolution")
			return err
		}
		effectiveConfig = snapshot.Config
		refs := make([]varrefs.VariableVersionRef, 0, len(snapshot.VariableVersions))
		for _, ref := range snapshot.VariableVersions {
			refs = append(refs, varrefs.VariableVersionRef{
				ID:                 ref.ID,
				ServiceID:          ref.ServiceID,
				ServiceSlug:        ref.ServiceSlug,
				ServiceDisplayName: ref.ServiceDisplayName,
				Name:               ref.Name,
				Version:            ref.Version,
			})
		}
		pinnedBuildVariables = &refs
	} else {
		// Compatibility seam for isolated unit executors. Production wiring
		// always sets serviceConfig and therefore never trusts this queue copy.
		effectiveConfig.RootDirectory = buildSourceRootDirectory(payload.Request.Source)
	}
	if err := e.beginCanonicalBuild(ctx, payload.OperationID); err != nil {
		e.failOperation(ctx, opID, err, "canonical build transition")
		return err
	}
	source.Subpath = effectiveConfig.RootDirectory
	// Transition operation -> running so the operation FSM matches the queue
	// lease state.
	if _, err := e.operationStore.Transition(ctx, opID, orailtypes.OperationRunning); err != nil {
		// Best-effort: the operation may already be running on a retry.
		fmt.Fprintf(e.stderr, "build worker: transition to running failed: %v\n", err)
	}
	e.appendEvent(ctx, opID, orailtypes.OperationRunning, fmt.Sprintf("build worker leased job_id=%s queue=%s", job.ID, job.Queue))
	if canceled, err := e.cancelBuildAtSafePoint(
		ctx,
		opID,
		"before source checkout",
	); err != nil {
		return err
	} else if canceled {
		return nil
	}

	checkout, err := e.checkoutSource(
		ctx,
		payload.ServiceID,
		payload.Request.Source,
		source,
		identity,
	)
	if err != nil {
		e.failOperation(ctx, opID, err, "source checkout")
		return err
	}
	defer func() { _ = checkout.Discard() }()
	e.appendEvent(ctx, opID, orailtypes.OperationRunning, fmt.Sprintf("source checkout: repo=%s ref=%s commit=%s", source.Repo, source.Ref, checkout.CommitID))

	var buildSecretIDs []string
	if buildPolicy.AllowBuildSecrets {
		buildSecretIDs, err = listBuildSecretIDsAtVersions(
			ctx,
			e.buildVariables,
			payload.ProjectID,
			payload.EnvironmentID,
			payload.ServiceID,
			pinnedBuildVariables,
		)
		if err != nil {
			e.failOperation(ctx, opID, err, "build variable metadata")
			return err
		}
	}
	driver := build.BuildKitDriver{
		Runner:    build.BuildctlRunner{BuildctlPath: e.cfg.BuildctlPath, Address: e.cfg.BuildkitAddr},
		CacheRoot: buildPolicy.CacheRoot,
		OutputDir: filepath.Join(e.cfg.OutputRoot, sanitizeID(payload.ServiceID)+"-"+sanitizeID(payload.OperationID)),
		ProgressSink: func(line string) {
			e.appendEvent(ctx, opID, orailtypes.OperationRunning, "build progress: "+line)
		},
	}
	dockerfileBuilder := &build.DockerfileBuilder{
		ServiceID:      payload.ServiceID,
		Driver:         driver,
		DockerfileName: effectiveConfig.DockerfilePath,
	}
	railpackBuilder := build.RailpackBuilder{
		ServiceID:      payload.ServiceID,
		Binary:         e.cfg.RailpackPath,
		FrontendSource: e.cfg.RailpackFrontend,
		Driver:         driver,
		SecretIDs:      append([]string(nil), buildSecretIDs...),
		BuildCommand:   effectiveConfig.BuildCommand,
	}
	builder, err := selectConfiguredBuilder(effectiveConfig.Builder, checkout, dockerfileBuilder, &railpackBuilder)
	if err != nil {
		e.failOperation(ctx, opID, err, "builder selection")
		return err
	}
	if effectiveConfig.BuildCommand != "" {
		if _, railpack := builder.(*build.RailpackBuilder); !railpack {
			err := fmt.Errorf("%w: build_command is supported by Railpack builds, not Dockerfile builds", serviceconfig.ErrUnsupported)
			e.failOperation(ctx, opID, err, "deployment config")
			return err
		}
	}

	imageRepository := buildImageRepository(payload.ServiceID, payload.ProjectID, buildPolicy.QuarantineImage)
	tag := buildImageTag(checkout.CommitID, payload.OperationID)
	target := build.BuildTarget{
		Image:    imageRepository + ":" + tag,
		Registry: e.cfg.Registry,
		Insecure: e.cfg.RegistryInsec,
	}
	pushCfg := registry.PushConfig{Endpoint: e.cfg.Registry, Insecure: e.cfg.RegistryInsec}
	fullImage := pushCfg.QualifiedImageRef(target.Image)
	target.Image = fullImage

	planSnapshot, planErr := builder.Plan(ctx, checkout)
	if planErr != nil {
		e.failOperation(ctx, opID, planErr, "build plan")
		return planErr
	}
	e.appendEvent(ctx, opID, orailtypes.OperationRunning, fmt.Sprintf("build plan: kind=%s context=%s", planSnapshot.Kind, planSnapshot.ContextDir))
	if planSnapshot.Kind == build.PlanKindRailpack {
		e.appendEvent(ctx, opID, orailtypes.OperationRunning, fmt.Sprintf(
			"railpack prepared: version=%s providers=%s plan=%s info=%s",
			planSnapshot.Meta["railpack_version"],
			planSnapshot.Meta["providers"],
			planSnapshot.Meta["plan_path"],
			planSnapshot.Meta["info_path"],
		))
	}

	if err := e.verifyGitHubSourceAuthority(
		ctx,
		payload.ProjectID,
		payload.ServiceID,
		payload.Request.Source,
	); err != nil {
		e.failOperation(ctx, opID, err, "GitHub source authority before build variables")
		return err
	}
	var buildSecrets []build.BuildSecret
	if buildPolicy.AllowBuildSecrets {
		buildSecrets, err = resolveBuildSecretsAtVersions(
			secrets.WithDecryptAuditExecution(ctx, secrets.DecryptAuditExecution{
				Kind:         secrets.DecryptExecutionBuild,
				ExecutionID:  job.ID,
				DeploymentID: deploymentID,
				OperationID:  payload.OperationID,
				Attempt:      job.Attempt,
			}),
			e.buildVariables,
			payload.ProjectID,
			payload.EnvironmentID,
			payload.ServiceID,
			buildSecretIDs,
			pinnedBuildVariables,
		)
		if err != nil {
			e.failOperation(ctx, opID, err, "build variable resolution")
			return err
		}
	}
	defer build.ZeroBuildSecrets(buildSecrets)
	secretValues := make([]string, 0, len(buildSecrets))
	for _, secret := range buildSecrets {
		secretValues = append(secretValues, string(secret.Value))
	}
	previousRedactor := e.redactor
	e.redactor = build.NewRedactor(secretValues)
	defer func() { e.redactor = previousRedactor }()

	dockerfileBuilder.Driver.Secrets = build.CopyBuildSecrets(buildSecrets)
	defer build.ZeroBuildSecrets(dockerfileBuilder.Driver.Secrets)
	railpackBuilder.Driver.Secrets = build.CopyBuildSecrets(buildSecrets)
	defer build.ZeroBuildSecrets(railpackBuilder.Driver.Secrets)
	if len(buildSecrets) > 0 {
		buildArgs := make(map[string]string, len(railpackBuilder.Driver.BuildArgs)+1)
		for name, value := range railpackBuilder.Driver.BuildArgs {
			buildArgs[name] = value
		}
		buildArgs[build.RailpackSecretsHashBuildArg] = build.BuildSecretsHash(buildSecrets)
		railpackBuilder.Driver.BuildArgs = buildArgs
	}
	e.appendEvent(ctx, opID, orailtypes.OperationRunning, fmt.Sprintf("build variables resolved for ephemeral BuildKit mounts: count=%d", len(buildSecrets)))
	if canceled, err := e.cancelBuildAtSafePoint(
		ctx,
		opID,
		"before build solve",
	); err != nil {
		return err
	} else if canceled {
		return nil
	}
	if err := e.verifyGitHubSourceAuthority(
		ctx,
		payload.ProjectID,
		payload.ServiceID,
		payload.Request.Source,
	); err != nil {
		e.failOperation(ctx, opID, err, "GitHub source authority before build solve")
		return err
	}

	imageRef, err := builder.Build(ctx, checkout, target)
	if err != nil {
		e.failOperation(ctx, opID, err, "build solve")
		return err
	}
	if canceled, err := e.cancelBuildAtSafePoint(
		ctx,
		opID,
		"after build solve before deploy handoff",
	); err != nil {
		return err
	} else if canceled {
		return nil
	}
	digestRef := imageRef.DigestRef()
	if digestRef == "" {
		err := errors.New("build worker: builder returned empty digest")
		e.failOperation(ctx, opID, err, "build digest")
		return err
	}
	var canonicalDeployment api.DeploymentRecord
	if e.deploymentStore != nil {
		deployment, lookupErr := e.deploymentStore.GetDeploymentByOperation(ctx, payload.OperationID)
		if lookupErr != nil {
			err := fmt.Errorf("canonical deployment lookup after build: %w", lookupErr)
			e.failOperation(ctx, opID, err, "build persistence")
			return err
		}
		canonicalDeployment = deployment
		if e.handoffStore == nil {
			if err := e.deploymentStore.RecordDeploymentBuild(
				ctx,
				deployment.ID,
				checkout.CommitID,
				string(planSnapshot.Kind),
				imageRef.Image,
				imageRef.Digest,
			); err != nil {
				err = fmt.Errorf("canonical build result persistence: %w", err)
				e.failOperation(ctx, opID, err, "build persistence")
				return err
			}
		}
	}
	e.appendEvent(ctx, opID, orailtypes.OperationRunning, "build pushed: image="+digestRef)

	// Hand off to the deploy queue with the resolved digest image. Reuse the
	// original payload's route/previous_active/instance so the deploy worker
	// can promote in a single hop.
	handoff := api.BuildDeployHandoffCommit{
		OperationID:  payload.OperationID,
		DeploymentID: canonicalDeployment.ID,
		SourceSHA:    checkout.CommitID,
		Builder:      string(planSnapshot.Kind),
		ImageRef:     imageRef.Image,
		ImageDigest:  imageRef.Digest,
		Message:      "build result and deploy handoff durably committed",
	}
	deployHandoff, err := e.enqueueDeployAfterBuild(ctx, payload, digestRef, handoff)
	if err != nil {
		e.failOperation(ctx, opID, err, "deploy handoff")
		return err
	}
	if deployHandoff.Durable {
		if deployHandoff.Created {
			e.appendEvent(ctx, opID, orailtypes.OperationRunning, fmt.Sprintf("build phase completed: image_digest=%s deploy_outbox_id=%s delivery_state=pending", digestRef, deployHandoff.OutboxID))
		}
	} else {
		e.appendEvent(ctx, opID, orailtypes.OperationRunning, fmt.Sprintf("build phase completed: image_digest=%s deploy_job_id=%s", digestRef, deployHandoff.Job.ID))
	}
	if build.IsLocalSourceURI(source.Repo) {
		if err := cleanupLocalUpload(source.Repo, e.cfg.UploadRoot); err != nil {
			e.appendEvent(ctx, opID, orailtypes.OperationRunning, "local upload cleanup deferred: "+err.Error())
		}
	}
	return nil
}

func listBuildSecretIDs(ctx context.Context, resolver *varrefs.Resolver, projectID, environmentID, serviceID string) ([]string, error) {
	return listBuildSecretIDsAtVersions(ctx, resolver, projectID, environmentID, serviceID, nil)
}

func listBuildSecretIDsAtVersions(
	ctx context.Context,
	resolver *varrefs.Resolver,
	projectID, environmentID, serviceID string,
	pinned *[]varrefs.VariableVersionRef,
) ([]string, error) {
	if resolver == nil || resolver.Lookup == nil {
		return nil, nil
	}
	lookup := resolver.Lookup
	if pinned != nil {
		versioned, ok := lookup.(varrefs.VersionedVariableLookup)
		if !ok {
			return nil, errors.New("versioned build variable lookup is unavailable")
		}
		lookup = versioned.WithVariableVersions(*pinned)
	}
	names, err := lookup.ListServiceVariables(ctx, projectID, environmentID, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list build variable metadata: %w", err)
	}
	sort.Strings(names)
	if err := build.ValidateBuildSecretIDs(names); err != nil {
		return nil, err
	}
	return names, nil
}

func resolveBuildSecrets(
	ctx context.Context,
	resolver *varrefs.Resolver,
	projectID, environmentID, serviceID string,
	secretIDs []string,
) ([]build.BuildSecret, error) {
	return resolveBuildSecretsAtVersions(ctx, resolver, projectID, environmentID, serviceID, secretIDs, nil)
}

func resolveBuildSecretsAtVersions(
	ctx context.Context,
	resolver *varrefs.Resolver,
	projectID, environmentID, serviceID string,
	secretIDs []string,
	pinned *[]varrefs.VariableVersionRef,
) ([]build.BuildSecret, error) {
	if len(secretIDs) == 0 {
		return nil, nil
	}
	if resolver == nil || resolver.Lookup == nil {
		return nil, errors.New("build variable resolver is unavailable")
	}
	var (
		resolved map[string]string
		err      error
	)
	if pinned == nil {
		resolved, err = resolver.Resolve(ctx, projectID, environmentID, serviceID)
	} else {
		resolved, err = resolver.ResolveAtVersions(ctx, projectID, environmentID, serviceID, *pinned)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve build variables: %w", err)
	}
	secretsOut := make([]build.BuildSecret, 0, len(secretIDs))
	for _, id := range secretIDs {
		value, exists := resolved[id]
		if !exists {
			build.ZeroBuildSecrets(secretsOut)
			return nil, fmt.Errorf("build variable %q changed during plan preparation", id)
		}
		secretsOut = append(secretsOut, build.BuildSecret{ID: id, Value: []byte(value)})
		delete(resolved, id)
	}
	return secretsOut, nil
}

var errGitHubAppCredentialsUnavailable = errors.New("github app credentials unavailable")

type installationTokenMinter interface {
	MintRepositoryToken(ctx context.Context, installationID, repositoryID int64) (githubapp.InstallationToken, error)
}

// githubAppTokenMinter loads the operator key and constructs the App client
// only when a private checkout actually needs it. Public git deployments do
// not require GitHub configuration, and the webhook HMAC secret is never
// distributed to this worker.
type githubAppTokenMinter struct {
	appID      string
	keyPath    string
	apiBaseURL string
	readFile   func(string) ([]byte, error)
	httpDoer   githubapp.HTTPDoer
}

func (m *githubAppTokenMinter) MintRepositoryToken(
	ctx context.Context,
	installationID,
	repositoryID int64,
) (githubapp.InstallationToken, error) {
	if m == nil {
		return githubapp.InstallationToken{}, errGitHubAppCredentialsUnavailable
	}
	appID, err := strconv.ParseInt(strings.TrimSpace(m.appID), 10, 64)
	if err != nil || appID <= 0 || strings.TrimSpace(m.keyPath) == "" {
		return githubapp.InstallationToken{}, fmt.Errorf("%w: set MIDNIGHT_GITHUB_APP_ID and MIDNIGHT_GITHUB_PRIVATE_KEY_PATH on the build worker", errGitHubAppCredentialsUnavailable)
	}
	readFile := m.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	privateKey, err := readFile(strings.TrimSpace(m.keyPath))
	if err != nil {
		return githubapp.InstallationToken{}, fmt.Errorf("%w: read private key: %v", errGitHubAppCredentialsUnavailable, err)
	}
	opts := []githubapp.AppClientOption{}
	if m.httpDoer != nil {
		opts = append(opts, githubapp.WithHTTPDoer(m.httpDoer))
	}
	client, err := githubapp.NewAppClient(githubapp.AppConfig{
		AppID:         appID,
		PrivateKeyPEM: privateKey,
		APIBaseURL:    strings.TrimSpace(m.apiBaseURL),
	}, opts...)
	if err != nil {
		return githubapp.InstallationToken{}, fmt.Errorf("%w: %v", errGitHubAppCredentialsUnavailable, err)
	}
	token, err := client.MintRepositoryToken(ctx, installationID, repositoryID)
	if err != nil {
		return githubapp.InstallationToken{}, fmt.Errorf("github installation token mint failed: %w", err)
	}
	return token, nil
}

var errSourceArtifactOwnership = errors.New("source checkout: active artifact ownership validation failed")

type sourceArtifactCheckoutIdentity struct {
	OperationID       string
	DeploymentID      string
	ProjectID         string
	ServiceID         string
	EnvironmentID     string
	Queue             queue.Name
	ParentOperationID string
}

func (e *buildJobExecutor) checkoutSource(
	ctx context.Context,
	serviceID string,
	descriptor *buildJobSourceIntent,
	source build.SourceRef,
	identity *sourceArtifactCheckoutIdentity,
) (build.SourceCheckout, error) {
	planner := build.SourceCheckoutPlanner{
		Root:         e.cfg.CheckoutRoot,
		ArtifactRoot: e.cfg.UploadRoot,
		GitBinary:    e.cfg.GitPath,
	}
	provider := ""
	if descriptor != nil {
		provider = descriptor.Provider
	}
	if provider == "" || provider == "git" {
		if err := api.ValidateGenericGitSourceRepo(source.Repo); err != nil {
			return build.SourceCheckout{}, errors.New("source checkout: generic git source must be a canonical public-host HTTPS URL")
		}
		return planner.Checkout(ctx, serviceID, source)
	}
	if provider == api.DeploySourceProviderLegacyLocal {
		if err := validateOwnedLegacyLocalSource(source.Repo, e.cfg.UploadRoot); err != nil {
			return build.SourceCheckout{}, err
		}
		return planner.Checkout(ctx, serviceID, source)
	}
	if provider == "artifact" {
		reference, err := e.validateSourceArtifactOwnership(
			ctx,
			serviceID,
			descriptor,
			source,
			identity,
			true,
		)
		if err != nil {
			return build.SourceCheckout{}, err
		}
		filesystem, err := sourceartifact.NewFSStore(e.cfg.UploadRoot)
		if err != nil {
			return build.SourceCheckout{}, errors.New("source checkout: artifact storage is unavailable")
		}
		if err := filesystem.RestoreRequiredBlob(ctx, reference.Digest); err != nil {
			return build.SourceCheckout{}, errors.New("source checkout: owned artifact storage integrity check failed")
		}
		return planner.Checkout(ctx, serviceID, source)
	}
	if provider != "github" {
		return build.SourceCheckout{}, fmt.Errorf("source checkout: unsupported provider %q", descriptor.Provider)
	}
	if descriptor.InstallationID <= 0 ||
		descriptor.RepositoryID <= 0 ||
		descriptor.LinkResourceVersion <= 0 ||
		strings.TrimSpace(descriptor.WorkspaceID) == "" ||
		strings.TrimSpace(descriptor.Repository) == "" {
		return build.SourceCheckout{}, errors.New("source checkout: github descriptor requires canonical workspace, installation, repository, and link version")
	}
	if err := validateGitHubCheckoutTarget(source.Repo, descriptor.Repository, e.cfg.GitHubCloneHost, e.cfg.GitHubAPIBaseURL); err != nil {
		return build.SourceCheckout{}, err
	}
	projectID := ""
	if identity != nil {
		projectID = identity.ProjectID
	}
	if err := e.verifyGitHubSourceAuthority(ctx, projectID, serviceID, descriptor); err != nil {
		return build.SourceCheckout{}, err
	}
	if e.githubTokenMinter == nil {
		return build.SourceCheckout{}, fmt.Errorf("%w: build worker token minter is not configured", errGitHubAppCredentialsUnavailable)
	}
	installationToken, err := e.githubTokenMinter.MintRepositoryToken(
		ctx,
		descriptor.InstallationID,
		descriptor.RepositoryID,
	)
	if err != nil {
		return build.SourceCheckout{}, err
	}
	// Token.Value() is read at the final transport boundary. The planner uses
	// an environment-only http.extraHeader, redacts git stderr, and falls out
	// of scope as soon as Checkout returns.
	planner.Token = installationToken.Token.Value()
	return planner.Checkout(ctx, serviceID, source)
}

var errGitHubSourceAuthorityRevoked = errors.New("source checkout: canonical GitHub source authority was revoked or changed")

func (e *buildJobExecutor) verifyGitHubSourceAuthority(
	ctx context.Context,
	projectID,
	serviceID string,
	descriptor *buildJobSourceIntent,
) error {
	if descriptor == nil || strings.ToLower(strings.TrimSpace(descriptor.Provider)) != "github" {
		return nil
	}
	if e.githubSourceLinks == nil {
		return errors.New("source checkout: canonical GitHub source authority is unavailable")
	}
	workspaceID := strings.TrimSpace(descriptor.WorkspaceID)
	projectID = strings.TrimSpace(projectID)
	serviceID = strings.TrimSpace(serviceID)
	repository := strings.ToLower(strings.TrimSpace(descriptor.Repository))
	branch := strings.TrimSpace(strings.TrimPrefix(descriptor.AuthorityBranch, "refs/heads/"))
	if branch == "" {
		// Compatibility for jobs accepted before authority_branch became part of
		// the durable contract. New server-owned GitHub jobs always set it.
		branch = strings.TrimSpace(strings.TrimPrefix(descriptor.Ref, "refs/heads/"))
	}
	if workspaceID == "" ||
		projectID == "" ||
		serviceID == "" ||
		descriptor.InstallationID <= 0 ||
		descriptor.RepositoryID <= 0 ||
		descriptor.LinkResourceVersion <= 0 ||
		repository == "" ||
		branch == "" {
		return errGitHubSourceAuthorityRevoked
	}
	current, err := e.githubSourceLinks.GetServiceLink(
		ctx,
		workspaceID,
		projectID,
		serviceID,
	)
	if errors.Is(err, githubapp.ErrSourceLinkNotFound) {
		return errGitHubSourceAuthorityRevoked
	}
	if err != nil {
		return fmt.Errorf("source checkout: canonical GitHub source authority lookup: %w", err)
	}
	requireAutoDeploy := descriptor.TrustProfile != api.DeployTrustProfileGitHubManual
	if (requireAutoDeploy && !current.AutoDeploy) ||
		current.ResourceVersion != descriptor.LinkResourceVersion ||
		current.Link.InstallationID != descriptor.InstallationID ||
		current.Link.RepositoryID != descriptor.RepositoryID ||
		!strings.EqualFold(current.Link.Repo, repository) ||
		current.Link.Branch != branch ||
		current.Link.CloneURL != strings.TrimSpace(descriptor.Repo) {
		return errGitHubSourceAuthorityRevoked
	}
	return nil
}

func (e *buildJobExecutor) validateSourceArtifactOwnership(
	ctx context.Context,
	serviceID string,
	descriptor *buildJobSourceIntent,
	source build.SourceRef,
	identity *sourceArtifactCheckoutIdentity,
	requireActive bool,
) (sourceartifact.Reference, error) {
	digest, err := sourceartifact.ParseURI(source.Repo)
	if err != nil {
		return sourceartifact.Reference{}, errSourceArtifactOwnership
	}
	canonicalURI, err := sourceartifact.URI(digest)
	if err != nil ||
		descriptor == nil ||
		descriptor.Provider != "artifact" ||
		descriptor.Repo != source.Repo ||
		descriptor.Ref != source.Ref ||
		source.Repo != canonicalURI ||
		source.Ref != digest ||
		identity == nil ||
		identity.OperationID == "" ||
		identity.DeploymentID == "" ||
		identity.ProjectID == "" ||
		identity.ServiceID == "" ||
		identity.EnvironmentID == "" ||
		identity.ServiceID != serviceID ||
		identity.Queue != queue.Build ||
		identity.ParentOperationID != identity.OperationID ||
		e.sourceArtifacts == nil {
		return sourceartifact.Reference{}, errSourceArtifactOwnership
	}
	reference, found, err := e.sourceArtifacts.GetReference(ctx, identity.OperationID)
	if err != nil || !found ||
		reference.OperationID != identity.OperationID ||
		reference.DeploymentID != identity.DeploymentID ||
		reference.ProjectID != identity.ProjectID ||
		reference.ServiceID != identity.ServiceID ||
		reference.EnvironmentID != identity.EnvironmentID ||
		reference.Digest != digest ||
		(requireActive && reference.ReleasedAt != nil) {
		return sourceartifact.Reference{}, errSourceArtifactOwnership
	}
	return reference, nil
}

func validateBuildJobLineage(job queue.Job, payload buildJobPayload) (string, error) {
	deploymentID := firstNonEmptyString(payload.Request.DeploymentID, payload.Candidate.DeploymentID)
	if job.Queue != queue.Build ||
		payload.SchemaVersion != buildJobPayloadSchema ||
		strings.TrimSpace(payload.OperationID) == "" ||
		job.ParentOperationID != payload.OperationID ||
		strings.TrimSpace(deploymentID) == "" ||
		(payload.Request.DeploymentID != "" &&
			payload.Candidate.DeploymentID != "" &&
			payload.Request.DeploymentID != payload.Candidate.DeploymentID) ||
		(payload.Request.EnvironmentID != "" &&
			payload.EnvironmentID != payload.Request.EnvironmentID) ||
		(payload.Candidate.ProjectID != "" &&
			payload.ProjectID != payload.Candidate.ProjectID) ||
		(payload.Candidate.ServiceID != "" &&
			payload.ServiceID != payload.Candidate.ServiceID) ||
		(payload.Candidate.EnvironmentID != "" &&
			payload.EnvironmentID != payload.Candidate.EnvironmentID) {
		return "", errSourceArtifactOwnership
	}
	return deploymentID, nil
}

func validateOwnedLegacyLocalSource(repo, uploadRoot string) error {
	rawPath := build.LocalSourcePath(repo)
	if rawPath == "" {
		return errors.New("source checkout: legacy local provider requires a local source URI")
	}
	path := filepath.Clean(rawPath)
	root := filepath.Clean(strings.TrimSpace(uploadRoot))
	if !filepath.IsAbs(path) ||
		!filepath.IsAbs(root) ||
		repo != build.LocalSourceScheme+path ||
		filepath.Dir(path) != root ||
		!strings.HasPrefix(filepath.Base(path), "midnight-up-src-") {
		return errors.New("source checkout: legacy local source is outside the configured upload root")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("source checkout: legacy local source is not an owned directory")
	}
	return nil
}

func buildSourceRootDirectory(source *buildJobSourceIntent) string {
	if source == nil {
		return ""
	}
	if root := strings.TrimSpace(source.RootDirectory); root != "" {
		return root
	}
	return strings.TrimSpace(source.Subpath)
}

func selectConfiguredBuilder(kind string, checkout build.SourceCheckout, dockerfileBuilder, railpackBuilder build.Builder) (build.Builder, error) {
	switch strings.TrimSpace(kind) {
	case "", serviceconfig.BuilderAuto:
		return build.SelectBuilder(checkout, dockerfileBuilder, railpackBuilder), nil
	case serviceconfig.BuilderDockerfile:
		if dockerfileBuilder == nil {
			return nil, errors.New("configured Dockerfile builder is unavailable")
		}
		return dockerfileBuilder, nil
	case serviceconfig.BuilderRailpack:
		if railpackBuilder == nil {
			return nil, errors.New("configured Railpack builder is unavailable")
		}
		return railpackBuilder, nil
	default:
		return nil, fmt.Errorf("unsupported durable builder %q", kind)
	}
}

func validateGitHubCheckoutTarget(rawURL, repository, configuredCloneHost, apiBaseURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" {
		return errors.New("source checkout: authenticated GitHub clone URL must use HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("source checkout: authenticated GitHub clone URL must not contain credentials, query, or fragment")
	}
	allowedHost, err := githubCloneHost(configuredCloneHost, apiBaseURL)
	if err != nil {
		return err
	}
	if !strings.EqualFold(parsed.Hostname(), allowedHost) || parsed.Port() != "" {
		return fmt.Errorf("source checkout: refusing to send GitHub installation credential to host %q", parsed.Host)
	}
	gotPath := strings.TrimSuffix(strings.TrimRight(parsed.EscapedPath(), "/"), ".git")
	wantPath := "/" + strings.Trim(strings.TrimSpace(repository), "/")
	if strings.Count(strings.Trim(strings.TrimSpace(repository), "/"), "/") != 1 || !strings.EqualFold(gotPath, wantPath) {
		return errors.New("source checkout: clone URL does not match durable GitHub repository identity")
	}
	return nil
}

func githubCloneHost(configured, apiBaseURL string) (string, error) {
	if host := strings.TrimSpace(configured); host != "" {
		if strings.ContainsAny(host, "/:@") {
			return "", errors.New("source checkout: MIDNIGHT_GITHUB_CLONE_HOST must be a hostname without scheme or port")
		}
		return strings.ToLower(host), nil
	}
	apiBaseURL = strings.TrimSpace(apiBaseURL)
	if apiBaseURL == "" || strings.EqualFold(strings.TrimRight(apiBaseURL, "/"), "https://api.github.com") {
		return "github.com", nil
	}
	return "", errors.New("source checkout: MIDNIGHT_GITHUB_CLONE_HOST is required with a custom GitHub API base URL")
}

func (e *buildJobExecutor) beginCanonicalBuild(ctx context.Context, operationID string) error {
	if e.deploymentStore == nil {
		return nil
	}
	deployment, err := e.deploymentStore.GetDeploymentByOperation(ctx, operationID)
	if err != nil {
		return fmt.Errorf("canonical deployment lookup: %w", err)
	}
	switch deployment.State {
	case deploy.DeploymentQueued:
		return e.deploymentStore.MarkDeploymentBuilding(ctx, deployment.ID)
	case deploy.DeploymentBuilding:
		return nil
	default:
		return fmt.Errorf("canonical deployment %s is in state %s", deployment.ID, deployment.State)
	}
}

// enqueueDeployAfterBuild hands the resolved image to the deploy worker while
// deliberately leaving the shared operation RUNNING. The deploy executor owns
// the terminal transition for the end-to-end source deployment. Marking the
// operation SUCCEEDED here would make its subsequent RUNNING transition
// invalid and strand every source-based deploy at the phase boundary.
type deployHandoffResult struct {
	Job      queue.Job
	OutboxID string
	Durable  bool
	Created  bool
}

func (e *buildJobExecutor) enqueueDeployAfterBuild(ctx context.Context, payload buildJobPayload, digestRef string, buildResults ...api.BuildDeployHandoffCommit) (deployHandoffResult, error) {
	deployPayload, err := encodeDeployPayloadAfterBuild(payload, digestRef)
	if err != nil {
		return deployHandoffResult{}, fmt.Errorf("deploy payload encode: %w", err)
	}
	enqueueRequest := queue.EnqueueRequest{
		Queue:             queue.Deploy,
		PartitionKey:      firstNonEmptyString(payload.ProjectID, payload.ServiceID),
		ParentOperationID: payload.OperationID,
		AvailableAt:       time.Now().UTC(),
		Payload:           deployPayload,
	}
	if e.handoffStore != nil {
		if len(buildResults) == 0 || strings.TrimSpace(buildResults[0].DeploymentID) == "" {
			return deployHandoffResult{}, errors.New("durable deploy handoff requires canonical build result")
		}
		commit := buildResults[0]
		commit.OperationID = payload.OperationID
		commit.Enqueue = enqueueRequest
		if commit.Now.IsZero() {
			commit.Now = enqueueRequest.AvailableAt
		}
		result, err := e.handoffStore.CommitBuildDeployHandoff(ctx, commit)
		if err != nil {
			return deployHandoffResult{}, fmt.Errorf("durable deploy handoff: %w", err)
		}
		return deployHandoffResult{OutboxID: result.OutboxID, Durable: true, Created: result.Created}, nil
	}
	deployJob, err := e.queueStore.Enqueue(ctx, enqueueRequest)
	if err != nil {
		return deployHandoffResult{}, fmt.Errorf("deploy job enqueue: %w", err)
	}
	e.appendEvent(ctx, orailtypes.ID(payload.OperationID), orailtypes.OperationRunning, fmt.Sprintf("deploy job enqueued: job_id=%s image=%s", deployJob.ID, digestRef))
	return deployHandoffResult{Job: deployJob}, nil
}

func (e *buildJobExecutor) appendEvent(ctx context.Context, id orailtypes.ID, state orailtypes.OperationState, message string) {
	if e.operationStore == nil {
		return
	}
	// Redact centrally so raw BuildKit progress lines and failure reasons are
	// scrubbed of credential-shaped tokens before they are persisted.
	message = e.redactor.RedactText(message)
	stored, err := e.operationStore.AppendEvent(ctx, api.OperationEvent{OperationID: id, State: state, Message: message})
	if err != nil || e.observability == nil || e.deploymentStore == nil {
		return
	}
	deployment, err := e.deploymentStore.GetDeploymentByOperation(ctx, string(id))
	if err != nil {
		return
	}
	_, _ = e.observability.IngestLog(ctx, observability.LogRecord{
		ObservedAt: stored.CreatedAt,
		Scope: observability.Scope{
			ProjectID: deployment.ProjectID, EnvironmentID: deployment.EnvironmentID,
			ServiceID: deployment.ServiceID, DeploymentID: deployment.ID,
			OperationID: string(id),
		},
		Surface: "build", Level: buildEventLevel(state), Component: "app.build",
		Message: message, Attributes: map[string]any{"operation_state": state},
		SourceKey: "build:" + string(id), SourceSequence: strconv.Itoa(stored.Seq),
	})
}

func (e *buildJobExecutor) cancelBuildAtSafePoint(
	ctx context.Context,
	operationID orailtypes.ID,
	safePoint string,
) (bool, error) {
	if e.operationStore == nil {
		return false, nil
	}
	events, err := e.operationStore.ListEvents(ctx, operationID)
	if err != nil {
		return false, fmt.Errorf("build cancellation lookup: %w", err)
	}
	requested := false
	for _, event := range events {
		message := strings.ToLower(strings.TrimSpace(event.Message))
		if strings.Contains(message, "cancel requested") &&
			strings.Contains(message, "before traffic promotion") {
			requested = true
			break
		}
	}
	if !requested {
		return false, nil
	}

	if store, ok := e.deploymentStore.(api.CanonicalDeploymentOperationCancelStore); ok {
		if _, err := store.CancelDeploymentOperation(ctx, operationID); err != nil {
			if current, getErr := e.operationStore.Get(ctx, operationID); getErr == nil &&
				current.State == orailtypes.OperationCanceled {
				return true, nil
			}
			return false, fmt.Errorf("build cancellation commit: %w", err)
		}
	} else {
		if e.deploymentStore == nil {
			return false, errors.New(
				"build cancellation requires canonical deployment storage",
			)
		}
		deployment, err := e.deploymentStore.GetDeploymentByOperation(
			ctx,
			string(operationID),
		)
		if err != nil {
			return false, fmt.Errorf("build cancellation deployment lookup: %w", err)
		}
		if err := e.deploymentStore.CancelDeployment(
			ctx,
			deployment.ID,
			deployment.State,
		); err != nil {
			return false, fmt.Errorf("build cancellation deployment: %w", err)
		}
		if _, err := e.operationStore.Transition(
			ctx,
			operationID,
			orailtypes.OperationCanceled,
		); err != nil {
			return false, fmt.Errorf("build cancellation operation: %w", err)
		}
	}
	e.appendEvent(
		ctx,
		operationID,
		orailtypes.OperationCanceled,
		"build canceled at worker safe point "+safePoint,
	)
	return true, nil
}

func buildEventLevel(state orailtypes.OperationState) string {
	if state == orailtypes.OperationFailed {
		return "error"
	}
	return "info"
}

func (e *buildJobExecutor) failOperation(ctx context.Context, id orailtypes.ID, err error, stage string) {
	if e.operationStore == nil {
		return
	}
	// Redact before persisting: the error text can embed a secret echoed by a
	// failing build step (appendEvent redacts the event copy independently).
	detail := err.Error()
	var infrastructure interface{ IsVariableInfrastructureFailure() }
	if errors.As(err, &infrastructure) {
		detail = "variable resolution infrastructure is unavailable"
	}
	reason := e.redactor.RedactText(stage + ": " + detail)
	if _, ferr := e.operationStore.Fail(ctx, id, orailtypes.FailureCode(failurecodes.InternalError), reason); ferr != nil {
		fmt.Fprintf(e.stderr, "build worker: fail operation failed: %v\n", ferr)
	}
	e.appendEvent(ctx, id, orailtypes.OperationFailed, "build failed: "+reason)
	if e.deploymentStore != nil {
		if deployment, lookupErr := e.deploymentStore.GetDeploymentByOperation(ctx, string(id)); lookupErr == nil {
			switch deployment.State {
			case deploy.DeploymentQueued, deploy.DeploymentBuilding:
				_ = e.deploymentStore.FailDeployment(ctx, deployment.ID, deployment.State, string(failurecodes.InternalError), reason)
			}
		}
	}
}

func encodeDeployPayloadAfterBuild(in buildJobPayload, digestRef string) ([]byte, error) {
	out := struct {
		SchemaVersion  string                     `json:"schema_version"`
		OperationID    string                     `json:"operation_id"`
		ServiceID      string                     `json:"service_id"`
		ProjectID      string                     `json:"project_id,omitempty"`
		EnvironmentID  string                     `json:"environment_id,omitempty"`
		Request        map[string]any             `json:"request"`
		Candidate      buildJobCandidate          `json:"candidate,omitempty"`
		Route          *buildJobRouteIntent       `json:"route,omitempty"`
		PreviousActive *buildJobPreviousActiveRef `json:"previous_active,omitempty"`
		EnqueuedAt     time.Time                  `json:"enqueued_at"`
	}{
		SchemaVersion: "midnight.dev/deploy-job/v1alpha1",
		OperationID:   in.OperationID,
		ServiceID:     in.ServiceID,
		ProjectID:     in.ProjectID,
		EnvironmentID: in.EnvironmentID,
		Request: map[string]any{
			"environment_id": in.EnvironmentID,
			"image":          digestRef,
			"deployment_id":  in.Request.DeploymentID,
			"instance_id":    in.Request.InstanceID,
			"route":          in.Request.Route,
		},
		Candidate: buildJobCandidate{
			DeploymentID:  in.Request.DeploymentID,
			InstanceID:    in.Request.InstanceID,
			ProjectID:     in.ProjectID,
			EnvironmentID: in.EnvironmentID,
			ServiceID:     in.ServiceID,
			Image:         digestRef,
		},
		Route:          in.Request.Route,
		PreviousActive: in.PreviousActive,
		EnqueuedAt:     time.Now().UTC(),
	}
	return json.Marshal(out)
}

func buildImageRepository(serviceID, projectID string, quarantine bool) string {
	prj := sanitizeID(projectID)
	svc := sanitizeID(serviceID)
	if prj == "" {
		prj = "default"
	}
	if svc == "" {
		svc = "service"
	}
	prefix := "midnight/"
	if quarantine {
		prefix += "quarantine/"
	}
	return prefix + prj + "/" + svc
}

func validateBuildSourceTrustProfile(source *buildJobSourceIntent) error {
	if source == nil {
		return errors.New("build worker: source is required")
	}
	provider := strings.ToLower(strings.TrimSpace(source.Provider))
	profile := strings.TrimSpace(source.TrustProfile)
	if provider != "github" {
		if profile != "" || strings.TrimSpace(source.FetchRef) != "" {
			return errors.New("build worker: trust profile and fetch ref are reserved for GitHub App intake")
		}
		return nil
	}
	if fetchRef := strings.TrimSpace(source.FetchRef); fetchRef != "" {
		if !validGitHubPullFetchRef(fetchRef) || !validLowerGitSHA(source.Ref) {
			return errors.New("build worker: GitHub fetch ref must be a pull head paired with an exact commit SHA")
		}
	}
	switch profile {
	case "", api.DeployTrustProfileGitHubTrusted, api.DeployTrustProfileGitHubManual, api.DeployTrustProfileGitHubForkPreview:
		return nil
	default:
		return errors.New("build worker: unsupported GitHub trust profile")
	}
}

func validGitHubPullFetchRef(value string) bool {
	const prefix, suffix = "refs/pull/", "/head"
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return false
	}
	number := strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
	parsed, err := strconv.ParseUint(number, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == number
}

func validLowerGitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

type buildExecutionPolicy struct {
	AllowBuildSecrets bool
	CacheRoot         string
	QuarantineImage   bool
}

func sourceBuildExecutionPolicy(source *buildJobSourceIntent, sharedCacheRoot string) buildExecutionPolicy {
	if source != nil && source.TrustProfile == api.DeployTrustProfileGitHubForkPreview {
		return buildExecutionPolicy{QuarantineImage: true}
	}
	return buildExecutionPolicy{
		AllowBuildSecrets: true,
		CacheRoot:         sharedCacheRoot,
	}
}

func buildImageTag(commit, operationID string) string {
	commit = strings.TrimSpace(commit)
	if commit != "" {
		if len(commit) > 12 {
			commit = commit[:12]
		}
		return "rev_" + commit
	}
	if operationID != "" {
		return "op_" + sanitizeID(operationID)
	}
	return "latest"
}

func sanitizeID(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	s := strings.Trim(string(out), "-_")
	if s == "" {
		return "x"
	}
	return s
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func isLocalPath(repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	if strings.HasPrefix(repo, "http://") || strings.HasPrefix(repo, "https://") || strings.HasPrefix(repo, "git@") || strings.HasPrefix(repo, "ssh://") || strings.HasPrefix(repo, "git://") {
		return false
	}
	info, err := os.Stat(repo)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func cleanupLocalUpload(repo, uploadRoot string) error {
	path, err := filepath.Abs(build.LocalSourcePath(repo))
	if err != nil {
		return fmt.Errorf("resolve local upload path: %w", err)
	}
	root, err := filepath.Abs(strings.TrimSpace(uploadRoot))
	if err != nil {
		return fmt.Errorf("resolve upload root: %w", err)
	}
	if filepath.Dir(path) != root || !strings.HasPrefix(filepath.Base(path), "midnight-up-src-") {
		return fmt.Errorf("refusing to remove local upload outside configured root")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect local upload: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refusing to remove non-directory local upload")
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove local upload: %w", err)
	}
	return nil
}

func envBool(fallback bool, names ...string) bool {
	for _, name := range names {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			continue
		}
		switch strings.ToLower(value) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

func envDuration(fallback time.Duration, names ...string) time.Duration {
	for _, name := range names {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			continue
		}
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func strictEnvDuration(fallback time.Duration, name string) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration", name)
	}
	return parsed, nil
}

func strictEnvPositiveInt(fallback int, name string) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func strictEnvPositiveInt64(fallback int64, name string) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}
