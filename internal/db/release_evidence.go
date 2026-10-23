package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	ReleaseEvidenceStatusReady       = "ready"
	ReleaseEvidenceStatusNeedsReview = "needs-review"
	ReleaseEvidenceStatusBlocked     = "blocked"

	ReleaseEvidenceIssuePendingMigration       = "MIGRATION_PENDING"
	ReleaseEvidenceIssueMigrationGap           = "MIGRATION_GAP"
	ReleaseEvidenceIssueUnsupportedSQL         = "SCHEMA_SQL_UNSUPPORTED"
	ReleaseEvidenceIssueInvalidSQL             = "SCHEMA_SQL_INVALID"
	ReleaseEvidenceIssueChecksumPlaceholder    = "MIGRATION_CHECKSUM_PLACEHOLDER"
	ReleaseEvidenceIssueSchemaStatusBlocked    = "SCHEMA_STATUS_BLOCKED"
	ReleaseEvidenceIssueMigrationStatusBlocked = "MIGRATION_STATUS_BLOCKED"
)

var ErrInvalidReleaseEvidencePacket = errors.New("invalid release evidence packet")

type ReleaseEvidencePacket struct {
	SchemaVersion int                    `json:"schema_version"`
	LatestVersion int                    `json:"latest_version"`
	Status        string                 `json:"status"`
	IssueCodes    []string               `json:"issue_codes"`
	Issues        []ReleaseEvidenceIssue `json:"issues"`
	Schema        SchemaEvidence         `json:"schema"`
	MigrationPlan MigrationExecutionPlan `json:"migration_plan"`
}

type ReleaseEvidenceIssue struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func BuildReleaseEvidencePacket(schema SchemaEvidence, plan MigrationExecutionPlan) ReleaseEvidencePacket {
	packet := ReleaseEvidencePacket{
		SchemaVersion: schema.SchemaVersion,
		LatestVersion: schema.LatestVersion,
		Status:        ReleaseEvidenceStatusReady,
		IssueCodes:    []string{},
		Issues:        []ReleaseEvidenceIssue{},
		Schema:        schema,
		MigrationPlan: plan,
	}
	if plan.LatestVersion > packet.LatestVersion {
		packet.LatestVersion = plan.LatestVersion
	}
	if plan.SchemaVersion > packet.SchemaVersion {
		packet.SchemaVersion = plan.SchemaVersion
	}

	if schema.UnsupportedStatementCount > 0 {
		packet.addIssue(ReleaseEvidenceIssueUnsupportedSQL, "schema evidence contains unsupported SQL statements", map[string]any{"count": schema.UnsupportedStatementCount})
	}
	if schema.InvalidStatementCount > 0 {
		packet.addIssue(ReleaseEvidenceIssueInvalidSQL, "schema evidence contains invalid SQL statements", map[string]any{"count": schema.InvalidStatementCount})
	}
	for _, migration := range schema.Migrations {
		if migration.ChecksumPlaceholder {
			packet.addIssue(ReleaseEvidenceIssueChecksumPlaceholder, "migration uses checksum placeholder evidence", map[string]any{"version": migration.Version, "name": migration.Name})
		}
	}
	for _, migration := range plan.PendingMigrations {
		packet.addIssue(ReleaseEvidenceIssuePendingMigration, "migration is pending", map[string]any{"version": migration.Version, "name": migration.Name})
	}
	for _, gap := range plan.BlockedGaps {
		packet.addIssue(ReleaseEvidenceIssueMigrationGap, gap.Reason, map[string]any{"version": gap.Version, "name": gap.Name, "blocked_by_version": gap.BlockedByVersion})
	}
	if schema.Status == SchemaEvidenceStatusBlocked {
		packet.addIssue(ReleaseEvidenceIssueSchemaStatusBlocked, "schema evidence status is blocked", nil)
	}
	if plan.Status == MigrationPlanStatusBlocked {
		packet.addIssue(ReleaseEvidenceIssueMigrationStatusBlocked, "migration plan status is blocked", nil)
	}

	packet.Status = rollupReleaseEvidenceStatus(schema.Status, plan.Status)
	if packet.Status == ReleaseEvidenceStatusReady && len(packet.Issues) > 0 {
		packet.Status = ReleaseEvidenceStatusNeedsReview
	}
	return packet
}

func LoadReleaseEvidencePacket(fsys fs.FS, manifestPath string, appliedVersions []int) (ReleaseEvidencePacket, error) {
	schema, err := LoadSchemaEvidence(fsys, manifestPath, appliedVersions)
	if err != nil {
		return ReleaseEvidencePacket{}, err
	}
	plan, err := LoadMigrationExecutionPlan(fsys, manifestPath, appliedVersions)
	if err != nil {
		return ReleaseEvidencePacket{}, err
	}
	return BuildReleaseEvidencePacket(schema, plan), nil
}

func RenderReleaseEvidencePacketJSON(packet ReleaseEvidencePacket) ([]byte, error) {
	data, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render release evidence packet: %w", err)
	}
	return append(data, '\n'), nil
}

func WriteReleaseEvidencePacketJSON(path string, packet ReleaseEvidencePacket) error {
	if err := validateReleaseEvidenceJSONPath(path); err != nil {
		return err
	}
	data, err := RenderReleaseEvidencePacketJSON(packet)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func ReadReleaseEvidencePacketJSON(path string) (ReleaseEvidencePacket, error) {
	if err := validateReleaseEvidenceJSONPath(path); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReleaseEvidencePacket{}, err
	}
	return DecodeReleaseEvidencePacketJSON(data)
}

func DecodeReleaseEvidencePacketJSON(data []byte) (ReleaseEvidencePacket, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var packet ReleaseEvidencePacket
	if err := decoder.Decode(&packet); err != nil {
		return ReleaseEvidencePacket{}, fmt.Errorf("%w: %v", ErrInvalidReleaseEvidencePacket, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ReleaseEvidencePacket{}, fmt.Errorf("%w: trailing JSON content", ErrInvalidReleaseEvidencePacket)
	}
	return packet, nil
}

func validateReleaseEvidenceJSONPath(path string) error {
	if filepath.Ext(path) != ".json" || filepath.Base(path) == ".json" {
		return fmt.Errorf("%w: release evidence path must end with .json", ErrInvalidReleaseEvidencePacket)
	}
	return nil
}

func (packet *ReleaseEvidencePacket) addIssue(code, message string, details map[string]any) {
	packet.IssueCodes = append(packet.IssueCodes, code)
	packet.Issues = append(packet.Issues, ReleaseEvidenceIssue{Code: code, Message: message, Details: details})
}

func rollupReleaseEvidenceStatus(schemaStatus, planStatus string) string {
	if schemaStatus == SchemaEvidenceStatusBlocked || planStatus == MigrationPlanStatusBlocked {
		return ReleaseEvidenceStatusBlocked
	}
	if schemaStatus == SchemaEvidenceStatusNeedsReview || planStatus == MigrationPlanStatusNeedsReview {
		return ReleaseEvidenceStatusNeedsReview
	}
	return ReleaseEvidenceStatusReady
}
