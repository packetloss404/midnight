//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	deploypkg "github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/secrets"
	templatepkg "github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/internal/varrefs"
	"github.com/midnight/midnight/migrations"
)

func TestOfficialPostgresTemplateIntakeIsAtomicDurableAndReplayable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	keyProvider := testVariableKeyProvider(t)
	stack, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool: pool, MigrationsFS: migrations.FS, ManifestPath: "manifest.yaml",
		CursorSecret:          []byte("0123456789abcdef0123456789abcdef"),
		AutomationTokenMACKey: []byte("template-provision-automation-key"),
		InvitationMACKey:      []byte("template-provision-invitation-key"),
		VariableIntentMACKey:  []byte("template-provision-variable-intent-key"),
		VariableKeyProvider:   keyProvider,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Template saga behavior is the subject of this test; authentication is
	// covered separately and production stacks otherwise fail closed.
	stack.ProductionAuthRequired = false
	resources := stack.ProjectStore.(*PostgresResourceStore)
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "template-proof", DisplayName: "Template proof"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRouterWithDataStack(stack)
	body := `{"project_id":"` + string(project.ID) + `","name":"Orders database"}`
	first := doRouterRequest(handler, http.MethodPost, "/api/v1alpha/templates/postgres/instances", body, map[string]string{
		"Idempotency-Key": "template-postgres-proof",
	})
	if first.Code != http.StatusAccepted {
		t.Fatalf("template status=%d body=%s", first.Code, first.Body.String())
	}
	var response TemplateDeployResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ProvisionRunID == "" || response.OperationID == "" ||
		response.DeploymentID == "" || response.EnvironmentID == "" ||
		len(response.CreatedServices) != 1 || len(response.CreatedVolumes) != 1 ||
		response.State != "queued" {
		t.Fatalf("template response = %#v", response)
	}

	for table, want := range map[string]int{
		"environments": 1, "services": 1, "service_environment_configs": 1,
		"volumes": 1, "service_variables": 9, "service_variable_versions": 9,
		"operations": 1, "deployments": 1, "deploy_intake_outbox": 1,
		"template_provision_runs": 1,
	} {
		assertTableCount(t, ctx, pool, table, want)
	}
	var image, sourceRef string
	if err := pool.QueryRow(ctx, `
		SELECT d.image_ref, o.payload->'request'->>'source_ref'
		  FROM deployments d
		  JOIN deploy_intake_outbox o ON o.deployment_id=d.id
		 WHERE d.id=$1`, response.DeploymentID).Scan(&image, &sourceRef); err != nil {
		t.Fatal(err)
	}
	if image != officialPostgresImage || sourceRef != "template://postgres-16" {
		t.Fatalf("runtime intent image=%q source_ref=%q", image, sourceRef)
	}
	var mountPath, healthKind string
	var targetPort, healthPort int
	if err := pool.QueryRow(ctx, `
		SELECT v.mount_path,c.target_port,c.health_kind,c.health_port
		  FROM volumes v
		  JOIN service_environment_configs c
		    ON c.service_id=v.service_id AND c.environment_id=v.environment_id
		 WHERE v.id=$1`, response.CreatedVolumes[0].ID,
	).Scan(&mountPath, &targetPort, &healthKind, &healthPort); err != nil {
		t.Fatal(err)
	}
	if mountPath != officialPostgresMountPath || targetPort != 5432 ||
		healthKind != "tcp" || healthPort != 5432 {
		t.Fatalf("volume/config = %q %d %q %d", mountPath, targetPort, healthKind, healthPort)
	}

	envelopes := secrets.NewPostgresEnvelopeStore(pool)
	serviceSlug := response.CreatedServices[0].Slug
	if serviceSlug == "Postgres" || !strings.HasPrefix(serviceSlug, "orders-database-") {
		t.Fatalf("created service slug=%q, want generated exact lookup name", serviceSlug)
	}
	resolvedServiceID, found, err := envelopes.LookupServiceID(
		ctx, string(project.ID), response.EnvironmentID, serviceSlug,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || resolvedServiceID != response.CreatedServices[0].ID {
		t.Fatalf("lookup by returned slug=%q found=%v service_id=%q, want %q",
			serviceSlug, found, resolvedServiceID, response.CreatedServices[0].ID)
	}
	if _, found, err := envelopes.LookupServiceID(
		ctx, string(project.ID), response.EnvironmentID, "Postgres",
	); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("legacy ${{Postgres.DATABASE_URL}} name unexpectedly resolved")
	}
	vault := secrets.NewVaultWithStore(keyProvider, secrets.AllowAll{}, envelopes)
	connectionString, err := varrefs.NewResolver(
		secrets.NewRuntimeLookup(vault, envelopes),
	).Render(
		ctx, string(project.ID), response.EnvironmentID, "svc_consumer", "DATABASE_URL",
		"${{"+serviceSlug+".DATABASE_URL}}",
	)
	if err != nil {
		t.Fatalf("resolve DATABASE_URL through returned service slug: %v", err)
	}
	if !strings.HasPrefix(connectionString, "postgres://postgres:") ||
		!strings.Contains(connectionString, "@"+response.CreatedServices[0].ID+".midnight.internal:5432/midnight") {
		t.Fatalf("resolved DATABASE_URL has unexpected shape")
	}

	password, err := envelopes.Find(ctx, string(project.ID), response.EnvironmentID, response.CreatedServices[0].ID, "POSTGRES_PASSWORD")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := secrets.OpenForVariable(ctx, keyProvider, password.Meta.ID, password.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(plaintext) != 32 {
		t.Fatalf("password length=%d, want 32", len(plaintext))
	}
	zeroTemplateBytes(plaintext)

	status := doRouterRequest(handler, http.MethodGet, response.StatusURL, "", nil)
	if status.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}
	var run templatepkg.ProvisionRun
	if err := json.Unmarshal(status.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.ID != response.ProvisionRunID || run.State != "queued" ||
		run.ConnectivityVerified || run.PersistenceVerified {
		t.Fatalf("queued run=%#v", run)
	}

	replay := doRouterRequest(handler, http.MethodPost, "/api/v1alpha/templates/postgres/instances", body, map[string]string{
		"Idempotency-Key": "template-postgres-proof",
	})
	if replay.Code != http.StatusAccepted || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
	var replayed TemplateDeployResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.ProvisionRunID != response.ProvisionRunID || replayed.OperationID != response.OperationID {
		t.Fatalf("replayed=%#v first=%#v", replayed, response)
	}
	assertTableCount(t, ctx, pool, "template_provision_runs", 1)

	assertTableCount(t, ctx, pool, "template_provision_runs", 1)

	deployments := NewPostgresDeploymentStore(pool)
	if err := deployments.MarkDeploymentDeploying(ctx, response.DeploymentID, deploypkg.DeploymentQueued); err != nil {
		t.Fatal(err)
	}
	runs := templatepkg.NewPostgresRunStore(pool)
	if err := runs.MarkRunning(ctx, response.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if err := deployments.PromoteTemplateDeployment(ctx, response.DeploymentID, ""); err == nil {
		t.Fatal("PromoteTemplateDeployment() without runtime evidence succeeded")
	}
	var deploymentState, runState string
	var active bool
	if err := pool.QueryRow(ctx, `SELECT state,active FROM deployments WHERE id=$1`, response.DeploymentID).
		Scan(&deploymentState, &active); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM template_provision_runs WHERE deployment_id=$1`, response.DeploymentID).
		Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if deploymentState != string(deploypkg.DeploymentDeploying) || active || runState != "running" {
		t.Fatalf("failed atomic promotion left deployment=%q active=%v run=%q", deploymentState, active, runState)
	}
	if err := runs.MarkVerified(ctx, response.DeploymentID, templatepkg.VerificationEvidence{
		WriteProbeContainerID: "write-proof",
		ReadProbeContainerID:  "read-proof",
		MarkerSHA256:          strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := runs.MarkCleanupCompleted(ctx, response.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if err := deployments.PromoteTemplateDeployment(ctx, response.DeploymentID, ""); err != nil {
		t.Fatal(err)
	}
	var cleanup bool
	if err := pool.QueryRow(ctx, `SELECT state,cleanup_completed FROM template_provision_runs WHERE deployment_id=$1`, response.DeploymentID).
		Scan(&runState, &cleanup); err != nil {
		t.Fatal(err)
	}
	if runState != "succeeded" || !cleanup {
		t.Fatalf("atomic template promotion left run=%q cleanup=%v", runState, cleanup)
	}
	proved, err := deployments.TemplateDeploymentPromotionCommitted(
		ctx,
		response.OperationID,
		response.DeploymentID,
		"",
	)
	if err != nil || !proved {
		t.Fatalf(
			"committed template promotion proof=%v err=%v, want true",
			proved,
			err,
		)
	}
	proved, err = deployments.DeploymentPromotionCommitted(
		ctx,
		response.OperationID,
		response.DeploymentID,
		"",
	)
	if err != nil || !proved {
		t.Fatalf(
			"committed deployment proof=%v err=%v, want true",
			proved,
			err,
		)
	}
	proved, err = deployments.DeploymentPromotionCommitted(
		ctx,
		"op_wrong_scope",
		response.DeploymentID,
		"",
	)
	if err != nil || proved {
		t.Fatalf(
			"foreign operation promotion proof=%v err=%v, want false",
			proved,
			err,
		)
	}
}

func TestOfficialRedisMySQLAndMongoDBTemplateIntakeUsesVerifiedSaga(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	keyProvider := testVariableKeyProvider(t)
	stack, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool: pool, MigrationsFS: migrations.FS, ManifestPath: "manifest.yaml",
		CursorSecret:          []byte("0123456789abcdef0123456789abcdef"),
		AutomationTokenMACKey: []byte("template-provision-automation-key"),
		InvitationMACKey:      []byte("template-provision-invitation-key"),
		VariableIntentMACKey:  []byte("template-provision-variable-intent-key"),
		VariableKeyProvider:   keyProvider,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Template saga behavior is the subject of this test; authentication is
	// covered separately and production stacks otherwise fail closed.
	stack.ProductionAuthRequired = false
	resources := stack.ProjectStore.(*PostgresResourceStore)
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "database-template-proof", DisplayName: "Database template proof"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRouterWithDataStack(stack)
	envelopes := secrets.NewPostgresEnvelopeStore(pool)

	cases := []struct {
		slug, image, sourceRef, mountPath, startCommand string
		port, variableCount                             int
		requiredVariables                               []string
		passwordVariable, connectionVariable            string
		connectionPrefix                                string
	}{
		{
			slug: officialRedisSlug, image: officialRedisImage,
			sourceRef: "template://" + officialRedisTemplateID,
			mountPath: "/data", port: 6379,
			startCommand:      `case "$REDIS_PASSWORD" in ""|*[!A-Za-z0-9]*) echo "REDIS_PASSWORD must be non-empty alphanumeric" >&2; exit 64;; esac; mkdir -p /run/midnight; chown root:redis /run/midnight; chmod 0710 /run/midnight; rm -f /run/midnight/redis.conf; (umask 027; set -C; printf 'appendonly yes\nrequirepass %s\n' "$REDIS_PASSWORD" > /run/midnight/redis.conf) || exit 1; chown root:redis /run/midnight/redis.conf; chmod 0640 /run/midnight/redis.conf; exec /usr/local/bin/docker-entrypoint.sh redis-server /run/midnight/redis.conf`,
			variableCount:     4,
			requiredVariables: []string{"REDIS_HOST", "REDIS_PASSWORD", "REDIS_PORT", "REDIS_URL"},
			passwordVariable:  "REDIS_PASSWORD", connectionVariable: "REDIS_URL",
			connectionPrefix: "redis://:",
		},
		{
			slug: officialMySQLSlug, image: officialMySQLImage,
			sourceRef: "template://" + officialMySQLTemplateID,
			mountPath: "/var/lib/mysql", port: 3306,
			variableCount: 8,
			requiredVariables: []string{
				"DATABASE_URL", "MYSQL_DATABASE", "MYSQL_HOST", "MYSQL_PASSWORD",
				"MYSQL_PORT", "MYSQL_ROOT_PASSWORD", "MYSQL_URL", "MYSQL_USER",
			},
			passwordVariable: "MYSQL_PASSWORD", connectionVariable: "MYSQL_URL",
			connectionPrefix: "mysql://midnight:",
		},
		{
			slug: officialMongoDBSlug, image: officialMongoDBImage,
			sourceRef: "template://" + officialMongoDBTemplateID,
			mountPath: "/data/db", port: 27017,
			variableCount: 7,
			requiredVariables: []string{
				"MONGODB_URL", "MONGO_HOST", "MONGO_INITDB_DATABASE",
				"MONGO_INITDB_ROOT_PASSWORD", "MONGO_INITDB_ROOT_USERNAME",
				"MONGO_PORT", "MONGO_URL",
			},
			passwordVariable: "MONGO_INITDB_ROOT_PASSWORD", connectionVariable: "MONGO_URL",
			connectionPrefix: "mongodb://midnight:",
		},
	}

	for _, tc := range cases {
		t.Run(tc.slug, func(t *testing.T) {
			body := `{"project_id":"` + string(project.ID) + `","name":"` + tc.slug + ` proof"}`
			key := "template-" + tc.slug + "-proof"
			first := doRouterRequest(handler, http.MethodPost, "/api/v1alpha/templates/"+tc.slug+"/instances", body, map[string]string{
				"Idempotency-Key": key,
			})
			if first.Code != http.StatusAccepted {
				t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
			}
			if strings.Contains(strings.ToLower(first.Body.String()), "password") {
				t.Fatalf("response contains credential metadata: %s", first.Body.String())
			}
			var response TemplateDeployResponse
			if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.TemplateSlug != tc.slug || response.ProvisionRunID == "" ||
				response.DeploymentID == "" || len(response.CreatedServices) != 1 ||
				len(response.CreatedVolumes) != 1 || response.State != "queued" {
				t.Fatalf("response=%#v", response)
			}

			var image, sourceRef, mountPath, startCommand, runSlug string
			var targetPort, healthPort int
			if err := pool.QueryRow(ctx, `
				SELECT d.image_ref,o.payload->'request'->>'source_ref',v.mount_path,
				       c.start_command,c.target_port,c.health_port,r.template_slug
				  FROM deployments d
				  JOIN deploy_intake_outbox o ON o.deployment_id=d.id
				  JOIN template_provision_runs r ON r.deployment_id=d.id
				  JOIN volumes v ON v.id=r.volume_id
				  JOIN service_environment_configs c
				    ON c.service_id=r.service_id AND c.environment_id=r.environment_id
				 WHERE d.id=$1`, response.DeploymentID,
			).Scan(&image, &sourceRef, &mountPath, &startCommand, &targetPort, &healthPort, &runSlug); err != nil {
				t.Fatal(err)
			}
			if image != tc.image || sourceRef != tc.sourceRef || mountPath != tc.mountPath ||
				startCommand != tc.startCommand || targetPort != tc.port || healthPort != tc.port ||
				runSlug != tc.slug {
				t.Fatalf("runtime intent image=%q source=%q mount=%q start=%q ports=%d/%d run=%q",
					image, sourceRef, mountPath, startCommand, targetPort, healthPort, runSlug)
			}

			rows, err := pool.Query(ctx, `
				SELECT name FROM service_variables WHERE service_id=$1 ORDER BY name`,
				response.CreatedServices[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			var variableNames []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				variableNames = append(variableNames, name)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if len(variableNames) != tc.variableCount || !slices.Equal(variableNames, tc.requiredVariables) {
				t.Fatalf("variables=%v, want %v", variableNames, tc.requiredVariables)
			}

			password, err := envelopes.Find(ctx, string(project.ID), response.EnvironmentID, response.CreatedServices[0].ID, tc.passwordVariable)
			if err != nil {
				t.Fatal(err)
			}
			plaintext, err := secrets.OpenForVariable(ctx, keyProvider, password.Meta.ID, password.Envelope)
			if err != nil {
				t.Fatal(err)
			}
			if len(plaintext) != 32 {
				t.Fatalf("%s length=%d, want 32", tc.passwordVariable, len(plaintext))
			}
			zeroTemplateBytes(plaintext)

			connection, err := envelopes.Find(ctx, string(project.ID), response.EnvironmentID, response.CreatedServices[0].ID, tc.connectionVariable)
			if err != nil {
				t.Fatal(err)
			}
			plaintext, err = secrets.OpenForVariable(ctx, keyProvider, connection.Meta.ID, connection.Envelope)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(plaintext), tc.connectionPrefix) ||
				!strings.Contains(string(plaintext), response.CreatedServices[0].ID+".midnight.internal") {
				t.Fatalf("%s has unexpected shape", tc.connectionVariable)
			}
			connectionURL, err := url.Parse(string(plaintext))
			if err != nil ||
				connectionURL.Hostname() != response.CreatedServices[0].ID+".midnight.internal" ||
				connectionURL.Port() != strconv.Itoa(tc.port) {
				t.Fatalf("%s is not a standard engine URL", tc.connectionVariable)
			}
			zeroTemplateBytes(plaintext)

			status := doRouterRequest(handler, http.MethodGet, response.StatusURL, "", nil)
			if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"template_slug":"`+tc.slug+`"`) {
				t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
			}
			replay := doRouterRequest(handler, http.MethodPost, "/api/v1alpha/templates/"+tc.slug+"/instances", body, map[string]string{
				"Idempotency-Key": key,
			})
			if replay.Code != http.StatusAccepted || replay.Header().Get("Idempotent-Replayed") != "true" {
				t.Fatalf("replay status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
			}
		})
	}

	for table, want := range map[string]int{
		"services": 3, "service_environment_configs": 3, "volumes": 3,
		"service_variables": 19, "service_variable_versions": 19,
		"operations": 3, "deployments": 3, "deploy_intake_outbox": 3,
		"template_provision_runs": 3,
	} {
		assertTableCount(t, ctx, pool, table, want)
	}
}
