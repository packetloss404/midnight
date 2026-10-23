package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const CommandEvidenceBatchSchemaV1 = "midnight.preflight.command_evidence_batch.v1"

// CommandEvidenceBatch is a deterministic support payload for command-backed preflight checks.
type CommandEvidenceBatch struct {
	Schema          string                         `json:"schema"`
	Report          Report                         `json:"report"`
	Evidence        []CommandEvidence              `json:"evidence"`
	RedactionReview CommandEvidenceRedactionReview `json:"redaction_review"`
}

// CollectCommandEvidenceBatch runs every allowed command spec through caller-supplied runner wiring.
// It never constructs an OS runner; live execution must be opted into by callers outside package defaults.
func CollectCommandEvidenceBatch(ctx context.Context, runner CommandRunner) (CommandEvidenceBatch, error) {
	if runner == nil {
		return CommandEvidenceBatch{}, fmt.Errorf("command runner is required")
	}

	specs := AllowedCommandSpecs()
	evidence := make([]CommandEvidence, 0, len(specs))
	results := make([]Result, 0, len(specs))
	for _, spec := range specs {
		item, err := RunCommandForEvidence(ctx, spec, runner)
		if err != nil {
			return CommandEvidenceBatch{}, err
		}
		evidence = append(evidence, item)
		results = append(results, resultFromCommandEvidence(item))
	}

	return CommandEvidenceBatch{
		Schema:          CommandEvidenceBatchSchemaV1,
		Report:          NewReport(results),
		Evidence:        evidence,
		RedactionReview: ReviewCommandEvidenceRedaction(evidence),
	}, nil
}

// MarshalCommandEvidenceBatchJSON serializes a command evidence batch with stable field order.
func MarshalCommandEvidenceBatchJSON(batch CommandEvidenceBatch) ([]byte, error) {
	return json.MarshalIndent(batch, "", "  ")
}

func resultFromCommandEvidence(evidence CommandEvidence) Result {
	result := Result{ID: evidence.CheckID, Severity: "fatal"}
	if evidence.TimedOut {
		result.Status = StatusFail
		result.Err = fmt.Errorf("%s timed out", evidence.SpecID)
		return result
	}
	if evidence.Error != "" {
		result.Status = StatusFail
		result.Err = fmt.Errorf("%s", evidence.Error)
		return result
	}
	if evidence.ExitCode != 0 {
		result.Status = StatusFail
		result.Err = fmt.Errorf("%s exited with status %d", evidence.SpecID, evidence.ExitCode)
		return result
	}

	switch evidence.SpecID {
	case CommandSpecCgroupFSType:
		ok, err := HasCgroupV2FSType(evidence.Stdout)
		if err != nil {
			result.Status = StatusFail
			result.Err = err
			return result
		}
		if !ok {
			result.Status = StatusFail
			result.Message = "cgroup v2 filesystem type not found"
			return result
		}
		result.Status = StatusPass
		result.Message = "cgroup v2 filesystem type found"
	case CommandSpecDockerAPI:
		version, err := DockerAPIVersionFromText(evidence.Stdout)
		if err != nil {
			result.Status = StatusFail
			result.Err = err
			return result
		}
		ok, err := DockerAPISupportsPhase0(version)
		if err != nil {
			result.Status = StatusFail
			result.Err = err
			return result
		}
		if !ok {
			result.Status = StatusFail
			result.Message = fmt.Sprintf("Docker API %s is below required %s", version, minimumDockerAPIVersion)
			return result
		}
		result.Status = StatusPass
		result.Message = fmt.Sprintf("Docker API %s satisfies required %s", version, minimumDockerAPIVersion)
	default:
		result.Status = StatusSkip
		result.Message = strings.TrimSpace(fmt.Sprintf("no preflight result mapper for %s", evidence.SpecID))
	}
	return result
}
