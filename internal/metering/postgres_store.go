// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package metering

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
)

var reservationIntentRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	defaultSummaryReconcileLookback  = 10 * time.Minute
	defaultSummaryReconcileBudget    = 750 * time.Millisecond
	summaryReconcileSourceInstance   = "usage-summary"
	scheduledReconcileSourceInstance = "usage-reconciler"
	defaultScheduledWorkspaceBatch   = 100
	defaultScheduledStageTimeout     = 10 * time.Second
)

type PostgresStore struct {
	Pool                     *pg.Pool
	Now                      func() time.Time
	SourceArtifactLimitBytes *int64
	Audit                    audit.AuditChain
	// SummaryReconcileLookback bounds how much retained HTTP/runtime source
	// history one interactive summary may inspect. Older unimported evidence is
	// represented as incomplete coverage rather than synchronously replayed.
	SummaryReconcileLookback time.Duration
	// SummaryReconcileBudget is an independent wall-time budget for each of the
	// HTTP and runtime producer stages. A budget expiry preserves already
	// committed replay-safe evidence and returns an explicitly partial summary.
	SummaryReconcileBudget time.Duration
}

func NewPostgresStore(pool *pg.Pool) *PostgresStore {
	return &PostgresStore{Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
}

func (store *PostgresStore) Append(ctx context.Context, event LedgerEvent) (AppendResult, error) {
	if store == nil || store.Pool == nil {
		return AppendResult{}, ErrUnavailable
	}
	if err := event.Validate(); err != nil {
		return AppendResult{}, err
	}
	attributes, err := json.Marshal(normalizeAttributes(event.Attributes))
	if err != nil {
		return AppendResult{}, fmt.Errorf("metering: encode attributes: %w", err)
	}
	var sequence int64
	err = store.Pool.QueryRow(ctx, `
INSERT INTO usage_events (
    event_id,source,source_instance,source_event_id,workspace_id,project_id,
    environment_id,dimension,quantity,value_kind,interval_start,observed_at,attributes
) VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9,$10,$11,$12,$13)
ON CONFLICT (source,source_instance,source_event_id) DO NOTHING
RETURNING sequence`, event.EventID, event.Source, event.SourceInstance, event.SourceEventID,
		event.WorkspaceID, event.ProjectID, event.EnvironmentID, string(event.Resource), event.Quantity,
		string(event.Kind), event.IntervalStart, event.ObservedAt.UTC(), attributes).Scan(&sequence)
	if err == nil {
		return AppendResult{Sequence: sequence, Stored: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return AppendResult{}, fmt.Errorf("metering: append event: %w", err)
	}
	var existing LedgerEvent
	var existingAttributes []byte
	err = store.Pool.QueryRow(ctx, `
SELECT sequence,event_id,source,source_instance,source_event_id,workspace_id,
       COALESCE(project_id,''),COALESCE(environment_id,''),dimension,quantity,
       value_kind,interval_start,observed_at,attributes
  FROM usage_events
 WHERE source=$1 AND source_instance=$2 AND source_event_id=$3`,
		event.Source, event.SourceInstance, event.SourceEventID).
		Scan(&existing.Sequence, &existing.EventID, &existing.Source, &existing.SourceInstance,
			&existing.SourceEventID, &existing.WorkspaceID, &existing.ProjectID,
			&existing.EnvironmentID, &existing.Resource, &existing.Quantity, &existing.Kind,
			&existing.IntervalStart, &existing.ObservedAt, &existingAttributes)
	if err != nil {
		return AppendResult{}, fmt.Errorf("metering: resolve replay: %w", err)
	}
	if err := json.Unmarshal(existingAttributes, &existing.Attributes); err != nil {
		return AppendResult{}, fmt.Errorf("metering: decode replay attributes: %w", err)
	}
	if !sameLedgerEventIntent(existing, event) {
		return AppendResult{}, ErrEventConflict
	}
	return AppendResult{Sequence: existing.Sequence, Stored: false}, nil
}

func (store *PostgresStore) Summary(ctx context.Context, query SummaryQuery) (Summary, error) {
	if store == nil || store.Pool == nil {
		return Summary{}, ErrUnavailable
	}
	if err := store.authorize(ctx, query.WorkspaceID, query.ActorUserID, false); err != nil {
		return Summary{}, err
	}
	if !query.WindowEnd.After(query.WindowStart) {
		return Summary{}, ErrInvalidWindow
	}
	reconciliation, err := store.reconcile(ctx, query.WorkspaceID, query.WindowStart.UTC(), query.WindowEnd.UTC(), summaryReconcileSourceInstance, store.summaryReconcileBudget(), store.summaryReconcileLookback(), store.summaryReconcileLookback())
	if err != nil {
		return Summary{}, err
	}
	profile := "self_hosted"
	_ = store.Pool.QueryRow(ctx, `SELECT profile_key FROM workspace_usage_policy_profiles WHERE workspace_id=$1`, query.WorkspaceID).Scan(&profile)
	resources := []Resource{ResourceSourceArtifactBytes, ResourceProjectCount, ResourceServiceCount,
		ResourceEnvironmentCount, ResourceVolumeCount, ResourceBuildSeconds, ResourceCPUSeconds, ResourceMemoryByteSeconds,
		ResourcePublicHTTPRequests, ResourcePublicHTTPResponseBytes}
	lines := make([]UsageLine, 0, len(resources))
	var replay int64
	for _, resource := range resources {
		line, lastSequence, err := store.usageLine(ctx, query, profile, resource, reconciliation.forResource(resource))
		if err != nil {
			return Summary{}, err
		}
		if lastSequence > replay {
			replay = lastSequence
		}
		lines = append(lines, line)
	}
	sortUsage(lines)
	return Summary{WorkspaceID: query.WorkspaceID, PolicyProfile: profile, WindowStart: query.WindowStart.UTC(),
		WindowEnd: query.WindowEnd.UTC(), AsOf: store.now(), ReplayAfter: replay,
		Usage: lines, Disclosures: Disclosures()}, nil
}

func (store *PostgresStore) usageLine(ctx context.Context, query SummaryQuery, profile string, resource Resource, reconciliation producerReconciliation) (UsageLine, int64, error) {
	line := UsageLine{Resource: resource, Unit: unitFor(resource)}
	var limit *int64
	var enforceable bool
	var source string
	err := store.Pool.QueryRow(ctx, `
SELECT limit_value,enforceable,evidence_source FROM usage_policy_limits
 WHERE profile_key=$1 AND dimension=$2`, profile, string(resource)).Scan(&limit, &enforceable, &source)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return line, 0, fmt.Errorf("metering: read policy: %w", err)
	}
	line.Limit, line.Enforceable = limit, enforceable
	if resource == ResourceSourceArtifactBytes {
		line.Limit = store.SourceArtifactLimitBytes
		line.Enforceable = store.SourceArtifactLimitBytes != nil
	}
	line.Coverage = Coverage{Status: CoverageUnavailable, Source: source, Note: "no evidence observed in this window"}
	var observed *time.Time
	var sequence int64
	if resource == ResourceSourceArtifactBytes || resource == ResourceProjectCount ||
		resource == ResourceServiceCount || resource == ResourceEnvironmentCount || resource == ResourceVolumeCount {
		err = store.Pool.QueryRow(ctx, `
SELECT quantity,observed_at,sequence FROM usage_events
 WHERE workspace_id=$1 AND dimension=$2 AND value_kind='gauge' AND observed_at <= $3
 ORDER BY observed_at DESC,sequence DESC LIMIT 1`, query.WorkspaceID, string(resource), query.WindowEnd.UTC()).Scan(&line.Used, &observed, &sequence)
	} else {
		err = store.Pool.QueryRow(ctx, `
SELECT COALESCE(SUM(quantity),0),MAX(observed_at),COALESCE(MAX(sequence),0)
 FROM usage_events WHERE workspace_id=$1 AND dimension=$2 AND value_kind='increment'
   AND observed_at >= $3 AND observed_at < $4`, query.WorkspaceID, string(resource),
			query.WindowStart.UTC(), query.WindowEnd.UTC()).Scan(&line.Used, &observed, &sequence)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return line, 0, fmt.Errorf("metering: summarize %s: %w", resource, err)
	}
	if observed != nil {
		at := observed.UTC()
		line.Coverage.LastObservedAt = &at
		line.Coverage.Status = CoverageComplete
		line.Coverage.Note = ""
		if resource == ResourceCPUSeconds || resource == ResourceMemoryByteSeconds {
			line.Coverage.Status = CoveragePartial
			line.Coverage.Note = "derived only across observed intervals of five minutes or less"
		}
		if resource == ResourcePublicHTTPRequests || resource == ResourcePublicHTTPResponseBytes {
			line.Coverage.Status = CoveragePartial
			line.Coverage.Note = "derived from retained Traefik access records; bounded ingestion cannot prove a complete request ledger"
		}
	}
	if resource == ResourceBuildSeconds {
		line.Coverage.Status = CoveragePartial
		line.Coverage.Source = "deployments_build_lifecycle"
		line.Coverage.Note = "exact for source builds with canonical lifecycle markers; pre-marker deployments are excluded"
	}
	err = store.Pool.QueryRow(ctx, `
SELECT COUNT(*) FROM usage_evidence_gaps
 WHERE workspace_id=$1 AND resolved_at IS NULL AND gap_start < $3 AND gap_end > $2
	   AND (($4 IN ('cpu_seconds','memory_byte_seconds') AND source='runtime_metrics')
		OR ($4 IN ('public_http_requests','public_http_response_bytes') AND source='traefik_access')
		OR ($4 = 'build_seconds' AND source='build_lifecycle'))`,
		query.WorkspaceID, query.WindowStart.UTC(), query.WindowEnd.UTC(), string(resource)).Scan(&line.Coverage.MissingIntervals)
	if err != nil {
		return line, 0, fmt.Errorf("metering: count evidence gaps: %w", err)
	}
	if line.Coverage.MissingIntervals > 0 && line.Coverage.Status != CoverageUnavailable {
		line.Coverage.Status = CoveragePartial
	}
	if reconciliation.Bounded || reconciliation.TimedOut {
		line.Coverage.MissingIntervals++
		if line.Coverage.Status != CoverageUnavailable {
			line.Coverage.Status = CoveragePartial
		}
		if reconciliation.TimedOut {
			line.Coverage.Note = appendCoverageNote(line.Coverage.Note, "request-time reconciliation reached its bounded time budget; totals include only committed evidence")
		} else {
			line.Coverage.Note = appendCoverageNote(line.Coverage.Note, "request-time reconciliation is bounded to recent or checkpointed evidence; earlier retained evidence may not yet be represented")
		}
	}
	return line, sequence, nil
}

type producerReconciliation struct {
	Bounded  bool
	TimedOut bool
}

type summaryReconciliation struct {
	HTTP    producerReconciliation
	Runtime producerReconciliation
	Build   producerReconciliation
}

func (result summaryReconciliation) forResource(resource Resource) producerReconciliation {
	switch resource {
	case ResourcePublicHTTPRequests, ResourcePublicHTTPResponseBytes:
		return result.HTTP
	case ResourceCPUSeconds, ResourceMemoryByteSeconds:
		return result.Runtime
	case ResourceBuildSeconds:
		return result.Build
	default:
		return producerReconciliation{}
	}
}

func appendCoverageNote(existing, note string) string {
	if strings.TrimSpace(existing) == "" {
		return note
	}
	return strings.TrimSuffix(existing, ".") + "; " + note
}

// reconcile imports only evidence that can be derived without guessing:
// durable gauges, public Traefik access records, and bounded runtime sample
// intervals. The unique producer identity makes every pass replay-safe.
func (store *PostgresStore) reconcile(ctx context.Context, workspaceID string, since, until time.Time, sourceInstance string, stageBudget, initialLookback, overlap time.Duration) (summaryReconciliation, error) {
	var result summaryReconciliation
	at := store.now().Truncate(time.Minute)
	type gauge struct {
		resource Resource
		quantity int64
	}
	gauges := []gauge{}
	queries := []struct {
		resource Resource
		sql      string
	}{
		{ResourceSourceArtifactBytes, `SELECT COALESCE(SUM(compressed_bytes),0) FROM source_artifact_workspace_charges WHERE workspace_id=$1`},
		{ResourceProjectCount, `SELECT COUNT(*) FROM projects WHERE workspace_id=$1 AND deleted_at IS NULL`},
		{ResourceServiceCount, `SELECT COUNT(*) FROM services s JOIN projects p ON p.id=s.project_id WHERE p.workspace_id=$1 AND p.deleted_at IS NULL AND s.deleted_at IS NULL`},
		{ResourceEnvironmentCount, `SELECT COUNT(*) FROM environments e JOIN projects p ON p.id=e.project_id WHERE p.workspace_id=$1 AND p.deleted_at IS NULL AND e.deleted_at IS NULL`},
		{ResourceVolumeCount, `SELECT COUNT(*) FROM volumes v JOIN projects p ON p.id=v.project_id WHERE p.workspace_id=$1 AND p.deleted_at IS NULL AND v.status IN ('provisioning','ready','attached')`},
	}
	for _, query := range queries {
		var quantity int64
		if err := store.Pool.QueryRow(ctx, query.sql, workspaceID).Scan(&quantity); err != nil {
			return result, fmt.Errorf("metering: reconcile %s: %w", query.resource, err)
		}
		gauges = append(gauges, gauge{query.resource, quantity})
	}
	for _, item := range gauges {
		if err := store.appendGauge(ctx, workspaceID, item.resource, item.quantity, at); err != nil {
			return result, err
		}
	}
	evidenceUntil := until
	if now := store.now(); evidenceUntil.After(now) {
		evidenceUntil = now
	}
	if !evidenceUntil.After(since) {
		result.HTTP.Bounded = true
		result.Runtime.Bounded = true
		result.Build.Bounded = true
		return result, nil
	}
	var err error
	result.HTTP, err = store.reconcileProducer(ctx, workspaceID, "traefik_access", sourceInstance, since, evidenceUntil, stageBudget, initialLookback, overlap, store.reconcileHTTP)
	if err != nil {
		return result, err
	}
	result.Runtime, err = store.reconcileProducer(ctx, workspaceID, "runtime_metrics", sourceInstance, since, evidenceUntil, stageBudget, initialLookback, overlap, store.reconcileRuntime)
	if err != nil {
		return result, err
	}
	result.Build, err = store.reconcileProducer(ctx, workspaceID, "build_lifecycle", sourceInstance, since, evidenceUntil, stageBudget, initialLookback, overlap, store.reconcileBuild)
	if err != nil {
		return result, err
	}
	return result, nil
}

type reconcileProducerFunc func(context.Context, string, time.Time, time.Time) error

func (store *PostgresStore) reconcileProducer(ctx context.Context, workspaceID, source, sourceInstance string, since, until time.Time, stageBudget, initialLookback, overlap time.Duration, reconcile reconcileProducerFunc) (producerReconciliation, error) {
	result := producerReconciliation{}
	if stageBudget <= 0 {
		stageBudget = defaultScheduledStageTimeout
	}
	stageCtx, cancel := context.WithTimeout(ctx, stageBudget)
	defer cancel()
	checkpoint, err := store.reconcileCheckpoint(stageCtx, workspaceID, source, sourceInstance)
	if err != nil {
		if store.reconciliationTimedOut(ctx, stageCtx) {
			result.TimedOut = true
			return result, nil
		}
		return result, err
	}
	start, bounded := boundedReconcileStart(since, until, checkpoint, initialLookback, overlap)
	result.Bounded = bounded
	if !start.Before(until) {
		return result, nil
	}
	if err := reconcile(stageCtx, workspaceID, start, until); err != nil {
		if store.reconciliationTimedOut(ctx, stageCtx) {
			result.TimedOut = true
			return result, nil
		}
		return result, err
	}
	if err := store.advanceReconcileCheckpoint(stageCtx, workspaceID, source, sourceInstance, until); err != nil {
		if store.reconciliationTimedOut(ctx, stageCtx) {
			result.TimedOut = true
			return result, nil
		}
		return result, err
	}
	return result, nil
}

func boundedReconcileStart(since, until time.Time, checkpoint *time.Time, initialLookback, overlap time.Duration) (time.Time, bool) {
	since, until = since.UTC(), until.UTC()
	start := since
	candidate := until.Add(-initialLookback)
	if checkpoint != nil && !checkpoint.After(until) {
		candidate = checkpoint.UTC().Add(-overlap)
	}
	if candidate.After(start) {
		start = candidate
	}
	if start.After(until) {
		start = until
	}
	return start, start.After(since)
}

func (store *PostgresStore) reconcileCheckpoint(ctx context.Context, workspaceID, source, sourceInstance string) (*time.Time, error) {
	var raw string
	err := store.Pool.QueryRow(ctx, `SELECT cursor FROM usage_source_cursors
	 WHERE workspace_id=$1 AND source=$2 AND source_instance=$3`, workspaceID, source, sourceInstance).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("metering: read %s summary checkpoint: %w", source, err)
	}
	micros, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("metering: invalid %s summary checkpoint", source)
	}
	checkpoint := time.UnixMicro(micros).UTC()
	return &checkpoint, nil
}

func (store *PostgresStore) advanceReconcileCheckpoint(ctx context.Context, workspaceID, source, sourceInstance string, until time.Time) error {
	// This cursor is a scan watermark, not an observation. Keep
	// last_observed_at NULL so the checkpoint cannot be mistaken for source
	// evidence or used to fabricate completeness.
	cursor := strconv.FormatInt(until.UTC().UnixMicro(), 10)
	_, err := store.Pool.Exec(ctx, `INSERT INTO usage_source_cursors
		(workspace_id,source,source_instance,cursor,last_sequence,last_observed_at,updated_at)
	VALUES ($1,$2,$3,$4,0,NULL,$5)
	ON CONFLICT (workspace_id,source,source_instance) DO UPDATE SET
		cursor=GREATEST(usage_source_cursors.cursor::numeric,EXCLUDED.cursor::numeric)::text,
		updated_at=GREATEST(usage_source_cursors.updated_at,EXCLUDED.updated_at)`,
		workspaceID, source, sourceInstance, cursor, store.now())
	if err != nil {
		return fmt.Errorf("metering: advance %s summary checkpoint: %w", source, err)
	}
	return nil
}

func (store *PostgresStore) reconciliationTimedOut(parent, stage context.Context) bool {
	return parent.Err() == nil && errors.Is(stage.Err(), context.DeadlineExceeded)
}

func (store *PostgresStore) summaryReconcileLookback() time.Duration {
	if store.SummaryReconcileLookback <= 0 {
		return defaultSummaryReconcileLookback
	}
	return store.SummaryReconcileLookback
}

func (store *PostgresStore) summaryReconcileBudget() time.Duration {
	if store.SummaryReconcileBudget <= 0 {
		return defaultSummaryReconcileBudget
	}
	return store.SummaryReconcileBudget
}

// appendGauge records only state transitions. The per-workspace/dimension
// transaction lock makes a transition's predecessor sequence a monotonic,
// replay-safe source identity. A 1→2→1 transition therefore produces three
// distinct events, while repeated reads of an unchanged 1 produce none.
func (store *PostgresStore) appendGauge(ctx context.Context, workspaceID string, resource Resource, quantity int64, observedAt time.Time) error {
	return store.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		_, err := store.appendGaugeInTx(ctx, tx, workspaceID, resource, quantity, observedAt)
		return err
	})
}

func (store *PostgresStore) appendGaugeInTx(ctx context.Context, tx *pg.Tx, workspaceID string, resource Resource, quantity int64, observedAt time.Time) (string, error) {
	lockKey := "midnight:usage-gauge:" + workspaceID + ":" + string(resource)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return "", err
	}
	var previousEventID string
	var previousQuantity, previousSequence int64
	var previousObserved time.Time
	err := tx.QueryRow(ctx, `SELECT event_id,quantity,sequence,observed_at FROM usage_events
 WHERE workspace_id=$1 AND dimension=$2 AND value_kind='gauge'
 ORDER BY observed_at DESC,sequence DESC LIMIT 1`, workspaceID, string(resource)).
		Scan(&previousEventID, &previousQuantity, &previousSequence, &previousObserved)
	if err == nil && previousQuantity == quantity {
		return previousEventID, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if observedAt.Before(previousObserved) {
		observedAt = previousObserved
	}
	identity := string(resource) + ":" + gaugeTransitionIdentity(previousSequence, quantity)
	eventID := "reconcile:" + workspaceID + ":" + string(resource) + ":" + identity
	var sequence int64
	err = tx.QueryRow(ctx, `INSERT INTO usage_events
            (event_id,source,source_instance,source_event_id,workspace_id,dimension,quantity,value_kind,observed_at)
			VALUES ($1,'resource_reconciler',$2,$3,$2,$4,$5,'gauge',$6) RETURNING sequence`,
		eventID, workspaceID, identity, string(resource), quantity, observedAt.UTC()).Scan(&sequence)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO usage_source_cursors
			(workspace_id,source,source_instance,cursor,last_sequence,last_observed_at,updated_at)
			VALUES ($1,'resource_reconciler',$1,$2,$3,$4,$4)
			ON CONFLICT (workspace_id,source,source_instance) DO UPDATE SET
			cursor=EXCLUDED.cursor,last_sequence=GREATEST(usage_source_cursors.last_sequence,EXCLUDED.last_sequence),
			last_observed_at=GREATEST(usage_source_cursors.last_observed_at,EXCLUDED.last_observed_at),updated_at=EXCLUDED.updated_at`,
		workspaceID, identity, sequence, observedAt.UTC())
	return eventID, err
}

// RecordResourceGaugeInTx records the authoritative live count inside a
// caller-owned transaction. This is used after an atomic multi-resource apply
// so the reservation, canonical rows, and resulting usage gauge share one
// commit boundary.
func (store *PostgresStore) RecordResourceGaugeInTx(ctx context.Context, tx *pg.Tx, workspaceID string, resource Resource, observedAt time.Time) (string, error) {
	if store == nil || store.Pool == nil || tx == nil {
		return "", ErrUnavailable
	}
	used, err := liveUsage(ctx, tx, workspaceID, resource)
	if err != nil {
		return "", err
	}
	return store.appendGaugeInTx(ctx, tx, workspaceID, resource, used, observedAt)
}

func gaugeTransitionIdentity(previousSequence, quantity int64) string {
	return fmt.Sprintf("after:%d:value:%d", previousSequence, quantity)
}

func (store *PostgresStore) reconcileHTTP(ctx context.Context, workspaceID string, since, until time.Time) error {
	_, err := store.Pool.Exec(ctx, `
WITH scoped AS (
    SELECT l.id,l.project_id,l.environment_id,l.observed_at,
		   CASE WHEN COALESCE(l.attributes->>'tx_bytes','') ~ '^[0-9]+$'
				THEN LEAST(9223372036854775807::numeric,(l.attributes->>'tx_bytes')::numeric)::bigint
				ELSE 0 END AS bytes
      FROM observability_log_records l JOIN projects p ON p.id=l.project_id
     WHERE p.workspace_id=$1 AND l.surface='http' AND l.stream='access'
       AND l.component='traefik' AND l.observed_at >= $2 AND l.observed_at < $3
), inserted_requests AS (
    INSERT INTO usage_events (event_id,source,source_instance,source_event_id,workspace_id,
        project_id,environment_id,dimension,quantity,value_kind,observed_at)
    SELECT 'traefik-request:'||id,'traefik_access','control-plane',id||':request',$1,
        project_id,environment_id,'public_http_requests',1,'increment',observed_at FROM scoped
    ON CONFLICT (source,source_instance,source_event_id) DO NOTHING
)
INSERT INTO usage_events (event_id,source,source_instance,source_event_id,workspace_id,
    project_id,environment_id,dimension,quantity,value_kind,observed_at)
SELECT 'traefik-bytes:'||id,'traefik_access','control-plane',id||':bytes',$1,
    project_id,environment_id,'public_http_response_bytes',bytes,'increment',observed_at FROM scoped
ON CONFLICT (source,source_instance,source_event_id) DO NOTHING`, workspaceID, since, until)
	if err != nil {
		return fmt.Errorf("metering: reconcile Traefik evidence: %w", err)
	}
	_, err = store.Pool.Exec(ctx, `
INSERT INTO usage_evidence_gaps (workspace_id,source,source_instance,gap_start,gap_end,reason)
SELECT $1,'traefik_access','control-plane',w.window_started_at,w.window_started_at+INTERVAL '1 minute',
       'bounded access-log ingestion dropped one or more records'
  FROM observability_ingestion_windows w JOIN projects p ON p.id=w.project_id
 WHERE p.workspace_id=$1 AND w.source_key LIKE 'traefik-access:%' AND w.dropped_count>0
   AND w.window_started_at < $3 AND w.window_started_at+INTERVAL '1 minute' > $2
ON CONFLICT (workspace_id,source,source_instance,gap_start,gap_end) DO NOTHING`, workspaceID, since, until)
	if err != nil {
		return fmt.Errorf("metering: record Traefik evidence gaps: %w", err)
	}
	return nil
}

func (store *PostgresStore) reconcileRuntime(ctx context.Context, workspaceID string, since, until time.Time) error {
	_, err := store.Pool.Exec(ctx, `
WITH samples AS (
    SELECT m.id,m.project_id,m.environment_id,m.service_id,m.replica_id,m.observed_at,
           m.cpu_percent,m.memory_bytes,
           LAG(m.id) OVER lane AS previous_id,
           LAG(m.observed_at) OVER lane AS interval_start,
           LAG(m.cpu_percent) OVER lane AS previous_cpu,
           LAG(m.memory_bytes) OVER lane AS previous_memory
      FROM observability_metric_samples m JOIN projects p ON p.id=m.project_id
     WHERE p.workspace_id=$1 AND m.sample_resolution='raw'
       AND m.observed_at >= $2::timestamptz - INTERVAL '5 minutes' AND m.observed_at < $3::timestamptz
     WINDOW lane AS (PARTITION BY m.project_id,m.service_id,m.deployment_id,m.replica_id ORDER BY m.observed_at,m.id)
), bounded AS (
    SELECT *,EXTRACT(EPOCH FROM observed_at-interval_start)::bigint AS seconds
      FROM samples WHERE interval_start IS NOT NULL AND interval_start >= $2::timestamptz
), cpu AS (
    INSERT INTO usage_events (event_id,source,source_instance,source_event_id,workspace_id,project_id,
        environment_id,dimension,quantity,value_kind,interval_start,observed_at,attributes)
    SELECT 'runtime-cpu:'||id,'runtime_metrics',service_id||':'||replica_id,id||':cpu',$1,
		project_id,environment_id,'cpu_seconds',
		LEAST(9223372036854775807::numeric,GREATEST(0,ROUND(previous_cpu::numeric/100.0*seconds)))::bigint,
		'increment',interval_start,observed_at,jsonb_build_object('sample_id',id::text,'previous_sample_id',previous_id::text)
      FROM bounded WHERE seconds > 0 AND seconds <= 300
    ON CONFLICT (source,source_instance,source_event_id) DO NOTHING
)
INSERT INTO usage_events (event_id,source,source_instance,source_event_id,workspace_id,project_id,
    environment_id,dimension,quantity,value_kind,interval_start,observed_at,attributes)
SELECT 'runtime-memory:'||id,'runtime_metrics',service_id||':'||replica_id,id||':memory',$1,
    project_id,environment_id,'memory_byte_seconds',
    GREATEST(0,LEAST(9223372036854775807::numeric,previous_memory::numeric*seconds))::bigint,
	'increment',interval_start,observed_at,jsonb_build_object('sample_id',id::text,'previous_sample_id',previous_id::text)
  FROM bounded WHERE seconds > 0 AND seconds <= 300
ON CONFLICT (source,source_instance,source_event_id) DO NOTHING`, workspaceID, since, until)
	if err != nil {
		return fmt.Errorf("metering: reconcile runtime evidence: %w", err)
	}
	_, err = store.Pool.Exec(ctx, `
WITH samples AS (
    SELECT m.service_id,m.replica_id,m.observed_at,
           LAG(m.observed_at) OVER (PARTITION BY m.project_id,m.service_id,m.deployment_id,m.replica_id ORDER BY m.observed_at,m.id) AS previous_at
      FROM observability_metric_samples m JOIN projects p ON p.id=m.project_id
     WHERE p.workspace_id=$1 AND m.sample_resolution='raw'
	       AND m.observed_at >= $2::timestamptz - INTERVAL '5 minutes' AND m.observed_at < $3::timestamptz
)
INSERT INTO usage_evidence_gaps (workspace_id,source,source_instance,gap_start,gap_end,reason)
SELECT $1,'runtime_metrics',service_id||':'||replica_id,previous_at,observed_at,'sample interval exceeded five minutes'
  FROM samples WHERE previous_at IS NOT NULL AND observed_at-previous_at > INTERVAL '5 minutes'
ON CONFLICT (workspace_id,source,source_instance,gap_start,gap_end) DO NOTHING`, workspaceID, since, until)
	if err != nil {
		return fmt.Errorf("metering: record runtime evidence gaps: %w", err)
	}
	return nil
}

// reconcileBuild imports only canonical build-phase windows. The worker owns
// both timestamps: building starts before checkout and completes in the same
// transaction as either durable deploy handoff or terminal build failure.
func (store *PostgresStore) reconcileBuild(ctx context.Context, workspaceID string, since, until time.Time) error {
	_, err := store.Pool.Exec(ctx, `
INSERT INTO usage_events (
    event_id,source,source_instance,source_event_id,workspace_id,project_id,
    environment_id,dimension,quantity,value_kind,interval_start,observed_at
)
SELECT 'build-duration:'||d.id,'build_lifecycle','control-plane',d.id||':duration',
       p.workspace_id,d.project_id,d.environment_id,'build_seconds',
       CEIL(EXTRACT(EPOCH FROM d.build_completed_at-d.build_started_at))::bigint,
       'increment',d.build_started_at,d.build_completed_at
  FROM deployments d
  JOIN projects p ON p.id=d.project_id
 WHERE p.workspace_id=$1
   AND d.build_started_at IS NOT NULL
   AND d.build_completed_at IS NOT NULL
   AND d.build_completed_at >= d.build_started_at
   AND d.build_completed_at >= $2
   AND d.build_completed_at < $3
ON CONFLICT (source,source_instance,source_event_id) DO NOTHING`, workspaceID, since, until)
	if err != nil {
		return fmt.Errorf("metering: reconcile build lifecycle evidence: %w", err)
	}
	_, err = store.Pool.Exec(ctx, `
UPDATE usage_evidence_gaps g
   SET resolved_at=COALESCE(g.resolved_at,GREATEST(g.detected_at,$2))
 WHERE g.workspace_id=$1
   AND g.source='build_lifecycle'
   AND EXISTS (
       SELECT 1 FROM deployments d
        WHERE d.id=g.source_instance
          AND d.build_started_at IS NOT NULL
          AND d.build_completed_at IS NOT NULL
          AND d.build_completed_at >= d.build_started_at
   )`, workspaceID, store.now())
	if err != nil {
		return fmt.Errorf("metering: resolve build lifecycle evidence gaps: %w", err)
	}
	_, err = store.Pool.Exec(ctx, `
WITH missing AS (
    SELECT d.id AS deployment_id,
           COALESCE(d.completed_at,d.build_started_at,d.started_at,d.created_at) AS gap_start,
           COALESCE(d.completed_at,d.build_started_at,d.started_at,d.created_at)+INTERVAL '1 microsecond' AS gap_end,
           CASE WHEN d.build_started_at IS NULL
                THEN 'source deployment predates canonical build lifecycle markers'
                ELSE 'terminal source deployment is missing build completion evidence'
           END AS reason
      FROM deployments d
      JOIN projects p ON p.id=d.project_id
     WHERE p.workspace_id=$1
       AND d.action IN ('deploy_source','redeploy_rebuild_ref')
       AND d.state <> 'queued'
       AND (
           d.build_started_at IS NULL OR
           (d.build_completed_at IS NULL AND d.state IN ('failed','canceled','deploying','active','superseded'))
       )
)
INSERT INTO usage_evidence_gaps (
    workspace_id,source,source_instance,gap_start,gap_end,reason
)
SELECT $1,'build_lifecycle',deployment_id,gap_start,gap_end,reason
  FROM missing
 WHERE gap_start < $3 AND gap_end > $2
ON CONFLICT (workspace_id,source,source_instance,gap_start,gap_end) DO NOTHING`, workspaceID, since, until)
	if err != nil {
		return fmt.Errorf("metering: record build lifecycle evidence gaps: %w", err)
	}
	return nil
}

// ReconcileScheduled performs a bounded, single-installation background pass.
// A session advisory lock prevents duplicate worker replicas from doing the
// same scan. Workspaces least recently checkpointed are selected first, so a
// fixed batch cannot starve later tenant IDs.
func (store *PostgresStore) ReconcileScheduled(ctx context.Context, request ScheduledReconcileRequest) (ScheduledReconcileResult, error) {
	if store == nil || store.Pool == nil {
		return ScheduledReconcileResult{StartedAt: time.Now().UTC()}, ErrUnavailable
	}
	result := ScheduledReconcileResult{StartedAt: store.now()}
	if !request.Until.After(request.Since) {
		return result, ErrInvalidWindow
	}
	if request.MaxWorkspaces <= 0 {
		request.MaxWorkspaces = defaultScheduledWorkspaceBatch
	}
	if request.MaxWorkspaces > 1000 {
		return result, errors.New("metering: scheduled workspace batch exceeds 1000")
	}
	if request.StageTimeout <= 0 {
		request.StageTimeout = defaultScheduledStageTimeout
	}
	if request.StageTimeout > 5*time.Minute {
		return result, errors.New("metering: scheduled producer timeout exceeds five minutes")
	}
	conn, err := store.Pool.Acquire(ctx)
	if err != nil {
		return result, fmt.Errorf("metering: acquire reconciliation lock connection: %w", err)
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('midnight:usage-reconciliation'))`).Scan(&locked); err != nil {
		return result, fmt.Errorf("metering: acquire reconciliation lock: %w", err)
	}
	if !locked {
		result.SkippedConcurrent = true
		result.CompletedAt = store.now()
		return result, nil
	}
	defer func() {
		var unlocked bool
		_ = conn.QueryRow(context.Background(), `SELECT pg_advisory_unlock(hashtext('midnight:usage-reconciliation'))`).Scan(&unlocked)
	}()

	rows, err := conn.Query(ctx, `
SELECT w.id
  FROM workspaces w
  LEFT JOIN LATERAL (
      SELECT CASE WHEN COUNT(DISTINCT c.source)=3 THEN MIN(c.updated_at) END AS last_reconciled_at
        FROM usage_source_cursors c
       WHERE c.workspace_id=w.id AND c.source_instance=$1
         AND c.source IN ('traefik_access','runtime_metrics','build_lifecycle')
  ) checkpoint ON TRUE
 WHERE w.deleted_at IS NULL
 ORDER BY checkpoint.last_reconciled_at NULLS FIRST,w.id
 LIMIT $2`, scheduledReconcileSourceInstance, request.MaxWorkspaces)
	if err != nil {
		return result, fmt.Errorf("metering: list scheduled workspaces: %w", err)
	}
	workspaceIDs := make([]string, 0, request.MaxWorkspaces)
	for rows.Next() {
		var workspaceID string
		if err := rows.Scan(&workspaceID); err != nil {
			rows.Close()
			return result, fmt.Errorf("metering: scan scheduled workspace: %w", err)
		}
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, fmt.Errorf("metering: scheduled workspace rows: %w", err)
	}
	rows.Close()

	var firstWorkspaceErr error
	for _, workspaceID := range workspaceIDs {
		reconciled, err := store.reconcile(ctx, workspaceID, request.Since.UTC(), request.Until.UTC(), scheduledReconcileSourceInstance, request.StageTimeout, request.Until.Sub(request.Since), store.summaryReconcileLookback())
		if err != nil {
			result.WorkspaceErrors++
			if firstWorkspaceErr == nil {
				firstWorkspaceErr = fmt.Errorf("metering: reconcile workspace %s: %w", workspaceID, err)
			}
			if ctx.Err() != nil {
				break
			}
			continue
		}
		result.WorkspacesExamined++
		for _, producer := range []producerReconciliation{reconciled.HTTP, reconciled.Runtime, reconciled.Build} {
			if producer.TimedOut {
				result.ProducerTimeouts++
			}
			if producer.Bounded {
				result.ProducerBounds++
			}
		}
	}
	result.CompletedAt = store.now()
	return result, firstWorkspaceErr
}

func (store *PostgresStore) Reserve(ctx context.Context, request ReservationRequest) (Reservation, error) {
	if store == nil || store.Pool == nil {
		return Reservation{}, ErrUnavailable
	}
	if err := store.authorize(ctx, request.WorkspaceID, request.ActorUserID, true); err != nil {
		return Reservation{}, err
	}
	if !reservationIntentRE.MatchString(request.IntentHash) || strings.TrimSpace(request.IdempotencyKey) == "" || request.Quantity <= 0 {
		return Reservation{}, errors.New("metering: invalid reservation request")
	}
	switch request.Resource {
	case ResourceSourceArtifactBytes, ResourceProjectCount, ResourceServiceCount, ResourceEnvironmentCount, ResourceVolumeCount:
	default:
		return Reservation{}, ErrUnavailable
	}
	ttl := request.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if ttl > time.Hour {
		return Reservation{}, errors.New("metering: reservation TTL exceeds one hour")
	}
	var result Reservation
	var decisionErr error
	err := store.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "midnight:usage-admission:"+request.WorkspaceID); err != nil {
			return err
		}
		_, _ = tx.Exec(ctx, `UPDATE usage_reservations SET state='expired',updated_at=$2 WHERE workspace_id=$1 AND state='active' AND expires_at <= $2`, request.WorkspaceID, store.now())
		var existingIntent string
		err := tx.QueryRow(ctx, `SELECT id,dimension,quantity,state,expires_at,intent_hash FROM usage_reservations
 WHERE workspace_id=$1 AND actor_id=$2 AND idempotency_key=$3`, request.WorkspaceID, request.ActorUserID, request.IdempotencyKey).
			Scan(&result.ID, &result.Resource, &result.Quantity, &result.State, &result.ExpiresAt, &existingIntent)
		if err == nil {
			if existingIntent != request.IntentHash || result.Resource != request.Resource || result.Quantity != request.Quantity {
				return ErrReservationConflict
			}
			result.WorkspaceID, result.IdempotentReplay = request.WorkspaceID, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var limit int64
		err = tx.QueryRow(ctx, `SELECT l.limit_value FROM usage_policy_limits l
 WHERE l.profile_key=COALESCE((SELECT profile_key FROM workspace_usage_policy_profiles WHERE workspace_id=$1),'self_hosted')
   AND l.dimension=$2 AND l.enforceable=TRUE`, request.WorkspaceID, string(request.Resource)).Scan(&limit)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnavailable
		}
		if err != nil {
			return err
		}
		used, err := liveUsage(ctx, tx, request.WorkspaceID, request.Resource)
		if err != nil {
			return err
		}
		var reserved int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(quantity),0) FROM usage_reservations
 WHERE workspace_id=$1 AND dimension=$2 AND state='active' AND expires_at > $3`,
			request.WorkspaceID, string(request.Resource), store.now()).Scan(&reserved); err != nil {
			return err
		}
		decision := "admit"
		reason := "within enforceable limit"
		if used > limit-reserved || request.Quantity > limit-used-reserved {
			decision, reason = "deny", "request would exceed enforceable limit"
			decisionErr = &LimitError{Resource: request.Resource, Used: used, Reserved: reserved, Requested: request.Quantity, Limit: limit}
		}
		if decision == "admit" {
			id, err := newReservationID()
			if err != nil {
				return err
			}
			result = Reservation{ID: id, WorkspaceID: request.WorkspaceID, Resource: request.Resource,
				Quantity: request.Quantity, State: "active", ExpiresAt: store.now().Add(ttl)}
			_, err = tx.Exec(ctx, `INSERT INTO usage_reservations
                (id,workspace_id,dimension,quantity,actor_id,idempotency_key,intent_hash,state,expires_at,created_at,updated_at)
                VALUES ($1,$2,$3,$4,$5,$6,$7,'active',$8,$9,$9)`, result.ID, request.WorkspaceID,
				string(request.Resource), request.Quantity, request.ActorUserID, request.IdempotencyKey,
				request.IntentHash, result.ExpiresAt, store.now())
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO usage_admission_decisions
            (workspace_id,reservation_id,actor_id,dimension,requested,used,reserved,limit_value,decision,reason)
            VALUES ($1,NULLIF($2,''),$3,$4,$5,$6,$7,$8,$9,$10)`, request.WorkspaceID, result.ID,
			request.ActorUserID, string(request.Resource), request.Quantity, used, reserved, limit, decision, reason)
		return err
	})
	if err != nil {
		return Reservation{}, fmt.Errorf("metering: reserve: %w", err)
	}
	if decisionErr != nil {
		_ = store.appendAdmissionAudit(ctx, request, Reservation{}, "deny", decisionErr)
		return Reservation{}, decisionErr
	}
	decision := "allow"
	if result.IdempotentReplay {
		decision = "replay"
	}
	if err := store.appendAdmissionAudit(ctx, request, result, decision, nil); err != nil {
		return Reservation{}, fmt.Errorf("metering: append admission audit: %w", err)
	}
	return result, nil
}

// ReserveIfEnforcedInTx admits one positive count delta inside the caller's
// serializable transaction. A false second result means the installation has
// no enforceable limit for this dimension; it is not an authorization bypass.
// Denials are inserted into usage_admission_decisions before returning the
// LimitError so the caller can commit a replayable failure response.
func (store *PostgresStore) ReserveIfEnforcedInTx(ctx context.Context, tx *pg.Tx, request ReservationRequest) (Reservation, bool, error) {
	if store == nil || store.Pool == nil || tx == nil {
		return Reservation{}, false, ErrUnavailable
	}
	if !reservationIntentRE.MatchString(request.IntentHash) || strings.TrimSpace(request.IdempotencyKey) == "" || request.Quantity <= 0 {
		return Reservation{}, false, errors.New("metering: invalid reservation request")
	}
	switch request.Resource {
	case ResourceSourceArtifactBytes, ResourceProjectCount, ResourceServiceCount, ResourceEnvironmentCount, ResourceVolumeCount:
	default:
		return Reservation{}, false, ErrUnavailable
	}
	if err := store.authorizeInTx(ctx, tx, request.WorkspaceID, request.ActorUserID, true); err != nil {
		return Reservation{}, false, err
	}
	ttl := request.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if ttl > time.Hour {
		return Reservation{}, false, errors.New("metering: reservation TTL exceeds one hour")
	}
	now := store.now()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "midnight:usage-admission:"+request.WorkspaceID); err != nil {
		return Reservation{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE usage_reservations SET state='expired',updated_at=$2 WHERE workspace_id=$1 AND state='active' AND expires_at <= $2`, request.WorkspaceID, now); err != nil {
		return Reservation{}, false, err
	}
	var result Reservation
	var existingIntent string
	err := tx.QueryRow(ctx, `SELECT id,dimension,quantity,state,expires_at,intent_hash FROM usage_reservations
 WHERE workspace_id=$1 AND actor_id=$2 AND idempotency_key=$3`, request.WorkspaceID, request.ActorUserID, request.IdempotencyKey).
		Scan(&result.ID, &result.Resource, &result.Quantity, &result.State, &result.ExpiresAt, &existingIntent)
	if err == nil {
		if existingIntent != request.IntentHash || result.Resource != request.Resource || result.Quantity != request.Quantity || result.State == "expired" || result.State == "released" {
			return Reservation{}, true, ErrReservationConflict
		}
		result.WorkspaceID, result.IdempotentReplay = request.WorkspaceID, true
		return result, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, err
	}
	var limit int64
	err = tx.QueryRow(ctx, `SELECT l.limit_value FROM usage_policy_limits l
 WHERE l.profile_key=COALESCE((SELECT profile_key FROM workspace_usage_policy_profiles WHERE workspace_id=$1),'self_hosted')
   AND l.dimension=$2 AND l.enforceable=TRUE`, request.WorkspaceID, string(request.Resource)).Scan(&limit)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, nil
	}
	if err != nil {
		return Reservation{}, false, err
	}
	used, err := liveUsage(ctx, tx, request.WorkspaceID, request.Resource)
	if err != nil {
		return Reservation{}, true, err
	}
	var reserved int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(quantity),0) FROM usage_reservations
 WHERE workspace_id=$1 AND dimension=$2 AND state='active' AND expires_at > $3`, request.WorkspaceID, string(request.Resource), now).Scan(&reserved); err != nil {
		return Reservation{}, true, err
	}
	if used > limit-reserved || request.Quantity > limit-used-reserved {
		limitErr := &LimitError{Resource: request.Resource, Used: used, Reserved: reserved, Requested: request.Quantity, Limit: limit}
		_, err = tx.Exec(ctx, `INSERT INTO usage_admission_decisions
            (workspace_id,reservation_id,actor_id,dimension,requested,used,reserved,limit_value,decision,reason)
            VALUES ($1,NULL,$2,$3,$4,$5,$6,$7,'deny','request would exceed enforceable limit')`, request.WorkspaceID,
			request.ActorUserID, string(request.Resource), request.Quantity, used, reserved, limit)
		if err != nil {
			return Reservation{}, true, err
		}
		return Reservation{}, true, limitErr
	}
	id, err := newReservationID()
	if err != nil {
		return Reservation{}, true, err
	}
	result = Reservation{ID: id, WorkspaceID: request.WorkspaceID, Resource: request.Resource,
		Quantity: request.Quantity, State: "active", ExpiresAt: now.Add(ttl)}
	if _, err := tx.Exec(ctx, `INSERT INTO usage_reservations
        (id,workspace_id,dimension,quantity,actor_id,idempotency_key,intent_hash,state,expires_at,created_at,updated_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,'active',$8,$9,$9)`, result.ID, request.WorkspaceID,
		string(request.Resource), request.Quantity, request.ActorUserID, request.IdempotencyKey,
		request.IntentHash, result.ExpiresAt, now); err != nil {
		return Reservation{}, true, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO usage_admission_decisions
        (workspace_id,reservation_id,actor_id,dimension,requested,used,reserved,limit_value,decision,reason)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'admit','within enforceable limit')`, request.WorkspaceID, result.ID,
		request.ActorUserID, string(request.Resource), request.Quantity, used, reserved, limit); err != nil {
		return Reservation{}, true, err
	}
	return result, true, nil
}

// CommitReservationInTx links an admitted count delta to the resulting
// authoritative gauge and marks the reservation terminal in the same commit.
func (store *PostgresStore) CommitReservationInTx(ctx context.Context, tx *pg.Tx, reservation Reservation, observedAt time.Time) error {
	if reservation.ID == "" || reservation.WorkspaceID == "" || reservation.Quantity <= 0 {
		return errors.New("metering: valid reservation is required")
	}
	eventID, err := store.RecordResourceGaugeInTx(ctx, tx, reservation.WorkspaceID, reservation.Resource, observedAt)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE usage_reservations
       SET state='committed',committed_event_id=$2,updated_at=$3
     WHERE id=$1 AND workspace_id=$4 AND dimension=$5 AND quantity=$6
       AND state='active' AND expires_at>$3`, reservation.ID, eventID, observedAt.UTC(),
		reservation.WorkspaceID, string(reservation.Resource), reservation.Quantity)
	if err != nil {
		return fmt.Errorf("metering: commit reservation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		var state string
		lookupErr := tx.QueryRow(ctx, `SELECT state FROM usage_reservations
         WHERE id=$1 AND workspace_id=$2 AND dimension=$3 AND quantity=$4`, reservation.ID,
			reservation.WorkspaceID, string(reservation.Resource), reservation.Quantity).Scan(&state)
		if lookupErr == nil && state == "committed" {
			return nil
		}
		return ErrReservationConflict
	}
	return nil
}

func (store *PostgresStore) appendAdmissionAudit(ctx context.Context, request ReservationRequest, reservation Reservation, decision string, cause error) error {
	if store.Audit == nil {
		return nil
	}
	resourceID := reservation.ID
	if resourceID == "" {
		resourceID = "denied:" + request.IntentHash[:12]
	}
	attrs := map[string]any{"dimension": request.Resource, "quantity": request.Quantity,
		"intent_fingerprint": request.IntentHash[:12]}
	reason := ""
	if cause != nil {
		reason = cause.Error()
	}
	_, err := store.Audit.Append(ctx, audit.Row{RequestID: "usage-reservation:" + resourceID,
		ActorID: request.ActorUserID, ActorType: "user", WorkspaceID: request.WorkspaceID,
		ResourceKind: "usage_reservation", ResourceID: resourceID,
		Action: "usage.reservation." + decision, PolicyDecision: decision,
		ReasonCode: reason, Attrs: attrs, OccurredAt: store.now(), RecordedAt: store.now()})
	return err
}

func liveUsage(ctx context.Context, tx *pg.Tx, workspaceID string, resource Resource) (int64, error) {
	queries := map[Resource]string{
		ResourceSourceArtifactBytes: `SELECT COALESCE(SUM(compressed_bytes),0) FROM source_artifact_workspace_charges WHERE workspace_id=$1`,
		ResourceProjectCount:        `SELECT COUNT(*) FROM projects WHERE workspace_id=$1 AND deleted_at IS NULL`,
		ResourceServiceCount:        `SELECT COUNT(*) FROM services s JOIN projects p ON p.id=s.project_id WHERE p.workspace_id=$1 AND p.deleted_at IS NULL AND s.deleted_at IS NULL`,
		ResourceEnvironmentCount:    `SELECT COUNT(*) FROM environments e JOIN projects p ON p.id=e.project_id WHERE p.workspace_id=$1 AND p.deleted_at IS NULL AND e.deleted_at IS NULL`,
		ResourceVolumeCount:         `SELECT COUNT(*) FROM volumes v JOIN projects p ON p.id=v.project_id WHERE p.workspace_id=$1 AND p.deleted_at IS NULL AND v.status IN ('provisioning','ready','attached')`,
	}
	query, ok := queries[resource]
	if !ok {
		return 0, ErrUnavailable
	}
	var used int64
	err := tx.QueryRow(ctx, query, workspaceID).Scan(&used)
	return used, err
}

func (store *PostgresStore) authorize(ctx context.Context, workspaceID, actorUserID string, mutate bool) error {
	return store.authorizeConn(ctx, store.Pool, workspaceID, actorUserID, mutate)
}

func (store *PostgresStore) authorizeInTx(ctx context.Context, tx *pg.Tx, workspaceID, actorUserID string, mutate bool) error {
	return store.authorizeConn(ctx, tx, workspaceID, actorUserID, mutate)
}

func (store *PostgresStore) authorizeConn(ctx context.Context, conn pg.Conn, workspaceID, actorUserID string, mutate bool) error {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(actorUserID) == "" {
		return ErrNotAuthorized
	}
	roles := []string{"admin", "member", "deployer", "viewer"}
	if mutate {
		roles = []string{"admin", "member"}
	}
	var authorized bool
	err := conn.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM workspace_members wm
 JOIN workspaces w ON w.id=wm.workspace_id
 WHERE wm.workspace_id=$1 AND wm.user_id=$2 AND w.deleted_at IS NULL
   AND wm.accepted_at IS NOT NULL AND wm.role=ANY($3::text[])
)`, workspaceID, actorUserID, roles).Scan(&authorized)
	if err != nil {
		return fmt.Errorf("metering: authorize workspace: %w", err)
	}
	if !authorized {
		return ErrNotAuthorized
	}
	return nil
}

func (store *PostgresStore) now() time.Time {
	if store.Now == nil {
		return time.Now().UTC()
	}
	return store.Now().UTC()
}

func newReservationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "usrsv_" + hex.EncodeToString(value[:]), nil
}

var _ Store = (*PostgresStore)(nil)
var _ Store = (*MemoryStore)(nil)
