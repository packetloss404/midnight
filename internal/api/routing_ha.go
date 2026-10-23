package api

import (
	"net/http"

	"github.com/midnight/midnight/internal/controlplane/ha"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/networking"
)

type regionRoutingPlanRequest struct {
	Candidates []networking.RegionRouteCandidate `json:"candidates"`
	Policy     networking.RegionRoutePolicy      `json:"policy"`
}

type profileCHAReadinessPlanRequest struct {
	ClaimedHAProfile string                         `json:"claimed_ha_profile"`
	QuorumPolicy     ha.QuorumPolicy                `json:"quorum_policy"`
	Nodes            []ha.Node                      `json:"nodes"`
	MigrationLeader  profileCMigrationLeaderRequest `json:"migration_leader"`
	Failover         profileCFailoverRequest        `json:"failover"`
	RegionRouting    regionRoutingPlanRequest       `json:"region_routing"`
}

type profileCMigrationLeaderRequest struct {
	CandidateID           string `json:"candidate_id"`
	SchemaEvidenceStatus  string `json:"schema_evidence_status"`
	MigrationPlanStatus   string `json:"migration_plan_status"`
	BackupReadinessStatus string `json:"backup_readiness_status,omitempty"`
}

type profileCFailoverRequest struct {
	Requested              bool   `json:"requested"`
	ReasonCode             string `json:"reason_code"`
	CurrentLeaderID        string `json:"current_leader_id"`
	TargetLeaderID         string `json:"target_leader_id"`
	OperatorApproved       bool   `json:"operator_approved"`
	HealthEvidenceAttached bool   `json:"health_evidence_attached"`
}

func regionRoutingPlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	var request regionRoutingPlanRequest
	if !decodeStrictJSONRequest(w, r, &request, "invalid region routing planning request") {
		return
	}
	plan, err := networking.PlanRegionRoute(request.Candidates, request.Policy)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	WriteJSON(w, http.StatusOK, plan)
}

func profileCHAReadinessPlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	var request profileCHAReadinessPlanRequest
	if !decodeStrictJSONRequest(w, r, &request, "invalid Profile C HA readiness planning request") {
		return
	}
	routing, err := networking.PlanRegionRoute(request.RegionRouting.Candidates, request.RegionRouting.Policy)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	quorum := ha.PlanQuorumReadiness(request.QuorumPolicy, request.Nodes)
	migration := ha.EvaluateMigrationLeaderEligibility(ha.MigrationLeaderInput{
		CandidateID:           request.MigrationLeader.CandidateID,
		Nodes:                 request.Nodes,
		Quorum:                quorum,
		SchemaEvidenceStatus:  request.MigrationLeader.SchemaEvidenceStatus,
		MigrationPlanStatus:   request.MigrationLeader.MigrationPlanStatus,
		BackupReadinessStatus: request.MigrationLeader.BackupReadinessStatus,
	})
	failover := ha.BuildFailoverDecisionEvidence(ha.FailoverInput{
		Requested:              request.Failover.Requested,
		ReasonCode:             request.Failover.ReasonCode,
		CurrentLeaderID:        request.Failover.CurrentLeaderID,
		TargetLeaderID:         request.Failover.TargetLeaderID,
		OperatorApproved:       request.Failover.OperatorApproved,
		HealthEvidenceAttached: request.Failover.HealthEvidenceAttached,
		Quorum:                 quorum,
		Nodes:                  request.Nodes,
	})
	WriteJSON(w, http.StatusOK, ha.BuildProfileCReadiness(ha.ProfileCInput{
		ClaimedHAProfile: request.ClaimedHAProfile,
		Quorum:           quorum,
		MigrationLeader:  migration,
		Failover:         failover,
		RegionRouting:    routing,
	}))
}
