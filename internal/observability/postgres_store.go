// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/db/pg"
	logrecords "github.com/midnight/midnight/internal/logs"
	"github.com/midnight/midnight/internal/redaction"
)

type PostgresStore struct {
	Pool        *pg.Pool
	Redactor    *redaction.Redactor
	Now         func() time.Time
	IngestLimit int64
	Retention   time.Duration
}

func NewPostgresStore(pool *pg.Pool) *PostgresStore {
	return &PostgresStore{
		Pool:        pool,
		Redactor:    redaction.DefaultRedactor(),
		Now:         func() time.Time { return time.Now().UTC() },
		IngestLimit: DefaultIngestPerMin,
		Retention:   DefaultRetention,
	}
}

func (s *PostgresStore) ValidateScope(ctx context.Context, scope Scope) error {
	if s == nil || s.Pool == nil {
		return errors.New("observability store is unavailable")
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	var valid bool
	err := s.Pool.QueryRow(ctx, `
SELECT
    EXISTS (SELECT 1 FROM projects p WHERE p.id = $1 AND p.deleted_at IS NULL)
    AND ($2 = '' OR EXISTS (
        SELECT 1 FROM environments e
        WHERE e.id = $2 AND e.project_id = $1 AND e.deleted_at IS NULL
    ))
    AND ($3 = '' OR EXISTS (
        SELECT 1 FROM services svc
        WHERE svc.id = $3 AND svc.project_id = $1 AND svc.deleted_at IS NULL
    ))
    AND ($4 = '' OR EXISTS (
        SELECT 1 FROM deployments d
        WHERE d.id = $4
          AND d.project_id = $1
          AND ($2 = '' OR d.environment_id = $2)
          AND ($3 = '' OR d.service_id = $3)
          AND ($5 = '' OR d.operation_id = $5)
    ))`,
		strings.TrimSpace(scope.ProjectID),
		strings.TrimSpace(scope.EnvironmentID),
		strings.TrimSpace(scope.ServiceID),
		strings.TrimSpace(scope.DeploymentID),
		strings.TrimSpace(scope.OperationID),
	).Scan(&valid)
	if err != nil {
		return fmt.Errorf("validate observability scope: %w", err)
	}
	if !valid {
		return errors.New("observability scope does not resolve to one project graph")
	}
	return nil
}

func (s *PostgresStore) IngestLog(ctx context.Context, record LogRecord) (IngestResult, error) {
	if s == nil || s.Pool == nil {
		return IngestResult{}, errors.New("observability store is unavailable")
	}
	record = s.sanitizeLog(record)
	if err := validateLog(record); err != nil {
		return IngestResult{}, err
	}
	if err := s.ValidateScope(ctx, record.Scope); err != nil {
		return IngestResult{}, err
	}
	attributes, err := json.Marshal(record.Attributes)
	if err != nil {
		return IngestResult{}, fmt.Errorf("marshal observability attributes: %w", err)
	}
	if len(attributes) > MaxAttributeJSONSize {
		return IngestResult{}, errors.New("observability attributes exceed bounded encoded size")
	}
	limit := s.IngestLimit
	if limit <= 0 {
		limit = DefaultIngestPerMin
	}
	window := record.ObservedAt.UTC().Truncate(time.Minute)
	var result IngestResult
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var existing int64
		err := tx.QueryRow(ctx, `
SELECT id FROM observability_log_records
WHERE source_key = $1 AND source_sequence = $2`,
			record.SourceKey, record.SourceSequence).Scan(&existing)
		if err == nil {
			result = IngestResult{Stored: true, LogID: existing}
			return nil
		}

		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
			record.SourceKey+"|"+window.Format(time.RFC3339)); err != nil {
			return err
		}
		var accepted int64
		err = tx.QueryRow(ctx, `
SELECT accepted_count
FROM observability_ingestion_windows
WHERE source_key = $1 AND window_started_at = $2
FOR UPDATE`, record.SourceKey, window).Scan(&accepted)
		switch {
		case err == nil:
		case errors.Is(err, pgx.ErrNoRows):
			accepted = 0
			_, err = tx.Exec(ctx, `
INSERT INTO observability_ingestion_windows (
    source_key, window_started_at, project_id, environment_id, service_id
) VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''))`,
				record.SourceKey, window, record.Scope.ProjectID,
				record.Scope.EnvironmentID, record.Scope.ServiceID)
			if err != nil {
				return err
			}
		default:
			return err
		}
		if accepted >= limit {
			if _, err := tx.Exec(ctx, `
UPDATE observability_ingestion_windows
SET dropped_count = dropped_count + 1, updated_at = CURRENT_TIMESTAMP
WHERE source_key = $1 AND window_started_at = $2`, record.SourceKey, window); err != nil {
				return err
			}
			result.Dropped = true
			return nil
		}
		if _, err := tx.Exec(ctx, `
UPDATE observability_ingestion_windows
SET accepted_count = accepted_count + 1, updated_at = CURRENT_TIMESTAMP
WHERE source_key = $1 AND window_started_at = $2`, record.SourceKey, window); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
INSERT INTO observability_log_records (
    observed_at, project_id, environment_id, service_id, deployment_id,
    operation_id, replica_id, surface, stream, level, component, message,
    attributes, source_key, source_sequence
) VALUES (
    $1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),
    NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13,$14,$15
)
ON CONFLICT (source_key, source_sequence) DO UPDATE
SET source_key = EXCLUDED.source_key
RETURNING id`,
			record.ObservedAt, record.Scope.ProjectID, record.Scope.EnvironmentID,
			record.Scope.ServiceID, record.Scope.DeploymentID, record.Scope.OperationID,
			record.Scope.ReplicaID, record.Surface, record.Stream, record.Level,
			record.Component, record.Message, attributes, record.SourceKey,
			record.SourceSequence).Scan(&result.LogID)
	})
	if err != nil {
		return IngestResult{}, fmt.Errorf("ingest observability log: %w", err)
	}
	result.Stored = !result.Dropped
	return result, nil
}

func (s *PostgresStore) sanitizeLog(record LogRecord) LogRecord {
	if record.ObservedAt.IsZero() {
		record.ObservedAt = s.now()
	}
	record.Attributes = redactSensitiveAttributes(record.Attributes)
	entry := logrecords.Redact(logrecords.Entry{
		At: record.ObservedAt, Level: record.Level, Component: record.Component,
		Message: record.Message, Fields: record.Attributes,
	})
	entry = logrecords.RedactWith(s.Redactor, entry)
	record.Message = truncateUTF8(entry.Message, MaxMessageBytes)
	record.Attributes = boundAttributes(entry.Fields)
	record.Level = strings.ToLower(strings.TrimSpace(record.Level))
	if record.Level == "" {
		record.Level = "info"
	}
	record.Component = strings.TrimSpace(record.Component)
	record.Surface = strings.ToLower(strings.TrimSpace(record.Surface))
	record.Stream = strings.ToLower(strings.TrimSpace(record.Stream))
	record.SourceKey = strings.TrimSpace(record.SourceKey)
	record.SourceSequence = strings.TrimSpace(record.SourceSequence)
	return record
}

func redactSensitiveAttributes(attributes map[string]any) map[string]any {
	if attributes == nil {
		return nil
	}
	out := make(map[string]any, len(attributes))
	for key, value := range attributes {
		if sensitiveAttributeKey(key) {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = redactSensitiveAttributeValue(value)
	}
	return out
}

func redactSensitiveAttributeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return redactSensitiveAttributes(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = redactSensitiveAttributeValue(item)
		}
		return out
	default:
		return value
	}
}

func sensitiveAttributeKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	for _, fragment := range []string{
		"token", "secret", "password", "passwd", "credential",
		"authorization", "api_key", "apikey", "private_key", "cookie",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func validateLog(record LogRecord) error {
	if err := record.Scope.Validate(); err != nil {
		return err
	}
	switch record.Surface {
	case "build", "runtime", "http", "platform":
	default:
		return errors.New("invalid observability log surface")
	}
	switch record.Stream {
	case "", "stdout", "stderr", "access":
	default:
		return errors.New("invalid observability log stream")
	}
	if record.Component == "" || record.SourceKey == "" || record.SourceSequence == "" {
		return errors.New("component, source key, and source sequence are required")
	}
	return nil
}

func (s *PostgresStore) QueryLogs(ctx context.Context, query LogQuery) (LogPage, error) {
	if err := s.ValidateScope(ctx, query.Scope); err != nil {
		return LogPage{}, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = DefaultQueryLimit
	}
	if limit > MaxQueryLimit {
		return LogPage{}, fmt.Errorf("log limit exceeds %d", MaxQueryLimit)
	}
	sqlText := `
SELECT id, observed_at, project_id, COALESCE(environment_id,''), COALESCE(service_id,''),
       COALESCE(deployment_id,''), COALESCE(operation_id,''), replica_id,
       surface, stream, level, component, message, attributes
FROM observability_log_records
WHERE project_id = $1
  AND ($2 = '' OR environment_id = $2)
  AND ($3 = '' OR service_id = $3)
  AND ($4 = '' OR deployment_id = $4)
  AND ($5 = '' OR operation_id = $5)
  AND ($6 = '' OR replica_id = $6)
  AND ($7::timestamptz IS NULL OR observed_at >= $7)
  AND ($8::timestamptz IS NULL OR observed_at <= $8)
  AND ($9::bigint = 0 OR id > $9)
  AND (COALESCE(cardinality($10::text[]),0) = 0 OR surface = ANY($10))
  AND (COALESCE(cardinality($11::text[]),0) = 0 OR stream = ANY($11))
  AND (COALESCE(cardinality($12::text[]),0) = 0 OR level = ANY($12))
  AND (COALESCE(cardinality($13::text[]),0) = 0 OR component = ANY($13))
  AND ($14 = '' OR message ILIKE '%' || $14 || '%' OR attributes::text ILIKE '%' || $14 || '%')
ORDER BY id ASC
LIMIT $15`
	var since, until any
	if !query.Since.IsZero() {
		since = query.Since
	}
	if !query.Until.IsZero() {
		until = query.Until
	}
	rows, err := s.Pool.Query(ctx, sqlText,
		query.Scope.ProjectID, query.Scope.EnvironmentID, query.Scope.ServiceID,
		query.Scope.DeploymentID, query.Scope.OperationID, query.Scope.ReplicaID,
		since, until, query.AfterID, query.Surfaces, query.Streams, query.Levels,
		query.Components, strings.TrimSpace(query.Search), limit+1)
	if err != nil {
		return LogPage{}, fmt.Errorf("query observability logs: %w", err)
	}
	defer rows.Close()
	page := LogPage{Records: make([]LogRecord, 0, limit)}
	for rows.Next() {
		var record LogRecord
		var attributes []byte
		if err := rows.Scan(
			&record.ID, &record.ObservedAt, &record.Scope.ProjectID,
			&record.Scope.EnvironmentID, &record.Scope.ServiceID,
			&record.Scope.DeploymentID, &record.Scope.OperationID,
			&record.Scope.ReplicaID, &record.Surface, &record.Stream,
			&record.Level, &record.Component, &record.Message, &attributes,
		); err != nil {
			return LogPage{}, err
		}
		if len(attributes) > 0 {
			if err := json.Unmarshal(attributes, &record.Attributes); err != nil {
				return LogPage{}, err
			}
		}
		if len(page.Records) == limit {
			page.Truncated = true
			break
		}
		page.NextID = record.ID
		page.Records = append(page.Records, record)
	}
	if err := rows.Err(); err != nil {
		return LogPage{}, err
	}
	if len(page.Records) == 0 {
		page.NextID = query.AfterID
	}
	var dropped int64
	err = s.Pool.QueryRow(ctx, `
SELECT COALESCE(SUM(dropped_count),0)
FROM observability_ingestion_windows
WHERE project_id = $1
  AND ($2 = '' OR environment_id = $2)
  AND ($3 = '' OR service_id = $3)
  AND ($4::timestamptz IS NULL OR window_started_at >= $4)`,
		query.Scope.ProjectID, query.Scope.EnvironmentID, query.Scope.ServiceID, since).Scan(&dropped)
	if err == nil {
		page.DropCounts = dropped
	}
	return page, nil
}

func (s *PostgresStore) IngestionCounters(ctx context.Context, scope Scope, since time.Time) ([]IngestionCounter, error) {
	if err := s.ValidateScope(ctx, scope); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
SELECT source_key, window_started_at, accepted_count, dropped_count
FROM observability_ingestion_windows
WHERE project_id = $1
  AND ($2 = '' OR environment_id = $2)
  AND ($3 = '' OR service_id = $3)
  AND window_started_at >= $4
ORDER BY window_started_at, source_key`,
		scope.ProjectID, scope.EnvironmentID, scope.ServiceID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IngestionCounter
	for rows.Next() {
		var counter IngestionCounter
		if err := rows.Scan(&counter.SourceKey, &counter.WindowStartedAt, &counter.Accepted, &counter.Dropped); err != nil {
			return nil, err
		}
		out = append(out, counter)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CollectorCheckpoint(ctx context.Context, sourceKey string) (time.Time, bool, error) {
	var at time.Time
	err := s.Pool.QueryRow(ctx, `
SELECT last_observed_at
FROM observability_collector_checkpoints
WHERE source_key = $1`, strings.TrimSpace(sourceKey)).Scan(&at)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	return at, true, nil
}

func (s *PostgresStore) CollectorCursor(ctx context.Context, sourceKey string, out any) (time.Time, bool, error) {
	var at time.Time
	var raw []byte
	err := s.Pool.QueryRow(ctx, `
SELECT last_observed_at, cursor
FROM observability_collector_checkpoints
WHERE source_key = $1`, strings.TrimSpace(sourceKey)).Scan(&at, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return time.Time{}, false, fmt.Errorf("decode collector cursor: %w", err)
		}
	}
	return at, true, nil
}

func (s *PostgresStore) SaveCollectorCursor(ctx context.Context, sourceKey string, at time.Time, cursor any) error {
	if strings.TrimSpace(sourceKey) == "" || at.IsZero() {
		return errors.New("collector cursor source and timestamp are required")
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return fmt.Errorf("encode collector cursor: %w", err)
	}
	_, err = s.Pool.Exec(ctx, `
INSERT INTO observability_collector_checkpoints (source_key, last_observed_at, cursor)
VALUES ($1,$2,$3)
ON CONFLICT (source_key) DO UPDATE
SET last_observed_at = EXCLUDED.last_observed_at,
    cursor = EXCLUDED.cursor,
    updated_at = CURRENT_TIMESTAMP`,
		strings.TrimSpace(sourceKey), at.UTC(), raw)
	return err
}

func (s *PostgresStore) SaveCollectorCheckpoint(ctx context.Context, sourceKey string, at time.Time) error {
	if strings.TrimSpace(sourceKey) == "" || at.IsZero() {
		return errors.New("collector checkpoint source and timestamp are required")
	}
	_, err := s.Pool.Exec(ctx, `
INSERT INTO observability_collector_checkpoints (source_key, last_observed_at)
VALUES ($1,$2)
ON CONFLICT (source_key) DO UPDATE
SET last_observed_at = GREATEST(
        observability_collector_checkpoints.last_observed_at,
        EXCLUDED.last_observed_at
    ),
    updated_at = CURRENT_TIMESTAMP`,
		strings.TrimSpace(sourceKey), at.UTC())
	return err
}

func (s *PostgresStore) HeartbeatCollector(ctx context.Context, collectorID, kind string, collectorErr error) error {
	status := "active"
	lastError := ""
	if collectorErr != nil {
		status = "degraded"
		lastError = collectorErr.Error()
		if s.Redactor != nil {
			lastError = s.Redactor.RedactString(lastError)
		}
		lastError = truncateUTF8(lastError, 2048)
	}
	_, err := s.Pool.Exec(ctx, `
INSERT INTO observability_collectors (
    collector_id, kind, status, last_error, last_seen_at
) VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (collector_id) DO UPDATE
SET kind = EXCLUDED.kind, status = EXCLUDED.status,
    last_error = EXCLUDED.last_error, last_seen_at = EXCLUDED.last_seen_at,
    updated_at = CURRENT_TIMESTAMP`,
		strings.TrimSpace(collectorID), strings.TrimSpace(kind), status, lastError, s.now())
	return err
}

func (s *PostgresStore) ActiveCollectors(ctx context.Context, maxAge time.Duration) ([]string, error) {
	if maxAge <= 0 {
		maxAge = DefaultHeartbeatAge
	}
	rows, err := s.Pool.Query(ctx, `
SELECT DISTINCT kind
FROM observability_collectors
WHERE last_seen_at >= $1 AND status IN ('active','degraded')
ORDER BY kind`, s.now().Add(-maxAge))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return nil, err
		}
		out = append(out, kind)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CollectorHealth(ctx context.Context, expectedKinds []string, maxAge time.Duration) ([]CollectorHealth, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("observability store is unavailable")
	}
	if maxAge <= 0 {
		maxAge = DefaultHeartbeatAge
	}
	kinds := normalizeCollectorKinds(expectedKinds)
	if len(kinds) == 0 {
		return []CollectorHealth{}, nil
	}
	rows, err := s.Pool.Query(ctx, `
SELECT DISTINCT ON (kind) kind, status, last_seen_at
FROM observability_collectors
WHERE kind = ANY($1::text[])
ORDER BY kind, last_seen_at DESC`, kinds)
	if err != nil {
		return nil, fmt.Errorf("query observability collector health: %w", err)
	}
	defer rows.Close()
	type heartbeat struct {
		status string
		seen   time.Time
	}
	latest := make(map[string]heartbeat, len(kinds))
	for rows.Next() {
		var kind string
		var item heartbeat
		if err := rows.Scan(&kind, &item.status, &item.seen); err != nil {
			return nil, err
		}
		latest[kind] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	now := s.now()
	health := make([]CollectorHealth, 0, len(kinds))
	for _, kind := range kinds {
		item, ok := latest[kind]
		if !ok {
			health = append(health, CollectorHealth{
				Kind: kind, Status: "unavailable", HeartbeatGap: true,
				Reason: "collector has not reported a heartbeat",
			})
			continue
		}
		seen := item.seen.UTC()
		status := CollectorHealth{Kind: kind, Status: item.status, LastSeenAt: &seen}
		if seen.Before(now.Add(-maxAge)) {
			status.Status = "stale"
			status.HeartbeatGap = true
			status.Reason = "collector heartbeat is stale"
		} else if item.status == "degraded" {
			status.Reason = "collector reported an error on its latest pass"
		}
		health = append(health, status)
	}
	return health, nil
}

func normalizeCollectorKinds(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		kind := strings.ToLower(strings.TrimSpace(value))
		switch kind {
		case CollectorKindDocker, CollectorKindTraefik:
			seen[kind] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for kind := range seen {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

func (s *PostgresStore) IngestMetric(ctx context.Context, sample MetricSample) error {
	if err := s.ValidateScope(ctx, sample.Scope); err != nil {
		return err
	}
	if sample.ObservedAt.IsZero() {
		sample.ObservedAt = s.now()
	}
	if sample.Source == "" {
		sample.Source = "docker"
	}
	if sample.Resolution == "" {
		sample.Resolution = "raw"
	}
	_, err := s.Pool.Exec(ctx, `
INSERT INTO observability_metric_samples (
    observed_at, project_id, environment_id, service_id, deployment_id,
    replica_id, source, sample_resolution, cpu_percent, memory_bytes,
    memory_limit_bytes, network_rx_bytes, network_tx_bytes, block_read_bytes,
    block_write_bytes, disk_usage_bytes, restart_count, marker
) VALUES (
    $1,$2,NULLIF($3,''),$4,NULLIF($5,''),$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18
)
ON CONFLICT DO NOTHING`,
		sample.ObservedAt, sample.Scope.ProjectID, sample.Scope.EnvironmentID,
		sample.Scope.ServiceID, sample.Scope.DeploymentID, sample.Scope.ReplicaID,
		sample.Source, sample.Resolution, maxFloat(sample.CPUPercent, 0),
		maxInt64(sample.MemoryBytes, 0), maxInt64(sample.MemoryLimitBytes, 0),
		maxInt64(sample.NetworkRXBytes, 0), maxInt64(sample.NetworkTXBytes, 0),
		maxInt64(sample.BlockReadBytes, 0), maxInt64(sample.BlockWriteBytes, 0),
		sample.DiskUsageBytes, maxInt64(sample.RestartCount, 0), sample.Marker)
	if err != nil {
		return fmt.Errorf("ingest observability metric: %w", err)
	}
	return nil
}

func (s *PostgresStore) RecordDeploymentMarker(ctx context.Context, scope Scope, marker string, at time.Time) error {
	if scope.DeploymentID == "" {
		return errors.New("deployment marker requires deployment scope")
	}
	if err := s.ValidateScope(ctx, scope); err != nil {
		return err
	}
	if at.IsZero() {
		at = s.now()
	}
	if marker == "" {
		marker = "deployed"
	}
	_, err := s.Pool.Exec(ctx, `
INSERT INTO observability_deployment_markers (
    deployment_id, project_id, environment_id, service_id, marker, observed_at
) VALUES ($1,$2,NULLIF($3,''),$4,$5,$6)
ON CONFLICT (deployment_id) DO NOTHING`,
		scope.DeploymentID, scope.ProjectID, scope.EnvironmentID, scope.ServiceID,
		marker, at)
	return err
}

func (s *PostgresStore) QueryMetrics(ctx context.Context, query MetricQuery) (MetricSeries, error) {
	if err := s.ValidateScope(ctx, query.Scope); err != nil {
		return MetricSeries{}, err
	}
	if query.Scope.ServiceID == "" {
		return MetricSeries{}, errors.New("metric query requires service scope")
	}
	if query.Mode == "" {
		query.Mode = "sum"
	}
	if query.Mode != "sum" && query.Mode != "replica" {
		return MetricSeries{}, errors.New("metric mode must be sum or replica")
	}
	now := s.now()
	if query.Until.IsZero() {
		query.Until = now
	}
	if query.Since.IsZero() {
		query.Since = query.Until.Add(-time.Hour)
	}
	retention := s.Retention
	if retention <= 0 {
		retention = DefaultRetention
	}
	if query.Since.Before(now.Add(-retention)) {
		query.Since = now.Add(-retention)
	}
	if !query.Until.After(query.Since) {
		return MetricSeries{}, errors.New("metric until must be after since")
	}
	query.Step = boundedStep(query.Since, query.Until, query.Step)
	const perReplicaSQL = `
WITH per_replica AS (
    SELECT date_bin($8::interval, observed_at, TIMESTAMPTZ '1970-01-01') AS bucket,
           replica_id,
           AVG(cpu_percent) AS cpu_percent,
           ROUND(AVG(memory_bytes))::bigint AS memory_bytes,
           ROUND(AVG(memory_limit_bytes))::bigint AS memory_limit_bytes,
           MAX(network_rx_bytes) AS network_rx_bytes,
           MAX(network_tx_bytes) AS network_tx_bytes,
           MAX(block_read_bytes) AS block_read_bytes,
           MAX(block_write_bytes) AS block_write_bytes,
           CASE WHEN COUNT(disk_usage_bytes) > 0
                THEN ROUND(AVG(disk_usage_bytes))::bigint ELSE -1 END AS disk_usage_bytes,
           MAX(restart_count) AS restart_count,
           COUNT(*)::bigint AS samples
    FROM observability_metric_samples
    WHERE project_id = $1 AND service_id = $2
      AND ($3 = '' OR environment_id = $3)
      AND ($4 = '' OR deployment_id = $4)
      AND ($5 = '' OR replica_id = $5)
      AND observed_at >= $6 AND observed_at <= $7
    GROUP BY bucket, replica_id
) `
	metricSQL := perReplicaSQL + `
SELECT bucket, replica_id, cpu_percent, memory_bytes, memory_limit_bytes,
       network_rx_bytes, network_tx_bytes, block_read_bytes, block_write_bytes,
       disk_usage_bytes, restart_count, 1::bigint AS replicas, samples
FROM per_replica
ORDER BY bucket DESC, replica_id
LIMIT $9`
	if query.Mode == "sum" {
		metricSQL = perReplicaSQL + `
SELECT bucket, '' AS replica_id, SUM(cpu_percent), SUM(memory_bytes),
       SUM(memory_limit_bytes), SUM(network_rx_bytes), SUM(network_tx_bytes),
       SUM(block_read_bytes), SUM(block_write_bytes),
       CASE WHEN BOOL_OR(disk_usage_bytes >= 0)
            THEN SUM(GREATEST(disk_usage_bytes,0)) ELSE -1 END,
       SUM(restart_count), COUNT(*)::bigint AS replicas, SUM(samples)
FROM per_replica
GROUP BY bucket
ORDER BY bucket DESC
LIMIT $9`
	}
	rows, err := s.Pool.Query(ctx, metricSQL,
		query.Scope.ProjectID, query.Scope.ServiceID, query.Scope.EnvironmentID,
		query.Scope.DeploymentID, query.Scope.ReplicaID, query.Since, query.Until,
		query.Step.String(), MaxMetricPoints+1)
	if err != nil {
		return MetricSeries{}, err
	}
	defer rows.Close()
	points := make([]MetricPoint, 0, MaxMetricPoints+1)
	for rows.Next() {
		var point MetricPoint
		var disk, replicas, samples int64
		if err := rows.Scan(
			&point.ObservedAt, &point.ReplicaID, &point.CPUPercent,
			&point.MemoryBytes, &point.MemoryLimitBytes, &point.NetworkRXBytes,
			&point.NetworkTXBytes, &point.BlockReadBytes, &point.BlockWriteBytes,
			&disk, &point.RestartCount, &replicas, &samples,
		); err != nil {
			return MetricSeries{}, err
		}
		point.Replicas = int(replicas)
		point.Samples = int(samples)
		if disk >= 0 {
			point.DiskUsageBytes = &disk
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return MetricSeries{}, err
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].ObservedAt.Equal(points[j].ObservedAt) {
			return points[i].ReplicaID < points[j].ReplicaID
		}
		return points[i].ObservedAt.Before(points[j].ObservedAt)
	})
	if len(points) > MaxMetricPoints {
		points = points[len(points)-MaxMetricPoints:]
	}
	markers, err := s.queryMarkers(ctx, query)
	if err != nil {
		return MetricSeries{}, err
	}
	collectorHealth, err := s.CollectorHealth(ctx, []string{CollectorKindDocker}, DefaultHeartbeatAge)
	if err != nil {
		return MetricSeries{}, err
	}
	collectors := make([]string, 0, len(collectorHealth))
	for _, collector := range collectorHealth {
		if !collector.HeartbeatGap && (collector.Status == "active" || collector.Status == "degraded") {
			collectors = append(collectors, collector.Kind)
		}
	}
	return MetricSeries{
		Mode: query.Mode, Step: query.Step.String(), Points: points,
		Markers: markers, Collectors: collectors, CollectorHealth: collectorHealth,
	}, nil
}

func (s *PostgresStore) queryMarkers(ctx context.Context, query MetricQuery) ([]DeploymentMarker, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT deployment_id, marker, observed_at
FROM observability_deployment_markers
WHERE project_id = $1 AND service_id = $2
  AND ($3 = '' OR environment_id = $3)
  AND ($4 = '' OR deployment_id = $4)
  AND observed_at >= $5 AND observed_at <= $6
ORDER BY observed_at`,
		query.Scope.ProjectID, query.Scope.ServiceID, query.Scope.EnvironmentID,
		query.Scope.DeploymentID, query.Since, query.Until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeploymentMarker
	for rows.Next() {
		var marker DeploymentMarker
		if err := rows.Scan(&marker.DeploymentID, &marker.Marker, &marker.ObservedAt); err != nil {
			return nil, err
		}
		out = append(out, marker)
	}
	return out, rows.Err()
}

func (s *PostgresStore) Cleanup(ctx context.Context, before time.Time) (CleanupResult, error) {
	if before.IsZero() {
		retention := s.Retention
		if retention <= 0 {
			retention = DefaultRetention
		}
		before = s.now().Add(-retention)
	}
	var result CleanupResult
	logsTag, err := s.Pool.Exec(ctx, `DELETE FROM observability_log_records WHERE observed_at < $1`, before)
	if err != nil {
		return result, err
	}
	result.Logs = logsTag.RowsAffected()
	metricsTag, err := s.Pool.Exec(ctx, `DELETE FROM observability_metric_samples WHERE observed_at < $1`, before)
	if err != nil {
		return result, err
	}
	result.Metrics = metricsTag.RowsAffected()
	windowsTag, err := s.Pool.Exec(ctx, `DELETE FROM observability_ingestion_windows WHERE window_started_at < $1`, before)
	if err != nil {
		return result, err
	}
	result.Windows = windowsTag.RowsAffected()
	checkpointsTag, err := s.Pool.Exec(ctx, `DELETE FROM observability_collector_checkpoints WHERE last_observed_at < $1`, before)
	if err != nil {
		return result, err
	}
	result.Checkpoints = checkpointsTag.RowsAffected()
	collectorsTag, err := s.Pool.Exec(ctx, `DELETE FROM observability_collectors WHERE last_seen_at < $1`, before)
	if err != nil {
		return result, err
	}
	result.Collectors = collectorsTag.RowsAffected()
	return result, nil
}

// Query adapts the durable store to the existing operation log surface.
func (s *PostgresStore) Query(filter logrecords.Filter, cursor logrecords.Cursor, pageSize int) (logrecords.QueryResult, error) {
	if err := filter.Validate(); err != nil {
		return logrecords.QueryResult{}, err
	}
	scope, err := s.scopeForLegacyFilter(context.Background(), filter)
	if err != nil {
		return logrecords.QueryResult{}, err
	}
	afterID := int64(0)
	if cursor.File != "" && cursor.File != "postgres" {
		return logrecords.QueryResult{}, errors.New("invalid durable log cursor")
	}
	if cursor.Offset > 0 {
		afterID = int64(cursor.Offset)
	}
	page, err := s.QueryLogs(context.Background(), LogQuery{
		Scope: scope, Since: filter.Since, Until: filter.Until, Search: filter.FreeText,
		AfterID: afterID, Limit: pageSize, Components: filter.Components,
	})
	if err != nil {
		return logrecords.QueryResult{}, err
	}
	result := logrecords.QueryResult{Truncated: page.Truncated}
	for _, record := range page.Records {
		if len(filter.Components) > 0 && !containsFold(filter.Components, record.Component) {
			continue
		}
		entry := logrecords.Entry{
			At: record.ObservedAt, Level: record.Level, Component: record.Component,
			Message: record.Message, Fields: copyAttributes(record),
		}
		if filter.Match(entry) {
			result.Entries = append(result.Entries, entry)
		}
	}
	if page.Truncated {
		result.NextCursor = logrecords.Cursor{File: "postgres", Offset: int(page.NextID)}
	}
	return result, nil
}

func (s *PostgresStore) scopeForLegacyFilter(ctx context.Context, filter logrecords.Filter) (Scope, error) {
	var scope Scope
	var deployment, operation, resolvedOperation string
	if len(filter.Deployments) > 0 {
		deployment = filter.Deployments[0]
	}
	if len(filter.Operations) > 0 {
		operation = filter.Operations[0]
	}
	err := s.Pool.QueryRow(ctx, `
SELECT d.project_id, d.environment_id, d.service_id, d.id, COALESCE(d.operation_id,'')
FROM deployments d
WHERE ($1 = '' OR d.id = $1) AND ($2 = '' OR d.operation_id = $2)
ORDER BY d.created_at DESC LIMIT 1`, deployment, operation).Scan(
		&scope.ProjectID, &scope.EnvironmentID, &scope.ServiceID,
		&scope.DeploymentID, &resolvedOperation)
	if err != nil {
		return Scope{}, fmt.Errorf("resolve operation log scope: %w", err)
	}
	// A deployment filter asks for every surface produced by that deployment.
	// Docker runtime records do not claim an operation identity, so only apply
	// the operation predicate when the caller actually supplied one.
	if operation != "" {
		scope.OperationID = resolvedOperation
	}
	return scope, nil
}

func copyAttributes(record LogRecord) map[string]any {
	out := make(map[string]any, len(record.Attributes)+8)
	for key, value := range record.Attributes {
		out[key] = value
	}
	out["project_id"] = record.Scope.ProjectID
	out["environment_id"] = record.Scope.EnvironmentID
	out["service_id"] = record.Scope.ServiceID
	out["deployment_id"] = record.Scope.DeploymentID
	out["operation_id"] = record.Scope.OperationID
	out["replica_id"] = record.Scope.ReplicaID
	out["surface"] = record.Surface
	out["stream"] = record.Stream
	return out
}

func boundAttributes(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > MaxAttributes {
		keys = keys[:MaxAttributes]
	}
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		switch value := in[key].(type) {
		case string:
			out[key] = truncateUTF8(value, 2048)
		case bool, float32, float64, int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64, nil:
			out[key] = value
		default:
			encoded, err := json.Marshal(value)
			if err == nil {
				out[key] = truncateUTF8(string(encoded), 2048)
			}
		}
	}
	for len(out) > 0 {
		encoded, _ := json.Marshal(out)
		if len(encoded) <= MaxAttributeJSONSize {
			break
		}
		outKeys := make([]string, 0, len(out))
		for key := range out {
			outKeys = append(outKeys, key)
		}
		sort.Strings(outKeys)
		delete(out, outKeys[len(outKeys)-1])
	}
	return out
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func boundedStep(since, until time.Time, requested time.Duration) time.Duration {
	minimum := 10 * time.Second
	duration := until.Sub(since)
	auto := time.Duration((duration.Nanoseconds() + MaxMetricPoints - 1) / MaxMetricPoints)
	if auto > minimum {
		minimum = auto
	}
	minimum = minimum.Round(time.Second)
	if requested < minimum {
		return minimum
	}
	return requested
}

func (s *PostgresStore) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func containsFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
