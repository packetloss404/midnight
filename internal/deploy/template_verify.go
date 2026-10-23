// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/deploy/readiness"
	"github.com/midnight/midnight/internal/runtime"
	templatepkg "github.com/midnight/midnight/internal/templates"
)

const (
	officialPostgresTemplateRef = "template://postgres-16"
	officialRedisTemplateRef    = "template://redis-7-durable"
	officialMySQLTemplateRef    = "template://mysql-8"
	officialMongoDBTemplateRef  = "template://mongodb-7-single-node"
	templateSmokeTimeout        = 2 * time.Minute
	templateRestartGrace        = 30 * time.Second
)

var (
	ErrTemplateVerificationUnavailable = errors.New("official template runtime verification is not configured")
	ErrTemplateUnsupported             = errors.New("official template runtime is not supported")
	ErrTemplateSmokeFailed             = errors.New("official template database smoke check failed")
)

// TemplateVerificationRequest contains the already-resolved, in-memory
// candidate and its private routing target. It is never persisted.
type TemplateVerificationRequest struct {
	TemplateRef string
	Candidate   runtime.ScheduleRequest
	Target      runtime.RoutingTarget
}

// TemplateVerifier is the database-specific gate between generic readiness
// and promotion.
type TemplateVerifier interface {
	Verify(context.Context, TemplateVerificationRequest) (templatepkg.VerificationEvidence, error)
	Cleanup(context.Context, templatepkg.VerificationEvidence) error
}

// TemplateVerificationRuntime is implemented by the Docker adapter. The
// verifier needs both isolated one-offs and an actual candidate replacement.
type TemplateVerificationRuntime interface {
	PredeployRunner
	RuntimeActionRuntime
	Schedule(context.Context, runtime.ScheduleRequest) (runtime.InstanceStatus, error)
	ResolveRoutingTarget(context.Context, string, string) (runtime.RoutingTarget, error)
	CleanupTemplateSmoke(context.Context, []string) error
}

type databaseTemplateVerificationSpec struct {
	Name          string
	ExpectedImage string
	MountPath     string
	Port          int
	Write         func(string) string
	Read          func(string) string
}

var databaseTemplateVerificationSpecs = map[string]databaseTemplateVerificationSpec{
	officialPostgresTemplateRef: {
		Name:          "PostgreSQL",
		ExpectedImage: "postgres:16.14@sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20",
		MountPath:     "/var/lib/postgresql/data", Port: 5432,
		Write: postgresWriteSmokeCommand, Read: postgresReadSmokeCommand,
	},
	officialRedisTemplateRef: {
		Name:          "Redis",
		ExpectedImage: "redis:7.2.5@sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e",
		MountPath:     "/data", Port: 6379,
		Write: redisWriteSmokeCommand, Read: redisReadSmokeCommand,
	},
	officialMySQLTemplateRef: {
		Name:          "MySQL",
		ExpectedImage: "mysql:8.4.0@sha256:dab7049abafe3a0e12cbe5e49050cf149881c0cd9665c289e5808b9dad39c9e0",
		MountPath:     "/var/lib/mysql", Port: 3306,
		Write: mysqlWriteSmokeCommand, Read: mysqlReadSmokeCommand,
	},
	officialMongoDBTemplateRef: {
		Name:          "MongoDB",
		ExpectedImage: "mongo:7.0.9@sha256:97aac78a80553735b3d9b9b7212803468781b4859645f892a3d04e6b621a7b77",
		MountPath:     "/data/db", Port: 27017,
		Write: mongoDBWriteSmokeCommand, Read: mongoDBReadSmokeCommand,
	},
}

func validateOfficialTemplateImage(templateRef, image string) error {
	spec, ok := databaseTemplateVerificationSpecs[strings.TrimSpace(templateRef)]
	if !ok {
		return fmt.Errorf("%w: %s", ErrTemplateUnsupported, strings.TrimSpace(templateRef))
	}
	if strings.TrimSpace(image) != spec.ExpectedImage {
		return fmt.Errorf("%w: %s candidate image does not match the exact official image", ErrTemplateSmokeFailed, spec.Name)
	}
	return nil
}

type OfficialDatabaseTemplateVerifier struct {
	Runtime   TemplateVerificationRuntime
	Timeout   time.Duration
	Now       func() time.Time
	Readiness ReadinessConfig
}

// PostgresTemplateVerifier is retained as a source-compatible alias for
// callers that wired the first verified template. Its behavior now covers the
// complete official single-node database set.
type PostgresTemplateVerifier = OfficialDatabaseTemplateVerifier

func (v *OfficialDatabaseTemplateVerifier) Verify(ctx context.Context, request TemplateVerificationRequest) (templatepkg.VerificationEvidence, error) {
	if v == nil || v.Runtime == nil {
		return templatepkg.VerificationEvidence{}, ErrTemplateVerificationUnavailable
	}
	spec, ok := databaseTemplateVerificationSpecs[strings.TrimSpace(request.TemplateRef)]
	if !ok {
		return templatepkg.VerificationEvidence{}, fmt.Errorf("%w: %s", ErrTemplateUnsupported, strings.TrimSpace(request.TemplateRef))
	}
	candidate := request.Candidate
	if candidate.DeploymentID == "" || candidate.InstanceID == "" ||
		candidate.ProjectID == "" || candidate.EnvironmentID == "" ||
		candidate.ServiceID == "" || candidate.Image == "" ||
		strings.TrimSpace(request.Target.ContainerIP) == "" {
		return templatepkg.VerificationEvidence{}, fmt.Errorf("%w: candidate identity and private target are required", ErrTemplateSmokeFailed)
	}
	if err := validateOfficialTemplateImage(request.TemplateRef, candidate.Image); err != nil {
		return templatepkg.VerificationEvidence{}, err
	}
	if !hasWritableTemplateDataMount(candidate.Mounts, spec.MountPath) {
		return templatepkg.VerificationEvidence{}, fmt.Errorf("%w: writable %s data volume is not attached", ErrTemplateSmokeFailed, spec.Name)
	}

	timeout := v.Timeout
	if timeout <= 0 {
		timeout = templateSmokeTimeout
	}
	marker := templateMarker(candidate.DeploymentID)
	writeCommand := spec.Write(marker)
	readCommand := spec.Read(marker)
	probeCandidate := candidate
	probeCandidate.Mounts = nil
	probeCandidate.RestartPolicy = "no"
	probeCandidate.RestartMaxRetries = 0
	probeCandidate.Env = withTemplateProbeEnvironment(candidate.Env, request.Target.ContainerIP, marker)

	write, err := v.runProbe(ctx, probeCandidate, "write", writeCommand, "", timeout)
	evidence := templatepkg.VerificationEvidence{WriteProbeContainerID: write.ContainerID}
	if err != nil {
		return evidence, err
	}
	if err := v.Runtime.Stop(ctx, candidate.DeploymentID, candidate.InstanceID, templateRestartGrace); err != nil {
		return evidence, fmt.Errorf("%w: candidate replacement stop failed: %v", ErrTemplateSmokeFailed, err)
	}
	if err := v.Runtime.Remove(ctx, candidate.DeploymentID, candidate.InstanceID); err != nil {
		return evidence, fmt.Errorf("%w: candidate replacement remove failed: %v", ErrTemplateSmokeFailed, err)
	}
	if _, err := v.Runtime.Schedule(ctx, candidate); err != nil {
		return evidence, fmt.Errorf("%w: candidate replacement schedule failed: %v", ErrTemplateSmokeFailed, err)
	}
	if err := v.Runtime.Start(ctx, candidate.DeploymentID, candidate.InstanceID); err != nil {
		return evidence, fmt.Errorf("%w: candidate replacement start failed: %v", ErrTemplateSmokeFailed, err)
	}
	if err := v.waitAfterRestart(ctx, candidate, request.Target, spec); err != nil {
		return evidence, err
	}
	restartedTarget, err := v.Runtime.ResolveRoutingTarget(ctx, candidate.DeploymentID, candidate.InstanceID)
	if err != nil {
		return evidence, fmt.Errorf("%w: resolve replacement private target failed: %v", ErrTemplateSmokeFailed, err)
	}
	if strings.TrimSpace(restartedTarget.ContainerID) == "" {
		return evidence, fmt.Errorf("%w: replacement container identity is missing", ErrTemplateSmokeFailed)
	}
	if prior := strings.TrimSpace(request.Target.ContainerID); prior != "" && prior == restartedTarget.ContainerID {
		return evidence, fmt.Errorf("%w: candidate replacement retained the prior container identity", ErrTemplateSmokeFailed)
	}
	probeCandidate.Env = withTemplateProbeEnvironment(candidate.Env, restartedTarget.ContainerIP, marker)
	read, err := v.runProbe(ctx, probeCandidate, "read", readCommand, restartedTarget.ContainerID, timeout)
	evidence.ReadProbeContainerID = read.ContainerID
	if err != nil {
		return evidence, err
	}
	sum := sha256.Sum256([]byte(marker))
	evidence.MarkerSHA256 = hex.EncodeToString(sum[:])
	return evidence, nil
}

func hasWritableTemplateDataMount(mounts []runtime.ScheduleMount, target string) bool {
	for _, mount := range mounts {
		if mount.Target == target && mount.IsVolume &&
			!mount.ReadOnly && strings.TrimSpace(mount.VolumeID) != "" &&
			strings.TrimSpace(mount.Source) != "" {
			return true
		}
	}
	return false
}

func (v *OfficialDatabaseTemplateVerifier) Cleanup(ctx context.Context, evidence templatepkg.VerificationEvidence) error {
	if v == nil || v.Runtime == nil {
		return ErrTemplateVerificationUnavailable
	}
	ids := make([]string, 0, 2)
	if strings.TrimSpace(evidence.WriteProbeContainerID) != "" {
		ids = append(ids, evidence.WriteProbeContainerID)
	}
	if strings.TrimSpace(evidence.ReadProbeContainerID) != "" {
		ids = append(ids, evidence.ReadProbeContainerID)
	}
	return v.Runtime.CleanupTemplateSmoke(ctx, ids)
}

func (v *OfficialDatabaseTemplateVerifier) runProbe(
	ctx context.Context,
	candidate runtime.ScheduleRequest,
	phase, command, replacementContainerID string,
	timeout time.Duration,
) (runtime.OneOffResult, error) {
	spec, err := templateProbeSpec(candidate, phase, command, replacementContainerID)
	if err != nil {
		return runtime.OneOffResult{}, err
	}
	result, err := v.Runtime.RunOneOff(ctx, runtime.OneOffRequest{
		ScheduleRequest: candidate,
		WorkloadKind:    "template-smoke",
		Command:         command,
		SpecSHA256:      spec,
		Timeout:         timeout,
		Deadline:        v.now().Add(timeout),
	})
	if err != nil {
		return result, fmt.Errorf("%w: %s probe runtime failed: %v", ErrTemplateSmokeFailed, phase, err)
	}
	if result.TimedOut {
		return result, fmt.Errorf("%w: %s probe timed out", ErrTemplateSmokeFailed, phase)
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("%w: %s probe exited with status %d", ErrTemplateSmokeFailed, phase, result.ExitCode)
	}
	return result, nil
}

func (v *OfficialDatabaseTemplateVerifier) waitAfterRestart(
	ctx context.Context,
	candidate runtime.ScheduleRequest,
	previous runtime.RoutingTarget,
	spec databaseTemplateVerificationSpec,
) error {
	cfg := v.Readiness
	if cfg.Timeout <= 0 {
		cfg.Timeout = templateSmokeTimeout
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	err := readiness.Wait(ctx, readiness.ProbeFunc(func(ctx context.Context) error {
		target, err := v.Runtime.ResolveRoutingTarget(ctx, candidate.DeploymentID, candidate.InstanceID)
		if err != nil {
			return err
		}
		host := strings.TrimSpace(target.ContainerIP)
		if host == "" {
			host = strings.TrimSpace(previous.ContainerIP)
		}
		port := target.InternalPort
		if port == 0 {
			port = previous.InternalPort
		}
		if port == 0 {
			port = spec.Port
		}
		return (readiness.TCPProbe{Address: net.JoinHostPort(host, fmt.Sprintf("%d", port))}).CheckReady(ctx)
	}), readiness.Options{
		Timeout: cfg.Timeout, Interval: cfg.Interval, Now: cfg.Now, Sleep: cfg.Sleep,
	})
	if err != nil {
		return fmt.Errorf("%w: replacement %s did not become ready: %v", ErrTemplateSmokeFailed, spec.Name, err)
	}
	return nil
}

func (v *OfficialDatabaseTemplateVerifier) now() time.Time {
	if v != nil && v.Now != nil {
		return v.Now().UTC()
	}
	return time.Now().UTC()
}

func templateMarker(deploymentID string) string {
	sum := sha256.Sum256([]byte("midnight-template-persistence:" + strings.TrimSpace(deploymentID)))
	return hex.EncodeToString(sum[:])
}

func postgresWriteSmokeCommand(marker string) string {
	return postgresAuthenticatedReadyCommand() +
		`export PGPASSWORD="$POSTGRES_PASSWORD"; ` +
		`psql --no-password --set=ON_ERROR_STOP=1 --host="$MIDNIGHT_TEMPLATE_HOST" --port=5432 --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" ` +
		`--command="CREATE TABLE IF NOT EXISTS midnight_template_smoke (marker text PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now()); ` +
		`INSERT INTO midnight_template_smoke(marker) VALUES ('` + marker + `') ON CONFLICT (marker) DO NOTHING; ` +
		`SELECT marker FROM midnight_template_smoke WHERE marker='` + marker + `';"`
}

func postgresReadSmokeCommand(marker string) string {
	return postgresAuthenticatedReadyCommand() +
		`export PGPASSWORD="$POSTGRES_PASSWORD"; ` +
		`test "$(psql --no-password --tuples-only --no-align --set=ON_ERROR_STOP=1 --host="$MIDNIGHT_TEMPLATE_HOST" --port=5432 --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" ` +
		`--command="SELECT marker FROM midnight_template_smoke WHERE marker='` + marker + `';")" = "` + marker + `"`
}

func postgresAuthenticatedReadyCommand() string {
	return `attempt=0; until PGPASSWORD="$POSTGRES_PASSWORD" psql --no-password --tuples-only --no-align ` +
		`--host="$MIDNIGHT_TEMPLATE_HOST" --port=5432 --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" ` +
		`--command="SELECT 1;" >/dev/null 2>&1; do ` +
		`attempt=$((attempt+1)); test "$attempt" -lt 90 || exit 1; sleep 1; done; `
}

func redisWriteSmokeCommand(_ string) string {
	return `REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli -h "$MIDNIGHT_TEMPLATE_HOST" -p 6379 ` +
		`SET midnight_template_smoke "$MIDNIGHT_TEMPLATE_MARKER" >/dev/null && ` +
		`REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli -h "$MIDNIGHT_TEMPLATE_HOST" -p 6379 SAVE >/dev/null`
}

func redisReadSmokeCommand(_ string) string {
	return `test "$(REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli -h "$MIDNIGHT_TEMPLATE_HOST" -p 6379 ` +
		`GET midnight_template_smoke)" = "$MIDNIGHT_TEMPLATE_MARKER"`
}

func mysqlWriteSmokeCommand(_ string) string {
	return mysqlAuthenticatedReadyCommand() +
		`MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql --protocol=TCP --host="$MIDNIGHT_TEMPLATE_HOST" --port=3306 --user=root ` +
		`"$MYSQL_DATABASE" ` +
		`--execute="CREATE TABLE IF NOT EXISTS midnight_template_smoke (` +
		`marker varchar(64) PRIMARY KEY); INSERT INTO midnight_template_smoke(marker) ` +
		`VALUES ('$MIDNIGHT_TEMPLATE_MARKER') ON DUPLICATE KEY UPDATE marker=VALUES(marker);"`
}

func mysqlReadSmokeCommand(_ string) string {
	return mysqlAuthenticatedReadyCommand() +
		`test "$(MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql --protocol=TCP --host="$MIDNIGHT_TEMPLATE_HOST" --port=3306 --user=root ` +
		`"$MYSQL_DATABASE" --batch --skip-column-names ` +
		`--execute="SELECT marker FROM midnight_template_smoke WHERE marker='$MIDNIGHT_TEMPLATE_MARKER';")" ` +
		`= "$MIDNIGHT_TEMPLATE_MARKER"`
}

func mysqlAuthenticatedReadyCommand() string {
	return `attempt=0; until MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql --protocol=TCP ` +
		`--host="$MIDNIGHT_TEMPLATE_HOST" --port=3306 --user=root ` +
		`--execute="SELECT 1;" >/dev/null 2>&1; do ` +
		`attempt=$((attempt+1)); test "$attempt" -lt 90 || exit 1; sleep 1; done; `
}

func mongoDBWriteSmokeCommand(_ string) string {
	return mongoDBAuthenticatedReadyCommand() +
		`mongosh --quiet --nodb --eval '` + mongoDBConnectionPrelude() +
		`const d=c.getSiblingDB("midnight"); ` +
		`d.midnight_template_smoke.updateOne({_id:"marker"},{$set:{value:process.env.MIDNIGHT_TEMPLATE_MARKER}},{upsert:true}); ` +
		`const x=d.midnight_template_smoke.findOne({_id:"marker"}); if (!x || x.value !== process.env.MIDNIGHT_TEMPLATE_MARKER) quit(2);'`
}

func mongoDBReadSmokeCommand(_ string) string {
	return mongoDBAuthenticatedReadyCommand() +
		`test "$(mongosh --quiet --nodb --eval '` + mongoDBConnectionPrelude() +
		`const x=c.getSiblingDB("midnight").midnight_template_smoke.findOne({_id:"marker"}); ` +
		`if (!x) quit(2); print(x.value);')" = "$MIDNIGHT_TEMPLATE_MARKER"`
}

func mongoDBAuthenticatedReadyCommand() string {
	return `attempt=0; until mongosh --quiet --nodb --eval '` + mongoDBURIStatement() +
		`try { const c=connect(uri); quit(c.runCommand({ping:1}).ok === 1 ? 0 : 2); } catch (e) { quit(2); }' >/dev/null 2>&1; do ` +
		`attempt=$((attempt+1)); test "$attempt" -lt 90 || exit 1; sleep 1; done; `
}

func mongoDBConnectionPrelude() string {
	return mongoDBURIStatement() + `const c=connect(uri); `
}

func mongoDBURIStatement() string {
	return `const uri="mongodb://"+encodeURIComponent(process.env.MONGO_INITDB_ROOT_USERNAME)+":"` +
		`+encodeURIComponent(process.env.MONGO_INITDB_ROOT_PASSWORD)+"@"+process.env.MIDNIGHT_TEMPLATE_HOST` +
		`+":27017/admin?authSource=admin"; `
}

func withTemplateProbeEnvironment(existing []string, host, marker string) []string {
	out := make([]string, 0, len(existing)+2)
	for _, item := range existing {
		key, _, _ := strings.Cut(item, "=")
		if key == "MIDNIGHT_TEMPLATE_HOST" || key == "MIDNIGHT_TEMPLATE_MARKER" {
			continue
		}
		out = append(out, item)
	}
	return append(out, "MIDNIGHT_TEMPLATE_HOST="+host, "MIDNIGHT_TEMPLATE_MARKER="+marker)
}

func templateProbeSpec(
	candidate runtime.ScheduleRequest,
	phase, command, replacementContainerID string,
) (string, error) {
	// Values are intentionally omitted: the immutable deployment variable
	// snapshot binds them, while this digest only needs to distinguish the
	// write/read phases and candidate identity for retained-container replay.
	keys := make([]string, 0, len(candidate.Env))
	for _, item := range candidate.Env {
		key, _, _ := strings.Cut(item, "=")
		keys = append(keys, key)
	}
	sortStrings(keys)
	payload, err := json.Marshal(struct {
		DeploymentID string   `json:"deployment_id"`
		InstanceID   string   `json:"instance_id"`
		Image        string   `json:"image"`
		Phase        string   `json:"phase"`
		Command      string   `json:"command"`
		Environment  []string `json:"environment_keys"`
		Replacement  string   `json:"replacement_container_id,omitempty"`
	}{
		DeploymentID: candidate.DeploymentID,
		InstanceID:   candidate.InstanceID,
		Image:        candidate.Image,
		Phase:        phase,
		Command:      command,
		Environment:  keys,
		Replacement:  replacementContainerID,
	})
	if err != nil {
		return "", fmt.Errorf("template probe specification: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
