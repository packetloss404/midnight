package db

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
)

const (
	MigrationPlanStatusReady       = "ready"
	MigrationPlanStatusNeedsReview = "needs-review"
	MigrationPlanStatusBlocked     = "blocked"
)

type MigrationExecutionPlan struct {
	SchemaVersion     int                    `json:"schema_version"`
	LatestVersion     int                    `json:"latest_version"`
	Status            string                 `json:"status"`
	AppliedVersions   []int                  `json:"applied_versions"`
	PendingMigrations []MigrationPlanStep    `json:"pending_migrations"`
	BlockedGaps       []MigrationPlanBlocked `json:"blocked_gaps"`
}

type MigrationPlanStep struct {
	Version             int    `json:"version"`
	Name                string `json:"name"`
	Up                  string `json:"up"`
	Down                string `json:"down"`
	ChecksumPlaceholder bool   `json:"checksum_placeholder"`
}

type MigrationPlanBlocked struct {
	Version          int    `json:"version"`
	Name             string `json:"name"`
	Reason           string `json:"reason"`
	BlockedByVersion int    `json:"blocked_by_version"`
}

func LoadMigrationExecutionPlan(fsys fs.FS, manifestPath string, appliedVersions []int) (MigrationExecutionPlan, error) {
	manifest, err := LoadMigrationManifest(fsys, manifestPath)
	if err != nil {
		return MigrationExecutionPlan{}, err
	}
	base := path.Dir(manifestPath)
	for _, migration := range manifest.Migrations {
		if _, err := fs.Stat(fsys, path.Join(base, migration.Up)); err != nil {
			return MigrationExecutionPlan{}, err
		}
		if _, err := fs.Stat(fsys, path.Join(base, migration.Down)); err != nil {
			return MigrationExecutionPlan{}, err
		}
	}
	return BuildMigrationExecutionPlan(manifest, appliedVersions)
}

func BuildMigrationExecutionPlan(manifest MigrationManifest, appliedVersions []int) (MigrationExecutionPlan, error) {
	evidence, err := BuildSchemaEvidence(manifest, appliedVersions)
	if err != nil {
		return MigrationExecutionPlan{}, err
	}

	appliedSet := make(map[int]struct{}, len(appliedVersions))
	for _, version := range appliedVersions {
		appliedSet[version] = struct{}{}
	}
	orderedApplied := make([]int, 0, len(appliedVersions))
	orderedApplied = append(orderedApplied, appliedVersions...)
	sort.Ints(orderedApplied)

	plan := MigrationExecutionPlan{
		SchemaVersion:     evidence.SchemaVersion,
		LatestVersion:     evidence.LatestVersion,
		Status:            MigrationPlanStatusReady,
		AppliedVersions:   orderedApplied,
		PendingMigrations: []MigrationPlanStep{},
		BlockedGaps:       []MigrationPlanBlocked{},
	}

	maxApplied := 0
	for _, version := range orderedApplied {
		if version > maxApplied {
			maxApplied = version
		}
	}
	for _, migration := range evidence.Migrations {
		if _, ok := appliedSet[migration.Version]; ok {
			continue
		}
		step := MigrationPlanStep{
			Version:             migration.Version,
			Name:                migration.Name,
			Up:                  migration.Up,
			Down:                migration.Down,
			ChecksumPlaceholder: migration.ChecksumPlaceholder,
		}
		plan.PendingMigrations = append(plan.PendingMigrations, step)
		if migration.Version < maxApplied {
			plan.BlockedGaps = append(plan.BlockedGaps, MigrationPlanBlocked{
				Version:          migration.Version,
				Name:             migration.Name,
				Reason:           "pending migration precedes an applied migration",
				BlockedByVersion: maxApplied,
			})
		}
	}

	if len(plan.BlockedGaps) > 0 {
		plan.Status = MigrationPlanStatusBlocked
	} else if len(plan.PendingMigrations) > 0 {
		plan.Status = MigrationPlanStatusNeedsReview
	}
	return plan, nil
}

func RenderMigrationExecutionPlanJSON(plan MigrationExecutionPlan) ([]byte, error) {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render migration execution plan: %w", err)
	}
	return append(data, '\n'), nil
}
