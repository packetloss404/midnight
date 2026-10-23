package secrets

import (
	"context"
	"errors"
	"fmt"
)

// ErrRotationInProgress is returned when a concurrent rotation is in
// flight and the caller should retry.
var ErrRotationInProgress = errors.New("midnight: rotation in progress")

// RotationReport describes a single KEK-rotation pass. Audit consumers
// persist the entire report; no plaintext is ever included.
type RotationReport struct {
	PreviousKEK   KEKID
	NewKEK        KEKID
	Rewrapped     int
	UnchangedDEKs int
	FailedIDs     []VariableID
}

// RotateKEK rotates the active KEK, re-wraps every persisted Envelope
// under the new KEK, and returns a redaction-safe RotationReport. The
// previous KEK is retained according to the KeyProvider's grace policy.
//
// Panel decision pinning (line 118):
//
//	"Rotation generates KEK_n+1, rewraps all DEKs in a background
//	 operations job with progress, retains KEK_n for
//	 secret_kek_grace_days (default 30)."
//
// The implementation in Lane 4 is synchronous (no operations job) — it
// is the building block the operations-job worker will call. Callers
// MUST emit an audit row for this report (panel: line 118 audit hook).
func (vlt *Vault) RotateKEK(ctx context.Context) (RotationReport, error) {
	vlt.mu.Lock()
	defer vlt.mu.Unlock()
	newID, prevID, err := vlt.kp.Rotate(ctx)
	if err != nil {
		return RotationReport{}, fmt.Errorf("midnight: rotate kek: %w", err)
	}
	report := RotationReport{PreviousKEK: prevID, NewKEK: newID}
	var variables map[VariableID]*Variable
	if vlt.store != nil {
		persisted, listErr := vlt.store.List(ctx)
		if listErr != nil {
			return report, fmt.Errorf("midnight: list variables for kek rotation: %w", listErr)
		}
		variables = make(map[VariableID]*Variable, len(persisted))
		for _, record := range persisted {
			variables[record.Meta.ID] = vlt.variableFromPersisted(ctx, record)
		}
	} else {
		variables = vlt.byID
	}
	for id, v := range variables {
		if v.IsReference() {
			report.UnchangedDEKs++
			continue
		}
		if v.envelope.KEKID == newID {
			report.UnchangedDEKs++
			continue
		}
		rewrapped, err := ReWrap(ctx, vlt.kp, v.envelope)
		if err != nil {
			report.FailedIDs = append(report.FailedIDs, id)
			continue
		}
		v.envelope = rewrapped
		if v.sealed != nil {
			v.sealed.mu.Lock()
			v.sealed.envelope = rewrapped
			v.sealed.mu.Unlock()
		}
		fp, err := vlt.kp.Fingerprint(ctx)
		if err == nil {
			v.meta.KEKFingerprint = fp
			if v.sealed != nil {
				v.sealed.mu.Lock()
				v.sealed.meta.KEKFingerprint = fp
				v.sealed.mu.Unlock()
			}
		}
		if vlt.store != nil {
			persisted := PersistedVariable{Meta: v.meta, Envelope: v.envelope}
			var err error
			if lifecycle, ok := vlt.store.(LifecycleEnvelopeStore); ok {
				err = lifecycle.PutCAS(ctx, persisted, v.meta.Version)
			} else {
				err = vlt.store.Put(ctx, persisted)
			}
			if err != nil {
				report.FailedIDs = append(report.FailedIDs, id)
				continue
			}
		}
		vlt.byID[id] = v
		report.Rewrapped++
	}
	if len(report.FailedIDs) > 0 {
		return report, fmt.Errorf("midnight: rotate failed for %d variable(s)", len(report.FailedIDs))
	}
	return report, nil
}

// PurgeExpiredKEKs drops KEKs whose grace window has elapsed. Returns
// the ids that were purged; never returns plaintext.
func (vlt *Vault) PurgeExpiredKEKs(ctx context.Context) ([]KEKID, error) {
	vlt.mu.Lock()
	defer vlt.mu.Unlock()
	return vlt.kp.PurgeExpired(ctx)
}
