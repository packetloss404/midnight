//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package monitors_test

import (
	"context"
	"errors"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/monitors"
	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresMonitorStreakIncidentAndDeliveryOutbox(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := (&dbpg.MigrationRunner{
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO projects(id,workspace_id,slug,display_name) VALUES ('prj_mon','wrk_default','monitor','Monitor');
INSERT INTO environments(id,project_id,slug,name)
VALUES ('env_mon','prj_mon','production','Production');
INSERT INTO services(id,project_id,slug,display_name)
VALUES ('svc_mon','prj_mon','api','API');
INSERT INTO service_variables(
    id,project_id,environment_id,service_id,name,scope,value_ciphertext
) VALUES
    ('var_endpoint','prj_mon','env_mon','svc_mon','MONITOR_URL','secret','cipher'),
    ('var_signing','prj_mon','env_mon','svc_mon','MONITOR_SIGNING','secret','cipher')`); err != nil {
		t.Fatal(err)
	}
	store := monitors.NewPostgresStore(pool)
	store.IDFactory = func(prefix string) string {
		switch prefix {
		case "mon":
			return "mon_integration"
		case "mch":
			return "mch_integration"
		case "minc":
			return "minc_integration"
		default:
			return prefix + "_integration"
		}
	}
	channel, err := store.CreateChannel(ctx, monitors.Channel{
		ProjectID: "prj_mon", DisplayName: "operations", Type: "webhook",
		EndpointVariableID: "var_endpoint", SigningVariableID: "var_signing",
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := store.CreateDefinition(ctx, monitors.Definition{
		Name: "CPU high",
		Scope: observability.Scope{
			ProjectID: "prj_mon", EnvironmentID: "env_mon", ServiceID: "svc_mon",
		},
		Signal: monitors.SignalCPUPercent, Operator: monitors.OperatorGreaterThan,
		Threshold: 80, Window: time.Minute, EvaluationInterval: time.Minute,
		ConsecutiveFailures: 2, ConsecutiveRecoveries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Subscribe(ctx, "prj_mon", definition.ID, channel.ID); err != nil {
		t.Fatal(err)
	}

	firstAt := definition.NextEvaluationAt.Add(time.Second)
	definitions, err := store.LeaseDue(ctx, "worker-1", firstAt, time.Minute, 10)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("first lease = %#v, %v", definitions, err)
	}
	value := 90.0
	transition, err := store.RecordEvaluation(ctx, "worker-1", monitors.Evaluation{
		MonitorID: definition.ID, ObservedAt: firstAt, Value: &value,
		Breached: true, HasData: true,
	}, firstAt)
	if err != nil {
		t.Fatal(err)
	}
	if transition.EventType != "" {
		t.Fatalf("first transition = %#v", transition)
	}

	secondAt := firstAt.Add(time.Minute + time.Second)
	definitions, err = store.LeaseDue(ctx, "worker-2", secondAt, time.Minute, 10)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("second lease = %#v, %v", definitions, err)
	}
	transition, err = store.RecordEvaluation(ctx, "worker-2", monitors.Evaluation{
		MonitorID: definition.ID, ObservedAt: secondAt, Value: &value,
		Breached: true, HasData: true,
	}, secondAt)
	if err != nil {
		t.Fatal(err)
	}
	if transition.EventType != monitors.EventFiring || transition.To != monitors.StateFiring {
		t.Fatalf("firing transition = %#v", transition)
	}
	deliveries, err := store.LeaseDeliveries(ctx, "delivery-1", secondAt, time.Minute, 10)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries = %#v, %v", deliveries, err)
	}
	if deliveries[0].Channel.EndpointVariableID != "var_endpoint" {
		t.Fatalf("delivery channel = %#v", deliveries[0].Channel)
	}
	if err := store.CompleteDelivery(ctx, deliveries[0].ID, secondAt, 204); err != nil {
		t.Fatal(err)
	}
	var state string
	var attempts int
	if err := pool.QueryRow(ctx,
		`SELECT state,attempts FROM monitor_delivery_outbox WHERE id=$1`,
		deliveries[0].ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" || attempts != 1 {
		t.Fatalf("delivery state = %q attempts = %d", state, attempts)
	}

	incidents, err := store.ListIncidents(ctx, monitors.IncidentQuery{
		ProjectID: "prj_mon", ServiceID: "svc_mon", State: "open", Limit: 10,
	})
	if err != nil || len(incidents) != 1 || incidents[0].DeliveryCount != 1 {
		t.Fatalf("incidents = %#v, %v", incidents, err)
	}
	statuses, err := store.ListDeliveryStatuses(ctx, monitors.DeliveryStatusQuery{
		ProjectID: "prj_mon", ServiceID: "svc_mon", State: "delivered", Limit: 10,
	})
	if err != nil || len(statuses) != 1 || statuses[0].State != "delivered" {
		t.Fatalf("delivery statuses = %#v, %v", statuses, err)
	}

	// Force the row into the durable dead-letter state with deliberately
	// sensitive-looking raw text. The status read model must return only its
	// fixed category, and manual retry must reset a bounded attempt cycle while
	// preserving a separate retry count.
	if _, err := pool.Exec(ctx, `
UPDATE monitor_delivery_outbox
   SET state='dead',attempts=$2,last_error=$3,response_status=NULL
 WHERE id=$1`,
		deliveries[0].ID, monitors.MaxDeliveryAttempts,
		`request to https://hooks.example.invalid/private?token=secret failed`,
	); err != nil {
		t.Fatal(err)
	}
	statuses, err = store.ListDeliveryStatuses(ctx, monitors.DeliveryStatusQuery{
		ProjectID: "prj_mon", State: "dead", Limit: 10,
	})
	if err != nil || len(statuses) != 1 {
		t.Fatalf("dead statuses = %#v, %v", statuses, err)
	}
	if statuses[0].ErrorSummary != "delivery failed before receiving an HTTP response" {
		t.Fatalf("error summary = %q", statuses[0].ErrorSummary)
	}
	requeued, err := store.RetryDeadDelivery(ctx, "prj_mon", deliveries[0].ID, secondAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if requeued.State != "pending" || requeued.Attempts != 0 || requeued.ManualRetries != 1 ||
		requeued.ErrorSummary != "" {
		t.Fatalf("requeued = %#v", requeued)
	}
	if _, err := store.RetryDeadDelivery(
		ctx, "prj_mon", deliveries[0].ID, secondAt.Add(2*time.Minute),
	); !errors.Is(err, monitors.ErrDeliveryNotDead) {
		t.Fatalf("second manual retry error = %v, want ErrDeliveryNotDead", err)
	}
}
