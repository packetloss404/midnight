package db

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

const (
	SchemaEvidenceStatusReady       = "ready"
	SchemaEvidenceStatusNeedsReview = "needs-review"
	SchemaEvidenceStatusBlocked     = "blocked"
)

type SchemaEvidence struct {
	SchemaVersion             int                        `json:"schema_version"`
	LatestVersion             int                        `json:"latest_version"`
	MigrationCount            int                        `json:"migration_count"`
	AppliedCount              int                        `json:"applied_count"`
	PendingCount              int                        `json:"pending_count"`
	Status                    string                     `json:"status"`
	DetectedTables            []string                   `json:"detected_tables"`
	DetectedObjects           []SchemaObjectEvidence     `json:"detected_objects"`
	DetectedTableCount        int                        `json:"detected_table_count"`
	DetectedIndexCount        int                        `json:"detected_index_count"`
	DetectedPartitionCount    int                        `json:"detected_partition_count"`
	DetectedColumnCount       int                        `json:"detected_column_count"`
	Tables                    []TableEvidence            `json:"tables,omitempty"`
	Indexes                   []IndexEvidence            `json:"indexes,omitempty"`
	Partitions                []PartitionEvidence        `json:"partitions,omitempty"`
	UnsupportedStatementCount int                        `json:"unsupported_statement_count"`
	InvalidStatementCount     int                        `json:"invalid_statement_count"`
	Migrations                []SchemaMigrationEvidence  `json:"migrations"`
	SourceFiles               []SchemaSourceFileEvidence `json:"source_files"`
}

type SchemaObjectEvidence struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type SchemaMigrationEvidence struct {
	Version             int    `json:"version"`
	Name                string `json:"name"`
	Up                  string `json:"up"`
	Down                string `json:"down"`
	Applied             bool   `json:"applied"`
	ChecksumPlaceholder bool   `json:"checksum_placeholder"`
}

type SchemaSourceFileEvidence struct {
	Filename  string `json:"filename"`
	Kind      string `json:"kind,omitempty"`
	ByteCount int    `json:"byte_count"`
	SHA256    string `json:"sha256"`
}

func LoadSchemaEvidence(fsys fs.FS, manifestPath string, appliedVersions []int) (SchemaEvidence, error) {
	manifest, err := LoadMigrationManifest(fsys, manifestPath)
	if err != nil {
		return SchemaEvidence{}, err
	}
	report, err := BuildSchemaEvidence(manifest, appliedVersions)
	if err != nil {
		return SchemaEvidence{}, err
	}
	base := path.Dir(manifestPath)
	for _, migration := range manifest.Migrations {
		upPath := path.Join(base, migration.Up)
		data, err := fs.ReadFile(fsys, upPath)
		if err != nil {
			return SchemaEvidence{}, err
		}
		report.AddSourceFileEvidence(upPath, data)
		report.AddSQLSubsetEvidence(string(data))

		downPath := path.Join(base, migration.Down)
		data, err = fs.ReadFile(fsys, downPath)
		if err != nil {
			return SchemaEvidence{}, err
		}
		report.AddSourceFileEvidence(downPath, data)
	}
	report.finalizeSQLSubsetEvidence()
	return report, nil
}

func BuildSchemaEvidence(manifest MigrationManifest, appliedVersions []int) (SchemaEvidence, error) {
	if err := ValidateMigrationManifest(manifest); err != nil {
		return SchemaEvidence{}, err
	}
	if err := validateManifestSequence(manifest); err != nil {
		return SchemaEvidence{}, err
	}
	if err := validateChecksumPlaceholders(manifest); err != nil {
		return SchemaEvidence{}, err
	}

	applied := make(map[int]struct{}, len(appliedVersions))
	for _, version := range appliedVersions {
		if version <= 0 {
			return SchemaEvidence{}, fmt.Errorf("%w: applied version %d is invalid", ErrInvalidMigrationManifest, version)
		}
		applied[version] = struct{}{}
	}

	report := SchemaEvidence{
		SchemaVersion:  manifest.SchemaVersion,
		LatestVersion:  manifest.SchemaVersion,
		Status:         SchemaEvidenceStatusReady,
		MigrationCount: len(manifest.Migrations),
		Migrations:     make([]SchemaMigrationEvidence, 0, len(manifest.Migrations)),
		SourceFiles:    []SchemaSourceFileEvidence{},
	}
	for _, migration := range manifest.Migrations {
		_, isApplied := applied[migration.Version]
		if isApplied {
			report.AppliedCount++
			delete(applied, migration.Version)
		} else {
			report.PendingCount++
		}
		report.Migrations = append(report.Migrations, SchemaMigrationEvidence{
			Version:             migration.Version,
			Name:                migration.Name,
			Up:                  migration.Up,
			Down:                migration.Down,
			Applied:             isApplied,
			ChecksumPlaceholder: migration.UpChecksum != "" || migration.DownChecksum != "",
		})
	}
	if len(applied) > 0 {
		for version := range applied {
			return SchemaEvidence{}, fmt.Errorf("%w: applied version %d is not in manifest", ErrInvalidMigrationManifest, version)
		}
	}
	report.Status = schemaEvidenceStatus(report)
	return report, nil
}

func (report *SchemaEvidence) AddSQLSubsetEvidence(sql string) {
	tableSet := make(map[string]struct{}, len(report.DetectedTables))
	for _, table := range report.DetectedTables {
		tableSet[table] = struct{}{}
	}
	objectSet := make(map[SchemaObjectEvidence]struct{}, len(report.DetectedObjects))
	for _, object := range report.DetectedObjects {
		objectSet[object] = struct{}{}
	}

	for _, rawStatement := range strings.Split(sql, ";") {
		statement := strings.TrimSpace(rawStatement)
		if statement == "" {
			continue
		}
		statement = strings.TrimSpace(stripLineComments(statement))
		if statement == "" {
			continue
		}
		kind, name, ok, supported := parseSQLSubsetStatement(statement)
		if !supported {
			report.UnsupportedStatementCount++
			continue
		}
		if !ok {
			report.InvalidStatementCount++
			continue
		}
		objectSet[SchemaObjectEvidence{Kind: kind, Name: name}] = struct{}{}
		if kind == "table" {
			tableSet[name] = struct{}{}
		}
	}

	// Run the richer offline parser to surface column types, indexes, and
	// partitions. Counts are recomputed from the merged result so repeated
	// AddSQLSubsetEvidence calls remain idempotent.
	tables, indexes, partitions, _, _ := ParseSQLSubset(sql)
	report.mergeRichEvidence(tables, indexes, partitions)
	for _, table := range tables {
		tableSet[table.Name] = struct{}{}
		objectSet[SchemaObjectEvidence{Kind: "table", Name: table.Name}] = struct{}{}
	}
	for _, index := range indexes {
		objectSet[SchemaObjectEvidence{Kind: "index", Name: index.Name}] = struct{}{}
	}

	report.DetectedTables = report.DetectedTables[:0]
	for table := range tableSet {
		report.DetectedTables = append(report.DetectedTables, table)
	}
	report.DetectedObjects = report.DetectedObjects[:0]
	for object := range objectSet {
		report.DetectedObjects = append(report.DetectedObjects, object)
	}
	report.recountDetected()
	report.finalizeSQLSubsetEvidence()
}

func (report *SchemaEvidence) mergeRichEvidence(tables []TableEvidence, indexes []IndexEvidence, partitions []PartitionEvidence) {
	existingTables := make(map[string]int, len(report.Tables))
	for i, table := range report.Tables {
		existingTables[table.Name] = i
	}
	for _, table := range tables {
		if idx, ok := existingTables[table.Name]; ok {
			report.Tables[idx] = table
			continue
		}
		existingTables[table.Name] = len(report.Tables)
		report.Tables = append(report.Tables, table)
	}

	existingIndexes := make(map[string]int, len(report.Indexes))
	for i, index := range report.Indexes {
		existingIndexes[index.Name] = i
	}
	for _, index := range indexes {
		if idx, ok := existingIndexes[index.Name]; ok {
			report.Indexes[idx] = index
			continue
		}
		existingIndexes[index.Name] = len(report.Indexes)
		report.Indexes = append(report.Indexes, index)
	}

	existingPartitions := make(map[string]int, len(report.Partitions))
	for i, partition := range report.Partitions {
		existingPartitions[partition.Name] = i
	}
	for _, partition := range partitions {
		if idx, ok := existingPartitions[partition.Name]; ok {
			report.Partitions[idx] = partition
			continue
		}
		existingPartitions[partition.Name] = len(report.Partitions)
		report.Partitions = append(report.Partitions, partition)
	}
}

func (report *SchemaEvidence) recountDetected() {
	report.DetectedTableCount = len(report.Tables)
	report.DetectedIndexCount = len(report.Indexes)
	report.DetectedPartitionCount = len(report.Partitions)
	columns := 0
	for _, table := range report.Tables {
		columns += len(table.Columns)
	}
	report.DetectedColumnCount = columns
}

func (report *SchemaEvidence) AddSourceFileEvidence(filename string, data []byte) {
	digest := sha256.Sum256(data)
	evidence := SchemaSourceFileEvidence{
		Filename:  filename,
		Kind:      inferMigrationSourceKind(filename),
		ByteCount: len(data),
		SHA256:    hex.EncodeToString(digest[:]),
	}

	for i, existing := range report.SourceFiles {
		if existing.Filename == filename {
			report.SourceFiles[i] = evidence
			report.finalizeSourceFileEvidence()
			return
		}
	}
	report.SourceFiles = append(report.SourceFiles, evidence)
	report.finalizeSourceFileEvidence()
}

func (report *SchemaEvidence) finalizeSQLSubsetEvidence() {
	sort.Strings(report.DetectedTables)
	sort.Slice(report.DetectedObjects, func(i, j int) bool {
		if report.DetectedObjects[i].Kind == report.DetectedObjects[j].Kind {
			return report.DetectedObjects[i].Name < report.DetectedObjects[j].Name
		}
		return report.DetectedObjects[i].Kind < report.DetectedObjects[j].Kind
	})
	sort.Slice(report.Tables, func(i, j int) bool { return report.Tables[i].Name < report.Tables[j].Name })
	sort.Slice(report.Indexes, func(i, j int) bool { return report.Indexes[i].Name < report.Indexes[j].Name })
	sort.Slice(report.Partitions, func(i, j int) bool { return report.Partitions[i].Name < report.Partitions[j].Name })
	if report.InvalidStatementCount > 0 || report.UnsupportedStatementCount > 0 {
		report.Status = SchemaEvidenceStatusBlocked
	}
}

func (report *SchemaEvidence) finalizeSourceFileEvidence() {
	sort.Slice(report.SourceFiles, func(i, j int) bool {
		return report.SourceFiles[i].Filename < report.SourceFiles[j].Filename
	})
}

func RenderSchemaEvidenceJSON(report SchemaEvidence) ([]byte, error) {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func schemaEvidenceStatus(report SchemaEvidence) string {
	pendingBeforeApplied := false
	for _, migration := range report.Migrations {
		if !migration.Applied {
			pendingBeforeApplied = true
			continue
		}
		if pendingBeforeApplied {
			return SchemaEvidenceStatusBlocked
		}
	}
	if report.PendingCount > 0 {
		return SchemaEvidenceStatusNeedsReview
	}
	return SchemaEvidenceStatusReady
}

func inferMigrationSourceKind(filename string) string {
	parsed, err := ParseMigrationFilename(path.Base(filename))
	if err != nil {
		return ""
	}
	return string(parsed.Direction)
}

func parseSQLSubsetStatement(statement string) (kind string, name string, ok bool, supported bool) {
	fields := strings.Fields(stripLineComments(statement))
	if len(fields) < 2 || !strings.EqualFold(fields[0], "create") {
		return "", "", false, false
	}
	subject := strings.ToLower(fields[1])
	switch subject {
	case "table", "index":
		nameIndex := 2
		if len(fields) > nameIndex+2 && strings.EqualFold(fields[nameIndex], "if") && strings.EqualFold(fields[nameIndex+1], "not") && strings.EqualFold(fields[nameIndex+2], "exists") {
			nameIndex += 3
		}
		if len(fields) <= nameIndex {
			return subject, "", false, true
		}
		name := cleanSQLIdentifier(fields[nameIndex])
		if name == "" {
			return subject, "", false, true
		}
		return subject, name, true, true
	default:
		return "", "", false, false
	}
}

func stripLineComments(statement string) string {
	lines := strings.Split(statement, "\n")
	for i, line := range lines {
		if before, _, ok := strings.Cut(line, "--"); ok {
			lines[i] = before
		}
	}
	return strings.Join(lines, "\n")
}

func cleanSQLIdentifier(value string) string {
	value = strings.Trim(value, " \t\r\n\"`[](),")
	if value == "" {
		return ""
	}
	if dot := strings.LastIndex(value, "."); dot >= 0 {
		value = value[dot+1:]
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return ""
	}
	return strings.ToLower(value)
}

func validateManifestSequence(manifest MigrationManifest) error {
	for i, migration := range manifest.Migrations {
		expected := i + 1
		if migration.Version != expected {
			return fmt.Errorf("%w: migration version %d must be ordered as %d", ErrInvalidMigrationManifest, migration.Version, expected)
		}
	}
	return nil
}

func validateChecksumPlaceholders(manifest MigrationManifest) error {
	for _, migration := range manifest.Migrations {
		for _, checksum := range []string{migration.UpChecksum, migration.DownChecksum} {
			if checksum == "" {
				continue
			}
			if !strings.HasPrefix(checksum, "sha256:") || len(checksum) <= len("sha256:") {
				return fmt.Errorf("%w: checksum placeholder for migration %d must use sha256:<placeholder>", ErrInvalidMigrationManifest, migration.Version)
			}
		}
	}
	return nil
}
