// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package cloudevents

// Type is the panel-locked CloudEvents `type` enumeration. The values are
// transcribed from agile/engineering/contracts/WEBHOOK_AND_EVENT_CONTRACT.md
// section 4 and SHOULD NOT be reordered without a panel-locked amendment.
type Type string

const (
	// TypeDeploymentQueued — control plane accepted the deployment row.
	TypeDeploymentQueued Type = "dev.midnight.deployment.queued"
	// TypeDeploymentBuilding — builder accepted the build; image production started.
	TypeDeploymentBuilding Type = "dev.midnight.deployment.building"
	// TypeDeploymentDeploying — image produced; instances being scheduled.
	TypeDeploymentDeploying Type = "dev.midnight.deployment.deploying"
	// TypeDeploymentActive — promotion guard committed `state='ACTIVE'`.
	TypeDeploymentActive Type = "dev.midnight.deployment.active"
	// TypeDeploymentFailed — terminal failure with reason_code.
	TypeDeploymentFailed Type = "dev.midnight.deployment.failed"
	// TypeDeploymentSuperseded — newer deployment took over.
	TypeDeploymentSuperseded Type = "dev.midnight.deployment.superseded"
	// TypeDeploymentCanceled — cancellation completed cleanly.
	TypeDeploymentCanceled Type = "dev.midnight.deployment.canceled"

	// TypeDomainVerified — TXT + routing verification both passed.
	TypeDomainVerified Type = "dev.midnight.domain.verified"
	// TypeDomainTLSIssued — ACME certificate issued.
	TypeDomainTLSIssued Type = "dev.midnight.domain.tls_issued"
	// TypeDomainOwnershipLapsed — periodic re-verification failed; entered grace.
	TypeDomainOwnershipLapsed Type = "dev.midnight.domain.ownership_lapsed"

	// TypeBackupSucceeded — backup job completed; metadata persisted.
	TypeBackupSucceeded Type = "dev.midnight.backup.succeeded"
	// TypeBackupFailed — backup job failed; reason recorded.
	TypeBackupFailed Type = "dev.midnight.backup.failed"

	// TypeAuditCheckpoint — daily signed audit checkpoint written.
	TypeAuditCheckpoint Type = "dev.midnight.audit.checkpoint"

	// TypeHostHeartbeatLost — agent heartbeat missing past threshold.
	TypeHostHeartbeatLost Type = "dev.midnight.host.heartbeat_lost"

	// TypeOperationCompleted — Operation reached SUCCEEDED.
	TypeOperationCompleted Type = "dev.midnight.operation.completed"
	// TypeOperationFailed — Operation reached FAILED.
	TypeOperationFailed Type = "dev.midnight.operation.failed"
)

// catalogEntry pairs a panel-locked Type with its since-version metadata.
type catalogEntry struct {
	Type  Type
	Since string
}

// catalog is the ordered slice of panel-locked types. Iteration order is
// stable and matches WEBHOOK_AND_EVENT_CONTRACT.md.
var catalog = []catalogEntry{
	{TypeDeploymentQueued, "v1alpha"},
	{TypeDeploymentBuilding, "v1alpha"},
	{TypeDeploymentDeploying, "v1alpha"},
	{TypeDeploymentActive, "v1alpha"},
	{TypeDeploymentFailed, "v1alpha"},
	{TypeDeploymentSuperseded, "v1alpha"},
	{TypeDeploymentCanceled, "v1alpha"},
	{TypeDomainVerified, "v1alpha"},
	{TypeDomainTLSIssued, "v1alpha"},
	{TypeDomainOwnershipLapsed, "v1alpha"},
	{TypeBackupSucceeded, "v1alpha"},
	{TypeBackupFailed, "v1alpha"},
	{TypeAuditCheckpoint, "v1alpha"},
	{TypeHostHeartbeatLost, "v1alpha"},
	{TypeOperationCompleted, "v1alpha"},
	{TypeOperationFailed, "v1alpha"},
}

// catalogIndex backs IsRegistered without allocating per-call.
var catalogIndex = func() map[Type]string {
	out := make(map[Type]string, len(catalog))
	for _, entry := range catalog {
		out[entry.Type] = entry.Since
	}
	return out
}()

// IsRegistered reports whether the supplied type appears in the locked
// catalog. Unknown types are rejected by ValidateEvent.
func IsRegistered(t Type) bool {
	_, ok := catalogIndex[t]
	return ok
}

// SinceVersion returns the v1alpha-or-later cadence label for the
// supplied type. The empty string is returned for unknown types.
func SinceVersion(t Type) string {
	return catalogIndex[t]
}

// All returns the catalog in its panel-locked declaration order.
func All() []Type {
	out := make([]Type, 0, len(catalog))
	for _, entry := range catalog {
		out = append(out, entry.Type)
	}
	return out
}
