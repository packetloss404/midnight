package audit

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// DrainPlan describes a single audit-drain destination configuration.
// The drain plan is what an operator hands midnight to ship audit rows
// off-host (s3://, sftp://, file://). Lane 4 ships the plan validator;
// the actual streaming worker is Phase 2 §2.10 (Splunk HEC / Elastic).
type DrainPlan struct {
	// ID is the plan identifier used in error messages and audit emit.
	ID string `json:"id"`
	// Destination is the canonical destination URL (file://, s3://,
	// gcs://, sftp://). Validated against the panel-locked scheme set.
	Destination string `json:"destination"`
	// IncludeCheckpoints toggles whether audit_checkpoints rows are
	// included in the export. Default true.
	IncludeCheckpoints bool `json:"include_checkpoints"`
	// PartitionLabel optionally bounds the export to a single
	// monthly partition; empty means "all partitions in retention".
	PartitionLabel string `json:"partition_label,omitempty"`
	// SigningKeyFingerprint optionally pins the verifying key the
	// recipient is expected to validate the export against.
	SigningKeyFingerprint string `json:"signing_key_fingerprint,omitempty"`
}

// ErrDrainPlanInvalid is returned by ValidateDrainPlan for any rejected
// configuration. The wrapped sentinel describes the specific cause.
var ErrDrainPlanInvalid = errors.New("audit: drain plan invalid")

// ValidateDrainPlan returns nil when the supplied plan is structurally
// valid. The validator does NOT contact any remote destination; it
// strictly validates structure.
func ValidateDrainPlan(p DrainPlan) error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("%w: id is required", ErrDrainPlanInvalid)
	}
	if p.Destination == "" {
		return fmt.Errorf("%w: destination is required", ErrDrainPlanInvalid)
	}
	if !strings.Contains(p.Destination, "://") && !strings.HasPrefix(p.Destination, "/") && !strings.HasPrefix(p.Destination, ".") {
		return fmt.Errorf("%w: destination %q must be an absolute path or a URL", ErrDrainPlanInvalid, p.Destination)
	}
	if strings.Contains(p.Destination, "://") {
		u, err := url.Parse(p.Destination)
		if err != nil {
			return fmt.Errorf("%w: destination url: %v", ErrDrainPlanInvalid, err)
		}
		switch u.Scheme {
		case "file", "s3", "gcs", "sftp", "azure", "memory":
			// allowed
		default:
			return fmt.Errorf("%w: unsupported destination scheme %q", ErrDrainPlanInvalid, u.Scheme)
		}
	}
	if p.SigningKeyFingerprint != "" && len(p.SigningKeyFingerprint) != 64 {
		return fmt.Errorf("%w: signing_key_fingerprint must be a 64-character hex string", ErrDrainPlanInvalid)
	}
	if p.PartitionLabel != "" {
		if !partitionLabelRegex.MatchString(p.PartitionLabel) {
			return fmt.Errorf("%w: partition_label %q must look like YYYY-MM", ErrDrainPlanInvalid, p.PartitionLabel)
		}
	}
	return nil
}
