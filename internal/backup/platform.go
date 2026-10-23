// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

const (
	PlatformBackupSchema   = "midnight.dev/platform-backup/v1"
	PlatformIndexSchema    = "midnight.dev/platform-backup-index/v2"
	platformArchiveFormat  = "midnight-backup-aes256gcm-gzip-v1"
	platformIndexKDF       = "pbkdf2-sha256"
	platformIndexKDFRounds = 310_000
	platformIndexSaltBytes = 16
)

// PlatformRoot maps one offline filesystem source into a stable relative path
// inside the encrypted platform archive.
type PlatformRoot struct {
	Source      string `json:"-"`
	ArchivePath string `json:"archive_path"`
}

type PlatformArtifact struct {
	Key          string `json:"key"`
	ObjectSHA256 string `json:"object_sha256"`
	ObjectBytes  int64  `json:"object_bytes"`
	SourceSHA256 string `json:"source_sha256"`
	SourceBytes  int64  `json:"source_bytes"`
	Format       string `json:"format"`
}

type PlatformManifest struct {
	SchemaVersion    string           `json:"schema_version"`
	ID               string           `json:"id"`
	CreatedAt        time.Time        `json:"created_at"`
	OfflineConfirmed bool             `json:"offline_confirmed"`
	Postgres         PlatformArtifact `json:"postgres"`
	Filesystem       PlatformArtifact `json:"filesystem"`
	Roots            []PlatformRoot   `json:"roots"`
	VolumeBackupIDs  []string         `json:"volume_backup_ids,omitempty"`
}

type PlatformIndex struct {
	SchemaVersion  string `json:"schema_version"`
	ID             string `json:"id"`
	ManifestKey    string `json:"manifest_key"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ManifestBytes  int64  `json:"manifest_bytes"`
	IndexKDF       string `json:"index_kdf"`
	IndexKDFRounds int    `json:"index_kdf_rounds"`
	IndexKDFSalt   string `json:"index_kdf_salt"`
	HMACSHA256     string `json:"hmac_sha256"`
}

type PostgreSQLDumper interface {
	Dump(context.Context) (io.ReadCloser, error)
}

type PostgreSQLRestorer interface {
	Restore(context.Context, string) error
}

// PostgreSQLRestoreError reports whether a failed restore might nevertheless
// have committed. Unclassified errors are treated as commit-unknown.
type PostgreSQLRestoreError struct {
	Err                    error
	CommitMayHaveSucceeded bool
}

func (e *PostgreSQLRestoreError) Error() string {
	if e == nil || e.Err == nil {
		return "postgres restore failed"
	}
	return e.Err.Error()
}

func (e *PostgreSQLRestoreError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ExecPostgreSQL streams pg_dump and applies pg_restore in a single
// transaction. The DSN is passed through PGDATABASE, not command arguments or
// log text.
type ExecPostgreSQL struct {
	DSN           string
	DumpBinary    string
	RestoreBinary string
	AdditionalEnv []string
}

func (p ExecPostgreSQL) Dump(ctx context.Context) (io.ReadCloser, error) {
	if strings.TrimSpace(p.DSN) == "" {
		return nil, errors.New("postgres dsn is required")
	}
	binary := strings.TrimSpace(p.DumpBinary)
	if binary == "" {
		binary = "pg_dump"
	}
	cmd := exec.CommandContext(ctx, binary, "--format=custom", "--no-owner", "--no-acl")
	cmd.Env = append(append(os.Environ(), p.AdditionalEnv...), "PGDATABASE="+p.DSN)
	stderr := &boundedBuffer{limit: 4096}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pg_dump: %w", err)
	}
	return &commandReadCloser{
		ReadCloser: stdout,
		wait: func() error {
			if err := cmd.Wait(); err != nil {
				return fmt.Errorf("pg_dump failed: %w: %s", err, strings.TrimSpace(stderr.String()))
			}
			return nil
		},
	}, nil
}

func (p ExecPostgreSQL) Restore(ctx context.Context, dumpPath string) error {
	if strings.TrimSpace(p.DSN) == "" {
		return &PostgreSQLRestoreError{
			Err: errors.New("postgres dsn is required"),
		}
	}
	binary := strings.TrimSpace(p.RestoreBinary)
	if binary == "" {
		binary = "pg_restore"
	}
	cmd := exec.CommandContext(
		ctx,
		binary,
		"--single-transaction",
		"--exit-on-error",
		"--clean",
		"--if-exists",
		"--no-owner",
		"--no-acl",
		"--dbname=",
		filepath.Clean(dumpPath),
	)
	cmd.Env = append(append(os.Environ(), p.AdditionalEnv...), "PGDATABASE="+p.DSN)
	stderr := &boundedBuffer{limit: 4096}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		return &PostgreSQLRestoreError{
			Err: fmt.Errorf(
				"pg_restore failed: %w: %s",
				err,
				strings.TrimSpace(stderr.String()),
			),
			CommitMayHaveSucceeded: errors.As(err, &exitErr),
		}
	}
	return nil
}

type PlatformBackupRequest struct {
	ID               string
	OfflineConfirmed bool
	Roots            []PlatformRoot
	VolumeBackupIDs  []string
	Destination      Destination
	Passphrase       []byte
	Postgres         PostgreSQLDumper
	Now              func() time.Time
}

// CreatePlatformBackup streams a real PostgreSQL custom dump and a
// deterministic filesystem tar into separately authenticated encrypted
// objects. The encrypted manifest is anchored by a small HMAC-authenticated
// index so an object-store attacker cannot forge checksum metadata.
func CreatePlatformBackup(ctx context.Context, request PlatformBackupRequest) (PlatformIndex, PlatformManifest, error) {
	if !request.OfflineConfirmed {
		return PlatformIndex{}, PlatformManifest{}, errors.New("platform backup requires explicit offline confirmation")
	}
	if request.Destination == nil || request.Postgres == nil {
		return PlatformIndex{}, PlatformManifest{}, errors.New("platform backup destination and postgres dumper are required")
	}
	if len(request.Passphrase) < 16 {
		return PlatformIndex{}, PlatformManifest{}, errors.New("platform backup passphrase must be at least 16 bytes")
	}
	id, err := validatePlatformBackupID(request.ID)
	if err != nil {
		return PlatformIndex{}, PlatformManifest{}, err
	}
	roots, err := normalizePlatformRoots(request.Roots)
	if err != nil {
		return PlatformIndex{}, PlatformManifest{}, err
	}
	now := time.Now().UTC()
	if request.Now != nil {
		now = request.Now().UTC()
	}
	prefix := "platform/" + id + "/"
	existing, err := request.Destination.List(ctx, prefix)
	if err != nil {
		return PlatformIndex{}, PlatformManifest{}, fmt.Errorf("platform backup destination preflight: %w", err)
	}
	if len(existing) > 0 {
		return PlatformIndex{}, PlatformManifest{}, fmt.Errorf("platform backup id %q already has destination objects", id)
	}

	dump, err := request.Postgres.Dump(ctx)
	if err != nil {
		return PlatformIndex{}, PlatformManifest{}, err
	}
	postgresResult, err := PutEncryptedArchive(ctx, request.Destination, prefix+"postgres.dump.mnbk", dump, request.Passphrase)
	closeErr := dump.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return PlatformIndex{}, PlatformManifest{}, fmt.Errorf("platform postgres backup: %w", err)
	}

	filesystemReader, filesystemDone := streamPlatformFilesystem(ctx, roots)
	filesystemResult, err := PutEncryptedArchive(ctx, request.Destination, prefix+"filesystem.tar.mnbk", filesystemReader, request.Passphrase)
	streamErr := <-filesystemDone
	if err = errors.Join(err, streamErr); err != nil {
		return PlatformIndex{}, PlatformManifest{}, fmt.Errorf("platform filesystem backup: %w", err)
	}

	manifest := PlatformManifest{
		SchemaVersion: PlatformBackupSchema,
		ID:            id, CreatedAt: now, OfflineConfirmed: true,
		Postgres:        artifactFromStream(prefix+"postgres.dump.mnbk", postgresResult),
		Filesystem:      artifactFromStream(prefix+"filesystem.tar.mnbk", filesystemResult),
		Roots:           roots,
		VolumeBackupIDs: normalizeStringSet(request.VolumeBackupIDs),
	}
	if err := manifest.Validate(); err != nil {
		return PlatformIndex{}, PlatformManifest{}, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return PlatformIndex{}, PlatformManifest{}, err
	}
	manifestResult, err := PutEncryptedArchive(ctx, request.Destination, prefix+"manifest.mnbk", bytes.NewReader(manifestJSON), request.Passphrase)
	if err != nil {
		return PlatformIndex{}, PlatformManifest{}, fmt.Errorf("platform manifest backup: %w", err)
	}
	index := PlatformIndex{
		SchemaVersion: PlatformIndexSchema, ID: id,
		ManifestKey:    prefix + "manifest.mnbk",
		ManifestSHA256: manifestResult.Object.SHA256,
		ManifestBytes:  manifestResult.Object.Bytes,
		IndexKDF:       platformIndexKDF,
		IndexKDFRounds: platformIndexKDFRounds,
	}
	var indexSalt [platformIndexSaltBytes]byte
	if _, err := io.ReadFull(rand.Reader, indexSalt[:]); err != nil {
		return PlatformIndex{}, PlatformManifest{}, fmt.Errorf("platform index salt: %w", err)
	}
	index.IndexKDFSalt = hex.EncodeToString(indexSalt[:])
	index.HMACSHA256, err = signPlatformIndex(index, request.Passphrase)
	if err != nil {
		return PlatformIndex{}, PlatformManifest{}, err
	}
	indexJSON, err := json.Marshal(index)
	if err != nil {
		return PlatformIndex{}, PlatformManifest{}, err
	}
	if _, err := request.Destination.Put(ctx, prefix+"index.json", bytes.NewReader(indexJSON)); err != nil {
		return PlatformIndex{}, PlatformManifest{}, fmt.Errorf("platform index write: %w", err)
	}
	return index, manifest, nil
}

type PlatformRestoreStageRequest struct {
	ID          string
	StagingDir  string
	Destination Destination
	Passphrase  []byte
}

type PlatformRestoreStage struct {
	Manifest          PlatformManifest `json:"manifest"`
	Directory         string           `json:"directory"`
	PostgresDump      string           `json:"postgres_dump"`
	FilesystemArchive string           `json:"filesystem_archive"`
	FilesystemDir     string           `json:"filesystem_dir"`
}

// StagePlatformRestore authenticates and decrypts every artifact into a
// distinct staging directory before any destination state is changed.
func StagePlatformRestore(ctx context.Context, request PlatformRestoreStageRequest) (PlatformRestoreStage, error) {
	id, err := validatePlatformBackupID(request.ID)
	if err != nil {
		return PlatformRestoreStage{}, err
	}
	if request.Destination == nil || len(request.Passphrase) < 16 {
		return PlatformRestoreStage{}, errors.New("restore destination and passphrase are required")
	}
	staging := filepath.Clean(request.StagingDir)
	if staging == "." || staging == string(filepath.Separator) || strings.TrimSpace(request.StagingDir) == "" {
		return PlatformRestoreStage{}, errors.New("a dedicated restore staging directory is required")
	}
	if err := os.MkdirAll(filepath.Dir(staging), 0o700); err != nil {
		return PlatformRestoreStage{}, err
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return PlatformRestoreStage{}, err
	}
	prefix := "platform/" + id + "/"
	indexBody, _, err := request.Destination.Get(ctx, prefix+"index.json")
	if err != nil {
		return PlatformRestoreStage{}, fmt.Errorf("platform index read: %w", err)
	}
	var index PlatformIndex
	decodeErr := json.NewDecoder(indexBody).Decode(&index)
	closeErr := indexBody.Close()
	if err := errors.Join(decodeErr, closeErr); err != nil {
		return PlatformRestoreStage{}, fmt.Errorf("platform index decode: %w", err)
	}
	if err := verifyPlatformIndex(index, id, request.Passphrase); err != nil {
		return PlatformRestoreStage{}, err
	}
	var manifestBuffer bytes.Buffer
	manifestEvidence, err := GetEncryptedArchive(
		ctx, request.Destination, index.ManifestKey, &manifestBuffer,
		request.Passphrase, index.ManifestSHA256,
	)
	if err != nil {
		return PlatformRestoreStage{}, fmt.Errorf("platform manifest restore: %w", err)
	}
	var manifest PlatformManifest
	if err := json.Unmarshal(manifestBuffer.Bytes(), &manifest); err != nil {
		return PlatformRestoreStage{}, fmt.Errorf("platform manifest decode: %w", err)
	}
	if manifestEvidence.RestoredSHA256 != sha256Hex(manifestBuffer.Bytes()) {
		return PlatformRestoreStage{}, errors.New("platform manifest plaintext checksum mismatch")
	}
	if err := manifest.Validate(); err != nil {
		return PlatformRestoreStage{}, err
	}
	if manifest.ID != id {
		return PlatformRestoreStage{}, errors.New("platform manifest id does not match requested backup")
	}

	dumpPath := filepath.Join(staging, "postgres.dump")
	dumpFile, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return PlatformRestoreStage{}, err
	}
	dumpEvidence, restoreErr := GetEncryptedArchive(
		ctx, request.Destination, manifest.Postgres.Key, dumpFile,
		request.Passphrase, manifest.Postgres.ObjectSHA256,
	)
	closeErr = dumpFile.Close()
	if err := errors.Join(restoreErr, closeErr); err != nil {
		return PlatformRestoreStage{}, fmt.Errorf("platform postgres stage: %w", err)
	}
	if err := validateRestoredArtifact(manifest.Postgres, dumpEvidence); err != nil {
		return PlatformRestoreStage{}, err
	}

	filesystemTar := filepath.Join(staging, "filesystem.tar")
	tarFile, err := os.OpenFile(filesystemTar, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return PlatformRestoreStage{}, err
	}
	fsEvidence, restoreErr := GetEncryptedArchive(
		ctx, request.Destination, manifest.Filesystem.Key, tarFile,
		request.Passphrase, manifest.Filesystem.ObjectSHA256,
	)
	closeErr = tarFile.Close()
	if err := errors.Join(restoreErr, closeErr); err != nil {
		return PlatformRestoreStage{}, fmt.Errorf("platform filesystem stage: %w", err)
	}
	if err := validateRestoredArtifact(manifest.Filesystem, fsEvidence); err != nil {
		return PlatformRestoreStage{}, err
	}
	filesystemDir := filepath.Join(staging, "filesystem")
	if err := extractPlatformTar(filesystemTar, filesystemDir); err != nil {
		return PlatformRestoreStage{}, err
	}
	stage := PlatformRestoreStage{
		Manifest: manifest, Directory: staging,
		PostgresDump: dumpPath, FilesystemArchive: filesystemTar,
		FilesystemDir: filesystemDir,
	}
	stageJSON, err := json.Marshal(stage)
	if err != nil {
		return PlatformRestoreStage{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, "stage.json"), stageJSON, 0o600); err != nil {
		return PlatformRestoreStage{}, err
	}
	return stage, nil
}

type PlatformRestoreApplyRequest struct {
	Stage      PlatformRestoreStage
	TargetRoot string
	Postgres   PostgreSQLRestorer
	Confirmed  bool
}

const platformRestoreJournalSchema = "midnight.dev/platform-restore-apply/v1"

type platformRestoreJournal struct {
	SchemaVersion string          `json:"schema_version"`
	BackupID      string          `json:"backup_id"`
	TargetRoot    string          `json:"target_root"`
	Phase         string          `json:"phase"`
	Moves         []installedRoot `json:"moves"`
}

// ApplyPlatformRestore installs validated filesystem roots with rollback
// retention, then runs pg_restore as one transaction. A fsync'd apply journal
// makes filesystem installation resumable across process death: interrupted
// renames roll back, while an interrupted database restore is safely replayed.
// Services must remain offline for the complete operation.
func ApplyPlatformRestore(ctx context.Context, request PlatformRestoreApplyRequest) error {
	if !request.Confirmed {
		return errors.New("platform restore requires explicit destructive confirmation")
	}
	if request.Postgres == nil {
		return errors.New("postgres restorer is required")
	}
	if strings.TrimSpace(request.TargetRoot) == "" {
		return errors.New("restore target root is required")
	}
	targetRoot, err := filepath.Abs(filepath.Clean(request.TargetRoot))
	if err != nil {
		return errors.New("restore target root is invalid")
	}
	if stat, statErr := os.Stat(targetRoot); statErr != nil || !stat.IsDir() {
		return errors.New("restore target root must be an existing directory")
	}
	if err := validatePlatformRestoreStage(request.Stage); err != nil {
		return err
	}
	if err := validateRestorePathSeparation(request.Stage, targetRoot); err != nil {
		return err
	}
	journalPath := filepath.Join(request.Stage.Directory, "apply-journal.json")
	journal, err := readPlatformRestoreJournal(journalPath)
	if err != nil {
		return err
	}
	if journal != nil {
		if journal.SchemaVersion != platformRestoreJournalSchema ||
			journal.BackupID != request.Stage.Manifest.ID ||
			journal.TargetRoot != targetRoot {
			return errors.New("platform restore apply journal does not match this stage and target")
		}
		switch journal.Phase {
		case "completed":
			return nil
		case "filesystem-installed", "database-restoring", "database-outcome-unknown":
			switch journal.Phase {
			case "filesystem-installed":
				journal.Phase = "database-restoring"
			case "database-restoring":
				// This process did not observe the prior restore call return.
				// It may have committed before the crash, so no later replay
				// failure is allowed to roll the filesystem generation back.
				journal.Phase = "database-outcome-unknown"
			}
			if err := writePlatformRestoreJournal(journalPath, *journal); err != nil {
				return err
			}
			if err := request.Postgres.Restore(ctx, request.Stage.PostgresDump); err != nil {
				return handlePlatformRestoreFailure(journalPath, journal, err)
			}
			journal.Phase = "database-restored"
			if err := writePlatformRestoreJournal(journalPath, *journal); err != nil {
				return err
			}
			journal.Phase = "completed"
			return writePlatformRestoreJournal(journalPath, *journal)
		case "database-restored":
			journal.Phase = "completed"
			return writePlatformRestoreJournal(journalPath, *journal)
		case "prepared", "installing", "rollback-failed":
			rollbackErr := rollbackInstalledRoots(journal.Moves)
			if rollbackErr != nil {
				journal.Phase = "rollback-failed"
				return errors.Join(
					fmt.Errorf("recover interrupted platform restore: %w", rollbackErr),
					writePlatformRestoreJournal(journalPath, *journal),
				)
			}
			journal.Phase = "rolled-back"
			if err := writePlatformRestoreJournal(journalPath, *journal); err != nil {
				return err
			}
		case "rolled-back":
			// The previous attempt is safely retained under its failed suffix.
		default:
			return fmt.Errorf("platform restore apply journal has unknown phase %q", journal.Phase)
		}
	}
	applyFilesystemDir := filepath.Join(request.Stage.Directory, "apply-filesystem")
	if err := os.RemoveAll(applyFilesystemDir); err != nil {
		return fmt.Errorf("reset platform restore apply staging: %w", err)
	}
	if err := extractPlatformTar(request.Stage.FilesystemArchive, applyFilesystemDir); err != nil {
		return fmt.Errorf("prepare verified platform filesystem: %w", err)
	}
	verifiedStage := request.Stage
	verifiedStage.FilesystemDir = applyFilesystemDir
	moves, err := planInstalledRoots(verifiedStage, targetRoot)
	if err != nil {
		return err
	}
	journal = &platformRestoreJournal{
		SchemaVersion: platformRestoreJournalSchema,
		BackupID:      request.Stage.Manifest.ID,
		TargetRoot:    targetRoot,
		Phase:         "prepared",
		Moves:         moves,
	}
	if err := writePlatformRestoreJournal(journalPath, *journal); err != nil {
		return err
	}
	journal.Phase = "installing"
	if err := writePlatformRestoreJournal(journalPath, *journal); err != nil {
		return err
	}
	if err := installPlannedRoots(verifiedStage, moves); err != nil {
		rollbackErr := rollbackInstalledRoots(moves)
		if rollbackErr != nil {
			journal.Phase = "rollback-failed"
		} else {
			journal.Phase = "rolled-back"
		}
		return errors.Join(err, rollbackErr, writePlatformRestoreJournal(journalPath, *journal))
	}
	journal.Phase = "filesystem-installed"
	if err := writePlatformRestoreJournal(journalPath, *journal); err != nil {
		return err
	}
	journal.Phase = "database-restoring"
	if err := writePlatformRestoreJournal(journalPath, *journal); err != nil {
		return err
	}
	if err := request.Postgres.Restore(ctx, request.Stage.PostgresDump); err != nil {
		return handlePlatformRestoreFailure(journalPath, journal, err)
	}
	journal.Phase = "database-restored"
	if err := writePlatformRestoreJournal(journalPath, *journal); err != nil {
		return err
	}
	journal.Phase = "completed"
	return writePlatformRestoreJournal(journalPath, *journal)
}

func (m PlatformManifest) Validate() error {
	if m.SchemaVersion != PlatformBackupSchema || strings.TrimSpace(m.ID) == "" ||
		m.CreatedAt.IsZero() || !m.OfflineConfirmed {
		return errors.New("platform backup manifest identity or offline evidence is invalid")
	}
	if len(m.Roots) == 0 {
		return errors.New("platform backup manifest has no filesystem roots")
	}
	for _, artifact := range []PlatformArtifact{m.Postgres, m.Filesystem} {
		if strings.TrimSpace(artifact.Key) == "" ||
			len(artifact.ObjectSHA256) != 64 || len(artifact.SourceSHA256) != 64 ||
			artifact.ObjectBytes <= 0 || artifact.SourceBytes < 0 ||
			artifact.Format != platformArchiveFormat {
			return errors.New("platform backup manifest artifact evidence is invalid")
		}
	}
	if _, err := validatePlatformArchiveRoots(m.Roots); err != nil {
		return err
	}
	prefix := "platform/" + m.ID + "/"
	if m.Postgres.Key != prefix+"postgres.dump.mnbk" ||
		m.Filesystem.Key != prefix+"filesystem.tar.mnbk" {
		return errors.New("platform backup manifest artifact keys are invalid")
	}
	return nil
}

func artifactFromStream(key string, result StreamBackupResult) PlatformArtifact {
	return PlatformArtifact{
		Key: key, ObjectSHA256: result.Object.SHA256,
		ObjectBytes: result.Object.Bytes, SourceSHA256: result.SourceSHA256,
		SourceBytes: result.SourceBytes, Format: result.Format,
	}
}

func validateRestoredArtifact(expected PlatformArtifact, observed StreamRestoreResult) error {
	if observed.RestoredSHA256 != expected.SourceSHA256 ||
		observed.RestoredBytes != expected.SourceBytes ||
		observed.Format != expected.Format {
		return errors.New("staged platform artifact does not match manifest evidence")
	}
	return nil
}

func validatePlatformRestoreStage(stage PlatformRestoreStage) error {
	if err := stage.Manifest.Validate(); err != nil {
		return err
	}
	directory, err := filepath.Abs(filepath.Clean(stage.Directory))
	if err != nil || strings.TrimSpace(stage.Directory) == "" {
		return errors.New("platform restore stage directory is invalid")
	}
	for label, value := range map[string]string{
		"postgres dump":      stage.PostgresDump,
		"filesystem archive": stage.FilesystemArchive,
		"filesystem root":    stage.FilesystemDir,
	} {
		path, pathErr := filepath.Abs(filepath.Clean(value))
		if pathErr != nil || strings.TrimSpace(value) == "" {
			return fmt.Errorf("platform restore stage %s path is invalid", label)
		}
		relative, pathErr := filepath.Rel(directory, path)
		if pathErr != nil || relative == "." || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("platform restore stage %s escapes its directory", label)
		}
	}
	if err := validatePlaintextFile(stage.PostgresDump, stage.Manifest.Postgres); err != nil {
		return fmt.Errorf("platform restore postgres evidence: %w", err)
	}
	if err := validatePlaintextFile(stage.FilesystemArchive, stage.Manifest.Filesystem); err != nil {
		return fmt.Errorf("platform restore filesystem evidence: %w", err)
	}
	if stat, err := os.Stat(stage.FilesystemDir); err != nil || !stat.IsDir() {
		return errors.New("platform restore staged filesystem root is missing")
	}
	return nil
}

func validateRestorePathSeparation(stage PlatformRestoreStage, targetRoot string) error {
	stageDirectory, err := filepath.EvalSymlinks(stage.Directory)
	if err != nil {
		return fmt.Errorf("resolve platform restore stage directory: %w", err)
	}
	targetDirectory, err := filepath.EvalSymlinks(targetRoot)
	if err != nil {
		return fmt.Errorf("resolve platform restore target root: %w", err)
	}
	for _, root := range stage.Manifest.Roots {
		target := filepath.Join(targetDirectory, filepath.FromSlash(root.ArchivePath))
		if pathContains(target, stageDirectory) || pathContains(stageDirectory, target) {
			return fmt.Errorf(
				"platform restore staging directory overlaps archived target %q",
				root.ArchivePath,
			)
		}
	}
	return nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil &&
		(relative == "." ||
			(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func validatePlaintextFile(path string, artifact PlatformArtifact) error {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if size != artifact.SourceBytes || hex.EncodeToString(hash.Sum(nil)) != artifact.SourceSHA256 {
		return errors.New("staged plaintext checksum or size mismatch")
	}
	return nil
}

func validatePlatformBackupID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, `/\`) ||
		strings.Contains(value, "..") {
		return "", errors.New("platform backup id must be one safe path segment")
	}
	return value, nil
}

func normalizePlatformRoots(values []PlatformRoot) ([]PlatformRoot, error) {
	out := make([]PlatformRoot, 0, len(values))
	for _, value := range values {
		source := filepath.Clean(strings.TrimSpace(value.Source))
		archive := filepath.ToSlash(filepath.Clean(strings.TrimSpace(value.ArchivePath)))
		archive = strings.TrimPrefix(archive, "./")
		if source == "." || strings.TrimSpace(value.Source) == "" ||
			archive == "." || archive == "" || strings.HasPrefix(archive, "/") ||
			strings.HasPrefix(archive, "../") || strings.Contains(archive, `\`) {
			return nil, errors.New("platform root source and safe relative archive_path are required")
		}
		if _, err := os.Lstat(source); err != nil {
			return nil, fmt.Errorf("platform root %s: %w", source, err)
		}
		out = append(out, PlatformRoot{Source: source, ArchivePath: archive})
	}
	return validatePlatformArchiveRoots(out)
}

func validatePlatformArchiveRoots(values []PlatformRoot) ([]PlatformRoot, error) {
	out := append([]PlatformRoot(nil), values...)
	sort.Slice(out, func(i, j int) bool { return out[i].ArchivePath < out[j].ArchivePath })
	if len(out) == 0 {
		return nil, errors.New("at least one platform filesystem root is required")
	}
	for index := range out {
		archive := filepath.ToSlash(filepath.Clean(strings.TrimSpace(out[index].ArchivePath)))
		archive = strings.TrimPrefix(archive, "./")
		if archive == "." || archive == "" || strings.HasPrefix(archive, "/") ||
			strings.HasPrefix(archive, "../") || strings.Contains(archive, `\`) {
			return nil, errors.New("a safe relative platform archive_path is required")
		}
		out[index].ArchivePath = archive
		if index > 0 {
			previous := out[index-1].ArchivePath
			if archive == previous || strings.HasPrefix(archive, previous+"/") {
				return nil, fmt.Errorf("overlapping platform archive roots %q and %q", previous, archive)
			}
		}
	}
	return out, nil
}

func streamPlatformFilesystem(ctx context.Context, roots []PlatformRoot) (io.Reader, <-chan error) {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		tarWriter := tar.NewWriter(writer)
		var archiveErr error
		for _, root := range roots {
			if archiveErr = addPlatformRoot(ctx, tarWriter, root); archiveErr != nil {
				break
			}
		}
		closeErr := tarWriter.Close()
		archiveErr = errors.Join(archiveErr, closeErr)
		_ = writer.CloseWithError(archiveErr)
		done <- archiveErr
		close(done)
	}()
	return reader, done
}

func addPlatformRoot(ctx context.Context, out *tar.Writer, root PlatformRoot) error {
	return filepath.Walk(root.Source, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root.Source, current)
		if err != nil {
			return err
		}
		name := root.ArchivePath
		if relative != "." {
			name = filepath.ToSlash(filepath.Join(root.ArchivePath, relative))
		}
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(current)
			if err != nil {
				return err
			}
			if filepath.IsAbs(linkTarget) || strings.HasPrefix(filepath.Clean(linkTarget), "..") {
				return fmt.Errorf("unsafe symlink %s -> %s", current, linkTarget)
			}
		}
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		header.Name = name
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		header.ModTime = time.Unix(0, 0).UTC()
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		if err := out.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			return fmt.Errorf("unsupported platform archive file type: %s", current)
		}
		file, err := os.Open(current)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, file)
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
}

func extractPlatformTar(tarPath, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return err
	}
	file, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(header.Name)
		if filepath.IsAbs(name) || filepath.Clean(name) == "." ||
			strings.HasPrefix(filepath.Clean(name), "..") {
			return errors.New("platform archive contains an unsafe path")
		}
		destination := filepath.Join(target, filepath.Clean(name))
		relative, err := filepath.Rel(target, destination)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("platform archive path escapes staging")
		}
		mode := os.FileMode(header.Mode) & 0o777
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, mode); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return err
			}
			output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(output, reader)
			closeErr := output.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(header.Linkname) || strings.HasPrefix(filepath.Clean(header.Linkname), "..") {
				return errors.New("platform archive contains an unsafe symlink")
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, destination); err != nil {
				return err
			}
		default:
			return fmt.Errorf("platform archive contains unsupported type %d", header.Typeflag)
		}
	}
}

type installedRoot struct {
	Target   string `json:"target"`
	Retained string `json:"retained,omitempty"`
	Failed   string `json:"failed"`
}

func planInstalledRoots(stage PlatformRestoreStage, targetRoot string) ([]installedRoot, error) {
	moves := make([]installedRoot, 0, len(stage.Manifest.Roots))
	for _, root := range stage.Manifest.Roots {
		source := filepath.Join(stage.FilesystemDir, filepath.FromSlash(root.ArchivePath))
		target := filepath.Join(targetRoot, filepath.FromSlash(root.ArchivePath))
		if _, err := os.Lstat(source); err != nil {
			return nil, fmt.Errorf("staged root %s: %w", root.ArchivePath, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		retained := target + ".midnight-retained-" + stage.Manifest.ID
		failed, err := availableFailedRestorePath(
			target + ".midnight-failed-" + stage.Manifest.ID,
		)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(retained); err == nil {
			return nil, fmt.Errorf("retained restore target already exists: %s", retained)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if _, err := os.Lstat(target); err == nil {
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		} else {
			retained = ""
		}
		moves = append(moves, installedRoot{Target: target, Retained: retained, Failed: failed})
	}
	return moves, nil
}

func installPlannedRoots(stage PlatformRestoreStage, moves []installedRoot) error {
	if len(stage.Manifest.Roots) != len(moves) {
		return errors.New("platform restore move plan does not match manifest")
	}
	for index, root := range stage.Manifest.Roots {
		move := moves[index]
		source := filepath.Join(stage.FilesystemDir, filepath.FromSlash(root.ArchivePath))
		if move.Retained != "" {
			if err := os.Rename(move.Target, move.Retained); err != nil {
				return err
			}
			if err := syncDirectories(filepath.Dir(move.Target)); err != nil {
				return err
			}
		}
		if err := os.Rename(source, move.Target); err != nil {
			return err
		}
		if err := syncDirectories(filepath.Dir(move.Target), filepath.Dir(source)); err != nil {
			return err
		}
	}
	return nil
}

func availableFailedRestorePath(base string) (string, error) {
	for attempt := 1; attempt <= 10_000; attempt++ {
		candidate := base
		if attempt > 1 {
			candidate = fmt.Sprintf("%s-%d", base, attempt)
		}
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("platform restore has too many retained failed attempts")
}

func rollbackInstalledRoots(moves []installedRoot) error {
	var errs []error
	for index := len(moves) - 1; index >= 0; index-- {
		move := moves[index]
		retainedExists := false
		if move.Retained != "" {
			if _, err := os.Lstat(move.Retained); err == nil {
				retainedExists = true
			} else if errors.Is(err, os.ErrNotExist) {
				// A planned move with an original target has not started until
				// the retained path exists. Never move that untouched target.
				continue
			} else {
				errs = append(errs, err)
				continue
			}
		}
		if _, err := os.Lstat(move.Target); err == nil {
			if renameErr := os.Rename(move.Target, move.Failed); renameErr != nil {
				errs = append(errs, renameErr)
				continue
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		if retainedExists {
			if renameErr := os.Rename(move.Retained, move.Target); renameErr != nil {
				errs = append(errs, renameErr)
			}
		}
		if err := syncDirectories(filepath.Dir(move.Target), filepath.Dir(move.Failed)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func readPlatformRestoreJournal(path string) (*platformRestoreJournal, error) {
	value, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read platform restore apply journal: %w", err)
	}
	var journal platformRestoreJournal
	if err := json.Unmarshal(value, &journal); err != nil {
		return nil, fmt.Errorf("decode platform restore apply journal: %w", err)
	}
	return &journal, nil
}

func handlePlatformRestoreFailure(
	journalPath string,
	journal *platformRestoreJournal,
	restoreErr error,
) error {
	commitMayHaveSucceeded := true
	var classified *PostgreSQLRestoreError
	if journal.Phase != "database-outcome-unknown" &&
		errors.As(restoreErr, &classified) {
		commitMayHaveSucceeded = classified.CommitMayHaveSucceeded
	}
	if commitMayHaveSucceeded {
		journal.Phase = "database-outcome-unknown"
		journalErr := writePlatformRestoreJournal(journalPath, *journal)
		return errors.Join(
			restoreErr,
			errors.New("postgres restore outcome is unknown; filesystem state is retained and the same apply must be retried while services remain offline"),
			journalErr,
		)
	}
	rollbackErr := rollbackInstalledRoots(journal.Moves)
	if rollbackErr != nil {
		journal.Phase = "rollback-failed"
	} else {
		journal.Phase = "rolled-back"
	}
	return errors.Join(
		restoreErr,
		rollbackErr,
		writePlatformRestoreJournal(journalPath, *journal),
	)
}

func writePlatformRestoreJournal(path string, journal platformRestoreJournal) error {
	value, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(value); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err = errors.Join(err, closeErr); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return syncDirectories(filepath.Dir(path))
}

func syncDirectories(paths ...string) error {
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		directory, err := os.Open(path)
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}

func signPlatformIndex(index PlatformIndex, passphrase []byte) (string, error) {
	unsigned := index
	unsigned.HMACSHA256 = ""
	data, err := json.Marshal(unsigned)
	if err != nil {
		return "", err
	}
	if index.IndexKDF != platformIndexKDF ||
		index.IndexKDFRounds != platformIndexKDFRounds {
		return "", errors.New("platform backup index KDF parameters are invalid")
	}
	salt, err := hex.DecodeString(index.IndexKDFSalt)
	if err != nil || len(salt) != platformIndexSaltBytes {
		return "", errors.New("platform backup index KDF salt is invalid")
	}
	key := pbkdf2.Key(passphrase, salt, index.IndexKDFRounds, 32, sha256.New)
	defer zeroBytes(key)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func verifyPlatformIndex(index PlatformIndex, id string, passphrase []byte) error {
	if index.SchemaVersion != PlatformIndexSchema || index.ID != id ||
		len(index.ManifestSHA256) != 64 || index.ManifestBytes <= 0 ||
		index.ManifestKey != "platform/"+id+"/manifest.mnbk" ||
		index.IndexKDF != platformIndexKDF ||
		index.IndexKDFRounds != platformIndexKDFRounds {
		return errors.New("platform backup index is invalid")
	}
	want, err := signPlatformIndex(index, passphrase)
	if err != nil {
		return err
	}
	got, err := hex.DecodeString(index.HMACSHA256)
	if err != nil {
		return errors.New("platform backup index signature is invalid")
	}
	expected, _ := hex.DecodeString(want)
	if !hmac.Equal(got, expected) {
		return errors.New("platform backup index signature mismatch")
	}
	return nil
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func normalizeStringSet(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

type boundedBuffer struct {
	value bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.value.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.value.Write(value)
	}
	return original, nil
}

func (b *boundedBuffer) String() string { return b.value.String() }
