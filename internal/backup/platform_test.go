// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type staticPostgreSQLDump struct {
	value []byte
}

func (d staticPostgreSQLDump) Dump(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(d.value)), nil
}

type recordingPostgreSQLRestore struct {
	value []byte
	err   error
	calls int
}

type rawFailingPostgreSQLRestore struct {
	err error
}

func (r rawFailingPostgreSQLRestore) Restore(context.Context, string) error {
	return r.err
}

func (r *recordingPostgreSQLRestore) Restore(_ context.Context, path string) error {
	r.calls++
	value, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	r.value = value
	if r.err != nil {
		return &PostgreSQLRestoreError{Err: r.err}
	}
	return nil
}

func TestPlatformRestoreRecoversInterruptedFilesystemInstall(t *testing.T) {
	fixture := newPlatformBackupFixture(t, "filesystem-crash")
	stage := fixture.stage(t)
	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, "etc", "midnight")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), []byte("old-config"), 0o600); err != nil {
		t.Fatal(err)
	}

	applyDir := filepath.Join(stage.Directory, "apply-filesystem")
	if err := extractPlatformTar(stage.FilesystemArchive, applyDir); err != nil {
		t.Fatal(err)
	}
	verified := stage
	verified.FilesystemDir = applyDir
	moves, err := planInstalledRoots(verified, targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	journal := platformRestoreJournal{
		SchemaVersion: platformRestoreJournalSchema,
		BackupID:      stage.Manifest.ID,
		TargetRoot:    targetRoot,
		Phase:         "prepared",
		Moves:         moves,
	}
	if err := writePlatformRestoreJournal(
		filepath.Join(stage.Directory, "apply-journal.json"),
		journal,
	); err != nil {
		t.Fatal(err)
	}
	if err := installPlannedRoots(verified, moves); err != nil {
		t.Fatal(err)
	}

	restorer := &recordingPostgreSQLRestore{}
	if err := ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: targetRoot, Postgres: restorer, Confirmed: true,
	}); err != nil {
		t.Fatalf("ApplyPlatformRestore() recovery error = %v", err)
	}
	assertFileValue(t, filepath.Join(target, "config.yaml"), "new-config")
	assertFileValue(
		t,
		target+".midnight-retained-"+fixture.id+string(filepath.Separator)+"config.yaml",
		"old-config",
	)
	assertFileValue(
		t,
		target+".midnight-failed-"+fixture.id+string(filepath.Separator)+"config.yaml",
		"new-config",
	)
	if restorer.calls != 1 {
		t.Fatalf("postgres restore calls = %d, want 1", restorer.calls)
	}
}

func TestPlatformRestoreReplaysDatabaseRestoreAfterInterruptedCommitBoundary(t *testing.T) {
	fixture := newPlatformBackupFixture(t, "database-crash")
	stage := fixture.stage(t)
	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, "etc", "midnight")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), []byte("old-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := &recordingPostgreSQLRestore{}
	if err := ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: targetRoot, Postgres: first, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(stage.Directory, "apply-journal.json")
	journal, err := readPlatformRestoreJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	journal.Phase = "database-restoring"
	if err := writePlatformRestoreJournal(journalPath, *journal); err != nil {
		t.Fatal(err)
	}

	knownAbort := &recordingPostgreSQLRestore{err: errors.New("replay rejected")}
	err = ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: targetRoot, Postgres: knownAbort, Confirmed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("ApplyPlatformRestore() crash-boundary replay error = %v, want outcome-unknown", err)
	}
	assertFileValue(t, filepath.Join(target, "config.yaml"), "new-config")
	journal, err = readPlatformRestoreJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != "database-outcome-unknown" {
		t.Fatalf("journal phase after crash replay = %q, want database-outcome-unknown", journal.Phase)
	}

	replayed := &recordingPostgreSQLRestore{}
	if err := ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: targetRoot, Postgres: replayed, Confirmed: true,
	}); err != nil {
		t.Fatalf("ApplyPlatformRestore() replay error = %v", err)
	}
	if replayed.calls != 1 {
		t.Fatalf("replayed postgres restore calls = %d, want 1", replayed.calls)
	}
	journal, err = readPlatformRestoreJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != "completed" {
		t.Fatalf("journal phase = %q, want completed", journal.Phase)
	}
	assertFileValue(t, filepath.Join(target, "config.yaml"), "new-config")
}

func TestPlatformRestoreRetainsFilesystemWhenDatabaseOutcomeIsUnknown(t *testing.T) {
	fixture := newPlatformBackupFixture(t, "database-unknown")
	stage := fixture.stage(t)
	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, "etc", "midnight")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), []byte("old-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknown := errors.New("connection lost after restore command")
	err := ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: targetRoot,
		Postgres: rawFailingPostgreSQLRestore{err: unknown}, Confirmed: true,
	})
	if !errors.Is(err, unknown) || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("ApplyPlatformRestore() error = %v, want outcome-unknown failure", err)
	}
	assertFileValue(t, filepath.Join(target, "config.yaml"), "new-config")
	assertFileValue(
		t,
		target+".midnight-retained-"+fixture.id+string(filepath.Separator)+"config.yaml",
		"old-config",
	)
	if _, err := os.Stat(target + ".midnight-failed-" + fixture.id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown database outcome rolled filesystem back: %v", err)
	}
	journalPath := filepath.Join(stage.Directory, "apply-journal.json")
	journal, err := readPlatformRestoreJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != "database-outcome-unknown" {
		t.Fatalf("journal phase = %q, want database-outcome-unknown", journal.Phase)
	}

	knownAbort := &recordingPostgreSQLRestore{err: errors.New("replay rejected")}
	err = ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: targetRoot, Postgres: knownAbort, Confirmed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("ApplyPlatformRestore() failed replay error = %v, want preserved outcome-unknown", err)
	}
	assertFileValue(t, filepath.Join(target, "config.yaml"), "new-config")
	journal, err = readPlatformRestoreJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != "database-outcome-unknown" {
		t.Fatalf("journal phase after failed replay = %q, want database-outcome-unknown", journal.Phase)
	}

	replayed := &recordingPostgreSQLRestore{}
	if err := ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: targetRoot, Postgres: replayed, Confirmed: true,
	}); err != nil {
		t.Fatalf("ApplyPlatformRestore() retry error = %v", err)
	}
	if replayed.calls != 1 {
		t.Fatalf("replayed postgres restore calls = %d, want 1", replayed.calls)
	}
	journal, err = readPlatformRestoreJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != "completed" {
		t.Fatalf("journal phase = %q, want completed", journal.Phase)
	}
}

func TestPlatformBackupRestoreRoundTripRetainsPreviousFilesystem(t *testing.T) {
	fixture := newPlatformBackupFixture(t, "round-trip")
	stage := fixture.stage(t)
	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, "etc", "midnight")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), []byte("old-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	restorer := &recordingPostgreSQLRestore{}

	err := ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: targetRoot, Postgres: restorer, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("ApplyPlatformRestore() error = %v", err)
	}
	assertFileValue(t, filepath.Join(target, "config.yaml"), "new-config")
	assertFileValue(
		t,
		target+".midnight-retained-"+fixture.id+string(filepath.Separator)+"config.yaml",
		"old-config",
	)
	if !bytes.Equal(restorer.value, fixture.database) {
		t.Fatalf("restored postgres = %q, want %q", restorer.value, fixture.database)
	}
}

func TestPlatformRestoreRejectsTamperedIndexAndStagedDump(t *testing.T) {
	t.Run("index", func(t *testing.T) {
		fixture := newPlatformBackupFixture(t, "tampered-index")
		indexPath := filepath.Join(fixture.destinationRoot, "platform", fixture.id, "index.json")
		value, err := os.ReadFile(indexPath)
		if err != nil {
			t.Fatal(err)
		}
		var index PlatformIndex
		if err := json.Unmarshal(value, &index); err != nil {
			t.Fatal(err)
		}
		index.ManifestSHA256 = strings.Repeat("0", 64)
		value, err = json.Marshal(index)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(indexPath, value, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := StagePlatformRestore(context.Background(), PlatformRestoreStageRequest{
			ID: fixture.id, StagingDir: filepath.Join(t.TempDir(), "stage"),
			Destination: fixture.destination, Passphrase: fixture.passphrase,
		}); err == nil || !strings.Contains(err.Error(), "signature mismatch") {
			t.Fatalf("StagePlatformRestore() error = %v, want signature mismatch", err)
		}
	})

	t.Run("staged postgres", func(t *testing.T) {
		fixture := newPlatformBackupFixture(t, "tampered-stage")
		stage := fixture.stage(t)
		if err := os.WriteFile(stage.PostgresDump, []byte("forged"), 0o600); err != nil {
			t.Fatal(err)
		}
		targetRoot := t.TempDir()
		if err := ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
			Stage: stage, TargetRoot: targetRoot,
			Postgres: &recordingPostgreSQLRestore{}, Confirmed: true,
		}); err == nil || !strings.Contains(err.Error(), "checksum or size mismatch") {
			t.Fatalf("ApplyPlatformRestore() error = %v, want staged evidence failure", err)
		}
		if _, err := os.Stat(filepath.Join(targetRoot, "etc", "midnight")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("filesystem changed before evidence validation: %v", err)
		}
	})
}

func TestPlatformRestoreRollsBackFilesystemWhenPostgresRestoreFails(t *testing.T) {
	fixture := newPlatformBackupFixture(t, "rollback")
	stage := fixture.stage(t)
	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, "etc", "midnight")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), []byte("old-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreFailure := errors.New("postgres restore failed")

	err := ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: targetRoot,
		Postgres: &recordingPostgreSQLRestore{err: restoreFailure}, Confirmed: true,
	})
	if !errors.Is(err, restoreFailure) {
		t.Fatalf("ApplyPlatformRestore() error = %v, want %v", err, restoreFailure)
	}
	assertFileValue(t, filepath.Join(target, "config.yaml"), "old-config")
	assertFileValue(
		t,
		target+".midnight-failed-"+fixture.id+string(filepath.Separator)+"config.yaml",
		"new-config",
	)
	if _, err := os.Stat(target + ".midnight-retained-" + fixture.id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained root still present after rollback: %v", err)
	}
}

func TestPlatformRestoreRejectsStagingInsideArchivedTarget(t *testing.T) {
	fixture := newPlatformBackupFixture(t, "overlapping-stage")
	targetRoot := t.TempDir()
	stage, err := StagePlatformRestore(context.Background(), PlatformRestoreStageRequest{
		ID: fixture.id,
		StagingDir: filepath.Join(
			targetRoot,
			"etc",
			"midnight",
			"restore-stage",
		),
		Destination: fixture.destination,
		Passphrase:  fixture.passphrase,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = ApplyPlatformRestore(context.Background(), PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: targetRoot,
		Postgres: &recordingPostgreSQLRestore{}, Confirmed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "overlaps archived target") {
		t.Fatalf("ApplyPlatformRestore() error = %v, want path-overlap rejection", err)
	}
}

func TestPlatformBackupRejectsOverlapAndExistingBackupID(t *testing.T) {
	source := t.TempDir()
	child := filepath.Join(source, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizePlatformRoots([]PlatformRoot{
		{Source: source, ArchivePath: "etc"},
		{Source: child, ArchivePath: "etc/midnight"},
	}); err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("normalizePlatformRoots() error = %v, want overlap rejection", err)
	}

	fixture := newPlatformBackupFixture(t, "unique-id")
	if _, _, err := CreatePlatformBackup(context.Background(), fixture.request()); err == nil ||
		!strings.Contains(err.Error(), "already has destination objects") {
		t.Fatalf("second CreatePlatformBackup() error = %v, want collision rejection", err)
	}
}

func TestExecPostgreSQLUsesPGDatabaseAndSafeFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("operator PostgreSQL subprocesses are packaged for Linux")
	}
	root := t.TempDir()
	dumpArgs := filepath.Join(root, "dump-args")
	dumpDSN := filepath.Join(root, "dump-dsn")
	dumpBinary := writeExecutable(
		t,
		root,
		"pg_dump",
		`#!/bin/sh
printf '%s\n' "$@" > "$ARGS_FILE"
printf '%s' "$PGDATABASE" > "$DSN_FILE"
printf 'custom-dump'
`,
	)
	postgres := ExecPostgreSQL{
		DSN: "postgres://operator-secret/midnight", DumpBinary: dumpBinary,
		AdditionalEnv: []string{"ARGS_FILE=" + dumpArgs, "DSN_FILE=" + dumpDSN},
	}
	reader, err := postgres.Dump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	value, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if string(value) != "custom-dump" {
		t.Fatalf("pg_dump output = %q", value)
	}
	assertFileValue(t, dumpArgs, "--format=custom\n--no-owner\n--no-acl\n")
	assertFileValue(t, dumpDSN, "postgres://operator-secret/midnight")

	restoreArgs := filepath.Join(root, "restore-args")
	restoreDSN := filepath.Join(root, "restore-dsn")
	restoreBinary := writeExecutable(
		t,
		root,
		"pg_restore",
		`#!/bin/sh
printf '%s\n' "$@" > "$ARGS_FILE"
printf '%s' "$PGDATABASE" > "$DSN_FILE"
`,
	)
	dumpPath := filepath.Join(root, "archive.dump")
	if err := os.WriteFile(dumpPath, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	postgres.RestoreBinary = restoreBinary
	postgres.AdditionalEnv = []string{"ARGS_FILE=" + restoreArgs, "DSN_FILE=" + restoreDSN}
	if err := postgres.Restore(context.Background(), dumpPath); err != nil {
		t.Fatal(err)
	}
	assertFileValue(
		t,
		restoreArgs,
		"--single-transaction\n--exit-on-error\n--clean\n--if-exists\n--no-owner\n--no-acl\n--dbname=\n"+dumpPath+"\n",
	)
	assertFileValue(t, restoreDSN, "postgres://operator-secret/midnight")
}

type platformBackupFixture struct {
	id              string
	source          string
	destinationRoot string
	destination     Destination
	passphrase      []byte
	database        []byte
}

func newPlatformBackupFixture(t *testing.T, id string) platformBackupFixture {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "config.yaml"), []byte("new-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	destinationRoot := t.TempDir()
	destination, err := NewLocalDestination(destinationRoot)
	if err != nil {
		t.Fatal(err)
	}
	fixture := platformBackupFixture{
		id: id, source: source, destinationRoot: destinationRoot,
		destination: destination, passphrase: []byte("correct horse battery staple"),
		database: []byte("real-custom-format-postgres-dump"),
	}
	if _, _, err := CreatePlatformBackup(context.Background(), fixture.request()); err != nil {
		t.Fatalf("CreatePlatformBackup() error = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(destinationRoot, "platform", id, "postgres.dump.mnbk"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, fixture.database) {
		t.Fatal("encrypted postgres object contains plaintext dump")
	}
	return fixture
}

func (f platformBackupFixture) request() PlatformBackupRequest {
	return PlatformBackupRequest{
		ID: f.id, OfflineConfirmed: true,
		Roots:       []PlatformRoot{{Source: f.source, ArchivePath: "etc/midnight"}},
		Destination: f.destination, Passphrase: f.passphrase,
		Postgres: staticPostgreSQLDump{value: f.database},
		Now:      func() time.Time { return time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC) },
	}
}

func (f platformBackupFixture) stage(t *testing.T) PlatformRestoreStage {
	t.Helper()
	stage, err := StagePlatformRestore(context.Background(), PlatformRestoreStageRequest{
		ID: f.id, StagingDir: filepath.Join(t.TempDir(), "stage"),
		Destination: f.destination, Passphrase: f.passphrase,
	})
	if err != nil {
		t.Fatalf("StagePlatformRestore() error = %v", err)
	}
	return stage
}

func assertFileValue(t *testing.T, path, want string) {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != want {
		t.Fatalf("%s = %q, want %q", path, value, want)
	}
}

func writeExecutable(t *testing.T, root, name, value string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(value), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
