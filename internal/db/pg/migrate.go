// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package pg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db"
)

// MigrationAdvisoryLockKey is hashtext('midnight_upgrade'). The migration
// runner acquires this lock for the lifetime of every UP/DOWN run so
// concurrent runners (e.g. installer + manual) cannot race. The literal
// integer was extracted from Postgres on first design (panel SCHEMA_NOTES
// section 6 — "Every migration runs inside a Postgres transaction that
// first acquires pg_advisory_xact_lock(hashtext('midnight_upgrade'))").
const MigrationAdvisoryLockKey int64 = 6071876_82321_45221

// MigrationFile pairs a manifest entry with its on-disk SHA-256.
type MigrationFile struct {
	Version    int
	Name       string
	Up         string
	Down       string
	UpSHA256   string
	DownSHA256 string
}

// LoadOrderedMigrations reads the manifest, walks every referenced .up.sql
// and .down.sql file, and returns the ordered list with computed SHA-256
// digests. Filename order MUST equal manifest order; the manifest's own
// version sequence enforces this.
func LoadOrderedMigrations(fsys fs.FS, manifestPath string) ([]MigrationFile, db.MigrationManifest, error) {
	manifest, err := db.LoadMigrationManifest(fsys, manifestPath)
	if err != nil {
		return nil, db.MigrationManifest{}, fmt.Errorf("load manifest: %w", err)
	}
	base := path.Dir(manifestPath)
	files := make([]MigrationFile, 0, len(manifest.Migrations))
	for _, m := range manifest.Migrations {
		upPath := path.Join(base, m.Up)
		upSum, err := hashFile(fsys, upPath)
		if err != nil {
			return nil, db.MigrationManifest{}, fmt.Errorf("hash %s: %w", upPath, err)
		}
		downPath := path.Join(base, m.Down)
		downSum, err := hashFile(fsys, downPath)
		if err != nil {
			return nil, db.MigrationManifest{}, fmt.Errorf("hash %s: %w", downPath, err)
		}
		files = append(files, MigrationFile{
			Version:    m.Version,
			Name:       m.Name,
			Up:         m.Up,
			Down:       m.Down,
			UpSHA256:   upSum,
			DownSHA256: downSum,
		})
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Version < files[j].Version })
	return files, manifest, nil
}

func hashFile(fsys fs.FS, name string) (string, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// AppliedMigration is one row in schema_migrations.
type AppliedMigration struct {
	Version   int
	Name      string
	UpSHA256  string
	AppliedAt string
}

// EnsureSchemaMigrationsTable creates the bookkeeping table that records
// applied versions + the SHA-256 each row was applied with. The table is
// idempotent: if it already exists we leave it alone so the Phase 0
// `CREATE TABLE IF NOT EXISTS schema_migrations` line in the alpha_core
// migration also works.
func EnsureSchemaMigrationsTable(ctx context.Context, conn Conn) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,
    up_sha256   TEXT NOT NULL DEFAULT '',
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`
	if _, err := conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	// Add up_sha256 column if it predates this migration runner. ALTER
	// TABLE IF NOT EXISTS column does not exist on every Postgres minor;
	// we use a DO block.
	const addCol = `
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'schema_migrations' AND column_name = 'up_sha256'
    ) THEN
        ALTER TABLE schema_migrations ADD COLUMN up_sha256 TEXT NOT NULL DEFAULT '';
    END IF;
END$$`
	if _, err := conn.Exec(ctx, addCol); err != nil {
		return fmt.Errorf("ensure schema_migrations.up_sha256: %w", err)
	}
	return nil
}

// ListApplied returns the rows currently recorded in schema_migrations.
func ListApplied(ctx context.Context, conn Conn) ([]AppliedMigration, error) {
	if err := EnsureSchemaMigrationsTable(ctx, conn); err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, `SELECT version, name, COALESCE(up_sha256, ''), to_char(applied_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("list applied: %w", err)
	}
	defer rows.Close()
	out := make([]AppliedMigration, 0)
	for rows.Next() {
		var rec AppliedMigration
		if err := rows.Scan(&rec.Version, &rec.Name, &rec.UpSHA256, &rec.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan applied: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// MigrationRunner applies pending migrations against pool. The runner is
// safe to call repeatedly; already-applied versions are skipped.
type MigrationRunner struct {
	Pool *Pool
	FS   fs.FS
	// ManifestPath is the path-within-FS to manifest.yaml. Defaults to
	// "manifest.yaml" so the embedded migrations FS works without
	// configuration.
	ManifestPath string
}

// Up applies every migration whose version is greater than the highest
// row in schema_migrations. Each migration runs inside its own
// transaction that first acquires the migration advisory lock; this is
// the panel-mandated upgrade-lock contract.
func (r *MigrationRunner) Up(ctx context.Context) ([]MigrationFile, error) {
	manifestPath := r.ManifestPath
	if manifestPath == "" {
		manifestPath = "manifest.yaml"
	}
	files, _, err := LoadOrderedMigrations(r.FS, manifestPath)
	if err != nil {
		return nil, err
	}
	if err := EnsureSchemaMigrationsTable(ctx, r.Pool); err != nil {
		return nil, err
	}
	applied, err := ListApplied(ctx, r.Pool)
	if err != nil {
		return nil, err
	}
	appliedByVersion := make(map[int]AppliedMigration, len(applied))
	for _, row := range applied {
		appliedByVersion[row.Version] = row
	}
	out := make([]MigrationFile, 0)
	for _, m := range files {
		if existing, ok := appliedByVersion[m.Version]; ok {
			if existing.UpSHA256 != "" && existing.UpSHA256 != m.UpSHA256 {
				return nil, fmt.Errorf("migration %d (%s) already applied with sha256 %q, file now hashes to %q", m.Version, m.Name, existing.UpSHA256, m.UpSHA256)
			}
			continue
		}
		if err := r.applyOne(ctx, m); err != nil {
			return out, fmt.Errorf("apply migration %d (%s): %w", m.Version, m.Name, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// Down rolls back the latest applied migration. Used by the dev / CI
// `db:down` path; production upgrades never call Down — they roll
// forward via the upgrade-transaction snapshot/rollback flow owned by
// `internal/upgrade` (Lane 2 / Phase 1.13).
func (r *MigrationRunner) Down(ctx context.Context) (*MigrationFile, error) {
	manifestPath := r.ManifestPath
	if manifestPath == "" {
		manifestPath = "manifest.yaml"
	}
	files, _, err := LoadOrderedMigrations(r.FS, manifestPath)
	if err != nil {
		return nil, err
	}
	if err := EnsureSchemaMigrationsTable(ctx, r.Pool); err != nil {
		return nil, err
	}
	applied, err := ListApplied(ctx, r.Pool)
	if err != nil {
		return nil, err
	}
	if len(applied) == 0 {
		return nil, nil
	}
	latest := applied[len(applied)-1]
	var target *MigrationFile
	for i := range files {
		if files[i].Version == latest.Version {
			target = &files[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("schema_migrations records version %d but manifest does not", latest.Version)
	}
	downSQL, err := readSQL(r.FS, path.Join(path.Dir(manifestPath), target.Down))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", target.Down, err)
	}
	if err := r.Pool.WithTxOpts(ctx, pgx.TxOptions{}, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", MigrationAdvisoryLockKey); err != nil {
			return fmt.Errorf("acquire advisory lock: %w", err)
		}
		if _, err := tx.Exec(ctx, downSQL); err != nil {
			return fmt.Errorf("execute down sql: %w", err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", target.Version); err != nil {
			return fmt.Errorf("delete schema_migrations row: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return target, nil
}

func (r *MigrationRunner) applyOne(ctx context.Context, m MigrationFile) error {
	manifestPath := r.ManifestPath
	if manifestPath == "" {
		manifestPath = "manifest.yaml"
	}
	upSQL, err := readSQL(r.FS, path.Join(path.Dir(manifestPath), m.Up))
	if err != nil {
		return fmt.Errorf("read %s: %w", m.Up, err)
	}
	return r.Pool.WithTxOpts(ctx, pgx.TxOptions{}, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", MigrationAdvisoryLockKey); err != nil {
			return fmt.Errorf("acquire advisory lock: %w", err)
		}
		if _, err := tx.Exec(ctx, upSQL); err != nil {
			return fmt.Errorf("execute up sql: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name, up_sha256) VALUES ($1, $2, $3)
			 ON CONFLICT (version) DO UPDATE SET name = EXCLUDED.name, up_sha256 = EXCLUDED.up_sha256`,
			m.Version, m.Name, m.UpSHA256,
		); err != nil {
			return fmt.Errorf("insert schema_migrations: %w", err)
		}
		return nil
	})
}

func readSQL(fsys fs.FS, name string) (string, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", err
	}
	sql := strings.TrimSpace(string(data))
	if sql == "" {
		return "", errors.New("empty sql file")
	}
	return string(data), nil
}
