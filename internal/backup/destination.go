package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrNotImplemented marks Phase 0 destination methods that are intentionally placeholders.
var ErrNotImplemented = errors.New("backup destination is not implemented")

// Object describes one backup artifact stored in a destination.
type Object struct {
	Key    string `json:"key"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
}

// PlanArtifact describes one required platform backup artifact.
type PlanArtifact struct {
	Name        string
	Source      string
	Destination string
	Required    bool
}

// BackupPlan records the artifacts an operator intends to include in a backup.
type BackupPlan struct {
	Artifacts          []PlanArtifact
	OperatorPassphrase string
}

// Validate checks that sensitive artifacts have explicit operator approval.
func (p BackupPlan) Validate() error {
	for _, artifact := range p.Artifacts {
		if isSecretsKeyArtifact(artifact) && strings.TrimSpace(p.OperatorPassphrase) == "" {
			return errors.New("operator passphrase is required to include secrets.key")
		}
	}
	return nil
}

func isSecretsKeyArtifact(artifact PlanArtifact) bool {
	return artifact.Name == "secrets-key" || artifact.Source == "secrets.key" || artifact.Destination == "secrets.key"
}

// RestoreStepState is the persisted lifecycle state for one restore step.
type RestoreStepState string

const (
	RestoreStepPending   RestoreStepState = "pending"
	RestoreStepRunning   RestoreStepState = "running"
	RestoreStepSucceeded RestoreStepState = "succeeded"
	RestoreStepFailed    RestoreStepState = "failed"
	RestoreStepSkipped   RestoreStepState = "skipped"
)

// RestoreStep records the current state of one backup restore operation.
type RestoreStep struct {
	Name       string           `json:"name"`
	State      RestoreStepState `json:"state"`
	ReasonCode string           `json:"reasonCode,omitempty"`
	Message    string           `json:"message,omitempty"`
}

// RestoreSmokeProbeResult records the outcome of a non-destructive restore probe.
type RestoreSmokeProbeResult struct {
	Name       string           `json:"name"`
	State      RestoreStepState `json:"state"`
	CheckedAt  time.Time        `json:"checkedAt"`
	Object     Object           `json:"object,omitempty"`
	ReasonCode string           `json:"reasonCode,omitempty"`
	Message    string           `json:"message,omitempty"`
}

// VerificationResultSummary is the release-gate aggregate for backup verification probes.
type VerificationResultSummary struct {
	Total         int              `json:"total"`
	Succeeded     int              `json:"succeeded"`
	Failed        int              `json:"failed"`
	Skipped       int              `json:"skipped"`
	Pending       int              `json:"pending"`
	Running       int              `json:"running"`
	OverallState  RestoreStepState `json:"overallState"`
	FailedReasons map[string]int   `json:"failedReasons"`
}

// Validate checks that a smoke probe result is safe to persist and gate on.
func (r RestoreSmokeProbeResult) Validate() error {
	step := RestoreStep{Name: r.Name, State: r.State, ReasonCode: r.ReasonCode, Message: r.Message}
	if err := step.Validate(); err != nil {
		return err
	}
	if r.CheckedAt.IsZero() {
		return errors.New("restore smoke probe checkedAt is required")
	}
	if r.State == RestoreStepSucceeded {
		if strings.TrimSpace(r.Object.Key) == "" {
			return errors.New("successful restore smoke probe object key is required")
		}
		if r.Object.Bytes < 0 {
			return errors.New("restore smoke probe object bytes must be non-negative")
		}
	}
	return nil
}

// SummarizeVerificationResults validates and aggregates backup verification probe outcomes.
func SummarizeVerificationResults(results []RestoreSmokeProbeResult) (VerificationResultSummary, error) {
	summary := VerificationResultSummary{Total: len(results), OverallState: RestoreStepSucceeded, FailedReasons: map[string]int{}}
	for i, result := range results {
		if err := result.Validate(); err != nil {
			return VerificationResultSummary{}, fmt.Errorf("verificationResults[%d]: %w", i, err)
		}
		switch result.State {
		case RestoreStepSucceeded:
			summary.Succeeded++
		case RestoreStepFailed:
			summary.Failed++
			summary.FailedReasons[result.ReasonCode]++
		case RestoreStepSkipped:
			summary.Skipped++
		case RestoreStepPending:
			summary.Pending++
		case RestoreStepRunning:
			summary.Running++
		default:
			return VerificationResultSummary{}, errors.New("verification result state is invalid")
		}
	}
	if summary.Failed > 0 {
		summary.OverallState = RestoreStepFailed
	} else if summary.Running > 0 {
		summary.OverallState = RestoreStepRunning
	} else if summary.Pending > 0 {
		summary.OverallState = RestoreStepPending
	} else if summary.Skipped > 0 && summary.Succeeded == 0 {
		summary.OverallState = RestoreStepSkipped
	}
	return summary, nil
}

// Validate checks that the restore step has a known state and release-gate metadata.
func (s RestoreStep) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("restore step name is required")
	}
	switch s.State {
	case RestoreStepPending, RestoreStepRunning, RestoreStepSucceeded, RestoreStepFailed, RestoreStepSkipped:
	default:
		return errors.New("restore step state is invalid")
	}
	if s.State == RestoreStepFailed && strings.TrimSpace(s.ReasonCode) == "" {
		return errors.New("failed restore step reason code is required")
	}
	return nil
}

// PlatformBackupArtifacts returns the Phase 0 platform backup artifact list.
func PlatformBackupArtifacts() []PlanArtifact {
	return []PlanArtifact{
		{Name: "postgres", Source: "postgres", Destination: "postgres.dump", Required: true},
		{Name: "midnight-config", Source: "/etc/midnight", Destination: "etc-midnight.tar", Required: true},
		{Name: "secrets-key", Source: "secrets.key", Destination: "secrets.key", Required: true},
		{Name: "registry-metadata", Source: "registry metadata", Destination: "registry-metadata.json", Required: true},
		{Name: "acme-state", Source: "ACME state", Destination: "acme-state.tar", Required: true},
	}
}

// Destination is the storage contract used by backup and restore flows.
type Destination interface {
	Put(ctx context.Context, key string, body io.Reader) (Object, error)
	Get(ctx context.Context, key string) (io.ReadCloser, Object, error)
	List(ctx context.Context, prefix string) ([]Object, error)
}

// LocalDestination stores backup artifacts on the local filesystem.
type LocalDestination struct {
	Root string
}

// ParseLocalURI returns a filesystem path from a local:// or file:// backup URI.
func ParseLocalURI(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("local backup uri is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "local" && parsed.Scheme != "file" {
		return "", errors.New("local backup uri must use local:// or file://")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("local backup uri must not include query or fragment")
	}
	path := parsed.Path
	if parsed.Scheme == "local" && parsed.Host != "" {
		path = parsed.Host + "/" + strings.TrimPrefix(parsed.Path, "/")
	}
	if parsed.Scheme == "file" && parsed.Host != "" {
		path = "//" + parsed.Host + parsed.Path
	}
	path = filepath.FromSlash(path)
	if strings.TrimSpace(path) == "" || path == string(filepath.Separator) {
		return "", errors.New("local backup uri path is required")
	}
	return path, nil
}

// NewLocalDestination validates and returns a local filesystem destination.
func NewLocalDestination(root string) (*LocalDestination, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("local backup destination root is required")
	}
	return &LocalDestination{Root: filepath.Clean(root)}, nil
}

func (d *LocalDestination) Put(ctx context.Context, key string, body io.Reader) (Object, error) {
	path, cleanKey, err := d.objectPath(key)
	if err != nil {
		return Object{}, err
	}
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Object{}, fmt.Errorf("local backup mkdir %s: %w", cleanKey, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".midnight-backup-*")
	if err != nil {
		return Object{}, fmt.Errorf("local backup temp %s: %w", cleanKey, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	hash := sha256.New()
	written, copyErr := io.Copy(tmp, io.TeeReader(body, hash))
	closeErr := tmp.Close()
	if copyErr != nil {
		return Object{}, fmt.Errorf("local backup write %s: %w", cleanKey, copyErr)
	}
	if closeErr != nil {
		return Object{}, fmt.Errorf("local backup close %s: %w", cleanKey, closeErr)
	}
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return Object{}, fmt.Errorf("local backup commit %s: %w", cleanKey, err)
	}
	return Object{Key: cleanKey, Bytes: written, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (d *LocalDestination) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	path, cleanKey, err := d.objectPath(key)
	if err != nil {
		return nil, Object{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, Object{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, Object{}, fmt.Errorf("local backup open %s: %w", cleanKey, err)
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, Object{}, fmt.Errorf("local backup stat %s: %w", cleanKey, err)
	}
	return file, Object{Key: cleanKey, Bytes: stat.Size()}, nil
}

func (d *LocalDestination) List(ctx context.Context, prefix string) ([]Object, error) {
	cleanPrefix, err := cleanObjectPrefix(prefix)
	if err != nil {
		return nil, err
	}
	root := filepath.Clean(d.Root)
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return []Object{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("local backup stat root: %w", err)
	}
	objects := []Object{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if cleanPrefix != "" && !strings.HasPrefix(key, cleanPrefix) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		objects = append(objects, Object{Key: key, Bytes: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("local backup list %q: %w", cleanPrefix, err)
	}
	sort.SliceStable(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	return objects, nil
}

func (d *LocalDestination) objectPath(key string) (string, string, error) {
	cleanKey, err := cleanObjectKey(key)
	if err != nil {
		return "", "", err
	}
	root, err := filepath.Abs(filepath.Clean(d.Root))
	if err != nil {
		return "", "", fmt.Errorf("local backup root: %w", err)
	}
	path := filepath.Join(root, filepath.FromSlash(cleanKey))
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("local backup path: %w", err)
	}
	if absPath != root && !strings.HasPrefix(absPath, root+string(filepath.Separator)) {
		return "", "", errors.New("local backup object key escapes destination root")
	}
	return absPath, cleanKey, nil
}

func cleanObjectKey(key string) (string, error) {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	key = strings.TrimPrefix(key, "/")
	if key == "" {
		return "", errors.New("backup object key is required")
	}
	clean := pathClean(key)
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", errors.New("backup object key must be a safe relative path")
	}
	return clean, nil
}

func cleanObjectPrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(strings.ReplaceAll(prefix, "\\", "/"))
	prefix = strings.TrimPrefix(prefix, "/")
	if prefix == "" {
		return "", nil
	}
	hadTrailingSlash := strings.HasSuffix(prefix, "/")
	clean, err := cleanObjectKey(prefix)
	if err != nil {
		return "", err
	}
	if hadTrailingSlash {
		return clean + "/", nil
	}
	return clean, nil
}

func pathClean(key string) string {
	parts := strings.Split(key, "/")
	stack := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(stack) == 0 {
				return ".."
			}
			stack = stack[:len(stack)-1]
		default:
			stack = append(stack, part)
		}
	}
	if len(stack) == 0 {
		return "."
	}
	return strings.Join(stack, "/")
}
