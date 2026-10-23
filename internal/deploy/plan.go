// Package deploy composes the side-effect-free deployment journey plan.
package deploy

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/scheduler"
	"github.com/midnight/midnight/internal/tcpproxy"
)

const (
	SchemaVersion = "midnight.deploy.journey_plan.v1"

	ServiceTypeRuntime = "runtime"
	ServiceTypeCron    = "cron"
	ServiceTypeStatic  = "static"

	StepOutcomePlanned = "planned"
	StepOutcomeSkipped = "skipped"
	StepOutcomeBlocked = "blocked"
)

type Request struct {
	Now           time.Time
	DeploymentID  string
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	ServiceType   string
	Replicas      int
	BuildArtifact runtime.BuildArtifact
	Hosts         []scheduler.Host
	Predeploy     PredeploySpec
	Cron          CronMarker
	Static        StaticMarker
	TCPProxy      TCPProxySpec
}

type PredeploySpec struct {
	Enabled bool
	Command []string
	Timeout time.Duration
}

type CronMarker struct {
	Available         bool
	Schedule          string
	Timezone          string
	ConcurrencyPolicy string
	NextDueAt         time.Time
}

type StaticMarker struct {
	Available  bool
	Dist       string
	Index      string
	NotFound   string
	PromoteRef string
}

type TCPProxySpec struct {
	Enabled  bool
	Request  tcpproxy.EndpointRequest
	Existing []tcpproxy.ExistingEndpoint
	Ports    tcpproxy.PortRange
}

type Plan struct {
	SchemaVersion    string                     `json:"schema_version"`
	DeploymentID     string                     `json:"deployment_id"`
	ProjectID        string                     `json:"project_id"`
	EnvironmentID    string                     `json:"environment_id"`
	ServiceID        string                     `json:"service_id"`
	ServiceType      string                     `json:"service_type"`
	GeneratedAt      time.Time                  `json:"generated_at"`
	Steps            []Step                     `json:"steps"`
	Predeploy        *PredeployPlan             `json:"predeploy,omitempty"`
	Cron             *CronMarker                `json:"cron,omitempty"`
	Static           *StaticMarker              `json:"static,omitempty"`
	Placement        scheduler.PlacementPlan    `json:"placement"`
	RuntimeRequests  []runtime.ScheduleRequest  `json:"runtime_requests"`
	TCPProxy         *tcpproxy.EndpointContract `json:"tcp_proxy,omitempty"`
	AuditHints       []AuditHint                `json:"audit_hints"`
	TimelineHints    []TimelineHint             `json:"timeline_hints"`
	OperatorNotes    []string                   `json:"operator_notes,omitempty"`
	ReadyForDispatch bool                       `json:"ready_for_dispatch"`
}

type PredeployPlan struct {
	Command []string      `json:"command"`
	Timeout time.Duration `json:"timeout"`
}

type Step struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

type AuditHint struct {
	Action   string            `json:"action"`
	Resource string            `json:"resource"`
	Outcome  string            `json:"outcome"`
	Payload  map[string]string `json:"payload,omitempty"`
}

type TimelineHint struct {
	Type    string    `json:"type"`
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

var (
	ErrDeploymentIDRequired = errors.New("deploy planner: deployment id is required")
	ErrProjectIDRequired    = errors.New("deploy planner: project id is required")
	ErrEnvironmentRequired  = errors.New("deploy planner: environment id is required")
	ErrServiceIDRequired    = errors.New("deploy planner: service id is required")
)

func BuildPlan(req Request) (Plan, error) {
	req = normalizeRequest(req)
	if err := validateRequest(req); err != nil {
		return Plan{}, err
	}

	plan := Plan{
		SchemaVersion: SchemaVersion,
		DeploymentID:  req.DeploymentID,
		ProjectID:     req.ProjectID,
		EnvironmentID: req.EnvironmentID,
		ServiceID:     req.ServiceID,
		ServiceType:   req.ServiceType,
		GeneratedAt:   req.Now,
	}

	plan.Steps = append(plan.Steps, planPredeploy(req, &plan))
	plan.Steps = append(plan.Steps, planMarkers(req, &plan)...)

	placement, err := scheduler.PlanStatelessService(scheduler.StatelessServiceRequest{
		ID:        req.DeploymentID,
		ProjectID: req.ProjectID,
		ServiceID: req.ServiceID,
		Replicas:  req.Replicas,
		Stateless: true,
	}, req.Hosts)
	if err != nil {
		return Plan{}, err
	}
	plan.Placement = placement
	plan.Steps = append(plan.Steps, stepFromPlacements(placement))

	runtimeStep, runtimeRequests, err := planRuntimeBoundary(req, placement)
	if err != nil {
		return Plan{}, err
	}
	plan.RuntimeRequests = runtimeRequests
	plan.Steps = append(plan.Steps, runtimeStep)

	tcpStep, contract, err := planTCPProxy(req, runtimeRequests)
	if err != nil {
		return Plan{}, err
	}
	plan.Steps = append(plan.Steps, tcpStep)
	if contract != nil {
		plan.TCPProxy = contract
	}

	plan.ReadyForDispatch = ready(plan.Steps)
	plan.AuditHints = auditHints(plan)
	plan.TimelineHints = timelineHints(plan)
	plan.OperatorNotes = operatorNotes(plan)
	return plan, nil
}

func normalizeRequest(req Request) Request {
	if req.Now.IsZero() {
		req.Now = time.Unix(0, 0).UTC()
	} else {
		req.Now = req.Now.UTC()
	}
	req.DeploymentID = strings.TrimSpace(req.DeploymentID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.EnvironmentID = strings.TrimSpace(req.EnvironmentID)
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.ServiceType = strings.TrimSpace(req.ServiceType)
	if req.ServiceType == "" {
		req.ServiceType = ServiceTypeRuntime
	}
	if req.Replicas == 0 {
		req.Replicas = 1
	}
	if req.Replicas < 0 {
		req.Replicas = 0
	}
	return req
}

func validateRequest(req Request) error {
	if req.DeploymentID == "" {
		return ErrDeploymentIDRequired
	}
	if req.ProjectID == "" {
		return ErrProjectIDRequired
	}
	if req.EnvironmentID == "" {
		return ErrEnvironmentRequired
	}
	if req.ServiceID == "" {
		return ErrServiceIDRequired
	}
	return nil
}

func planPredeploy(req Request, plan *Plan) Step {
	if !req.Predeploy.Enabled {
		return Step{Name: "predeploy", Outcome: StepOutcomeSkipped, Reason: "no predeploy spec"}
	}
	command := trimCommand(req.Predeploy.Command)
	if len(command) == 0 {
		return Step{Name: "predeploy", Outcome: StepOutcomeBlocked, Reason: "predeploy command is required"}
	}
	timeout := req.Predeploy.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	plan.Predeploy = &PredeployPlan{Command: command, Timeout: timeout}
	return Step{Name: "predeploy", Outcome: StepOutcomePlanned}
}

func planMarkers(req Request, plan *Plan) []Step {
	steps := []Step{
		{Name: "cron-marker", Outcome: StepOutcomeSkipped, Reason: "no cron marker"},
		{Name: "static-marker", Outcome: StepOutcomeSkipped, Reason: "no static marker"},
	}
	if req.Cron.Available {
		marker := req.Cron
		marker.Schedule = strings.TrimSpace(marker.Schedule)
		marker.Timezone = strings.TrimSpace(marker.Timezone)
		marker.ConcurrencyPolicy = strings.TrimSpace(marker.ConcurrencyPolicy)
		plan.Cron = &marker
		steps[0] = Step{Name: "cron-marker", Outcome: StepOutcomePlanned}
	}
	if req.Static.Available {
		marker := req.Static
		marker.Dist = strings.TrimSpace(marker.Dist)
		marker.Index = strings.TrimSpace(marker.Index)
		marker.NotFound = strings.TrimSpace(marker.NotFound)
		marker.PromoteRef = strings.TrimSpace(marker.PromoteRef)
		plan.Static = &marker
		steps[1] = Step{Name: "static-marker", Outcome: StepOutcomePlanned}
	}
	return steps
}

func stepFromPlacements(plan scheduler.PlacementPlan) Step {
	if len(plan.Placements) == 0 {
		return Step{Name: "scheduler-placement", Outcome: StepOutcomeBlocked, Reason: "no replicas requested"}
	}
	for _, placement := range plan.Placements {
		if placement.Outcome != scheduler.OutcomeAccepted {
			return Step{Name: "scheduler-placement", Outcome: StepOutcomeBlocked, Reason: "one or more replicas were not placed"}
		}
	}
	return Step{Name: "scheduler-placement", Outcome: StepOutcomePlanned}
}

func planRuntimeBoundary(req Request, placement scheduler.PlacementPlan) (Step, []runtime.ScheduleRequest, error) {
	accepted := acceptedPlacements(placement.Placements)
	out := make([]runtime.ScheduleRequest, 0, len(accepted))
	for _, placed := range accepted {
		artifact := req.BuildArtifact
		artifact.DeploymentID = firstNonEmpty(artifact.DeploymentID, req.DeploymentID)
		artifact.ProjectID = firstNonEmpty(artifact.ProjectID, req.ProjectID)
		artifact.EnvironmentID = firstNonEmpty(artifact.EnvironmentID, req.EnvironmentID)
		artifact.ServiceID = firstNonEmpty(artifact.ServiceID, req.ServiceID)
		instanceID := fmt.Sprintf("%s-r%d", req.DeploymentID, placed.Replica)
		schedule, err := runtime.ScheduleRequestFromBuildArtifact(artifact, instanceID, req.DeploymentID+":"+instanceID)
		if err != nil {
			return Step{}, nil, err
		}
		schedule.Labels["midnight.host"] = placed.HostID
		out = append(out, schedule)
	}
	if len(out) == 0 {
		return Step{Name: "runtime-artifact-boundary", Outcome: StepOutcomeBlocked, Reason: "no accepted placements"}, out, nil
	}
	return Step{Name: "runtime-artifact-boundary", Outcome: StepOutcomePlanned}, out, nil
}

func planTCPProxy(req Request, runtimeRequests []runtime.ScheduleRequest) (Step, *tcpproxy.EndpointContract, error) {
	if !req.TCPProxy.Enabled {
		return Step{Name: "tcp-proxy-contract", Outcome: StepOutcomeSkipped, Reason: "tcp proxy disabled"}, nil, nil
	}
	if len(runtimeRequests) == 0 {
		return Step{Name: "tcp-proxy-contract", Outcome: StepOutcomeBlocked, Reason: "no accepted runtime target"}, nil, nil
	}
	endpoint := req.TCPProxy.Request
	endpoint.Enabled = true
	endpoint.ProjectID = firstNonEmpty(endpoint.ProjectID, req.ProjectID)
	endpoint.EnvironmentID = firstNonEmpty(endpoint.EnvironmentID, req.EnvironmentID)
	endpoint.ServiceID = firstNonEmpty(endpoint.ServiceID, req.ServiceID)
	if strings.TrimSpace(endpoint.BackendHost) == "" {
		endpoint.BackendHost = runtimeRequests[0].InstanceID
	}
	plan, err := tcpproxy.PlanEndpoint(tcpproxy.PlanInput{Request: endpoint, Existing: req.TCPProxy.Existing, Ports: req.TCPProxy.Ports})
	if err != nil {
		return Step{}, nil, err
	}
	contract, err := tcpproxy.ProductContract(plan)
	if err != nil {
		return Step{}, nil, err
	}
	return Step{Name: "tcp-proxy-contract", Outcome: StepOutcomePlanned}, &contract, nil
}

func acceptedPlacements(placements []scheduler.Placement) []scheduler.Placement {
	out := make([]scheduler.Placement, 0, len(placements))
	for _, placement := range placements {
		if placement.Outcome == scheduler.OutcomeAccepted {
			out = append(out, placement)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Replica < out[j].Replica })
	return out
}

func trimCommand(command []string) []string {
	out := make([]string, 0, len(command))
	for _, part := range command {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func ready(steps []Step) bool {
	for _, step := range steps {
		if step.Outcome == StepOutcomeBlocked {
			return false
		}
	}
	return true
}

func auditHints(plan Plan) []AuditHint {
	resource := "deployment/" + plan.DeploymentID
	hints := []AuditHint{{Action: "deployment.plan.created", Resource: resource, Outcome: "success", Payload: map[string]string{"service_type": plan.ServiceType}}}
	if plan.Predeploy != nil {
		hints = append(hints, AuditHint{Action: "deployment.predeploy.planned", Resource: resource, Outcome: "pending"})
	}
	if len(plan.RuntimeRequests) > 0 {
		hints = append(hints, AuditHint{Action: "deployment.runtime.planned", Resource: resource, Outcome: "pending", Payload: map[string]string{"instances": fmt.Sprint(len(plan.RuntimeRequests))}})
	}
	if plan.TCPProxy != nil {
		hints = append(hints, AuditHint{Action: "deployment.tcp_proxy.planned", Resource: resource, Outcome: "pending", Payload: map[string]string{"public_address": plan.TCPProxy.PublicAddress}})
	}
	if !plan.ReadyForDispatch {
		hints = append(hints, AuditHint{Action: "deployment.plan.blocked", Resource: resource, Outcome: "failure"})
	}
	return hints
}

func timelineHints(plan Plan) []TimelineHint {
	resource := plan.DeploymentID
	hints := []TimelineHint{{Type: "deployment.plan.created", At: plan.GeneratedAt, Message: "deployment journey plan created for " + resource}}
	for i, step := range plan.Steps {
		hints = append(hints, TimelineHint{Type: "deployment.step." + step.Name, At: plan.GeneratedAt.Add(time.Duration(i+1) * time.Millisecond), Message: step.Outcome})
	}
	return hints
}

func operatorNotes(plan Plan) []string {
	notes := make([]string, 0)
	for _, note := range plan.Placement.Notes {
		notes = append(notes, note.Message)
	}
	for _, step := range plan.Steps {
		if step.Outcome == StepOutcomeBlocked && step.Reason != "" {
			notes = append(notes, step.Name+": "+step.Reason)
		}
	}
	return notes
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
