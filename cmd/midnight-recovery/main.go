// Command midnight-recovery performs offline, operator-confirmed platform
// backup and disaster restore. It is intentionally separate from the online
// API so it can back up and restore the API's own PostgreSQL database.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/midnight/midnight/internal/backup"
	"github.com/midnight/midnight/internal/version"
)

const usage = `usage:
  midnight-recovery create --id ID --destination URI --dsn-file PATH --passphrase-file PATH --root SOURCE=ARCHIVE --offline-confirmed
  midnight-recovery stage  --id ID --destination URI --passphrase-file PATH --staging-dir PATH
  midnight-recovery apply  --stage-file PATH --target-root PATH --dsn-file PATH --offline-confirmed --confirm-restore ID
  midnight-recovery version

The control plane and workers must stay stopped for the complete create or
apply operation. Destination URI supports local://, file://, and s3://.`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "midnight-recovery:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return errors.New("subcommand required")
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version.Version)
		return nil
	case "create":
		return runCreate(ctx, args[1:], stdout, stderr)
	case "stage":
		return runStage(ctx, args[1:], stdout, stderr)
	case "apply":
		return runApply(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, usage)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runCreate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var roots repeatedFlag
	var volumeBackupIDs repeatedFlag
	id := flags.String("id", "", "unique backup ID")
	destinationURI := flags.String("destination", "", "backup destination URI")
	dsnFile := flags.String("dsn-file", "", "file containing the PostgreSQL DSN")
	passphraseFile := flags.String("passphrase-file", "", "file containing the backup passphrase")
	offlineConfirmed := flags.Bool("offline-confirmed", false, "confirm the control plane and workers are stopped")
	flags.Var(&roots, "root", "filesystem root mapping SOURCE=ARCHIVE (repeatable)")
	flags.Var(&volumeBackupIDs, "volume-backup-id", "completed volume backup ID to reference (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("create does not accept positional arguments")
	}
	dsn, err := readRequiredSecret(*dsnFile, "PostgreSQL DSN")
	if err != nil {
		return err
	}
	defer clearBytes(dsn)
	passphrase, err := readRequiredSecret(*passphraseFile, "backup passphrase")
	if err != nil {
		return err
	}
	defer clearBytes(passphrase)
	destination, err := backup.OpenDestination(ctx, *destinationURI)
	if err != nil {
		return err
	}
	parsedRoots, err := parseRoots(roots)
	if err != nil {
		return err
	}
	index, manifest, err := backup.CreatePlatformBackup(ctx, backup.PlatformBackupRequest{
		ID: *id, OfflineConfirmed: *offlineConfirmed, Roots: parsedRoots,
		VolumeBackupIDs: volumeBackupIDs, Destination: destination,
		Passphrase: passphrase, Postgres: backup.ExecPostgreSQL{DSN: string(dsn)},
	})
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		Status   string                  `json:"status"`
		Index    backup.PlatformIndex    `json:"index"`
		Manifest backup.PlatformManifest `json:"manifest"`
	}{
		Status: "completed", Index: index, Manifest: manifest,
	})
}

func runStage(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("stage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	id := flags.String("id", "", "backup ID")
	destinationURI := flags.String("destination", "", "backup destination URI")
	passphraseFile := flags.String("passphrase-file", "", "file containing the backup passphrase")
	stagingDir := flags.String("staging-dir", "", "new dedicated staging directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("stage does not accept positional arguments")
	}
	passphrase, err := readRequiredSecret(*passphraseFile, "backup passphrase")
	if err != nil {
		return err
	}
	defer clearBytes(passphrase)
	destination, err := backup.OpenDestination(ctx, *destinationURI)
	if err != nil {
		return err
	}
	stage, err := backup.StagePlatformRestore(ctx, backup.PlatformRestoreStageRequest{
		ID: *id, StagingDir: *stagingDir, Destination: destination, Passphrase: passphrase,
	})
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		Status    string `json:"status"`
		BackupID  string `json:"backup_id"`
		StageFile string `json:"stage_file"`
	}{
		Status: "staged", BackupID: stage.Manifest.ID,
		StageFile: filepath.Join(stage.Directory, "stage.json"),
	})
}

func runApply(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	stageFile := flags.String("stage-file", "", "stage.json emitted by the stage command")
	targetRoot := flags.String("target-root", "", "filesystem root under which archive paths are installed")
	dsnFile := flags.String("dsn-file", "", "file containing the PostgreSQL DSN")
	offlineConfirmed := flags.Bool("offline-confirmed", false, "confirm the control plane and workers are stopped")
	confirmRestore := flags.String("confirm-restore", "", "type the backup ID to authorize destructive restore")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("apply does not accept positional arguments")
	}
	if !*offlineConfirmed {
		return errors.New("apply requires --offline-confirmed")
	}
	stage, err := readStage(*stageFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*confirmRestore) == "" || *confirmRestore != stage.Manifest.ID {
		return errors.New("--confirm-restore must exactly match the staged backup ID")
	}
	dsn, err := readRequiredSecret(*dsnFile, "PostgreSQL DSN")
	if err != nil {
		return err
	}
	defer clearBytes(dsn)
	if err := backup.ApplyPlatformRestore(ctx, backup.PlatformRestoreApplyRequest{
		Stage: stage, TargetRoot: *targetRoot,
		Postgres: backup.ExecPostgreSQL{DSN: string(dsn)}, Confirmed: true,
	}); err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		Status   string `json:"status"`
		BackupID string `json:"backup_id"`
	}{
		Status: "completed", BackupID: stage.Manifest.ID,
	})
}

func readRequiredSecret(path, label string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%s file is required", label)
	}
	value, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read %s file: %w", label, err)
	}
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		clearBytes(value)
		return nil, fmt.Errorf("%s file is empty", label)
	}
	result := append([]byte(nil), trimmed...)
	clearBytes(value)
	return result, nil
}

func readStage(path string) (backup.PlatformRestoreStage, error) {
	if strings.TrimSpace(path) == "" {
		return backup.PlatformRestoreStage{}, errors.New("stage file is required")
	}
	value, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return backup.PlatformRestoreStage{}, fmt.Errorf("read stage file: %w", err)
	}
	var stage backup.PlatformRestoreStage
	if err := json.Unmarshal(value, &stage); err != nil {
		return backup.PlatformRestoreStage{}, fmt.Errorf("decode stage file: %w", err)
	}
	return stage, nil
}

func parseRoots(values []string) ([]backup.PlatformRoot, error) {
	roots := make([]backup.PlatformRoot, 0, len(values))
	for _, value := range values {
		source, archive, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(source) == "" || strings.TrimSpace(archive) == "" {
			return nil, fmt.Errorf("root %q must use SOURCE=ARCHIVE", value)
		}
		roots = append(roots, backup.PlatformRoot{
			Source: strings.TrimSpace(source), ArchivePath: strings.TrimSpace(archive),
		})
	}
	if len(roots) == 0 {
		return nil, errors.New("at least one --root SOURCE=ARCHIVE is required")
	}
	return roots, nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type repeatedFlag []string

func (f *repeatedFlag) String() string { return strings.Join(*f, ",") }
func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}
