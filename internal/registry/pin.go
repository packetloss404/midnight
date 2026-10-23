package registry

import (
	"fmt"
	"sort"
	"time"
)

// PinState identifies the lifecycle phase of a registry pin entry.
type PinState string

const (
	// PinStateActive marks the digest currently serving traffic for a
	// service.
	PinStateActive PinState = "active"
	// PinStateSuperseded marks a digest that previously served traffic and
	// is no longer pinned by an active deployment.
	PinStateSuperseded PinState = "superseded"
	// PinStateRollback marks a digest still inside the rollback window.
	PinStateRollback PinState = "rollback"
)

// PinReason mirrors the deployment_image_pins.reason column. Each row in the
// pin table carries one reason; a single digest may have multiple rows for
// distinct reasons.
type PinReason string

const (
	// PinReasonActive pins the currently-active deployment digest.
	PinReasonActive PinReason = "active"
	// PinReasonRollbackWindow pins a digest within the configured rollback
	// window.
	PinReasonRollbackWindow PinReason = "rollback_window"
	// PinReasonPromotion pins digests recorded by an environment-promotion
	// record.
	PinReasonPromotion PinReason = "promotion"
	// PinReasonRestoreManifest pins digests referenced by a backup restore
	// manifest.
	PinReasonRestoreManifest PinReason = "restore_manifest"
	// PinReasonAuditRetention pins digests retained for audit purposes.
	PinReasonAuditRetention PinReason = "audit_retention"
)

// PinTransition describes the desired pin updates produced by promoting a new
// active digest. The previous digest is moved into rollback or superseded
// state depending on whether a rollback window is supplied.
type PinTransition struct {
	Repository     string
	PreviousDigest string
	ActiveDigest   string
	RollbackUntil  time.Time
}

// RegistryPin is a single in-memory pin row produced by TransitionPins. It is
// the offline analog of the deployment_image_pins table.
type RegistryPin struct {
	Repository    string
	Digest        string
	State         PinState
	RollbackUntil time.Time
}

// PinRow is the reference-counted pin row used by the Phase 1 pin store.
// Compared to RegistryPin it carries the deployment id, an explicit reason,
// and an optional pinned-until timestamp.
type PinRow struct {
	Digest       string    `json:"digest"`
	Repository   string    `json:"repository"`
	ServiceID    string    `json:"serviceId"`
	DeploymentID string    `json:"deploymentId"`
	Reason       PinReason `json:"reason"`
	PinnedUntil  time.Time `json:"pinnedUntil,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// PinStore is the in-memory implementation of the deployment_image_pins
// reference-count table. All operations are deterministic and pure; callers
// supply the time used by guard checks.
type PinStore struct {
	rows []PinRow
}

// NewPinStore returns an empty pin store.
func NewPinStore() *PinStore {
	return &PinStore{}
}

// Insert adds a pin row. Duplicate (digest, service, deployment, reason)
// rows are rejected because the panel-locked schema forbids them.
func (s *PinStore) Insert(row PinRow) error {
	if err := validatePinRow(row); err != nil {
		return err
	}
	for _, existing := range s.rows {
		if pinPrimaryKeyEqual(existing, row) {
			return fmt.Errorf("pin row already exists for digest %q service %q deployment %q reason %q", row.Digest, row.ServiceID, row.DeploymentID, row.Reason)
		}
	}
	s.rows = append(s.rows, row)
	return nil
}

// Decrement removes the pin row matching the supplied primary key. It is the
// pin-decrement step the cleanup worker runs when a deployment leaves the
// rollback window.
func (s *PinStore) Decrement(digest, serviceID, deploymentID string, reason PinReason) error {
	for i, row := range s.rows {
		if row.Digest == digest && row.ServiceID == serviceID && row.DeploymentID == deploymentID && row.Reason == reason {
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("pin row not found for digest %q service %q deployment %q reason %q", digest, serviceID, deploymentID, reason)
}

// Rows returns a snapshot of the stored pin rows in insertion order.
func (s *PinStore) Rows() []PinRow {
	out := make([]PinRow, len(s.rows))
	copy(out, s.rows)
	return out
}

// PinCount returns the number of currently-effective pins for the supplied
// digest. Rows whose pinnedUntil has elapsed are not counted.
func (s *PinStore) PinCount(digest string, now time.Time) int {
	count := 0
	for _, row := range s.rows {
		if row.Digest != digest {
			continue
		}
		if !row.PinnedUntil.IsZero() && row.PinnedUntil.Before(now) {
			continue
		}
		count++
	}
	return count
}

// ReasonsFor returns the deduplicated, sorted reason set protecting the
// supplied digest.
func (s *PinStore) ReasonsFor(digest string, now time.Time) []PinReason {
	seen := map[PinReason]struct{}{}
	for _, row := range s.rows {
		if row.Digest != digest {
			continue
		}
		if !row.PinnedUntil.IsZero() && row.PinnedUntil.Before(now) {
			continue
		}
		seen[row.Reason] = struct{}{}
	}
	out := make([]PinReason, 0, len(seen))
	for reason := range seen {
		out = append(out, reason)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ProbeProtectedDigest returns a probe result describing whether the
// requested digest is protected by any active pin or by membership of the
// active rollback window.
func (s *PinStore) ProbeProtectedDigest(repository, digest string, now time.Time) ProtectedDigestProbeResult {
	result := ProtectedDigestProbeResult{Repository: repository, Digest: digest, ProbedAt: now}
	reasons := s.ReasonsFor(digest, now)
	if len(reasons) == 0 {
		return result
	}
	result.Protected = true
	result.Reason = string(reasons[0])
	return result
}

// RefuseGCIfReferenced returns an error when the supplied digest is
// referenced anywhere in the active rollback window or by any other reason
// that prevents reclamation. It is the helper a registry GC implementation
// calls before deleting blobs.
func (s *PinStore) RefuseGCIfReferenced(digest string, now time.Time) error {
	if s.PinCount(digest, now) == 0 {
		return nil
	}
	reasons := s.ReasonsFor(digest, now)
	return fmt.Errorf("registry GC refused: digest %s is pinned by %v", digest, reasons)
}

func validatePinRow(row PinRow) error {
	if row.Digest == "" {
		return fmt.Errorf("pin row digest must not be empty")
	}
	if row.ServiceID == "" {
		return fmt.Errorf("pin row serviceId must not be empty")
	}
	if row.DeploymentID == "" {
		return fmt.Errorf("pin row deploymentId must not be empty")
	}
	switch row.Reason {
	case PinReasonActive, PinReasonRollbackWindow, PinReasonPromotion, PinReasonRestoreManifest, PinReasonAuditRetention:
	default:
		return fmt.Errorf("pin row reason %q is not supported", row.Reason)
	}
	return nil
}

func pinPrimaryKeyEqual(a, b PinRow) bool {
	return a.Digest == b.Digest && a.ServiceID == b.ServiceID && a.DeploymentID == b.DeploymentID && a.Reason == b.Reason
}

// ProtectedDigestProbeResult records the outcome of a digest-protection probe.
type ProtectedDigestProbeResult struct {
	Repository string
	Digest     string
	Protected  bool
	Reason     string
	ProbedAt   time.Time
}

// TransitionPins is the legacy pin-state computation used by S04 hand-off.
// New code should use the PinStore reference-count helpers above instead.
func TransitionPins(transition PinTransition) []RegistryPin {
	pins := make([]RegistryPin, 0, 2)
	if transition.ActiveDigest != "" {
		pins = append(pins, RegistryPin{
			Repository: transition.Repository,
			Digest:     transition.ActiveDigest,
			State:      PinStateActive,
		})
	}
	if transition.PreviousDigest != "" && transition.PreviousDigest != transition.ActiveDigest {
		state := PinStateSuperseded
		if !transition.RollbackUntil.IsZero() {
			state = PinStateRollback
		}
		pins = append(pins, RegistryPin{
			Repository:    transition.Repository,
			Digest:        transition.PreviousDigest,
			State:         state,
			RollbackUntil: transition.RollbackUntil,
		})
	}
	return pins
}
