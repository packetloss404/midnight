package audit

import (
	"crypto/sha256"
	"time"
)

// CanonicalFormVersion is the only canonical-row form Lane 4 emits.
const CanonicalFormVersion = "v1"

// GenesisLabel is the panel-locked genesis seed for the chain head.
// row_hash for the first row uses prev_hash = SHA-256(GenesisLabel).
const GenesisLabel = "midnight-audit-v1"

// GenesisHash returns the panel-locked genesis prev_hash for the first
// row of any chain on a fresh install.
func GenesisHash() []byte {
	sum := sha256.Sum256([]byte(GenesisLabel))
	out := make([]byte, len(sum))
	copy(out, sum[:])
	return out
}

// Row is the in-memory shape of a single audit_logs row. Fields are
// byte-identical to the panel-locked schema in
// FEATURE_AUDIT_HASH_CHAIN.md (the "Row Schema" section).
//
// PrevHash, RowHash, and CheckpointID are populated by Append/Verify;
// callers MUST NOT set them manually.
type Row struct {
	// Sequence is the per-instance monotonic id, panel-locked bigserial.
	Sequence int64 `json:"id"`

	// Correlation
	RequestID  string    `json:"request_id"`
	TraceID    string    `json:"trace_id,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
	RecordedAt time.Time `json:"recorded_at"`

	// Actor
	ActorID    string `json:"actor_id,omitempty"`
	ActorType  string `json:"actor_type"`
	AuthMethod string `json:"auth_method,omitempty"`
	SourceIP   string `json:"source_ip,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`

	// Subject
	WorkspaceID   string `json:"workspace_id,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	ResourceKind  string `json:"resource_kind"`
	ResourceID    string `json:"resource_id,omitempty"`
	Action        string `json:"action"`

	// Payload
	Before         map[string]any `json:"before,omitempty"`
	After          map[string]any `json:"after,omitempty"`
	ReasonCode     string         `json:"reason_code,omitempty"`
	PolicyDecision string         `json:"policy_decision,omitempty"`
	Attrs          map[string]any `json:"attrs,omitempty"`

	// Integrity
	CanonicalFormVersion string `json:"canonical_form_version"`

	// Chain (populated by Append/Verify)
	PrevHash     []byte `json:"prev_hash"`
	RowHash      []byte `json:"row_hash"`
	CheckpointID int64  `json:"checkpoint_id,omitempty"`
}

// Checkpoint is the in-memory shape of a single audit_checkpoints row.
// Panel decision pinning (FEATURE_AUDIT_HASH_CHAIN.md §"audit_checkpoint
// row schema").
type Checkpoint struct {
	ID                    int64     `json:"id"`
	PartitionLabel        string    `json:"partition_label"`
	StartedAt             time.Time `json:"started_at"`
	EndedAt               time.Time `json:"ended_at"`
	FirstRowID            int64     `json:"first_row_id"`
	LastRowID             int64     `json:"last_row_id"`
	LastRowHash           []byte    `json:"last_row_hash"`
	SignatureAlg          string    `json:"signature_alg"`
	SigningKeyFingerprint string    `json:"signing_key_fingerprint"`
	Signature             []byte    `json:"signature"`
	PrevCheckpointID      int64     `json:"prev_checkpoint_id,omitempty"`
}

// JSONLRecordKind discriminates rows in the streaming JSONL export.
type JSONLRecordKind string

const (
	// RecordKindRow is a single audit_logs row.
	RecordKindRow JSONLRecordKind = "row"
	// RecordKindCheckpoint is a single audit_checkpoints row.
	RecordKindCheckpoint JSONLRecordKind = "checkpoint"
	// RecordKindManifest is the trailing manifest line at the end of the
	// stream. It contains the row count, checkpoint count, the first/last
	// checkpoint ids that bound the export, and a SHA-256 stream hash.
	RecordKindManifest JSONLRecordKind = "manifest"
)

// JSONLRecord is the on-the-wire shape of a single export line.
type JSONLRecord struct {
	Kind       JSONLRecordKind `json:"record_kind"`
	Row        *Row            `json:"row,omitempty"`
	Checkpoint *Checkpoint     `json:"checkpoint,omitempty"`
	Manifest   *Manifest       `json:"manifest,omitempty"`
}

// Manifest is the trailing record of a streaming export.
type Manifest struct {
	RowCount              int64  `json:"row_count"`
	CheckpointCount       int64  `json:"checkpoint_count"`
	StreamSHA256          string `json:"stream_sha256"` // hex; hash of all RecordKindRow + RecordKindCheckpoint canonical bytes (excluding the manifest line itself)
	FromCheckpointID      int64  `json:"from_checkpoint_id,omitempty"`
	ToCheckpointID        int64  `json:"to_checkpoint_id,omitempty"`
	GenesisHashHex        string `json:"genesis_hash"`
	CanonicalFormVersion  string `json:"canonical_form_version"`
	SigningKeyFingerprint string `json:"signing_key_fingerprint,omitempty"`
}
