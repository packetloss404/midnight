// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/secrets"
	templatepkg "github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	officialPostgresSlug       = "postgres"
	officialPostgresTemplateID = "postgres-16"
	officialPostgresImage      = "postgres:16.14@sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20"
	officialPostgresDigest     = "sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20"
	officialPostgresMountPath  = "/var/lib/postgresql/data"
	officialPostgresVolumeSize = int64(1 << 30)

	officialRedisSlug       = "redis"
	officialRedisTemplateID = "redis-7-durable"
	officialRedisImage      = "redis:7.2.5@sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e"
	officialRedisDigest     = "sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e"

	officialMySQLSlug       = "mysql"
	officialMySQLTemplateID = "mysql-8"
	officialMySQLImage      = "mysql:8.4.0@sha256:dab7049abafe3a0e12cbe5e49050cf149881c0cd9665c289e5808b9dad39c9e0"
	officialMySQLDigest     = "sha256:dab7049abafe3a0e12cbe5e49050cf149881c0cd9665c289e5808b9dad39c9e0"

	officialMongoDBSlug       = "mongodb"
	officialMongoDBTemplateID = "mongodb-7-single-node"
	officialMongoDBImage      = "mongo:7.0.9@sha256:97aac78a80553735b3d9b9b7212803468781b4859645f892a3d04e6b621a7b77"
	officialMongoDBDigest     = "sha256:97aac78a80553735b3d9b9b7212803468781b4859645f892a3d04e6b621a7b77"
)

type officialTemplateRuntimeSpec struct {
	Slug         string
	TemplateID   string
	DisplayName  string
	Engine       string
	Version      string
	Image        string
	Digest       string
	MountPath    string
	VolumeSize   int64
	TargetPort   int
	StartCommand string
}

var officialTemplateRuntimeSpecs = map[string]officialTemplateRuntimeSpec{
	officialPostgresSlug: {
		Slug: officialPostgresSlug, TemplateID: officialPostgresTemplateID,
		DisplayName: "PostgreSQL", Engine: "postgres", Version: "16",
		Image: officialPostgresImage, Digest: officialPostgresDigest,
		MountPath: officialPostgresMountPath, VolumeSize: officialPostgresVolumeSize,
		TargetPort: 5432,
	},
	officialRedisSlug: {
		Slug: officialRedisSlug, TemplateID: officialRedisTemplateID,
		DisplayName: "Redis", Engine: "redis", Version: "7.2",
		Image: officialRedisImage, Digest: officialRedisDigest,
		MountPath: "/data", VolumeSize: 256 << 20, TargetPort: 6379,
		StartCommand: `case "$REDIS_PASSWORD" in ""|*[!A-Za-z0-9]*) echo "REDIS_PASSWORD must be non-empty alphanumeric" >&2; exit 64;; esac; mkdir -p /run/midnight; chown root:redis /run/midnight; chmod 0710 /run/midnight; rm -f /run/midnight/redis.conf; (umask 027; set -C; printf 'appendonly yes\nrequirepass %s\n' "$REDIS_PASSWORD" > /run/midnight/redis.conf) || exit 1; chown root:redis /run/midnight/redis.conf; chmod 0640 /run/midnight/redis.conf; exec /usr/local/bin/docker-entrypoint.sh redis-server /run/midnight/redis.conf`,
	},
	officialMySQLSlug: {
		Slug: officialMySQLSlug, TemplateID: officialMySQLTemplateID,
		DisplayName: "MySQL", Engine: "mysql", Version: "8.4",
		Image: officialMySQLImage, Digest: officialMySQLDigest,
		MountPath: "/var/lib/mysql", VolumeSize: 1 << 30, TargetPort: 3306,
	},
	officialMongoDBSlug: {
		Slug: officialMongoDBSlug, TemplateID: officialMongoDBTemplateID,
		DisplayName: "MongoDB", Engine: "mongodb", Version: "7",
		Image: officialMongoDBImage, Digest: officialMongoDBDigest,
		MountPath: "/data/db", VolumeSize: 1 << 30, TargetPort: 27017,
	},
}

func officialTemplateRuntimeSpecForSlug(slug string) (officialTemplateRuntimeSpec, bool) {
	spec, ok := officialTemplateRuntimeSpecs[strings.TrimSpace(slug)]
	return spec, ok
}

func officialRuntimeTemplateSlugs() []string {
	return []string{officialPostgresSlug, officialRedisSlug, officialMySQLSlug, officialMongoDBSlug}
}

type PostgresTemplateProvisioner struct {
	Pool        *pg.Pool
	KeyProvider secrets.KeyProvider
	Now         func() time.Time
	IDFactory   func(orailtypes.IDPrefix) string
}

func NewPostgresTemplateProvisioner(pool *pg.Pool, keyProvider secrets.KeyProvider) *PostgresTemplateProvisioner {
	return &PostgresTemplateProvisioner{
		Pool: pool, KeyProvider: keyProvider,
		Now:       func() time.Time { return time.Now().UTC() },
		IDFactory: func(prefix orailtypes.IDPrefix) string { return mintID(prefix) },
	}
}

func (s *PostgresTemplateProvisioner) ProvisionTemplate(ctx context.Context, request TemplateProvisionRequest) (TemplateProvisionResult, error) {
	if s == nil || s.Pool == nil || s.KeyProvider == nil {
		return TemplateProvisionResult{}, errors.New("template provisioner is not configured")
	}
	request.TemplateSlug = strings.TrimSpace(request.TemplateSlug)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.EnvironmentID = strings.TrimSpace(request.EnvironmentID)
	request.Name = strings.TrimSpace(request.Name)
	spec, ok := officialTemplateRuntimeSpecForSlug(request.TemplateSlug)
	if !ok {
		return TemplateProvisionResult{}, fmt.Errorf("%w: no verified runtime saga for %q", templatepkg.ErrTemplateNotFound, request.TemplateSlug)
	}
	if request.ProjectID == "" || request.IdempotencyKey == "" {
		return TemplateProvisionResult{}, errors.New("template provision requires project_id and Idempotency-Key")
	}
	if request.IdempotencyRef.Key != request.IdempotencyKey {
		return TemplateProvisionResult{}, errors.New("template provision idempotency reference does not match key")
	}
	if err := idempotency.ValidateReference(ctx, request.IdempotencyRef); err != nil {
		return TemplateProvisionResult{}, fmt.Errorf("template provision idempotency reference: %w", err)
	}

	now := s.now()
	runID := s.id(orailtypes.IDPrefixTemplate)
	serviceID := s.id(orailtypes.IDPrefixService)
	environmentID := request.EnvironmentID
	operationID := s.id(orailtypes.IDPrefixOperation)
	deploymentID := s.id(orailtypes.IDPrefixDeployment)
	instanceID := s.id(orailtypes.IDPrefixInstance)
	volumeID := s.id(orailtypes.IDPrefixVolume)
	outboxID := randomOutboxID("dox")
	triggerKey := "template:" + operationID
	serviceSlug := templateServiceSlug(request.Name, spec.Slug, serviceID)
	displayName := request.Name
	if displayName == "" {
		displayName = spec.DisplayName
	}
	host := serviceID + ".midnight.internal"
	variables, err := officialTemplateVariables(spec, host)
	if err != nil {
		return TemplateProvisionResult{}, fmt.Errorf("template variable generation: %w", err)
	}
	defer zeroTemplateVariables(variables)

	fingerprint, err := s.KeyProvider.Fingerprint(ctx)
	if err != nil {
		return TemplateProvisionResult{}, fmt.Errorf("template active key fingerprint: %w", err)
	}

	idempotencyStore := idempotency.NewPostgresStore(s.Pool)
	idempotencyStore.Now = s.Now
	var result TemplateProvisionResult
	err = s.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "template:"+request.IdempotencyRef.Generation); err != nil {
			return fmt.Errorf("template provision lock: %w", err)
		}
		var projectExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1 AND deleted_at IS NULL)`, request.ProjectID).Scan(&projectExists); err != nil {
			return fmt.Errorf("template project lookup: %w", err)
		}
		if !projectExists {
			return ErrResourceNotFound
		}
		var environmentCreated bool
		if environmentID == "" {
			err := tx.QueryRow(ctx, `
				SELECT id FROM environments
				 WHERE project_id=$1 AND deleted_at IS NULL
				 ORDER BY (tier='production') DESC, created_at ASC LIMIT 1
				 FOR SHARE`, request.ProjectID).Scan(&environmentID)
			if errors.Is(err, pgx.ErrNoRows) {
				environmentID = s.id(orailtypes.IDPrefixEnvironment)
				if _, err := tx.Exec(ctx, `
					INSERT INTO environments
					    (id,project_id,slug,name,tier,is_protected,created_at,updated_at)
					VALUES ($1,$2,'production','Production','production',FALSE,$3,$3)`,
					environmentID, request.ProjectID, now); err != nil {
					return fmt.Errorf("template production environment create: %w", err)
				}
				environmentCreated = true
			} else if err != nil {
				return fmt.Errorf("template environment select: %w", err)
			}
		}
		if !environmentCreated {
			var valid bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM environments
					 WHERE id=$1 AND project_id=$2 AND deleted_at IS NULL
				)`, environmentID, request.ProjectID).Scan(&valid); err != nil {
				return fmt.Errorf("template environment scope: %w", err)
			}
			if !valid {
				return &MutationScopeError{
					Kind: MutationScopeMismatch, Resource: "environment",
					ID: environmentID, ProjectID: request.ProjectID,
					Err: ErrResourceInvalid,
				}
			}
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO services
			    (id,project_id,slug,display_name,kind,labels,spec,created_at,updated_at)
			VALUES ($1,$2,$3,$4,'database',$5::jsonb,$6::jsonb,$7,$7)`,
			serviceID, request.ProjectID, serviceSlug, displayName,
			fmt.Sprintf(`{"midnight.dev/template":%q,"midnight.dev/official":"true"}`, spec.TemplateID),
			fmt.Sprintf(`{"engine":%q,"engine_version":%q,"private_by_default":true}`, spec.Engine, spec.Version),
			now); err != nil {
			return fmt.Errorf("template service insert: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO service_environment_configs (
				project_id,service_id,environment_id,version,root_directory,builder,
				dockerfile_path,build_command,start_command,predeploy_command,target_port,
				health_kind,health_path,health_port,health_timeout_seconds,
				health_interval_seconds,restart_policy,restart_max_retries,replicas,
				overlap_enabled,drain_grace_seconds,created_at,updated_at
			) VALUES (
				$1,$2,$3,1,'','auto','Dockerfile','',$4,'',$5,
				'tcp','/',$5,300,1,'unless-stopped',0,1,FALSE,30,$6,$6
			)`, request.ProjectID, serviceID, environmentID, spec.StartCommand, spec.TargetPort, now); err != nil {
			return fmt.Errorf("template service config insert: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO volumes
			    (id,project_id,environment_id,service_id,name,size_bytes,mount_path,status,created_at,updated_at)
			VALUES ($1,$2,$3,$4,'data',$5,$6,'provisioning',$7,$7)`,
			volumeID, request.ProjectID, environmentID, serviceID,
			spec.VolumeSize, spec.MountPath, now); err != nil {
			return fmt.Errorf("template volume insert: %w", err)
		}

		for i := range variables {
			if err := s.insertTemplateVariable(ctx, tx, request.ProjectID, environmentID, serviceID, fingerprint, now, variables[i]); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO operations
			    (id,kind,state,idempotency_key,expires_at,created_at,updated_at)
			VALUES ($1,'template.deploy','QUEUED',$2,$3,$4,$4)`,
			operationID, string(request.IdempotencyKey), now.Add(24*time.Hour), now); err != nil {
			return fmt.Errorf("template operation insert: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO deployments (
				id,project_id,service_id,environment_id,action,source_type,
				image_ref,image_digest,state,active,operation_id,trigger_key,created_at,updated_at
			) VALUES (
				$1,$2,$3,$4,'deploy_image','image',$5,$6,'queued',FALSE,$7,$8,$9,$9
			)`, deploymentID, request.ProjectID, serviceID, environmentID,
			spec.Image, spec.Digest, operationID,
			triggerKey, now); err != nil {
			return fmt.Errorf("template deployment insert: %w", err)
		}
		acceptedMessage := "official " + spec.DisplayName + " template durably accepted"
		if _, err := tx.Exec(ctx, `
			INSERT INTO operation_events (id,operation_id,seq,state,message,created_at)
			VALUES ($1,$2,1,'QUEUED',$3,$4)`,
			operationID+"-1", operationID, acceptedMessage, now); err != nil {
			return fmt.Errorf("template operation event insert: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO deployment_events
			    (id,deployment_id,seq,event_type,state,message,data,created_at)
			VALUES ($1,$2,1,'state','queued',$3,'{}'::jsonb,$4)`,
			s.id(orailtypes.IDPrefixEvent), deploymentID, acceptedMessage, now); err != nil {
			return fmt.Errorf("template deployment event insert: %w", err)
		}

		payload, err := json.Marshal(deploy.DeployJobPayload{
			SchemaVersion: deploy.DeployJobPayloadSchema,
			OperationID:   operationID, ServiceID: serviceID,
			ProjectID: request.ProjectID, EnvironmentID: environmentID,
			Request: deploy.DeployJobRequest{
				EnvironmentID: environmentID,
				SourceRef:     "template://" + spec.TemplateID,
				Image:         spec.Image,
				Mode:          "template",
			},
			Candidate: deploy.DeployJobCandidate{
				DeploymentID: deploymentID, InstanceID: instanceID,
				ProjectID: request.ProjectID, EnvironmentID: environmentID,
				ServiceID: serviceID, Image: spec.Image,
				IdempotencyKey: operationID, Replicas: 1,
			},
			Route:      &deploy.DeployJobRoute{Name: serviceSlug, Public: false},
			EnqueuedAt: now,
		})
		if err != nil {
			return fmt.Errorf("template deploy payload: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO deploy_intake_outbox (
				id,trigger_key,operation_id,deployment_id,queue,partition_key,
				priority,available_at,payload,state,max_attempts,created_at,updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,0,$7,$8::jsonb,'pending',8,$7,$7)`,
			outboxID, triggerKey, operationID, deploymentID,
			string(queue.Deploy), request.ProjectID, now, string(payload)); err != nil {
			return fmt.Errorf("template deploy outbox insert: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO template_provision_runs (
				id,template_slug,project_id,environment_id,service_id,volume_id,
				operation_id,deployment_id,state,created_at,updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'queued',$9,$9)`,
			runID, spec.Slug, request.ProjectID, environmentID, serviceID, volumeID,
			operationID, deploymentID, now); err != nil {
			return fmt.Errorf("template provision run insert: %w", err)
		}

		run := templatepkg.ProvisionRun{
			ID: runID, TemplateSlug: spec.Slug,
			ProjectID: request.ProjectID, EnvironmentID: environmentID,
			ServiceID: serviceID, VolumeID: volumeID, OperationID: operationID,
			DeploymentID: deploymentID, State: "queued", CreatedAt: now, UpdatedAt: now,
		}
		response := TemplateDeployResponse{
			OperationID: operationID, ProvisionRunID: runID,
			TemplateSlug: spec.Slug, State: "queued",
			EnvironmentID: environmentID, DeploymentID: deploymentID,
			CreatedServices: []TemplateCreatedService{{ID: serviceID, Slug: serviceSlug}},
			CreatedVolumes:  []templatepkg.CreatedResource{{Kind: "volume", ID: volumeID, Name: "data"}},
			StatusURL:       v1alphaPrefix + "/templates/" + spec.Slug + "/instances/" + runID,
			Note:            templateVerificationNote(spec),
		}
		responseBody, err := json.Marshal(response)
		if err != nil {
			return fmt.Errorf("template response encode: %w", err)
		}
		responseBody = append(responseBody, '\n')
		location := response.StatusURL
		if _, err := idempotencyStore.AttachOperationInTx(ctx, tx, request.IdempotencyRef, operationID); err != nil {
			return fmt.Errorf("template idempotency operation attach: %w", err)
		}
		if _, err := idempotencyStore.CompleteSuccessInTx(ctx, tx, request.IdempotencyRef, idempotency.StoredResponse{
			StatusCode: http.StatusAccepted,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"Location":     []string{location},
			},
			Body: responseBody,
		}); err != nil {
			return fmt.Errorf("template idempotency completion: %w", err)
		}
		result = TemplateProvisionResult{
			Run:      run,
			Service:  TemplateCreatedService{ID: serviceID, Slug: serviceSlug},
			VolumeID: volumeID,
		}
		return nil
	})
	if err != nil {
		return TemplateProvisionResult{}, err
	}
	return result, nil
}

type templateVariable struct {
	name  string
	scope secrets.Scope
	value []byte
}

func (s *PostgresTemplateProvisioner) insertTemplateVariable(
	ctx context.Context,
	tx *pg.Tx,
	projectID, environmentID, serviceID, fingerprint string,
	now time.Time,
	variable templateVariable,
) error {
	id := secrets.VariableID(s.id(orailtypes.IDPrefixVariable))
	envelope, err := secrets.SealForVariable(ctx, s.KeyProvider, id, variable.value)
	if err != nil {
		return fmt.Errorf("template seal variable %s: %w", variable.name, err)
	}
	lengthBucket := secrets.LengthBucket(len(variable.value))
	if _, err := tx.Exec(ctx, `
		INSERT INTO service_variables (
			id,workspace_id,project_id,environment_id,service_id,name,scope,
			value_kind,value_ciphertext,kek_id,wrapped_dek,canonical_form,
			"references",length_bucket,kek_fingerprint,version,last_rotated_at,
			created_at,updated_at,deleted_at
		) VALUES (
			$1,'',$2,$3,$4,$5,$6,'literal',$7,$8,$9,$10,
			'[]'::jsonb,$11,$12,1,$13,$13,$13,NULL
		)`, id, projectID, environmentID, serviceID, variable.name, variable.scope,
		envelope.Ciphertext, envelope.KEKID, envelope.WrappedDEK, envelope.CanonicalForm,
		lengthBucket, fingerprint, now); err != nil {
		return fmt.Errorf("template variable insert %s: %w", variable.name, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO service_variable_versions (
			variable_id,version,workspace_id,project_id,environment_id,service_id,
			name,scope,value_kind,value_ciphertext,kek_id,wrapped_dek,canonical_form,
			"references",length_bucket,kek_fingerprint,last_rotated_at,created_at
		) VALUES (
			$1,1,'',$2,$3,$4,$5,$6,'literal',$7,$8,$9,$10,
			'[]'::jsonb,$11,$12,$13,$13
		)`, id, projectID, environmentID, serviceID, variable.name, variable.scope,
		envelope.Ciphertext, envelope.KEKID, envelope.WrappedDEK, envelope.CanonicalForm,
		lengthBucket, fingerprint, now); err != nil {
		return fmt.Errorf("template variable version insert %s: %w", variable.name, err)
	}
	return nil
}

func (s *PostgresTemplateProvisioner) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *PostgresTemplateProvisioner) id(prefix orailtypes.IDPrefix) string {
	if s != nil && s.IDFactory != nil {
		return s.IDFactory(prefix)
	}
	return mintID(prefix)
}

func templateServiceSlug(name, fallback, serviceID string) string {
	base := slugifyDashboardID(name)
	if base == "" {
		base = fallback
	}
	suffix := strings.TrimPrefix(serviceID, string(orailtypes.IDPrefixService)+"_")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return strings.Trim(base+"-"+suffix, "-")
}

func officialTemplateVariables(spec officialTemplateRuntimeSpec, host string) ([]templateVariable, error) {
	password, err := randomTemplatePassword(32)
	if err != nil {
		return nil, err
	}
	keepPassword := false
	defer func() {
		if !keepPassword {
			zeroTemplateBytes(password)
		}
	}()

	switch spec.Slug {
	case officialPostgresSlug:
		url := joinTemplateVariableBytes(
			[]byte("postgres://postgres:"), password, []byte("@"), []byte(host), []byte(":5432/midnight"),
		)
		keepPassword = true
		return []templateVariable{
			{name: "POSTGRES_PASSWORD", scope: secrets.ScopeSealed, value: password},
			{name: "POSTGRES_USER", scope: secrets.ScopePlain, value: []byte("postgres")},
			{name: "POSTGRES_DB", scope: secrets.ScopePlain, value: []byte("midnight")},
			{name: "PGUSER", scope: secrets.ScopePlain, value: []byte("postgres")},
			{name: "PGDATABASE", scope: secrets.ScopePlain, value: []byte("midnight")},
			{name: "PGPORT", scope: secrets.ScopePlain, value: []byte("5432")},
			{name: "PGHOST", scope: secrets.ScopePlain, value: []byte(host)},
			{name: "PGPASSWORD", scope: secrets.ScopeSealed, value: password},
			{name: "DATABASE_URL", scope: secrets.ScopeSealed, value: url},
		}, nil
	case officialRedisSlug:
		url := joinTemplateVariableBytes(
			[]byte("redis://:"), password, []byte("@"), []byte(host), []byte(":6379/0"),
		)
		keepPassword = true
		return []templateVariable{
			{name: "REDIS_PASSWORD", scope: secrets.ScopeSealed, value: password},
			{name: "REDIS_HOST", scope: secrets.ScopePlain, value: []byte(host)},
			{name: "REDIS_PORT", scope: secrets.ScopePlain, value: []byte("6379")},
			{name: "REDIS_URL", scope: secrets.ScopeSealed, value: url},
		}, nil
	case officialMySQLSlug:
		rootPassword, err := randomTemplatePassword(32)
		if err != nil {
			return nil, err
		}
		url := joinTemplateVariableBytes(
			[]byte("mysql://midnight:"), password, []byte("@"), []byte(host), []byte(":3306/midnight"),
		)
		keepPassword = true
		return []templateVariable{
			{name: "MYSQL_ROOT_PASSWORD", scope: secrets.ScopeSealed, value: rootPassword},
			{name: "MYSQL_PASSWORD", scope: secrets.ScopeSealed, value: password},
			{name: "MYSQL_DATABASE", scope: secrets.ScopePlain, value: []byte("midnight")},
			{name: "MYSQL_USER", scope: secrets.ScopePlain, value: []byte("midnight")},
			{name: "MYSQL_HOST", scope: secrets.ScopePlain, value: []byte(host)},
			{name: "MYSQL_PORT", scope: secrets.ScopePlain, value: []byte("3306")},
			{name: "MYSQL_URL", scope: secrets.ScopeSealed, value: url},
			{name: "DATABASE_URL", scope: secrets.ScopeSealed, value: append([]byte(nil), url...)},
		}, nil
	case officialMongoDBSlug:
		url := joinTemplateVariableBytes(
			[]byte("mongodb://midnight:"), password, []byte("@"), []byte(host),
			[]byte(":27017/midnight?authSource=admin"),
		)
		keepPassword = true
		return []templateVariable{
			{name: "MONGO_INITDB_ROOT_USERNAME", scope: secrets.ScopePlain, value: []byte("midnight")},
			{name: "MONGO_INITDB_ROOT_PASSWORD", scope: secrets.ScopeSealed, value: password},
			{name: "MONGO_INITDB_DATABASE", scope: secrets.ScopePlain, value: []byte("midnight")},
			{name: "MONGO_HOST", scope: secrets.ScopePlain, value: []byte(host)},
			{name: "MONGO_PORT", scope: secrets.ScopePlain, value: []byte("27017")},
			{name: "MONGO_URL", scope: secrets.ScopeSealed, value: url},
			{name: "MONGODB_URL", scope: secrets.ScopeSealed, value: append([]byte(nil), url...)},
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", templatepkg.ErrTemplateNotFound, spec.Slug)
	}
}

func joinTemplateVariableBytes(parts ...[]byte) []byte {
	size := 0
	for _, part := range parts {
		size += len(part)
	}
	out := make([]byte, 0, size)
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func zeroTemplateVariables(variables []templateVariable) {
	for i := range variables {
		zeroTemplateBytes(variables[i].value)
	}
}

func templateVerificationNote(spec officialTemplateRuntimeSpec) string {
	return "Success is reported only after " + spec.DisplayName +
		" accepts a private-network write and the same marker survives container replacement on its attached volume."
}

func randomTemplatePassword(length int) ([]byte, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	if length <= 0 {
		length = 32
	}
	random := make([]byte, length)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	out := make([]byte, length)
	for i, value := range random {
		out[i] = alphabet[int(value)%len(alphabet)]
	}
	zeroTemplateBytes(random)
	return out, nil
}

func zeroTemplateBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
