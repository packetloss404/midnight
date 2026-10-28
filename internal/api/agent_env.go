// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/agent"
	agentauth "github.com/midnight/midnight/internal/agent/auth"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	logrecords "github.com/midnight/midnight/internal/logs"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// Typed refusals for agent-driven mutations. The registry hides mutating
// tools from the model when mutations are disabled; when they are
// enabled these errors keep a refusal explicit and legible rather than
// pretending the change happened. Every one of them is surfaced to the
// model as an is_error tool result, so the text is written for the model
// to relay to a person.
var (
	// ErrAgentMutationUnavailable is returned when the durable
	// coordinator behind a mutating tool is not installed on this
	// stack (notably: every write path in memory mode).
	ErrAgentMutationUnavailable = errors.New("this change is not available through the agent yet; use the console or CLI")

	// ErrAgentMutationNeedsHumanSession is returned when the caller
	// reached the agent with an operator recovery token or an MCP
	// credential rather than an authorized signed-in human session. The
	// durable tenant coordinators authorize against a live human
	// session tuple, so there is no identity for the agent to act as.
	ErrAgentMutationNeedsHumanSession = errors.New(
		"this change requires an authorized signed-in human session: the agent acts as the person talking to it, " +
			"and this conversation is authenticated with an operator token instead. Ask the user to sign in, " +
			"or make the change from the console or CLI.")

	// ErrAgentSourceDeployUnavailable refuses the legacy repo/ref interface
	// or a stack without the bounded human-chat source coordinator.
	ErrAgentSourceDeployUnavailable = errors.New(
		"source deployment requires a scoped human conversation, bounded inline files, explicit target versions, and a configured source coordinator; " +
			"the legacy repo/ref interface is unavailable. Push a linked GitHub commit, or use `midnight up` for CLI source deployment.")

	// ErrAgentServiceHasNoDeployment is returned when redeploy is asked
	// for a service that has never deployed anything worth replaying.
	ErrAgentServiceHasNoDeployment = errors.New("this service has no completed deployment to redeploy; deploy it once first")

	// ErrAgentEnvironmentAmbiguous is returned when a variable write
	// omits environment_id and the service has more than one
	// environment. Guessing which environment to write a variable into
	// is exactly the kind of confident wrong answer this agent must not
	// give.
	ErrAgentEnvironmentAmbiguous = errors.New("this service has several environments; name the one to change with environment_id")
)

// buildAgentEnv binds the agent tool registry to the control plane's
// stores and durable mutation coordinators. Before this seam existed
// every tool answered "not wired" because the chat backend sent a zero
// Env, and no tool call was ever audited.
//
// Mutating tools deliberately reuse the same coordinators the human
// HTTP routes use — including their RBAC checks, resource-version
// fencing, idempotency reservations and redacted audit rows. The agent
// gets exactly the authority the person talking to it already has, and
// not one write path of its own.
func buildAgentEnv(stack DataStack, actor string) agent.Env {
	env := agent.Env{Actor: actor}
	gate := stack.AgentMutationGate
	if stack.ProjectStore != nil || stack.ServiceStore != nil {
		env.Services = &agentServicesAdapter{
			projects:     stack.ProjectStore,
			services:     stack.ServiceStore,
			environments: stack.EnvironmentStore,
			variables:    stack.TenantVariables,
			gate:         gate,
			actor:        actor,
			facade:       NewServicesAPI(stack, nil),
		}
	}
	if stack.OperationStore != nil || stack.DeploymentStore != nil {
		env.Operations = &agentOperationsAdapter{
			facade:        NewServicesAPI(stack, nil),
			operations:    stack.OperationStore,
			logs:          stack.LogStore,
			actions:       stack.DeploymentActions,
			tenantActions: stack.TenantDeploymentActions,
			gate:          gate,
			actor:         actor,
		}
	}
	env.Builds = NewTenantAgentSourceBuilds(stack, actor)
	if stack.AgentAuditSink != nil {
		env.Audit = &agentAuditAdapter{sink: stack.AgentAuditSink}
	}
	if gate != nil {
		env.AllowMutations = gate.AllowMutations(context.Background(), agentauth.SessionContext{AllowMutations: true})
	}
	return env
}

// humanIdentityFromContext pulls the authorized signed-in human tuple the
// agent is acting on behalf of out of the request context. The chat and
// MCP handlers run under optionalBearerTokenMiddleware, which installs
// this tuple when the caller presented an identity session token; an
// operator recovery token installs nothing, which is what makes the
// distinction enforceable here rather than advisory.
func humanIdentityFromContext(ctx context.Context) (identity.Principal, identity.Session, error) {
	principal, principalOK := identityPrincipalFromContext(ctx)
	session, sessionOK := identitySessionFromContext(ctx)
	if !principalOK || !sessionOK {
		return identity.Principal{}, identity.Session{}, ErrAgentMutationNeedsHumanSession
	}
	if principal.UserID == "" || session.ID == "" || session.UserID != principal.UserID {
		return identity.Principal{}, identity.Session{}, ErrAgentMutationNeedsHumanSession
	}
	return principal, session, nil
}

// checkOperatorGate re-evaluates the operator-level half of the mutation
// gate at dispatch time. The registry already refused the call unless
// Env.AllowMutations was set (operator gate AND the user's per-message
// toggle), but that value was computed when the Env was built; an
// operator who swaps the gate to off expects the next tool call to stop,
// not the next process restart.
func checkOperatorGate(ctx context.Context, gate agentauth.MutationGate, actor, tool string) error {
	if gate == nil {
		return nil
	}
	session := agentauth.SessionContext{
		AllowMutations: true,
		ToolName:       tool,
		Principal:      agentauth.Principal{Kind: agentauth.PrincipalKindAgent, ID: actor, Subject: actor},
	}
	if !gate.AllowMutations(ctx, session) {
		return agent.ErrMutationsDisabled
	}
	return nil
}

// agentIdempotencyKey derives a stable idempotency key for one agent
// mutation. Deriving it from the operation's own content (rather than
// minting a random one) means a retried identical tool call replays the
// stored response instead of writing twice — which matters because the
// model retries on its own initiative, without asking anyone.
func agentIdempotencyKey(actor, operation string, parts ...string) idempotency.Key {
	sum := sha256.Sum256([]byte(strings.Join(append([]string{actor, operation}, parts...), "\x00")))
	return idempotency.Key("agent-" + operation + "-" + hex.EncodeToString(sum[:16]))
}

type agentServicesAdapter struct {
	projects     ProjectStore
	services     ServiceStore
	environments EnvironmentStore
	variables    TenantVariableCoordinator
	gate         agentauth.MutationGate
	facade       *ServicesAPI
	actor        string
}

func (a *agentServicesAdapter) ListProjects(ctx context.Context) ([]agent.Project, error) {
	if a.projects == nil {
		return nil, errors.New("project store is not configured")
	}
	projects, err := a.projects.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]agent.Project, 0, len(projects))
	for _, p := range projects {
		out = append(out, agent.Project{ID: string(p.ID), Name: firstNonEmptyString(p.DisplayName, p.Slug), Slug: p.Slug, WorkspaceID: p.WorkspaceID, CreatedAt: p.CreatedAt})
	}
	return out, nil
}

func (a *agentServicesAdapter) ListServices(ctx context.Context, projectID string) ([]agent.Service, error) {
	if a.services == nil {
		return nil, errors.New("service store is not configured")
	}
	services, err := a.services.ListServices(ctx, ResourceListFilter{ParentID: orailtypes.ID(projectID)})
	if err != nil {
		return nil, err
	}
	out := make([]agent.Service, 0, len(services))
	for _, s := range services {
		out = append(out, a.projectService(ctx, s, false))
	}
	return out, nil
}

func (a *agentServicesAdapter) GetService(ctx context.Context, serviceID string) (agent.Service, error) {
	if a.services == nil {
		return agent.Service{}, errors.New("service store is not configured")
	}
	svc, err := a.services.GetService(ctx, orailtypes.ID(serviceID))
	if err != nil {
		return agent.Service{}, err
	}
	return a.projectService(ctx, svc, true), nil
}

// projectService maps a control-plane service onto the agent projection.
// Variable values are never included: the registry redacts values anyway,
// and metadata-only reads keep the agent on the same footing as a human.
func (a *agentServicesAdapter) projectService(ctx context.Context, svc Service, withDeployments bool) agent.Service {
	out := agent.Service{ID: string(svc.ID), ProjectID: string(svc.ProjectID), Name: firstNonEmptyString(svc.DisplayName, svc.Slug), CreatedAt: svc.CreatedAt}
	if gh, ok := svc.Spec["github"].(map[string]any); ok {
		if repo, ok := gh["repository"].(string); ok {
			out.GitHubRepo = repo
		} else if repo, ok := gh["repo"].(string); ok {
			out.GitHubRepo = repo
		}
		if ref, ok := gh["branch"].(string); ok {
			out.GitHubRef = ref
		}
	}
	if withDeployments && a.facade != nil {
		if deployments, srvErr := a.facade.ListDeploymentsForServiceService(ctx, string(svc.ID)); srvErr == nil {
			for _, d := range deployments {
				out.Deployments = append(out.Deployments, deploymentProjection(d))
			}
			if len(out.Deployments) > 0 {
				out.LatestStatus = out.Deployments[0].Status
			}
		}
	}
	return out
}

// SetVariable creates or rotates one variable through the same durable
// tenant coordinator the console's PATCH/POST routes use.
func (a *agentServicesAdapter) SetVariable(ctx context.Context, req agent.VariableWrite) error {
	return a.writeVariable(ctx, req, false)
}

// UnsetVariable tombstones one variable through the same coordinator.
func (a *agentServicesAdapter) UnsetVariable(ctx context.Context, req agent.VariableWrite) error {
	return a.writeVariable(ctx, req, true)
}

// writeVariable is the shared body of the two variable tools.
//
// Everything load-bearing here is delegated: RBAC (variables.write
// evaluated against the caller's own capabilities inside the
// coordinator's serializable transaction), compare-and-swap on the
// variable's resource version, the idempotency reservation, and the
// redacted audit row. What this function owns is the translation from
// the model's flat "set NAME on this service" request into the exact
// (service, environment, variable id, expected version) tuple the
// coordinator demands — and never letting the value back out.
func (a *agentServicesAdapter) writeVariable(ctx context.Context, req agent.VariableWrite, remove bool) error {
	tool := "set_variable"
	if remove {
		tool = "unset_variable"
	}
	if err := checkOperatorGate(ctx, a.gate, a.actor, tool); err != nil {
		return err
	}
	if a.variables == nil {
		return ErrAgentMutationUnavailable
	}
	principal, session, err := humanIdentityFromContext(ctx)
	if err != nil {
		return err
	}
	serviceID := strings.TrimSpace(req.ServiceID)
	name := strings.TrimSpace(req.Name)
	if serviceID == "" || name == "" {
		return errors.New("service_id and name are required")
	}
	environmentID, err := a.resolveEnvironmentID(ctx, serviceID, req.EnvironmentID)
	if err != nil {
		return err
	}

	read := TenantVariableReadRequest{
		Principal:     principal,
		Session:       session,
		ServiceID:     serviceID,
		EnvironmentID: environmentID,
	}
	existing, found, err := a.findVariableByName(ctx, read, name)
	if err != nil {
		return err
	}

	switch {
	case remove && !found:
		// Removing a variable that is already gone is the state the
		// caller asked for. Reporting an error here would push the
		// model into a retry loop over a no-op.
		return nil

	case remove:
		body := mustAgentJSON(map[string]any{"expected_version": existing.Version})
		result, err := a.variables.DeleteVariable(ctx, TenantVariableDeleteRequest{
			TenantVariableMutationMeta: a.mutationMeta(read, existing.ID, http.MethodDelete, body,
				agentIdempotencyKey(a.actor, "variable-delete", serviceID, environmentID, name, fmt.Sprint(existing.Version))),
			ExpectedVersion: existing.Version,
		})
		return agentTenantMutationError(err, result)

	case found:
		plaintext := []byte(req.Value)
		defer clear(plaintext)
		valueKind, references, parseErr := parseVariableValue(req.Value)
		if parseErr != nil {
			return fmt.Errorf("variable value is not a valid reference template: %w", parseErr)
		}
		body := mustAgentJSON(map[string]any{
			"expected_version": existing.Version,
			// The body is fingerprinted, not stored: a digest of the
			// value keeps the idempotency fingerprint sensitive to a
			// changed value without putting the value in a struct that
			// outlives this call.
			"value_digest": agentValueDigest(req.Value),
		})
		result, err := a.variables.UpdateVariable(ctx, TenantVariableUpdateRequest{
			TenantVariableMutationMeta: a.mutationMeta(read, existing.ID, http.MethodPatch, body,
				agentIdempotencyKey(a.actor, "variable-update", serviceID, environmentID, name,
					fmt.Sprint(existing.Version), agentValueDigest(req.Value))),
			ExpectedVersion: existing.Version,
			// A nil Scope retains the variable's current privacy class.
			// The agent must never be the reason a secret silently
			// becomes a plain value.
			Scope:      nil,
			Plaintext:  plaintext,
			ValueKind:  valueKind,
			References: references,
		})
		return agentTenantMutationError(err, result)

	default:
		plaintext := []byte(req.Value)
		defer clear(plaintext)
		valueKind, references, parseErr := parseVariableValue(req.Value)
		if parseErr != nil {
			return fmt.Errorf("variable value is not a valid reference template: %w", parseErr)
		}
		body := mustAgentJSON(map[string]any{
			"name":         name,
			"scope":        string(secrets.ScopeSecret),
			"value_digest": agentValueDigest(req.Value),
		})
		result, err := a.variables.CreateVariable(ctx, TenantVariableCreateRequest{
			TenantVariableMutationMeta: a.mutationMeta(read, "", http.MethodPost, body,
				agentIdempotencyKey(a.actor, "variable-create", serviceID, environmentID, name, agentValueDigest(req.Value))),
			Name: name,
			// New variables the agent creates default to the secret
			// privacy class: metadata-only reads, encrypted at rest.
			// A value the agent was told in chat is more likely to be a
			// credential than not.
			Scope:      secrets.ScopeSecret,
			Plaintext:  plaintext,
			ValueKind:  valueKind,
			References: references,
		})
		return agentTenantMutationError(err, result)
	}
}

func (a *agentServicesAdapter) mutationMeta(read TenantVariableReadRequest, variableID secrets.VariableID, method string, body []byte, key idempotency.Key) TenantVariableMutationMeta {
	return TenantVariableMutationMeta{
		Principal:     read.Principal,
		Session:       read.Session,
		ServiceID:     read.ServiceID,
		EnvironmentID: read.EnvironmentID,
		VariableID:    variableID,
		Method:        method,
		Key:           key,
		RawBody:       body,
		RequestID:     agentRequestID(a.actor),
		SourceIP:      agentSourceLabel,
		UserAgent:     a.actor,
	}
}

// findVariableByName resolves the model's variable name into the exact
// id + version the coordinator's CAS needs. The listing is metadata
// only: no implementation on this path can read a value back.
func (a *agentServicesAdapter) findVariableByName(ctx context.Context, read TenantVariableReadRequest, name string) (secrets.VariableMeta, bool, error) {
	metas, err := a.variables.ListVariables(ctx, read)
	if err != nil {
		return secrets.VariableMeta{}, false, err
	}
	for _, meta := range metas {
		if meta.Name == name {
			return meta, true, nil
		}
	}
	return secrets.VariableMeta{}, false, nil
}

// resolveEnvironmentID accepts the model's explicit choice, or resolves
// the only environment when there is exactly one. It never picks a
// "default" out of several.
func (a *agentServicesAdapter) resolveEnvironmentID(ctx context.Context, serviceID, requested string) (string, error) {
	if id := strings.TrimSpace(requested); id != "" {
		return id, nil
	}
	if a.services == nil || a.environments == nil {
		return "", ErrAgentEnvironmentAmbiguous
	}
	svc, err := a.services.GetService(ctx, orailtypes.ID(serviceID))
	if err != nil {
		return "", err
	}
	envs, err := a.environments.ListEnvironments(ctx, ResourceListFilter{ParentID: svc.ProjectID})
	if err != nil {
		return "", err
	}
	if len(envs) != 1 {
		return "", ErrAgentEnvironmentAmbiguous
	}
	return string(envs[0].ID), nil
}

type agentOperationsAdapter struct {
	facade        *ServicesAPI
	operations    OperationStore
	logs          LogQueryStore
	actions       DeploymentActionIntake
	tenantActions TenantDeploymentActionCoordinator
	gate          agentauth.MutationGate
	actor         string
}

func (a *agentOperationsAdapter) GetDeployment(ctx context.Context, deploymentID string) (agent.Deployment, error) {
	if a.facade == nil {
		return agent.Deployment{}, errors.New("deployment store is not configured")
	}
	resource, srvErr := a.facade.GetDeploymentService(ctx, deploymentID)
	if srvErr != nil {
		return agent.Deployment{}, fmt.Errorf("%s: %s", srvErr.Code, srvErr.Message)
	}
	return deploymentProjection(resource), nil
}

// GetLogs merges the durable operation timeline (build/deploy steps) with
// application log records for the deployment, newest last, capped at lines.
func (a *agentOperationsAdapter) GetLogs(ctx context.Context, deploymentID string, lines int, search string) ([]agent.LogLine, error) {
	if lines <= 0 {
		lines = 200
	}
	search = strings.ToLower(strings.TrimSpace(search))
	var out []agent.LogLine
	operationID := deploymentID
	if a.facade != nil {
		if resource, srvErr := a.facade.GetDeploymentService(ctx, deploymentID); srvErr == nil && resource.OperationID != "" {
			operationID = resource.OperationID
		}
	}
	if a.operations != nil {
		events, err := a.operations.ListEvents(ctx, orailtypes.ID(operationID))
		if err == nil {
			for _, e := range events {
				out = append(out, agent.LogLine{Timestamp: e.CreatedAt, Stream: "build", Message: e.Message})
			}
		}
	}
	if a.logs != nil {
		result, err := a.logs.Query(logrecords.Filter{Deployments: []string{deploymentID}, Operations: []string{operationID}, Tail: lines}, logrecords.Cursor{}, lines)
		if err == nil {
			for _, entry := range result.Entries {
				out = append(out, agent.LogLine{Timestamp: entry.At, Stream: streamForLevel(entry.Level), Message: entry.Message})
			}
		}
	}
	if search != "" {
		filtered := out[:0]
		for _, line := range out {
			if strings.Contains(strings.ToLower(line.Message), search) {
				filtered = append(filtered, line)
			}
		}
		out = filtered
	}
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	return out, nil
}

// Redeploy replays the service's current image as a new deployment. The
// tool takes a service, but the durable coordinator replays a specific
// *deployment*, so the active deployment is resolved here — the most
// recent one is not necessarily the one currently serving traffic, and
// replaying a failed build is not what "redeploy this service" means.
func (a *agentOperationsAdapter) Redeploy(ctx context.Context, serviceID string) (agent.Deployment, error) {
	if err := checkOperatorGate(ctx, a.gate, a.actor, "redeploy"); err != nil {
		return agent.Deployment{}, err
	}
	source, err := a.replaySource(ctx, serviceID)
	if err != nil {
		return agent.Deployment{}, err
	}
	return a.commitDeploymentAction(ctx, "redeploy", serviceID, source)
}

// Rollback makes a previously-successful deployment active again.
func (a *agentOperationsAdapter) Rollback(ctx context.Context, deploymentID string) (agent.Deployment, error) {
	if err := checkOperatorGate(ctx, a.gate, a.actor, "rollback"); err != nil {
		return agent.Deployment{}, err
	}
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return agent.Deployment{}, errors.New("deployment_id is required")
	}
	serviceID := ""
	if a.facade != nil {
		resource, srvErr := a.facade.GetDeploymentService(ctx, deploymentID)
		if srvErr != nil {
			return agent.Deployment{}, fmt.Errorf("%s: %s", srvErr.Code, srvErr.Message)
		}
		serviceID = resource.ServiceID
	}
	if serviceID == "" {
		return agent.Deployment{}, errors.New("could not resolve the service that owns this deployment")
	}
	return a.commitDeploymentAction(ctx, "rollback", serviceID, deploymentID)
}

// commitDeploymentAction routes one action to the same coordinator the
// human HTTP route would pick for this caller: the tenant coordinator
// (RBAC + audit + idempotency, authorized as the signed-in
// human) when the conversation carries a human session, and the
// durable action store directly when it carries an operator recovery
// token. That fork mirrors deploymentActionHandler exactly, which is the
// point — the agent is a second front door onto the same authority, not
// a new one.
func (a *agentOperationsAdapter) commitDeploymentAction(ctx context.Context, action, serviceID, deploymentID string) (agent.Deployment, error) {
	if a.tenantActions == nil && a.actions == nil {
		return agent.Deployment{}, ErrAgentMutationUnavailable
	}

	principal, session, identityErr := humanIdentityFromContext(ctx)
	if identityErr == nil {
		// A human session must go through the tenant coordinator, which
		// evaluates that person's own capabilities. Falling back to the
		// direct store here would quietly upgrade a signed-in
		// human onto the operator-recovery write path, which
		// runs no tenant RBAC — a divergence from the HTTP action route,
		// which refuses in exactly this situation.
		if a.tenantActions == nil {
			return agent.Deployment{}, ErrAgentMutationUnavailable
		}
		key := agentIdempotencyKey(a.actor, "deployment-action", action, serviceID, deploymentID)
		result, commitErr := a.tenantActions.CommitTenantDeploymentAction(ctx, TenantDeploymentActionRequest{
			Principal:    principal,
			Session:      session,
			ServiceID:    serviceID,
			DeploymentID: deploymentID,
			Action:       action,
			Key:          key,
			Fingerprint: idempotency.RequestFingerprint{
				Method:     http.MethodPost,
				Path:       tenantDeploymentActionIdempotencyPath(serviceID, deploymentID, action),
				BodySHA256: idempotency.BodyHashSHA256(nil),
			},
			RequestID: agentRequestID(a.actor),
			SourceIP:  agentSourceLabel,
			UserAgent: a.actor,
		})
		if commitErr != nil {
			return agent.Deployment{}, commitErr
		}
		return agentDeploymentFromStoredResponse(result.Response)
	}

	if a.actions == nil {
		return agent.Deployment{}, ErrAgentMutationNeedsHumanSession
	}
	// Operator-token path. The trigger key is server-derived and stable
	// for an identical repeat, matching the non-human REST branch.
	triggerKey := "agent:deployment-action:" + action + ":" + serviceID + ":" + deploymentID + ":" +
		string(agentIdempotencyKey(a.actor, "deployment-action", action, serviceID, deploymentID))
	replay := deploy.ActionRedeployImage
	if action == "rollback" {
		replay = deploy.ActionRollbackDigest
	}
	result, err := a.actions.CommitImageReplay(ctx, ImageReplayRequest{
		ServiceID:  serviceID,
		SourceID:   deploymentID,
		Action:     replay,
		TriggerKey: triggerKey,
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	return agentDeploymentFromRecord(result), nil
}

// replaySource picks the deployment whose image a redeploy should
// replay: the active one, else the most recent one that actually
// completed.
func (a *agentOperationsAdapter) replaySource(ctx context.Context, serviceID string) (string, error) {
	serviceID = strings.TrimSpace(serviceID)
	if serviceID == "" {
		return "", errors.New("service_id is required")
	}
	if a.facade == nil {
		return "", ErrAgentMutationUnavailable
	}
	deployments, srvErr := a.facade.ListDeploymentsForServiceService(ctx, serviceID)
	if srvErr != nil {
		return "", fmt.Errorf("%s: %s", srvErr.Code, srvErr.Message)
	}
	var fallback string
	for _, d := range deployments {
		if bound, ok := ctx.Value(tenantConversationToolScopeKey{}).(*tenantConversationTools); ok &&
			bound.environmentID != "" && d.EnvironmentID != bound.environmentID {
			continue
		}
		id := firstNonEmptyString(d.DeploymentID, d.ID)
		if id == "" {
			continue
		}
		if d.Active {
			return id, nil
		}
		if fallback == "" && d.OperationState == orailtypes.OperationSucceeded {
			fallback = id
		}
	}
	if fallback == "" {
		return "", ErrAgentServiceHasNoDeployment
	}
	return fallback, nil
}

// agentBuildsUnavailable retains the legacy repo/ref interface for older
// callers. Human chat source deployment uses the separately authorized bounded
// inline-files interface; it never accepts host paths or arbitrary clone URLs.
type agentBuildsUnavailable struct{}

func (agentBuildsUnavailable) DeployFromSource(context.Context, string, string, string) (agent.Deployment, error) {
	return agent.Deployment{}, ErrAgentSourceDeployUnavailable
}

// agentAuditAdapter forwards registry audit entries into the tamper-evident
// agent audit sink so every tool call lands on the audit chain.
type agentAuditAdapter struct {
	sink agentauth.AgentAuditSink
}

func (a *agentAuditAdapter) Record(ctx context.Context, entry agent.AuditEntry) error {
	if a == nil || a.sink == nil {
		return nil
	}
	row := agentauth.AgentAuditRow{
		OccurredAt: entry.Timestamp,
		Source:     agentauth.AgentAuditSourceChat,
		Principal:  agentauth.Principal{Kind: agentauth.PrincipalKindAgent, ID: entry.Actor, Subject: entry.Actor},
		ToolName:   entry.Tool,
		Action:     agentauth.AgentAuditActionToolCall,
		Resource:   agentAuditResource(entry),
	}
	if len(entry.Arguments) > 0 {
		row.ToolInput = redactAgentToolInput(entry.Arguments)
	}
	if entry.Outcome == "ok" {
		row.ToolResultSummary = entry.Outcome
	} else {
		row.ToolError = entry.Outcome
	}
	return a.sink.Record(ctx, row)
}

// agentAuditResource turns a tool call's resource hints into the audit
// chain's resource shape. Kind names the most specific resource the call
// actually addressed, so the chain can be queried by what changed rather
// than only by who changed it.
func agentAuditResource(entry agent.AuditEntry) agentauth.AgentAuditResource {
	resource := agentauth.AgentAuditResource{
		ProjectID:     entry.ProjectID,
		EnvironmentID: entry.EnvironmentID,
	}
	switch {
	case entry.DeploymentID != "":
		resource.Kind = "deployment"
		resource.ID = entry.DeploymentID
	case entry.ServiceID != "":
		resource.Kind = "service"
		resource.ID = entry.ServiceID
	case entry.ProjectID != "":
		resource.Kind = "project"
		resource.ID = entry.ProjectID
	}
	return resource
}

// agentSourceLabel stands in for the source IP on the request-evidence
// fields of a coordinator call. The agent is in-process: there is no
// remote address, and inventing the console's would be a lie in the
// audit row.
const agentSourceLabel = "in-process:agent"

// agentRequestID derives the request id recorded on coordinator audit
// rows for agent-driven mutations.
func agentRequestID(actor string) string {
	if actor == "" {
		actor = "agent"
	}
	return "agent:" + actor
}

// agentValueDigest hashes a variable value so the idempotency
// fingerprint is sensitive to a changed value without the value itself
// ever entering a request-evidence field.
func agentValueDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mustAgentJSON(v map[string]any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		// The inputs are all strings and ints assembled here; a failure
		// would be a programming error, and an empty body still yields a
		// stable fingerprint.
		return []byte("{}")
	}
	return body
}

// agentTenantMutationError turns a tenant coordinator's result into a Go
// error for the tool layer.
//
// These coordinators answer several in-transaction failures — CAS
// conflict, not-found, name collision — as a non-error result carrying
// an HTTP error response, because on the HTTP route that response is
// what gets stored under the idempotency key and replayed. The agent has
// no HTTP response to write, so a non-2xx status has to become an error
// here or a failed write would be reported to the user as a success.
func agentTenantMutationError(err error, result TenantMutationResult) error {
	if err != nil {
		return err
	}
	status := result.Response.StatusCode
	if status == 0 || (status >= 200 && status < 300) {
		return nil
	}
	if detail := agentProblemDetail(result.Response.Body); detail != "" {
		return fmt.Errorf("the change was refused: %s", detail)
	}
	return fmt.Errorf("the change was refused with status %d", status)
}

// agentProblemDetail pulls the human-readable half out of a problem
// document, so the model relays what actually went wrong instead of a
// bare status code.
func agentProblemDetail(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var problem struct {
		Detail string `json:"detail"`
		Title  string `json:"title"`
		Code   string `json:"code"`
	}
	if err := json.Unmarshal(body, &problem); err != nil {
		return ""
	}
	return firstNonEmptyString(problem.Detail, problem.Title, problem.Code)
}

// agentDeploymentFromStoredResponse decodes the deployment action result
// the tenant coordinator rendered as an HTTP response body.
func agentDeploymentFromStoredResponse(response idempotency.StoredResponse) (agent.Deployment, error) {
	if status := response.StatusCode; status != 0 && (status < 200 || status >= 300) {
		if detail := agentProblemDetail(response.Body); detail != "" {
			return agent.Deployment{}, fmt.Errorf("the deployment action was refused: %s", detail)
		}
		return agent.Deployment{}, fmt.Errorf("the deployment action was refused with status %d", status)
	}
	var result DeploymentActionResult
	if err := json.Unmarshal(response.Body, &result); err != nil {
		return agent.Deployment{}, fmt.Errorf("decode deployment action result: %w", err)
	}
	return agentDeploymentFromRecord(result), nil
}

// agentDeploymentFromRecord projects a durable action result onto the
// agent's deployment shape.
func agentDeploymentFromRecord(result DeploymentActionResult) agent.Deployment {
	return agent.Deployment{
		ID:        result.Deployment.ID,
		ServiceID: result.Deployment.ServiceID,
		Status:    string(result.Operation.State),
	}
}

// agentSecretArgumentFields are the tool-input field names whose values
// must never reach the audit chain.
//
// set_variable's whole purpose is to carry a plaintext secret, and the
// registry hands the raw input JSON to the audit sink verbatim. Writing
// that blob into a tamper-evident, long-retention chain would put every
// credential the agent ever set into the one store designed never to
// forget anything. The field is replaced with a digest so two rows can
// still be compared for "did the value change" without the value being
// recoverable.
var agentSecretArgumentFields = map[string]struct{}{
	"files":         {},
	"value":         {},
	"secret":        {},
	"token":         {},
	"password":      {},
	"api_key":       {},
	"plaintext":     {},
	"credential":    {},
	"private_key":   {},
	"authorization": {},
}

// redactAgentToolInput turns a tool's raw input into an audit-safe map.
// Unparseable input is dropped entirely rather than passed through: a
// blob this layer cannot inspect is a blob it cannot promise is clean.
func redactAgentToolInput(raw json.RawMessage) map[string]any {
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]any{"arguments": "[unparsed]"}
	}
	out := make(map[string]any, len(parsed))
	for key, value := range parsed {
		if _, secret := agentSecretArgumentFields[strings.ToLower(key)]; !secret {
			out[key] = value
			continue
		}
		if text, ok := value.(string); ok {
			out[key] = "[redacted sha256:" + agentValueDigest(text)[:16] + "]"
			continue
		}
		if strings.EqualFold(key, "files") {
			if encoded, err := json.Marshal(value); err == nil {
				out[key] = "[redacted sha256:" + agentValueDigest(string(encoded))[:16] + "]"
				continue
			}
		}
		out[key] = "[redacted]"
	}
	return out
}

func deploymentProjection(d DeploymentResource) agent.Deployment {
	return agent.Deployment{
		ID:        firstNonEmptyString(d.DeploymentID, d.ID),
		ServiceID: d.ServiceID,
		Status:    string(d.OperationState),
		Route:     d.Route,
		CommitSHA: d.CommitSHA,
	}
}

func streamForLevel(level string) string {
	switch strings.ToLower(level) {
	case "error", "warn", "warning":
		return "stderr"
	default:
		return "stdout"
	}
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
