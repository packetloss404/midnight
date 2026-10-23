package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/db"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

// runAdmin dispatches `midnight admin <subcommand>` invocations.
// Returns the exit code main.go passes to os.Exit.
func runAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "midnight admin: subcommand required")
		fmt.Fprintln(stderr, "  midnight admin create-user --email EMAIL --display-name NAME --workspace-slug SLUG --workspace-name NAME")
		fmt.Fprintln(stderr, "  midnight admin reset-password --email EMAIL [--keep-totp]  # operator recovery for a lost password")
		fmt.Fprintln(stderr, "  midnight admin migrate [--dsn URL] [--dry-run|--yes]")
		fmt.Fprintln(stderr, "  midnight admin authorize-cli [--api-base URL] [--browserless]  # operator recovery compatibility")
		return 64
	}
	switch args[0] {
	case "create-user":
		return runAdminCreateUser(args[1:], stdout, stderr)
	case "reset-password":
		return runAdminResetPassword(args[1:], stdout, stderr)
	case "migrate":
		return runAdminMigrate(args[1:], stdout, stderr)
	case "authorize-cli":
		return runAdminAuthorizeCLI(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight admin: unknown subcommand %q\n", args[0])
		return 64
	}
}

type adminMigrateFlags struct {
	dsn      *string
	dsnFile  *string
	timeout  *time.Duration
	yes      *bool
	dryRun   *bool
	noPrompt *bool
}

func defineAdminMigrateFlags(fs *flag.FlagSet) adminMigrateFlags {
	return adminMigrateFlags{
		dsn:      fs.String("dsn", "", "Postgres DSN (default: $MIDNIGHT_DB_DSN)"),
		dsnFile:  fs.String("dsn-file", pg.DefaultDSNFile, "Postgres DSN file (default: $MIDNIGHT_DB_DSN_FILE or /etc/midnight/db.dsn)"),
		timeout:  fs.Duration("timeout", 30*time.Second, "migration timeout"),
		yes:      fs.Bool("yes", false, "apply migrations without an interactive prompt"),
		dryRun:   fs.Bool("dry-run", false, "preview the target and schema version without applying migrations"),
		noPrompt: fs.Bool("no-prompt", false, "deprecated alias for --yes"),
	}
}

func runAdminMigrate(args []string, stdout, stderr io.Writer) int {
	return runAdminMigrateWithInput(args, os.Stdin, stdout, stderr, isTerminalInput(os.Stdin))
}

func runAdminMigrateWithInput(
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	interactive bool,
) int {
	fs := flag.NewFlagSet("midnight admin migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineAdminMigrateFlags(fs)
	dsn, dsnFile, timeout := f.dsn, f.dsnFile, f.timeout
	yes, dryRun, noPrompt := f.yes, f.dryRun, f.noPrompt
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *dryRun && (*yes || *noPrompt) {
		fmt.Fprintln(stderr, "midnight admin migrate: --dry-run cannot be combined with --yes or --no-prompt")
		return 64
	}
	loaded, err := loadAdminDSN(*dsn, *dsnFile)
	if err != nil {
		fmt.Fprintf(stderr, "midnight: --dsn, %s, or %s is required\n", pg.EnvDSN, pg.EnvDSNFile)
		return 64
	}
	schemaVersion, err := adminManifestSchemaVersion()
	if err != nil {
		fmt.Fprintf(stderr, "midnight: load migration manifest: %s\n", err)
		return 1
	}
	if *dryRun {
		fmt.Fprintf(stdout, "midnight: would apply migrations through schema_version %d against %s\n", schemaVersion, redactDSN(loaded.DSN))
		return 0
	}
	applyWithoutPrompt := *yes || *noPrompt
	if *noPrompt {
		fmt.Fprintln(stderr, "midnight admin migrate: --no-prompt is deprecated; use --yes")
	}
	if !applyWithoutPrompt {
		if !interactive {
			fmt.Fprintln(stderr, "midnight admin migrate: confirmation requires a terminal; use --yes for automation or --dry-run to preview")
			return 64
		}
		fmt.Fprintf(stderr, "Apply migrations through schema_version %d against %s? [y/N] ", schemaVersion, redactDSN(loaded.DSN))
		confirmed, err := readAdminMigrationConfirmation(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "midnight admin migrate: read confirmation: %s\n", err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(stderr, "midnight admin migrate: cancelled")
			return 0
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: loaded.DSN, ConnectTimeout: 10 * time.Second})
	if err != nil {
		fmt.Fprintf(stderr, "midnight: open database: %s\n", err)
		return 1
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(stderr, "midnight: ping database: %s\n", err)
		return 1
	}
	applied, err := (&pg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}).Up(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "midnight: apply migrations: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "midnight: applied %d migration(s), schema_version %d\n", len(applied), schemaVersion)
	fmt.Fprintf(stdout, "midnight: targeted DSN: %s\n", redactDSN(loaded.DSN))
	return 0
}

func isTerminalInput(input *os.File) bool {
	if input == nil {
		return false
	}
	info, err := input.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func readAdminMigrationConfirmation(input io.Reader) (bool, error) {
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func loadAdminDSN(explicitDSN, dsnFile string) (pg.LoadedDSN, error) {
	if dsn := strings.TrimSpace(explicitDSN); dsn != "" {
		return pg.LoadedDSN{DSN: dsn, Source: pg.DSNSourceEnv}, nil
	}
	if dsn := strings.TrimSpace(os.Getenv(pg.EnvDSN)); dsn != "" {
		return pg.LoadedDSN{DSN: dsn, Source: pg.DSNSourceEnv}, nil
	}
	if dsn := strings.TrimSpace(os.Getenv("MIDNIGHT_DATABASE_URL")); dsn != "" {
		return pg.LoadedDSN{DSN: dsn, Source: pg.DSNSourceEnv}, nil
	}
	return (&pg.FileDSNLoader{
		EnvVar:    pg.EnvDSN,
		EnvVarRef: pg.EnvDSNFile,
		Path:      dsnFile,
	}).Load()
}

func adminManifestSchemaVersion() (int, error) {
	manifest, err := db.LoadMigrationManifest(migrations.FS, "manifest.yaml")
	if err != nil {
		return 0, err
	}
	return manifest.SchemaVersion, nil
}

func redactDSN(dsn string) string {
	// Best-effort: strip the password component from a Postgres
	// DSN. Pure stdlib, no parsing dependency.
	const sentinel = "://"
	idx := indexOfRune(dsn, sentinel)
	if idx < 0 {
		return dsn
	}
	prefix := dsn[:idx+len(sentinel)]
	rest := dsn[idx+len(sentinel):]
	atIdx := -1
	for i, r := range rest {
		if r == '@' {
			atIdx = i
			break
		}
	}
	if atIdx < 0 {
		return dsn
	}
	creds := rest[:atIdx]
	suffix := rest[atIdx:]
	colon := -1
	for i, r := range creds {
		if r == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return prefix + creds + suffix
	}
	return prefix + creds[:colon+1] + "***" + suffix
}

func indexOfRune(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
