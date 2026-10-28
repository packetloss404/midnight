package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/agent"
	agentauth "github.com/midnight/midnight/internal/agent/auth"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// TenantAgentSourceConstraints are server-transport constraints additional to
// normal source upload authority. They are bound into the canonical upload
// fingerprint and checked under the same accepting transaction's row locks.
type TenantAgentSourceConstraints struct {
	ConversationID     string `json:"conversation_id"`
	ServiceVersion     int64  `json:"service_version"`
	EnvironmentVersion int64  `json:"environment_version"`
	ConfigVersion      int64  `json:"config_version"`
}

type tenantAgentSourceBuilds struct {
	intake    TenantUpIntakeCoordinator
	artifacts TenantUpArtifactStore
	gate      agentauth.MutationGate
	actor     string
}

// NewTenantAgentSourceBuilds uses the already-wired canonical human source
// upload coordinator. Memory stacks remain unavailable; no HTTP bearer or
// provider-automation authority is manufactured by this adapter.
func NewTenantAgentSourceBuilds(stack DataStack, actor string) agent.BuildIntake {
	return &tenantAgentSourceBuilds{intake: stack.TenantUpIntake, artifacts: stack.SourceArtifactStore, gate: stack.AgentMutationGate, actor: actor}
}
func (a *tenantAgentSourceBuilds) DeployFromSource(context.Context, string, string, string) (agent.Deployment, error) {
	return agent.Deployment{}, ErrAgentSourceDeployUnavailable
}

func (a *tenantAgentSourceBuilds) SourceDeploymentContext(ctx context.Context, serviceID, environmentID string) (agent.SourceDeploymentContext, error) {
	principal, session, err := humanIdentityFromContext(ctx)
	if err != nil {
		return agent.SourceDeploymentContext{}, err
	}
	reader, ok := a.intake.(interface {
		ReadAgentSourceDeploymentContext(context.Context, identity.Principal, identity.Session, string, string, string) (agent.SourceDeploymentContext, error)
	})
	if !ok || agent.SourceConversationID(ctx) == "" {
		return agent.SourceDeploymentContext{}, ErrAgentMutationUnavailable
	}
	result, err := reader.ReadAgentSourceDeploymentContext(ctx, principal, session, serviceID, environmentID, agent.SourceConversationID(ctx))
	if err != nil {
		return result, sourceToolError(err)
	}
	return result, nil
}

// ReadAgentSourceDeploymentContext obtains all three optimistic versions in
// one transaction under the same current-session, membership and chat checks
// used at acceptance. It exposes no filesystem or secret values.
func (c *PostgresTenantDeployIntakeCoordinator) ReadAgentSourceDeploymentContext(ctx context.Context, principal identity.Principal, session identity.Session, serviceID, environmentID, conversationID string) (agent.SourceDeploymentContext, error) {
	result := agent.SourceDeploymentContext{ServiceID: serviceID, EnvironmentID: environmentID}
	if c == nil || c.Pool == nil {
		return result, ErrAgentMutationUnavailable
	}
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			_, authorization, err := authorizeTenantDeployCapabilityInTx(ctx, tx, principal, session, serviceID, environmentID, c.now(), identity.CapabilityServiceRead)
			if err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT s.resource_version,e.resource_version,COALESCE(cfg.version,0) FROM services s JOIN environments e ON e.project_id=s.project_id AND e.id=$2 LEFT JOIN service_environment_configs cfg ON cfg.project_id=s.project_id AND cfg.service_id=s.id AND cfg.environment_id=e.id WHERE s.id=$1`, serviceID, environmentID).Scan(&result.ExpectedServiceVersion, &result.ExpectedEnvironmentVersion, &result.ExpectedConfigVersion); err != nil {
				return err
			}
			return validateTenantAgentSourceInTx(ctx, tx, principal, authorization, environmentID, &TenantAgentSourceConstraints{ConversationID: conversationID, ServiceVersion: result.ExpectedServiceVersion, EnvironmentVersion: result.ExpectedEnvironmentVersion, ConfigVersion: result.ExpectedConfigVersion})
		})
		if err == nil {
			return result, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return result, err
		}
	}
	return result, ErrAgentMutationUnavailable
}

func (a *tenantAgentSourceBuilds) DeployScopedSource(ctx context.Context, request agent.SourceDeployRequest) (agent.Deployment, error) {
	if err := ctx.Err(); err != nil {
		return agent.Deployment{}, err
	}
	if err := agent.ValidateSourceDeployRequest(request); err != nil {
		return agent.Deployment{}, err
	}
	if err := checkOperatorGate(ctx, a.gate, a.actor, "deploy_from_source"); err != nil {
		return agent.Deployment{}, err
	}
	principal, session, err := humanIdentityFromContext(ctx)
	if err != nil {
		return agent.Deployment{}, err
	}
	conversationID := agent.SourceConversationID(ctx)
	if conversationID == "" || len(conversationID) > 160 {
		return agent.Deployment{}, errors.New("source deployment requires an owned scoped conversation")
	}
	if a.intake == nil || a.artifacts == nil {
		return agent.Deployment{}, ErrAgentMutationUnavailable
	}
	archive, err := agentSourceArchive(request)
	if err != nil {
		return agent.Deployment{}, err
	}
	defer clear(archive)
	sum := sha256.Sum256(archive)
	constraints := &TenantAgentSourceConstraints{ConversationID: conversationID, ServiceVersion: request.ExpectedServiceVersion, EnvironmentVersion: request.ExpectedEnvironmentVersion, ConfigVersion: request.ExpectedConfigVersion}
	key := agentIdempotencyKey(string(idempotency.UserActor(principal.UserID)), "source", conversationID, request.RequestKey)
	preflight := TenantSourceUpPreflightRequest{Principal: principal, Session: session, ServiceID: orailtypes.ID(request.ServiceID), EnvironmentID: request.EnvironmentID, Key: key, ExpectedDigest: "sha256:" + hex.EncodeToString(sum[:]), CompressedBytes: int64(len(archive)), AgentSource: constraints}
	admission, err := a.intake.PreflightSourceUp(ctx, preflight)
	if err != nil {
		return agent.Deployment{}, sourceToolError(err)
	}
	finished := admission.Replay
	physicalMayRemain := false
	defer func() {
		if !finished {
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = a.intake.FinishSourceUpAdmission(cleanup, admission, physicalMayRemain)
		}
	}()
	staged, err := a.artifacts.Stage(ctx, bytes.NewReader(archive), sourceartifact.Limits{MaxCompressedBytes: 1 << 20, MaxUncompressedBytes: 1 << 20, MaxEntries: agent.MaxSourceFiles})
	if err != nil {
		return agent.Deployment{}, sourceToolError(err)
	}
	owned := true
	defer func() {
		if owned {
			_ = a.artifacts.Abort(staged)
		}
	}()
	if staged.Digest != preflight.ExpectedDigest || staged.CompressedBytes != preflight.CompressedBytes {
		return agent.Deployment{}, sourceToolError(sourceartifact.ErrIntegrity)
	}
	if admission.Replay {
		if err := a.artifacts.Abort(staged); err != nil {
			return agent.Deployment{}, sourceToolError(err)
		}
		owned = false
		response, err := a.intake.FinalizeSourceUpReplay(ctx, preflight)
		if err != nil {
			return agent.Deployment{}, sourceToolError(err)
		}
		return sourceToolDeployment(response, request.ServiceID)
	}
	physicalMayRemain = true
	artifact, err := a.intake.PublishSourceUp(ctx, TenantSourceUpPublishRequest{Preflight: preflight, Admission: admission, Staged: staged})
	if err != nil {
		return agent.Deployment{}, sourceToolError(err)
	}
	owned = false
	fingerprint, err := tenantUpFingerprintDigest(preflight.ServiceID, request.EnvironmentID, "", artifact, constraints)
	if err != nil {
		return agent.Deployment{}, sourceToolError(err)
	}
	result, err := a.intake.DeploySourceArtifact(ctx, TenantSourceUpRequest{Principal: principal, Session: session, ServiceID: preflight.ServiceID, EnvironmentID: request.EnvironmentID, Artifact: artifact, Admission: admission, Key: key, Fingerprint: idempotency.RequestFingerprint{Method: http.MethodPost, Path: tenantUpIdempotencyPath(preflight.ServiceID), BodySHA256: fingerprint}, RequestID: "agent-source-" + request.RequestKey, UserAgent: "midnight-agent/source", AgentSource: constraints})
	if err != nil {
		return agent.Deployment{}, sourceToolError(err)
	}
	finished = true
	return sourceToolDeployment(result.Response, request.ServiceID)
}

func agentSourceArchive(request agent.SourceDeployRequest) ([]byte, error) {
	if err := agent.ValidateSourceDeployRequest(request); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	compressed.Header.ModTime = time.Unix(0, 0)
	compressed.Header.OS = 255
	archive := tar.NewWriter(compressed)
	paths := make([]string, 0, len(request.Files))
	for path := range request.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		content := request.Files[path]
		if err := archive.WriteHeader(&tar.Header{Name: path, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}); err != nil {
			return nil, errors.New("source file archive could not be encoded")
		}
		if _, err := archive.Write([]byte(content)); err != nil {
			return nil, errors.New("source file archive could not be encoded")
		}
	}
	archiveErr := archive.Close()
	compressedErr := compressed.Close()
	if archiveErr != nil || compressedErr != nil {
		return nil, errors.New("source file archive could not be finalized")
	}
	if output.Len() > 1<<20 {
		return nil, errors.New("source file archive exceeds 1 MiB")
	}
	return output.Bytes(), nil
}
func sourceToolDeployment(response idempotency.StoredResponse, serviceID string) (agent.Deployment, error) {
	if response.StatusCode != http.StatusAccepted {
		return agent.Deployment{}, errors.New("source deployment was not accepted")
	}
	var accepted upOperationResponse
	if json.Unmarshal(response.Body, &accepted) != nil || accepted.DeploymentID == "" || accepted.OperationID == "" || string(accepted.Operation.ID) != accepted.OperationID {
		return agent.Deployment{}, errors.New("source deployment response has no canonical operation/deployment")
	}
	return agent.Deployment{ID: accepted.DeploymentID, ServiceID: serviceID, Status: string(accepted.Operation.State)}, nil
}

type sourceToolFailure struct {
	cause   error
	message string
}

func (e sourceToolFailure) Error() string { return e.message }
func (e sourceToolFailure) Unwrap() error { return e.cause }
func sourceToolError(err error) error {
	message := "source deployment could not be accepted; retry the same request key after resolving the failure"
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		message = "source deployment requires a current human session"
	case errors.Is(err, identity.ErrAccessNotFound), errors.Is(err, identity.ErrAuthorizationDenied):
		message = "source deployment is not permitted for this conversation and target"
	case errors.Is(err, ErrResourcePreconditionFailed):
		message = "source target versions changed; reload service, environment, and config versions"
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody), errors.Is(err, idempotency.ErrKeyReused):
		message = "request_key already identifies a different source deployment"
	case errors.Is(err, sourceartifact.ErrInvalidArchive), errors.Is(err, sourceartifact.ErrLimitExceeded):
		message = "source archive was refused by the canonical upload policy"
	}
	return sourceToolFailure{err, message}
}

func validateTenantAgentSourceInTx(ctx context.Context, tx *pg.Tx, principal identity.Principal, authorization tenantDeployAuthorization, environmentID string, constraints *TenantAgentSourceConstraints) error {
	if constraints == nil {
		return nil
	}
	if constraints.ConversationID == "" || constraints.ServiceVersion < 1 || constraints.EnvironmentVersion < 1 || constraints.ConfigVersion < 0 {
		return fmt.Errorf("%w: source conversation/version constraints are invalid", ErrResourceInvalid)
	}
	var actor, project, service string
	var metadata []byte
	err := tx.QueryRow(ctx, `SELECT actor_id,COALESCE(project_id,''),COALESCE(service_id,''),metadata::text FROM agent_conversations WHERE id=$1 FOR SHARE`, constraints.ConversationID).Scan(&actor, &project, &service, &metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrAccessNotFound
	}
	if err != nil {
		return err
	}
	var tags map[string]any
	if json.Unmarshal(metadata, &tags) != nil || actor != string(idempotency.UserActor(principal.UserID)) || project != authorization.ProjectID || (service != "" && service != string(authorization.Service.ID)) || tags["midnight.kind"] != "chat" {
		return identity.ErrAuthorizationDenied
	}
	for key, want := range map[string]string{"midnight.workspace_id": authorization.WorkspaceID, "midnight.project_id": authorization.ProjectID, "midnight.service_id": string(authorization.Service.ID), "midnight.environment_id": strings.TrimSpace(environmentID)} {
		if value, ok := tags[key]; ok && value != "" && value != want {
			return identity.ErrAuthorizationDenied
		}
	}
	var serviceVersion, environmentVersion int64
	if err := tx.QueryRow(ctx, `SELECT s.resource_version,e.resource_version FROM services s JOIN environments e ON e.project_id=s.project_id AND e.id=$2 WHERE s.id=$1 AND s.deleted_at IS NULL AND e.deleted_at IS NULL`, string(authorization.Service.ID), strings.TrimSpace(environmentID)).Scan(&serviceVersion, &environmentVersion); err != nil {
		return err
	}
	configVersion := int64(0)
	err = tx.QueryRow(ctx, `SELECT version FROM service_environment_configs WHERE project_id=$1 AND service_id=$2 AND environment_id=$3 FOR SHARE`, authorization.ProjectID, string(authorization.Service.ID), strings.TrimSpace(environmentID)).Scan(&configVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if serviceVersion != constraints.ServiceVersion || environmentVersion != constraints.EnvironmentVersion || configVersion != constraints.ConfigVersion {
		return ErrResourcePreconditionFailed
	}
	return nil
}
