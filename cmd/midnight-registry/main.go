// Command midnight-registry is the Phase 1 Lane 3 OCI registry orchestrator.
// It launches the pinned `registry` binary
// (github.com/distribution/distribution/v3) as a subprocess, manages the
// JSON-backed pin store, and runs GC dry-run / apply sweeps with writes
// paused.
//
// Subcommands:
//
//	midnight-registry version
//	midnight-registry serve --config <path>
//	midnight-registry pin add --pins <path> --digest sha256:... --service ... --deployment ... --reason ...
//	midnight-registry pin list --pins <path>
//	midnight-registry gc plan --pins <path> --tags <path>
//	midnight-registry gc apply --pins <path> --tags <path> --confirm
//
// The --pins flag points at the JSON-backed pin store on disk; the --tags
// flag points at a JSON file enumerating the registry tags to consider for
// GC. Lane 3 ships sample inputs under deploy/registry/.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/registry"
	"github.com/midnight/midnight/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "midnight-registry: subcommand required (version|serve|pin|gc)")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch os.Args[1] {
	case "version":
		fmt.Println(version.Version)
	case "serve":
		if err := runServe(ctx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "midnight-registry serve:", err)
			os.Exit(1)
		}
	case "pin":
		if err := runPin(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "midnight-registry pin:", err)
			os.Exit(1)
		}
	case "gc":
		if err := runGC(ctx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "midnight-registry gc:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "midnight-registry: unknown subcommand", os.Args[1])
		os.Exit(2)
	}
}

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	binary := fs.String("registry", "", "path to pinned registry binary (default: registry on PATH)")
	config := fs.String("config", "", "absolute path to distribution config.yml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := verifyRegistryVersion(ctx, *binary); err != nil {
		return err
	}
	embedded := newRegistryProcess(*binary, *config)
	if err := embedded.Start(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return embedded.Stop()
	case <-embedded.Done():
		if err := embedded.Wait(); err != nil {
			return fmt.Errorf("registry subprocess exited: %w", err)
		}
		return errors.New("registry subprocess exited unexpectedly")
	}
}

type registryProcess interface {
	Start(context.Context) error
	Stop() error
	Done() <-chan struct{}
	Wait() error
}

var newRegistryProcess = func(binary, config string) registryProcess {
	return &registry.EmbeddedRegistry{Binary: binary, ConfigPath: config, Logger: os.Stderr}
}

func runPin(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: midnight-registry pin <add|list>")
	}
	switch args[0] {
	case "add":
		return runPinAdd(args[1:])
	case "list":
		return runPinList(args[1:])
	default:
		return fmt.Errorf("unknown pin subcommand %q", args[0])
	}
}

func runPinAdd(args []string) error {
	fs := flag.NewFlagSet("pin add", flag.ContinueOnError)
	pinsPath := fs.String("pins", "", "absolute path to JSON pin store")
	repository := fs.String("repository", "", "image repository (e.g. midnight/demo)")
	digest := fs.String("digest", "", "image digest (sha256:...)")
	serviceID := fs.String("service", "", "service id")
	deploymentID := fs.String("deployment", "", "deployment id")
	reason := fs.String("reason", string(registry.PinReasonActive), "pin reason (active|rollback_window|promotion|restore_manifest|audit_retention)")
	pinnedUntil := fs.String("until", "", "RFC3339 timestamp; empty for active pins")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := registry.NewFilePinStore(absPath(*pinsPath))
	if err != nil {
		return err
	}
	row := registry.PinRow{
		Repository:   *repository,
		Digest:       *digest,
		ServiceID:    *serviceID,
		DeploymentID: *deploymentID,
		Reason:       registry.PinReason(*reason),
		CreatedAt:    time.Now().UTC(),
	}
	if strings.TrimSpace(*pinnedUntil) != "" {
		ts, err := time.Parse(time.RFC3339, *pinnedUntil)
		if err != nil {
			return fmt.Errorf("parse --until: %w", err)
		}
		row.PinnedUntil = ts.UTC()
	}
	return store.Insert(row)
}

func runPinList(args []string) error {
	fs := flag.NewFlagSet("pin list", flag.ContinueOnError)
	pinsPath := fs.String("pins", "", "absolute path to JSON pin store")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := registry.NewFilePinStore(absPath(*pinsPath))
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(store.Rows())
}

func runGC(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: midnight-registry gc <plan|apply>")
	}
	_ = ctx
	switch args[0] {
	case "plan":
		return runGCPlan(args[1:], false)
	case "apply":
		return runGCPlan(args[1:], true)
	default:
		return fmt.Errorf("unknown gc subcommand %q", args[0])
	}
}

func runGCPlan(args []string, apply bool) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	pinsPath := fs.String("pins", "", "absolute path to JSON pin store")
	tagsPath := fs.String("tags", "", "absolute path to JSON tag inventory")
	rulesPath := fs.String("rules", "", "absolute path to JSON retention rules (optional)")
	confirm := fs.Bool("confirm", false, "(apply only) confirm reclaim of unpinned digests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := registry.NewFilePinStore(absPath(*pinsPath))
	if err != nil {
		return err
	}
	tags, err := readTagsJSON(absPath(*tagsPath))
	if err != nil {
		return err
	}
	rules, err := readRulesJSON(*rulesPath)
	if err != nil {
		return err
	}
	orchestrator := registry.GCOrchestrator{
		Tags:         tags,
		Rules:        rules,
		Probe:        store,
		ConfirmApply: apply && *confirm,
		GeneratedAt:  time.Now().UTC(),
	}
	plan, err := orchestrator.PlanGC()
	if err != nil {
		return err
	}
	if !apply {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(plan)
	}
	evidence, err := orchestrator.ApplyGC(context.Background(), plan)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(evidence)
}

func readTagsJSON(path string) ([]registry.ImageTag, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tags []registry.ImageTag
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

func readRulesJSON(path string) ([]registry.RetentionRule, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules []registry.RetentionRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func absPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func verifyRegistryVersion(ctx context.Context, binary string) error {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		binary = "registry"
	}
	out, err := execCommandOutput(ctx, binary, "--version")
	if err != nil {
		return fmt.Errorf("registry --version failed: %w", err)
	}
	actual := build.ParsePinnedBinaryVersion(string(out))
	if actual == "" {
		return fmt.Errorf("registry --version output unparsable: %q", string(out))
	}
	if err := build.ValidateDistributionVersion(actual); err != nil {
		return err
	}
	return nil
}

var execCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
