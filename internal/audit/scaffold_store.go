package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// LedgerSchemaVersion is the audit-evidence JSON schema for dashboard /
// planner demos (distinct from canonical Row hashing).
const LedgerSchemaVersion = "midnight.audit.v1alpha1"

// DashboardLedgerEvent is a simplified ledger row used only by the scaffold Store for
// dashboard summaries (not the tamper-evident MemoryChain Row model).
type DashboardLedgerEvent struct {
	ID                   int64             `json:"id"`
	OccurredAt           time.Time         `json:"occurred_at"`
	Actor                string            `json:"actor"`
	Action               string            `json:"action"`
	Resource             string            `json:"resource"`
	Outcome              string            `json:"outcome"`
	Payload              map[string]string `json:"payload,omitempty"`
	PrevHash             string            `json:"prev_hash"`
	RowHash              string            `json:"row_hash"`
	CheckpointID         string            `json:"checkpoint_id,omitempty"`
	CanonicalFormVersion string            `json:"canonical_form_version"`
}

// Filter narrows audit listings and enterprise export plans.
type Filter struct {
	Actor  string    `json:"actor,omitempty"`
	Action string    `json:"action,omitempty"`
	Since  time.Time `json:"since,omitempty"`
	Until  time.Time `json:"until,omitempty"`
}

type LedgerQueryResult struct {
	Events     []DashboardLedgerEvent `json:"events"`
	NextCursor int64                  `json:"next_cursor,omitempty"`
	Truncated  bool                   `json:"truncated,omitempty"`
}

type DrainType string

const (
	DrainTypeHTTP   DrainType = "http"
	DrainTypeSyslog DrainType = "syslog"
)

type DrainConfig struct {
	ID          string    `json:"id"`
	Type        DrainType `json:"type"`
	Enabled     bool      `json:"enabled"`
	Endpoint    string    `json:"endpoint"`
	Format      string    `json:"format"`
	Status      string    `json:"status"`
	LastDrainID int64     `json:"last_drain_id"`
}

type Evidence struct {
	SchemaVersion string        `json:"schema_version"`
	Status        string        `json:"status"`
	EventCount    int           `json:"event_count"`
	FirstRowHash  string        `json:"first_row_hash,omitempty"`
	LastRowHash   string        `json:"last_row_hash,omitempty"`
	DrainCount    int           `json:"drain_count"`
	Drains        []DrainConfig `json:"drains"`
	GeneratedAt   time.Time     `json:"generated_at"`
}

type ExportManifest struct {
	Kind          string    `json:"kind"`
	SchemaVersion string    `json:"schema_version"`
	TotalRows     int       `json:"total_rows"`
	FirstRowHash  string    `json:"first_row_hash,omitempty"`
	LastRowHash   string    `json:"last_row_hash,omitempty"`
	StreamSHA256  string    `json:"stream_sha256"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// Store holds dashboard/demo ledger rows separate from MemoryChain.
type Store struct {
	mu     sync.Mutex
	events []DashboardLedgerEvent
	drains []DrainConfig
}

func NewEmptyLedgerStore() *Store {
	return &Store{}
}

func NewDemoLedgerStore() *Store {
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	return &Store{
		events: []DashboardLedgerEvent{
			{ID: 1, OccurredAt: base, Actor: "system", Action: "deployment.created", Resource: "deployment/dep-a", Outcome: "success", Payload: map[string]string{"tenant": "acme"}, CheckpointID: "chk_20260426", RowHash: "sha256:aaa"},
			{ID: 2, OccurredAt: base.Add(time.Minute), Actor: "operator", Action: "audit.exported", Resource: "audit/export", Outcome: "success", Payload: map[string]string{"format": "ndjson"}, CheckpointID: "chk_20260426", RowHash: "sha256:bbb"},
			{ID: 3, OccurredAt: base.Add(2 * time.Minute), Actor: "system", Action: "siem.drain.delivered", Resource: "drain/http-primary", Outcome: "success", Payload: map[string]string{"format": "jsonl"}, CheckpointID: "chk_20260426", RowHash: "sha256:ccc"},
		},
		drains: []DrainConfig{
			{ID: "http-primary", Type: DrainTypeHTTP, Enabled: true, Endpoint: "https://siem.example.invalid/midnight/audit", Format: "jsonl", Status: "ready", LastDrainID: 3},
		},
	}
}

// Evidence returns dashboard-facing ledger summary JSON (demo data).
func (store *Store) Evidence(now time.Time) Evidence {
	if store == nil {
		return Evidence{}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	evidence := Evidence{
		SchemaVersion: LedgerSchemaVersion,
		Status:        "ready",
		EventCount:    len(store.events),
		DrainCount:    len(store.drains),
		Drains:        append([]DrainConfig(nil), store.drains...),
		GeneratedAt:   now.UTC(),
	}
	if len(store.events) > 0 {
		evidence.FirstRowHash = store.events[0].RowHash
		evidence.LastRowHash = store.events[len(store.events)-1].RowHash
	}
	for _, drain := range store.drains {
		if drain.Enabled && drain.Status != "ready" {
			evidence.Status = "needs-review"
		}
	}
	return evidence
}

func (store *Store) Events(limit int) LedgerQueryResult {
	if store == nil {
		return LedgerQueryResult{}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	events := append([]DashboardLedgerEvent(nil), store.events...)
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	nextCursor := int64(0)
	if len(events) > 0 {
		nextCursor = events[len(events)-1].ID
	}
	return LedgerQueryResult{
		Events:     events,
		NextCursor: nextCursor,
		Truncated:  limit > 0 && len(store.events) > limit,
	}
}

func (store *Store) Drains() []DrainConfig {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]DrainConfig(nil), store.drains...)
}

func (store *Store) Export(now time.Time) ([]byte, ExportManifest) {
	if store == nil {
		return nil, ExportManifest{}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	stream := make([]byte, 0)
	for _, event := range store.events {
		line, err := json.Marshal(event)
		if err != nil {
			continue
		}
		stream = append(stream, line...)
		stream = append(stream, '\n')
	}
	sum := sha256.Sum256(stream)
	manifest := ExportManifest{
		Kind:          "audit-export",
		SchemaVersion: LedgerSchemaVersion,
		TotalRows:     len(store.events),
		StreamSHA256:  "sha256:" + hex.EncodeToString(sum[:]),
		GeneratedAt:   now.UTC(),
	}
	if len(store.events) > 0 {
		manifest.FirstRowHash = store.events[0].RowHash
		manifest.LastRowHash = store.events[len(store.events)-1].RowHash
	}
	return stream, manifest
}
