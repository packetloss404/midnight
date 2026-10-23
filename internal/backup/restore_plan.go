package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
)

// RestorePlanSchemaVersion is the locked schema version emitted by MarshalRestorePlan
// and required by UnmarshalRestorePlan. Schema v2 adds app/platform artifacts;
// UnmarshalRestorePlan keeps accepting v1 persisted plans from older releases.
const RestorePlanSchemaVersion = "2"

const legacyRestorePlanSchemaVersion = "1"

// restorePlanEnvelope is the on-the-wire JSON shape: schemaVersion + plan body.
// Fields are tagged in stable order (encoding/json honors struct order) so two
// identical plans always produce the same byte sequence.
type restorePlanEnvelope struct {
	SchemaVersion string      `json:"schemaVersion"`
	Plan          RestorePlan `json:"plan"`
}

// MarshalRestorePlan returns the stable JSON byte representation of a restore plan,
// wrapped in a `{"schemaVersion":"2","plan":{...}}` envelope. Steps are emitted in
// canonical Order ascending so two equivalent plans round-trip byte-identical.
//
// The function validates the plan before serializing; callers can rely on the
// returned bytes being safe to persist or transmit.
func MarshalRestorePlan(plan RestorePlan) ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, failurecodes.Wrap(failurecodes.RestorePlanSerializationFailed, "restore plan failed validation", err)
	}
	stable := plan
	stable.Steps = plan.SortedSteps()
	envelope := restorePlanEnvelope{SchemaVersion: RestorePlanSchemaVersion, Plan: stable}
	out, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return nil, failurecodes.Wrap(failurecodes.RestorePlanSerializationFailed, "restore plan marshal failed", err)
	}
	return out, nil
}

// UnmarshalRestorePlan decodes the stable JSON byte representation produced by
// MarshalRestorePlan and returns the inner RestorePlan. The envelope schemaVersion
// must equal RestorePlanSchemaVersion; unknown fields are rejected so future schema
// changes are caught explicitly.
func UnmarshalRestorePlan(data []byte) (RestorePlan, error) {
	if len(data) == 0 {
		return RestorePlan{}, failurecodes.New(failurecodes.RestorePlanSerializationFailed, "restore plan json is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var envelope restorePlanEnvelope
	if err := dec.Decode(&envelope); err != nil {
		return RestorePlan{}, failurecodes.Wrap(failurecodes.RestorePlanSerializationFailed, "restore plan decode failed", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return RestorePlan{}, failurecodes.New(failurecodes.RestorePlanSerializationFailed, "restore plan json must contain a single document")
		}
		return RestorePlan{}, failurecodes.Wrap(failurecodes.RestorePlanSerializationFailed, "restore plan trailing-document check failed", err)
	}
	artifacts, ok := restoreArtifactsForSchema(envelope.SchemaVersion)
	if !ok {
		return RestorePlan{}, failurecodes.New(failurecodes.RestorePlanSerializationFailed,
			fmt.Sprintf("restore plan schemaVersion %q is not supported; expected %q", envelope.SchemaVersion, RestorePlanSchemaVersion))
	}
	if err := validateRestorePlan(envelope.Plan, artifacts); err != nil {
		return RestorePlan{}, failurecodes.Wrap(failurecodes.RestorePlanSerializationFailed, "restore plan failed validation", err)
	}
	return envelope.Plan, nil
}

// RestorePlanDataClass groups restore steps into the MVP-visible backup lanes.
type RestorePlanDataClass string

const (
	RestorePlanDataClassApp      RestorePlanDataClass = "app"
	RestorePlanDataClassDatabase RestorePlanDataClass = "database"
	RestorePlanDataClassPlatform RestorePlanDataClass = "platform"
)

// RestorePlanArtifact identifies one artifact recoverable in a restore plan.
type RestorePlanArtifact string

const (
	// RestorePlanArtifactDatabase is the control-plane Postgres artifact.
	RestorePlanArtifactDatabase RestorePlanArtifact = "database"
	// RestorePlanArtifactAppData is user application data outside the control-plane database.
	RestorePlanArtifactAppData RestorePlanArtifact = "app-data"
	// RestorePlanArtifactPlatformData is Midnight platform configuration and state.
	RestorePlanArtifactPlatformData RestorePlanArtifact = "platform-data"
	// RestorePlanArtifactSecrets is the operator-passphrase-protected secret store.
	RestorePlanArtifactSecrets RestorePlanArtifact = "secrets"
	// RestorePlanArtifactImagePinTable is the image-pin/digest registry pin table.
	RestorePlanArtifactImagePinTable RestorePlanArtifact = "image-pin-table"
	// RestorePlanArtifactRoutes is the ingress route configuration set.
	RestorePlanArtifactRoutes RestorePlanArtifact = "routes"
)

// orderedRestoreArtifacts is the canonical MVP restore order: database first,
// app data next, then platform data and the detailed platform sub-steps needed
// before traffic can safely resume.
var orderedRestoreArtifacts = []RestorePlanArtifact{
	RestorePlanArtifactDatabase,
	RestorePlanArtifactAppData,
	RestorePlanArtifactPlatformData,
	RestorePlanArtifactSecrets,
	RestorePlanArtifactImagePinTable,
	RestorePlanArtifactRoutes,
}

var legacyRestoreArtifacts = []RestorePlanArtifact{
	RestorePlanArtifactDatabase,
	RestorePlanArtifactSecrets,
	RestorePlanArtifactImagePinTable,
	RestorePlanArtifactRoutes,
}

// RestorePlanInput describes the operator-provided context for one restore plan generation.
//
// All fields are required. BackupID anchors the idempotency-key derivation so re-running the
// generator with the same backup produces stable keys; restoring a different backup yields fresh keys.
type RestorePlanInput struct {
	BackupID      string
	Operator      string
	RollbackOwner string
	Notes         string
}

// RestorePlanStep is one ordered restore action with its rollback counterpart.
type RestorePlanStep struct {
	Order               int                  `json:"order"`
	Artifact            RestorePlanArtifact  `json:"artifact"`
	DataClass           RestorePlanDataClass `json:"dataClass,omitempty"`
	Action              string               `json:"action"`
	IdempotencyKey      string               `json:"idempotencyKey"`
	Rollback            string               `json:"rollback"`
	RollbackKey         string               `json:"rollbackKey"`
	BlockingPredecessor RestorePlanArtifact  `json:"blockingPredecessor,omitempty"`
}

// RestorePlan is the deterministic ordered sequence of restore steps for one backup.
type RestorePlan struct {
	BackupID      string            `json:"backupId"`
	Operator      string            `json:"operator"`
	RollbackOwner string            `json:"rollbackOwner"`
	Notes         string            `json:"notes,omitempty"`
	Steps         []RestorePlanStep `json:"steps"`
}

// GenerateRestorePlan returns the deterministic offline restore plan for the given backup ID.
//
// The plan is fully offline: it does not contact the destination, decrypt artifacts, or stat the
// filesystem. Each step gets a stable idempotency key derived from BackupID + artifact name so
// downstream executors can safely retry without double-applying.
func GenerateRestorePlan(input RestorePlanInput) (RestorePlan, error) {
	backupID := strings.TrimSpace(input.BackupID)
	if backupID == "" {
		return RestorePlan{}, errors.New("restore plan backupId is required")
	}
	operator := strings.TrimSpace(input.Operator)
	if operator == "" {
		return RestorePlan{}, errors.New("restore plan operator is required")
	}
	rollbackOwner := strings.TrimSpace(input.RollbackOwner)
	if rollbackOwner == "" {
		return RestorePlan{}, errors.New("restore plan rollbackOwner is required")
	}

	plan := RestorePlan{
		BackupID:      backupID,
		Operator:      operator,
		RollbackOwner: rollbackOwner,
		Notes:         strings.TrimSpace(input.Notes),
		Steps:         make([]RestorePlanStep, 0, len(orderedRestoreArtifacts)),
	}
	for i, artifact := range orderedRestoreArtifacts {
		step := RestorePlanStep{
			Order:          i + 1,
			Artifact:       artifact,
			DataClass:      dataClassFor(artifact),
			Action:         actionFor(artifact),
			IdempotencyKey: idempotencyKey(backupID, artifact, "apply"),
			Rollback:       rollbackFor(artifact),
			RollbackKey:    idempotencyKey(backupID, artifact, "rollback"),
		}
		if i > 0 {
			step.BlockingPredecessor = orderedRestoreArtifacts[i-1]
		}
		plan.Steps = append(plan.Steps, step)
	}
	return plan, nil
}

// Validate confirms the restore plan is internally consistent and contains the
// canonical artifact set in the expected database -> app -> platform order.
func (p RestorePlan) Validate() error {
	return validateRestorePlan(p, orderedRestoreArtifacts)
}

func validateRestorePlan(p RestorePlan, artifacts []RestorePlanArtifact) error {
	if strings.TrimSpace(p.BackupID) == "" {
		return errors.New("restore plan backupId is required")
	}
	if strings.TrimSpace(p.Operator) == "" {
		return errors.New("restore plan operator is required")
	}
	if strings.TrimSpace(p.RollbackOwner) == "" {
		return errors.New("restore plan rollbackOwner is required")
	}
	if len(p.Steps) != len(artifacts) {
		return fmt.Errorf("restore plan must include exactly %d steps", len(artifacts))
	}
	for i, step := range p.Steps {
		if step.Order != i+1 {
			return fmt.Errorf("steps[%d]: order must be %d", i, i+1)
		}
		if step.Artifact != artifacts[i] {
			return fmt.Errorf("steps[%d]: artifact must be %q", i, artifacts[i])
		}
		if step.DataClass != "" && step.DataClass != dataClassFor(step.Artifact) {
			return fmt.Errorf("steps[%d]: dataClass must be %q", i, dataClassFor(step.Artifact))
		}
		if strings.TrimSpace(step.IdempotencyKey) == "" || strings.TrimSpace(step.RollbackKey) == "" {
			return fmt.Errorf("steps[%d]: idempotency keys are required", i)
		}
		if step.IdempotencyKey == step.RollbackKey {
			return fmt.Errorf("steps[%d]: apply and rollback keys must differ", i)
		}
	}
	// Idempotency keys across all steps must be unique to prevent collisions.
	seen := make(map[string]struct{}, len(p.Steps)*2)
	for i, step := range p.Steps {
		for _, key := range []string{step.IdempotencyKey, step.RollbackKey} {
			if _, dup := seen[key]; dup {
				return fmt.Errorf("steps[%d]: duplicate idempotency key %q", i, key)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func restoreArtifactsForSchema(schemaVersion string) ([]RestorePlanArtifact, bool) {
	switch schemaVersion {
	case RestorePlanSchemaVersion:
		return orderedRestoreArtifacts, true
	case legacyRestorePlanSchemaVersion:
		return legacyRestoreArtifacts, true
	default:
		return nil, false
	}
}

// SortedSteps returns the plan's steps sorted by Order so callers cannot accidentally reorder them.
func (p RestorePlan) SortedSteps() []RestorePlanStep {
	out := append([]RestorePlanStep(nil), p.Steps...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

func actionFor(artifact RestorePlanArtifact) string {
	switch artifact {
	case RestorePlanArtifactDatabase:
		return "restore control-plane Postgres logical dump"
	case RestorePlanArtifactAppData:
		return "restore application volume and object data"
	case RestorePlanArtifactPlatformData:
		return "restore Midnight platform configuration and state"
	case RestorePlanArtifactSecrets:
		return "restore secrets store under operator passphrase"
	case RestorePlanArtifactImagePinTable:
		return "restore image-pin/digest table"
	case RestorePlanArtifactRoutes:
		return "restore ingress route configuration"
	}
	return "restore " + string(artifact)
}

func rollbackFor(artifact RestorePlanArtifact) string {
	switch artifact {
	case RestorePlanArtifactDatabase:
		return "drop restored database, retain pre-restore snapshot"
	case RestorePlanArtifactAppData:
		return "detach restored application data, retain pre-restore volume snapshot"
	case RestorePlanArtifactPlatformData:
		return "rewind Midnight platform state to pre-restore baseline"
	case RestorePlanArtifactSecrets:
		return "remove restored secrets store, leave operator passphrase intact"
	case RestorePlanArtifactImagePinTable:
		return "revert image-pin table to pre-restore baseline"
	case RestorePlanArtifactRoutes:
		return "rewind ingress route set to pre-restore baseline"
	}
	return "rollback " + string(artifact)
}

func dataClassFor(artifact RestorePlanArtifact) RestorePlanDataClass {
	switch artifact {
	case RestorePlanArtifactDatabase:
		return RestorePlanDataClassDatabase
	case RestorePlanArtifactAppData:
		return RestorePlanDataClassApp
	case RestorePlanArtifactPlatformData, RestorePlanArtifactSecrets, RestorePlanArtifactImagePinTable, RestorePlanArtifactRoutes:
		return RestorePlanDataClassPlatform
	default:
		return ""
	}
}

func idempotencyKey(backupID string, artifact RestorePlanArtifact, action string) string {
	sum := sha256.Sum256([]byte(backupID + ":" + string(artifact) + ":" + action))
	return "restore-" + string(artifact) + "-" + action + "-" + hex.EncodeToString(sum[:8])
}
