// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package monitors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/db/pg"
)

type PostgresStore struct {
	Pool      *pg.Pool
	IDFactory func(string) string
}

func NewPostgresStore(pool *pg.Pool) *PostgresStore {
	return &PostgresStore{Pool: pool}
}

func randomID(prefix string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate monitor id: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

func (store *PostgresStore) id(prefix string) (string, error) {
	if store != nil && store.IDFactory != nil {
		return store.IDFactory(prefix), nil
	}
	return randomID(prefix)
}

func (store *PostgresStore) ready() error {
	if store == nil || store.Pool == nil {
		return errors.New("monitor store requires Postgres")
	}
	return nil
}

func (store *PostgresStore) CreateDefinition(ctx context.Context, definition Definition) (Definition, error) {
	if err := store.ready(); err != nil {
		return Definition{}, err
	}
	if definition.ID == "" {
		var err error
		definition.ID, err = store.id("mon")
		if err != nil {
			return Definition{}, err
		}
	}
	if definition.Window == 0 {
		definition.Window = DefaultWindow
	}
	if definition.EvaluationInterval == 0 {
		definition.EvaluationInterval = DefaultEvaluationInterval
	}
	if definition.ConsecutiveFailures == 0 {
		definition.ConsecutiveFailures = 1
	}
	if definition.ConsecutiveRecoveries == 0 {
		definition.ConsecutiveRecoveries = 1
	}
	if definition.State == "" {
		definition.State = StateUnknown
	}
	if err := definition.Validate(); err != nil {
		return Definition{}, err
	}
	if !definition.Enabled {
		// Create defaults to enabled; callers can explicitly disable later
		// through a versioned update surface.
		definition.Enabled = true
	}
	err := store.Pool.QueryRow(ctx, `
INSERT INTO monitor_definitions(
    id,project_id,environment_id,service_id,name,signal,comparison_operator,
    threshold,window_seconds,evaluation_interval_seconds,
    consecutive_failures,consecutive_recoveries,enabled,state
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
RETURNING next_evaluation_at`,
		definition.ID, definition.Scope.ProjectID, definition.Scope.EnvironmentID,
		definition.Scope.ServiceID, strings.TrimSpace(definition.Name),
		definition.Signal, definition.Operator, definition.Threshold,
		int(definition.Window/time.Second), int(definition.EvaluationInterval/time.Second),
		definition.ConsecutiveFailures, definition.ConsecutiveRecoveries,
		definition.Enabled, definition.State,
	).Scan(&definition.NextEvaluationAt)
	if err != nil {
		return Definition{}, fmt.Errorf("create monitor: %w", err)
	}
	return definition, nil
}

func (store *PostgresStore) ListDefinitions(ctx context.Context, projectID string) ([]Definition, error) {
	if err := store.ready(); err != nil {
		return nil, err
	}
	rows, err := store.Pool.Query(ctx, `
SELECT id,name,project_id,environment_id,service_id,signal,comparison_operator,
       threshold,window_seconds,evaluation_interval_seconds,
       consecutive_failures,consecutive_recoveries,enabled,state,
       failure_streak,recovery_streak,next_evaluation_at,last_evaluated_at,last_value
  FROM monitor_definitions
 WHERE project_id=$1
 ORDER BY name,id`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	definitions := make([]Definition, 0)
	for rows.Next() {
		definition, err := scanDefinition(rows)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return definitions, rows.Err()
}

func (store *PostgresStore) DeleteDefinition(ctx context.Context, projectID, monitorID string) error {
	if err := store.ready(); err != nil {
		return err
	}
	tag, err := store.Pool.Exec(ctx,
		`DELETE FROM monitor_definitions WHERE id=$1 AND project_id=$2`,
		strings.TrimSpace(monitorID), strings.TrimSpace(projectID))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (store *PostgresStore) CreateChannel(ctx context.Context, channel Channel) (Channel, error) {
	if err := store.ready(); err != nil {
		return Channel{}, err
	}
	if channel.ID == "" {
		var err error
		channel.ID, err = store.id("mch")
		if err != nil {
			return Channel{}, err
		}
	}
	channel.ProjectID = strings.TrimSpace(channel.ProjectID)
	channel.DisplayName = strings.TrimSpace(channel.DisplayName)
	channel.EndpointVariableID = strings.TrimSpace(channel.EndpointVariableID)
	channel.SigningVariableID = strings.TrimSpace(channel.SigningVariableID)
	if channel.Type == "" {
		channel.Type = "webhook"
	}
	if channel.SigningKeyID == "" {
		channel.SigningKeyID = "monitor"
	}
	if channel.ProjectID == "" || channel.DisplayName == "" ||
		channel.EndpointVariableID == "" || channel.SigningVariableID == "" ||
		channel.Type != "webhook" {
		return Channel{}, errors.New("monitor webhook channel requires project, name, and endpoint/signing variable ids")
	}
	if channel.AllowInsecureLoopback {
		return Channel{}, errors.New("insecure loopback monitor channels are test-only")
	}
	err := store.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var matching int
		if err := tx.QueryRow(ctx, `
SELECT count(DISTINCT id)
  FROM service_variables
 WHERE project_id=$1 AND deleted_at IS NULL AND id=ANY($2::text[])`,
			channel.ProjectID, []string{channel.EndpointVariableID, channel.SigningVariableID}).Scan(&matching); err != nil {
			return err
		}
		required := 2
		if channel.EndpointVariableID == channel.SigningVariableID {
			required = 1
		}
		if matching != required {
			return errors.New("monitor channel variable references do not belong to the project")
		}
		_, err := tx.Exec(ctx, `
INSERT INTO monitor_channels(
    id,project_id,display_name,channel_type,endpoint_variable_id,
    signing_variable_id,signing_key_id,allow_insecure_loopback
) VALUES ($1,$2,$3,$4,$5,$6,$7,FALSE)`,
			channel.ID, channel.ProjectID, channel.DisplayName, channel.Type,
			channel.EndpointVariableID, channel.SigningVariableID, channel.SigningKeyID)
		return err
	})
	if err != nil {
		return Channel{}, fmt.Errorf("create monitor channel: %w", err)
	}
	return channel, nil
}

func (store *PostgresStore) ListChannels(ctx context.Context, projectID string) ([]Channel, error) {
	if err := store.ready(); err != nil {
		return nil, err
	}
	rows, err := store.Pool.Query(ctx, `
SELECT id,project_id,display_name,channel_type,endpoint_variable_id,
       signing_variable_id,signing_key_id,allow_insecure_loopback
  FROM monitor_channels WHERE project_id=$1 ORDER BY display_name,id`,
		strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := make([]Channel, 0)
	for rows.Next() {
		var channel Channel
		if err := rows.Scan(
			&channel.ID, &channel.ProjectID, &channel.DisplayName, &channel.Type,
			&channel.EndpointVariableID, &channel.SigningVariableID,
			&channel.SigningKeyID, &channel.AllowInsecureLoopback,
		); err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}

func (store *PostgresStore) Subscribe(ctx context.Context, projectID, monitorID, channelID string) error {
	if err := store.ready(); err != nil {
		return err
	}
	tag, err := store.Pool.Exec(ctx, `
INSERT INTO monitor_subscriptions(monitor_id,channel_id)
SELECT monitor.id,channel.id
  FROM monitor_definitions AS monitor
  JOIN monitor_channels AS channel ON channel.project_id=monitor.project_id
 WHERE monitor.id=$1 AND channel.id=$2 AND monitor.project_id=$3
ON CONFLICT (monitor_id,channel_id) DO NOTHING`,
		strings.TrimSpace(monitorID), strings.TrimSpace(channelID), strings.TrimSpace(projectID))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		// The row may already exist. Distinguish replay from an invalid scope.
		var exists bool
		if err := store.Pool.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM monitor_subscriptions AS subscription
  JOIN monitor_definitions AS monitor ON monitor.id=subscription.monitor_id
  WHERE subscription.monitor_id=$1 AND subscription.channel_id=$2
    AND monitor.project_id=$3
)`, monitorID, channelID, projectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return pgx.ErrNoRows
		}
	}
	return nil
}

func (store *PostgresStore) LeaseDue(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]Definition, error) {
	if err := store.ready(); err != nil {
		return nil, err
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || lease <= 0 || limit < 1 || limit > 100 {
		return nil, errors.New("monitor lease requires worker, positive lease, and bounded limit")
	}
	rows, err := store.Pool.Query(ctx, `
WITH due AS (
    SELECT id
      FROM monitor_definitions
     WHERE enabled = TRUE
       AND next_evaluation_at <= $1
       AND (lease_expires_at IS NULL OR lease_expires_at <= $1)
     ORDER BY next_evaluation_at, id
     FOR UPDATE SKIP LOCKED
     LIMIT $2
)
UPDATE monitor_definitions AS monitor
   SET lease_owner=$3, lease_expires_at=$1 + $4::interval, updated_at=$1
  FROM due
 WHERE monitor.id=due.id
RETURNING monitor.id, monitor.name, monitor.project_id, monitor.environment_id,
          monitor.service_id, monitor.signal, monitor.comparison_operator,
          monitor.threshold, monitor.window_seconds,
          monitor.evaluation_interval_seconds, monitor.consecutive_failures,
          monitor.consecutive_recoveries, monitor.enabled, monitor.state,
          monitor.failure_streak, monitor.recovery_streak,
          monitor.next_evaluation_at, monitor.last_evaluated_at,
          monitor.last_value`, now.UTC(), limit, workerID, intervalValue(lease))
	if err != nil {
		return nil, fmt.Errorf("lease monitors: %w", err)
	}
	defer rows.Close()
	out := make([]Definition, 0, limit)
	for rows.Next() {
		definition, err := scanDefinition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, definition)
	}
	return out, rows.Err()
}

func (store *PostgresStore) RecordEvaluation(ctx context.Context, workerID string, evaluation Evaluation, now time.Time) (Transition, error) {
	if err := store.ready(); err != nil {
		return Transition{}, err
	}
	var transition Transition
	err := store.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		definition, err := loadLeasedDefinition(ctx, tx, evaluation.MonitorID, workerID, now)
		if err != nil {
			return err
		}
		from, to := definition.State, definition.State
		failureStreak, recoveryStreak := definition.FailureStreak, definition.RecoveryStreak
		if evaluation.HasData {
			if evaluation.Breached {
				failureStreak++
				recoveryStreak = 0
				if definition.State != StateFiring && failureStreak >= definition.ConsecutiveFailures {
					to = StateFiring
				}
			} else {
				failureStreak = 0
				recoveryStreak++
				if definition.State == StateUnknown ||
					(definition.State == StateFiring && recoveryStreak >= definition.ConsecutiveRecoveries) {
					to = StateHealthy
				}
			}
		}
		if _, err := tx.Exec(ctx, `
UPDATE monitor_definitions
   SET state=$2,failure_streak=$3,recovery_streak=$4,last_value=$5,
       last_evaluated_at=$6::timestamptz,
       next_evaluation_at=$6::timestamptz + evaluation_interval_seconds * interval '1 second',
       lease_owner=NULL,lease_expires_at=NULL,updated_at=$6::timestamptz
 WHERE id=$1`, definition.ID, to, failureStreak, recoveryStreak,
			evaluation.Value, now.UTC()); err != nil {
			return err
		}
		transition = Transition{
			MonitorID: definition.ID, From: from, To: to,
			Value: evaluation.Value, At: now.UTC(),
		}
		if from == to {
			return nil
		}
		switch to {
		case StateFiring:
			transition.EventType = EventFiring
		case StateHealthy:
			if from == StateFiring {
				transition.EventType = EventResolved
			}
		}
		if transition.EventType == "" {
			return nil
		}
		return store.enqueueTransitionTx(ctx, tx, definition, transition)
	})
	if err != nil {
		return Transition{}, fmt.Errorf("record monitor evaluation: %w", err)
	}
	return transition, nil
}

func loadLeasedDefinition(ctx context.Context, tx *pg.Tx, monitorID, workerID string, now time.Time) (Definition, error) {
	var definition Definition
	var windowSeconds, intervalSeconds int
	var lastEvaluatedAt *time.Time
	err := tx.QueryRow(ctx, `
SELECT id,name,project_id,environment_id,service_id,signal,comparison_operator,
       threshold,window_seconds,evaluation_interval_seconds,
       consecutive_failures,consecutive_recoveries,enabled,state,
       failure_streak,recovery_streak,next_evaluation_at,last_evaluated_at,last_value
  FROM monitor_definitions
 WHERE id=$1 AND lease_owner=$2 AND lease_expires_at > $3
 FOR UPDATE`, monitorID, strings.TrimSpace(workerID), now.UTC()).Scan(
		&definition.ID, &definition.Name, &definition.Scope.ProjectID,
		&definition.Scope.EnvironmentID, &definition.Scope.ServiceID,
		&definition.Signal, &definition.Operator, &definition.Threshold,
		&windowSeconds, &intervalSeconds, &definition.ConsecutiveFailures,
		&definition.ConsecutiveRecoveries, &definition.Enabled,
		&definition.State, &definition.FailureStreak, &definition.RecoveryStreak,
		&definition.NextEvaluationAt, &lastEvaluatedAt, &definition.LastValue,
	)
	if lastEvaluatedAt != nil {
		definition.LastEvaluatedAt = lastEvaluatedAt.UTC()
	}
	definition.Window = time.Duration(windowSeconds) * time.Second
	definition.EvaluationInterval = time.Duration(intervalSeconds) * time.Second
	return definition, err
}

func (store *PostgresStore) enqueueTransitionTx(ctx context.Context, tx *pg.Tx, definition Definition, transition Transition) error {
	var incidentID string
	switch transition.EventType {
	case EventFiring:
		var err error
		incidentID, err = store.id("minc")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO monitor_incidents(id,monitor_id,opened_at,open_value)
VALUES ($1,$2,$3,$4)`, incidentID, definition.ID, transition.At, transition.Value); err != nil {
			return err
		}
	case EventResolved:
		err := tx.QueryRow(ctx, `
UPDATE monitor_incidents
   SET resolved_at=$2,close_value=$3
 WHERE id=(SELECT id FROM monitor_incidents
            WHERE monitor_id=$1 AND resolved_at IS NULL FOR UPDATE)
RETURNING id`, definition.ID, transition.At, transition.Value).Scan(&incidentID)
		if err != nil {
			return err
		}
	}
	payload := map[string]any{
		"event_type":  transition.EventType,
		"incident_id": incidentID,
		"monitor": map[string]any{
			"id": definition.ID, "name": definition.Name,
			"signal": definition.Signal, "operator": definition.Operator,
			"threshold": definition.Threshold, "state": transition.To,
			"value": transition.Value,
		},
		"scope": definition.Scope, "observed_at": transition.At,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
INSERT INTO monitor_delivery_outbox(
    id,incident_id,monitor_id,channel_id,event_type,payload,available_at
)
SELECT 'mdl_' || substr(md5($1 || ':' || subscription.channel_id || ':' || $3),1,24),$1,$2,
       subscription.channel_id,$3,$4::jsonb,$5
  FROM monitor_subscriptions AS subscription
 WHERE subscription.monitor_id=$2
ON CONFLICT (incident_id,channel_id,event_type) DO NOTHING`,
		incidentID, definition.ID, transition.EventType, string(encoded), transition.At)
	return err
}

func (store *PostgresStore) LeaseDeliveries(ctx context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]Delivery, error) {
	if err := store.ready(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workerID) == "" || lease <= 0 || limit < 1 || limit > 100 {
		return nil, errors.New("delivery lease requires worker, positive lease, and bounded limit")
	}
	rows, err := store.Pool.Query(ctx, `
WITH due AS (
    SELECT id
      FROM monitor_delivery_outbox
     WHERE available_at <= $1
       AND (state='pending' OR (state='leased' AND lease_expires_at <= $1))
     ORDER BY available_at,id
     FOR UPDATE SKIP LOCKED
     LIMIT $2
), leased AS (
    UPDATE monitor_delivery_outbox AS delivery
       SET state='leased',lease_owner=$3,
           lease_expires_at=$1 + $4::interval,updated_at=$1
      FROM due
     WHERE delivery.id=due.id
 RETURNING delivery.*
)
SELECT leased.id,leased.monitor_id,leased.event_type,leased.payload,
       leased.attempts,leased.available_at,channel.id,channel.project_id,
       channel.display_name,channel.channel_type,channel.endpoint_variable_id,
       channel.signing_variable_id,channel.signing_key_id,
       channel.allow_insecure_loopback
  FROM leased
  JOIN monitor_channels AS channel ON channel.id=leased.channel_id
 ORDER BY leased.available_at,leased.id`,
		now.UTC(), limit, strings.TrimSpace(workerID), intervalValue(lease))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := make([]Delivery, 0, limit)
	for rows.Next() {
		var delivery Delivery
		var payload []byte
		if err := rows.Scan(
			&delivery.ID, &delivery.MonitorID, &delivery.EventType, &payload,
			&delivery.Attempts, &delivery.AvailableAt, &delivery.Channel.ID,
			&delivery.Channel.ProjectID, &delivery.Channel.DisplayName,
			&delivery.Channel.Type, &delivery.Channel.EndpointVariableID,
			&delivery.Channel.SigningVariableID, &delivery.Channel.SigningKeyID,
			&delivery.Channel.AllowInsecureLoopback,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &delivery.Payload); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (store *PostgresStore) CompleteDelivery(ctx context.Context, id string, now time.Time, status int) error {
	tag, err := store.Pool.Exec(ctx, `
UPDATE monitor_delivery_outbox
   SET state='delivered',attempts=attempts+1,response_status=$2,last_error='',
       delivered_at=$3,lease_owner=NULL,lease_expires_at=NULL,updated_at=$3
 WHERE id=$1 AND state='leased'`, id, status, now.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (store *PostgresStore) RetryDelivery(ctx context.Context, id string, next time.Time, status int, message string, dead bool) error {
	state := "pending"
	if dead {
		state = "dead"
	}
	tag, err := store.Pool.Exec(ctx, `
UPDATE monitor_delivery_outbox
   SET state=$2,attempts=attempts+1,response_status=NULLIF($3,0),
       last_error=$4,available_at=$5,lease_owner=NULL,
       lease_expires_at=NULL,updated_at=CURRENT_TIMESTAMP
 WHERE id=$1 AND state='leased'`,
		id, state, status, truncateString(message, 512), next.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (store *PostgresStore) ListIncidents(ctx context.Context, query IncidentQuery) ([]IncidentStatus, error) {
	if err := store.ready(); err != nil {
		return nil, err
	}
	if query.Limit == 0 {
		query.Limit = 100
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	rows, err := store.Pool.Query(ctx, `
SELECT incident.id,incident.monitor_id,monitor.name,
       monitor.project_id,monitor.environment_id,monitor.service_id,
       incident.opened_at,incident.resolved_at,incident.open_value,incident.close_value,
       count(delivery.id)::integer,
       count(delivery.id) FILTER (WHERE delivery.state='dead')::integer
  FROM monitor_incidents AS incident
  JOIN monitor_definitions AS monitor ON monitor.id=incident.monitor_id
  LEFT JOIN monitor_delivery_outbox AS delivery ON delivery.incident_id=incident.id
 WHERE monitor.project_id=$1
   AND ($2='' OR monitor.environment_id=$2)
   AND ($3='' OR monitor.service_id=$3)
   AND ($4='' OR monitor.id=$4)
   AND ($5='' OR ($5='open' AND incident.resolved_at IS NULL)
              OR ($5='resolved' AND incident.resolved_at IS NOT NULL))
 GROUP BY incident.id,monitor.id
 ORDER BY incident.opened_at DESC,incident.id
 LIMIT $6`,
		strings.TrimSpace(query.ProjectID), strings.TrimSpace(query.EnvironmentID),
		strings.TrimSpace(query.ServiceID), strings.TrimSpace(query.MonitorID),
		strings.TrimSpace(query.State), query.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list monitor incidents: %w", err)
	}
	defer rows.Close()
	out := make([]IncidentStatus, 0)
	for rows.Next() {
		var incident IncidentStatus
		if err := rows.Scan(
			&incident.ID, &incident.MonitorID, &incident.MonitorName,
			&incident.Scope.ProjectID, &incident.Scope.EnvironmentID, &incident.Scope.ServiceID,
			&incident.OpenedAt, &incident.ResolvedAt, &incident.OpenValue, &incident.CloseValue,
			&incident.DeliveryCount, &incident.DeadCount,
		); err != nil {
			return nil, err
		}
		incident.State = IncidentStateOpen
		if incident.ResolvedAt != nil {
			incident.State = IncidentStateResolved
		}
		out = append(out, incident)
	}
	return out, rows.Err()
}

func (store *PostgresStore) ListDeliveryStatuses(ctx context.Context, query DeliveryStatusQuery) ([]DeliveryStatus, error) {
	if err := store.ready(); err != nil {
		return nil, err
	}
	if query.Limit == 0 {
		query.Limit = 100
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	rows, err := store.Pool.Query(ctx, `
SELECT delivery.id,delivery.incident_id,delivery.monitor_id,monitor.name,
       monitor.project_id,monitor.environment_id,monitor.service_id,
       channel.id,channel.display_name,channel.channel_type,
       delivery.event_type,delivery.state,delivery.attempts,delivery.manual_retries,
       delivery.available_at,delivery.response_status,delivery.last_error,
       delivery.delivered_at,delivery.created_at,delivery.updated_at
  FROM monitor_delivery_outbox AS delivery
  JOIN monitor_definitions AS monitor ON monitor.id=delivery.monitor_id
  JOIN monitor_channels AS channel ON channel.id=delivery.channel_id
 WHERE monitor.project_id=$1
   AND ($2='' OR monitor.environment_id=$2)
   AND ($3='' OR monitor.service_id=$3)
   AND ($4='' OR monitor.id=$4)
   AND ($5='' OR delivery.incident_id=$5)
   AND ($6='' OR delivery.state=$6)
 ORDER BY delivery.created_at DESC,delivery.id
 LIMIT $7`,
		strings.TrimSpace(query.ProjectID), strings.TrimSpace(query.EnvironmentID),
		strings.TrimSpace(query.ServiceID), strings.TrimSpace(query.MonitorID),
		strings.TrimSpace(query.IncidentID), strings.TrimSpace(query.State), query.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list monitor deliveries: %w", err)
	}
	defer rows.Close()
	out := make([]DeliveryStatus, 0)
	for rows.Next() {
		delivery, err := scanDeliveryStatus(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, delivery)
	}
	return out, rows.Err()
}

func (store *PostgresStore) RetryDeadDelivery(
	ctx context.Context,
	projectID, deliveryID string,
	now time.Time,
) (DeliveryStatus, error) {
	if err := store.ready(); err != nil {
		return DeliveryStatus{}, err
	}
	projectID = strings.TrimSpace(projectID)
	deliveryID = strings.TrimSpace(deliveryID)
	if projectID == "" || deliveryID == "" {
		return DeliveryStatus{}, errors.New("delivery retry requires project and delivery ids")
	}
	tag, err := store.Pool.Exec(ctx, `
UPDATE monitor_delivery_outbox AS delivery
   SET state='pending',attempts=0,manual_retries=manual_retries+1,
       available_at=$3,response_status=NULL,last_error='',delivered_at=NULL,
       lease_owner=NULL,lease_expires_at=NULL,updated_at=$3
  FROM monitor_definitions AS monitor
 WHERE delivery.id=$1 AND delivery.monitor_id=monitor.id
   AND monitor.project_id=$2 AND delivery.state='dead'`,
		deliveryID, projectID, now.UTC(),
	)
	if err != nil {
		return DeliveryStatus{}, fmt.Errorf("retry dead monitor delivery: %w", err)
	}
	if tag.RowsAffected() != 1 {
		var exists bool
		if err := store.Pool.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM monitor_delivery_outbox AS delivery
  JOIN monitor_definitions AS monitor ON monitor.id=delivery.monitor_id
  WHERE delivery.id=$1 AND monitor.project_id=$2
)`, deliveryID, projectID).Scan(&exists); err != nil {
			return DeliveryStatus{}, err
		}
		if !exists {
			return DeliveryStatus{}, pgx.ErrNoRows
		}
		return DeliveryStatus{}, ErrDeliveryNotDead
	}
	return store.getDeliveryStatus(ctx, projectID, deliveryID)
}

func (store *PostgresStore) getDeliveryStatus(
	ctx context.Context,
	projectID, deliveryID string,
) (DeliveryStatus, error) {
	row := store.Pool.QueryRow(ctx, `
SELECT delivery.id,delivery.incident_id,delivery.monitor_id,monitor.name,
       monitor.project_id,monitor.environment_id,monitor.service_id,
       channel.id,channel.display_name,channel.channel_type,
       delivery.event_type,delivery.state,delivery.attempts,delivery.manual_retries,
       delivery.available_at,delivery.response_status,delivery.last_error,
       delivery.delivered_at,delivery.created_at,delivery.updated_at
  FROM monitor_delivery_outbox AS delivery
  JOIN monitor_definitions AS monitor ON monitor.id=delivery.monitor_id
  JOIN monitor_channels AS channel ON channel.id=delivery.channel_id
 WHERE delivery.id=$1 AND monitor.project_id=$2`,
		strings.TrimSpace(deliveryID), strings.TrimSpace(projectID),
	)
	delivery, err := scanDeliveryStatus(row)
	if err != nil {
		return DeliveryStatus{}, err
	}
	return delivery, nil
}

func scanDeliveryStatus(row rowScanner) (DeliveryStatus, error) {
	var delivery DeliveryStatus
	var lastError string
	err := row.Scan(
		&delivery.ID, &delivery.IncidentID, &delivery.MonitorID, &delivery.MonitorName,
		&delivery.ProjectID, &delivery.EnvironmentID, &delivery.ServiceID,
		&delivery.ChannelID, &delivery.ChannelName, &delivery.ChannelType,
		&delivery.EventType, &delivery.State, &delivery.Attempts, &delivery.ManualRetries,
		&delivery.AvailableAt, &delivery.ResponseStatus, &lastError,
		&delivery.DeliveredAt, &delivery.CreatedAt, &delivery.UpdatedAt,
	)
	if lastError != "" {
		if delivery.ResponseStatus != nil {
			delivery.ErrorSummary = "remote endpoint returned a non-success status"
		} else {
			delivery.ErrorSummary = "delivery failed before receiving an HTTP response"
		}
	}
	return delivery, err
}

func intervalValue(duration time.Duration) string {
	return fmt.Sprintf("%f seconds", duration.Seconds())
}

func truncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

type rowScanner interface {
	Scan(...any) error
}

func scanDefinition(row rowScanner) (Definition, error) {
	var definition Definition
	var windowSeconds, intervalSeconds int
	var lastEvaluatedAt *time.Time
	err := row.Scan(
		&definition.ID, &definition.Name, &definition.Scope.ProjectID,
		&definition.Scope.EnvironmentID, &definition.Scope.ServiceID,
		&definition.Signal, &definition.Operator, &definition.Threshold,
		&windowSeconds, &intervalSeconds, &definition.ConsecutiveFailures,
		&definition.ConsecutiveRecoveries, &definition.Enabled, &definition.State,
		&definition.FailureStreak, &definition.RecoveryStreak,
		&definition.NextEvaluationAt, &lastEvaluatedAt, &definition.LastValue,
	)
	if lastEvaluatedAt != nil {
		definition.LastEvaluatedAt = lastEvaluatedAt.UTC()
	}
	definition.Window = time.Duration(windowSeconds) * time.Second
	definition.EvaluationInterval = time.Duration(intervalSeconds) * time.Second
	return definition, err
}

var _ Store = (*PostgresStore)(nil)
var _ DeliveryStore = (*PostgresStore)(nil)
var _ StatusStore = (*PostgresStore)(nil)
