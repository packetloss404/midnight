package registry

import (
	"errors"
	"fmt"
	"time"
)

// IncrRefInput is the input for PinStore.IncrRef. The deployment id and
// reason combine with the digest as the primary-key tuple, so duplicate
// IncrRef calls with the same tuple are rejected.
type IncrRefInput struct {
	Repository   string
	Digest       string
	ServiceID    string
	DeploymentID string
	Reason       PinReason
	PinnedUntil  time.Time
	CreatedAt    time.Time
}

// IncrRef inserts a new pin row for the supplied digest. It returns the row
// that was inserted. The store enforces the panel-locked invariant that the
// (digest, service, deployment, reason) tuple is unique.
func (s *PinStore) IncrRef(input IncrRefInput) (PinRow, error) {
	if input.Repository == "" {
		return PinRow{}, errors.New("pin IncrRef repository must not be empty")
	}
	row := PinRow{
		Digest:       input.Digest,
		Repository:   input.Repository,
		ServiceID:    input.ServiceID,
		DeploymentID: input.DeploymentID,
		Reason:       input.Reason,
		PinnedUntil:  input.PinnedUntil,
		CreatedAt:    input.CreatedAt,
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	if err := s.Insert(row); err != nil {
		return PinRow{}, err
	}
	return row, nil
}

// DecrRef removes the pin row matching the supplied tuple. It returns
// ErrPinNotFound if no row matches. DecrRef is the offline analog of the
// reference-decrement step the cleanup worker runs when a deployment leaves
// the rollback window.
func (s *PinStore) DecrRef(digest, serviceID, deploymentID string, reason PinReason) error {
	if digest == "" || serviceID == "" || deploymentID == "" {
		return errors.New("pin DecrRef requires digest, serviceId, and deploymentId")
	}
	return s.Decrement(digest, serviceID, deploymentID, reason)
}

// ProtectDuringRollbackWindowInput is the input for the panel-locked
// rollback-window protection helper.
type ProtectDuringRollbackWindowInput struct {
	Repository   string
	Digest       string
	ServiceID    string
	DeploymentID string
	// Now is the reference clock used to compute pinnedUntil.
	Now time.Time
	// Window is the rollback window duration. If zero, the panel-locked
	// default of 14 days applies.
	Window time.Duration
}

// PanelLockedRollbackWindow is the panel-locked default rollback window
// applied when the operator does not override it.
const PanelLockedRollbackWindow = 14 * 24 * time.Hour

// ProtectDuringRollbackWindow inserts a rollback-window pin row for the
// supplied digest. The pin's pinnedUntil is computed as Now+Window so the
// PinCount probe reports the digest as protected throughout the rollback
// window.
func (s *PinStore) ProtectDuringRollbackWindow(input ProtectDuringRollbackWindowInput) (PinRow, error) {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	window := input.Window
	if window <= 0 {
		window = PanelLockedRollbackWindow
	}
	return s.IncrRef(IncrRefInput{
		Repository:   input.Repository,
		Digest:       input.Digest,
		ServiceID:    input.ServiceID,
		DeploymentID: input.DeploymentID,
		Reason:       PinReasonRollbackWindow,
		PinnedUntil:  now.Add(window),
		CreatedAt:    now,
	})
}

// ApplyProtectedDigestProbe runs the gate the registry GC must apply before
// any destructive deletion. It returns nil when the digest may be reclaimed
// and an error containing the protection reasons when it must not.
func (s *PinStore) ApplyProtectedDigestProbe(repository, digest string, now time.Time) error {
	if digest == "" {
		return errors.New("pin probe digest must not be empty")
	}
	probe := s.ProbeProtectedDigest(repository, digest, now)
	if !probe.Protected {
		return nil
	}
	reasons := s.ReasonsFor(digest, now)
	return fmt.Errorf("pin probe refused GC: digest %s repository %s is protected by %v", digest, repository, reasons)
}
