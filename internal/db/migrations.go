package db

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"unicode"
)

type MigrationDirection string

const (
	MigrationDirectionUp   MigrationDirection = "up"
	MigrationDirectionDown MigrationDirection = "down"
)

var ErrInvalidMigrationManifest = errors.New("invalid migration manifest")

type MigrationManifest struct {
	SchemaVersion int         `json:"schema_version"`
	Migrations    []Migration `json:"migrations"`
}

type Migration struct {
	Version      int    `json:"version"`
	Name         string `json:"name"`
	Up           string `json:"up"`
	Down         string `json:"down"`
	UpChecksum   string `json:"up_checksum,omitempty"`
	DownChecksum string `json:"down_checksum,omitempty"`
}

type MigrationFile struct {
	Version   int
	Name      string
	Direction MigrationDirection
}

func LoadMigrationManifest(fsys fs.FS, path string) (MigrationManifest, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return MigrationManifest{}, err
	}
	manifest, err := ParseMigrationManifest(data)
	if err != nil {
		return MigrationManifest{}, err
	}
	if err := ValidateMigrationManifest(manifest); err != nil {
		return MigrationManifest{}, err
	}
	return manifest, nil
}

func ParseMigrationFilename(filename string) (MigrationFile, error) {
	parts := strings.Split(filename, ".")
	if len(parts) != 3 || parts[2] != "sql" {
		return MigrationFile{}, fmt.Errorf("%w: migration filename %q must be NNNN_name.(up|down).sql", ErrInvalidMigrationManifest, filename)
	}
	if parts[1] != string(MigrationDirectionUp) && parts[1] != string(MigrationDirectionDown) {
		return MigrationFile{}, fmt.Errorf("%w: migration filename %q has invalid direction", ErrInvalidMigrationManifest, filename)
	}

	prefix, name, ok := strings.Cut(parts[0], "_")
	if !ok || len(prefix) != 4 || name == "" {
		return MigrationFile{}, fmt.Errorf("%w: migration filename %q must include four digit version and name", ErrInvalidMigrationManifest, filename)
	}
	for _, r := range prefix {
		if !unicode.IsDigit(r) {
			return MigrationFile{}, fmt.Errorf("%w: migration filename %q has non-numeric version", ErrInvalidMigrationManifest, filename)
		}
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return MigrationFile{}, fmt.Errorf("%w: migration filename %q has invalid name", ErrInvalidMigrationManifest, filename)
	}

	version, err := strconv.Atoi(prefix)
	if err != nil || version == 0 {
		return MigrationFile{}, fmt.Errorf("%w: migration filename %q has invalid version", ErrInvalidMigrationManifest, filename)
	}
	return MigrationFile{Version: version, Name: name, Direction: MigrationDirection(parts[1])}, nil
}

func ParseMigrationManifest(data []byte) (MigrationManifest, error) {
	var manifest MigrationManifest
	var current *Migration
	inMigrations := false

	flush := func() {
		if current != nil {
			manifest.Migrations = append(manifest.Migrations, *current)
			current = nil
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "schema_version:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "schema_version:"))
			version, err := strconv.Atoi(value)
			if err != nil {
				return MigrationManifest{}, fmt.Errorf("%w: line %d has invalid schema_version", ErrInvalidMigrationManifest, lineNumber)
			}
			manifest.SchemaVersion = version
			continue
		}

		if line == "migrations:" || line == "migrations: []" {
			inMigrations = line == "migrations:"
			continue
		}

		if !inMigrations {
			return MigrationManifest{}, fmt.Errorf("%w: line %d has unexpected field", ErrInvalidMigrationManifest, lineNumber)
		}

		if strings.HasPrefix(line, "- ") {
			flush()
			current = &Migration{}
			line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if line == "" {
				continue
			}
		} else if current == nil {
			return MigrationManifest{}, fmt.Errorf("%w: line %d has migration field before item", ErrInvalidMigrationManifest, lineNumber)
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return MigrationManifest{}, fmt.Errorf("%w: line %d must be key: value", ErrInvalidMigrationManifest, lineNumber)
		}
		if err := setMigrationField(current, strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), "\"'")); err != nil {
			return MigrationManifest{}, fmt.Errorf("%w: line %d: %v", ErrInvalidMigrationManifest, lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return MigrationManifest{}, err
	}
	flush()
	return manifest, nil
}

func ValidateMigrationManifest(manifest MigrationManifest) error {
	if manifest.SchemaVersion < 0 {
		return fmt.Errorf("%w: schema_version must be non-negative", ErrInvalidMigrationManifest)
	}
	seen := map[int]struct{}{}
	maxVersion := 0
	for _, migration := range manifest.Migrations {
		if migration.Version <= 0 || migration.Name == "" || migration.Up == "" || migration.Down == "" {
			return fmt.Errorf("%w: migration entries require version, name, up, and down", ErrInvalidMigrationManifest)
		}
		if _, ok := seen[migration.Version]; ok {
			return fmt.Errorf("%w: duplicate migration version %d", ErrInvalidMigrationManifest, migration.Version)
		}
		seen[migration.Version] = struct{}{}
		if migration.Version > maxVersion {
			maxVersion = migration.Version
		}
		if err := validateMigrationFile(migration, migration.Up, MigrationDirectionUp); err != nil {
			return err
		}
		if err := validateMigrationFile(migration, migration.Down, MigrationDirectionDown); err != nil {
			return err
		}
	}
	if manifest.SchemaVersion != maxVersion {
		return fmt.Errorf("%w: schema_version %d must equal latest migration version %d", ErrInvalidMigrationManifest, manifest.SchemaVersion, maxVersion)
	}
	return nil
}

func setMigrationField(migration *Migration, key, value string) error {
	switch key {
	case "version":
		version, err := strconv.Atoi(value)
		if err != nil {
			return errors.New("version must be an integer")
		}
		migration.Version = version
	case "name":
		migration.Name = value
	case "up":
		migration.Up = value
	case "down":
		migration.Down = value
	case "up_checksum":
		migration.UpChecksum = value
	case "down_checksum":
		migration.DownChecksum = value
	default:
		return fmt.Errorf("unexpected migration field %q", key)
	}
	return nil
}

func validateMigrationFile(migration Migration, filename string, direction MigrationDirection) error {
	parsed, err := ParseMigrationFilename(filename)
	if err != nil {
		return err
	}
	if parsed.Version != migration.Version || parsed.Name != migration.Name || parsed.Direction != direction {
		return fmt.Errorf("%w: migration file %q does not match manifest entry", ErrInvalidMigrationManifest, filename)
	}
	return nil
}
