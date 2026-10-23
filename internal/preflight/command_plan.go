package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const (
	CommandSpecDockerAPI    = "preflight.docker_api_version"
	CommandSpecCgroupFSType = "preflight.cgroup_fs_type"
	CommandEvidenceSchemaV1 = "midnight.preflight.command_evidence.v1"
	defaultCommandTimeout   = 3 * time.Second
)

// CommandSpec is the immutable allow-list entry for a command-backed preflight check.
type CommandSpec struct {
	ID          string  `json:"id"`
	CheckID     string  `json:"check_id"`
	Command     Command `json:"command"`
	Description string  `json:"description"`
}

// CommandPlanEntry is the JSON-facing safe execution plan shape.
type CommandPlanEntry struct {
	ID           string   `json:"id"`
	CheckID      string   `json:"check_id"`
	Executable   string   `json:"executable"`
	Args         []string `json:"args"`
	Timeout      string   `json:"timeout"`
	RequiresRoot bool     `json:"requires_root"`
	Description  string   `json:"description"`
}

// CommandEvidence is JSON-safe evidence derived from an injected command runner result.
type CommandEvidence struct {
	Schema       string   `json:"schema"`
	SpecID       string   `json:"spec_id"`
	CheckID      string   `json:"check_id"`
	Executable   string   `json:"executable"`
	Args         []string `json:"args"`
	Timeout      string   `json:"timeout"`
	RequiresRoot bool     `json:"requires_root"`
	ExitCode     int      `json:"exit_code"`
	Stdout       string   `json:"stdout,omitempty"`
	Stderr       string   `json:"stderr,omitempty"`
	TimedOut     bool     `json:"timed_out,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// CommandEvidenceRedactionStatus is the supportability decision for redacted command evidence.
type CommandEvidenceRedactionStatus string

const (
	CommandEvidenceRedactionClean       CommandEvidenceRedactionStatus = "clean"
	CommandEvidenceRedactionNeedsReview CommandEvidenceRedactionStatus = "needs-review"
	CommandEvidenceRedactionBlocked     CommandEvidenceRedactionStatus = "blocked"
)

// CommandEvidenceRedactionFinding identifies a field that should be checked before evidence is shared.
type CommandEvidenceRedactionFinding struct {
	SpecID  string                         `json:"spec_id"`
	Field   string                         `json:"field"`
	Status  CommandEvidenceRedactionStatus `json:"status"`
	Message string                         `json:"message"`
}

// CommandEvidenceRedactionReview summarizes whether redacted command evidence is safe to share.
type CommandEvidenceRedactionReview struct {
	Status   CommandEvidenceRedactionStatus    `json:"status"`
	Findings []CommandEvidenceRedactionFinding `json:"findings,omitempty"`
}

// CommandResult is the richer fake-runner result shape used for evidence.
// ExitCode follows process conventions: 0 for success, non-zero for failures,
// and -1 when no process-like exit status exists.
type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	TimedOut bool
}

// CommandResultRunner can be implemented by tests or caller wiring that can
// report stderr and an exit-like status. RunCommandForEvidence still accepts a
// CommandRunner so existing injected runners remain valid.
type CommandResultRunner interface {
	CommandRunner
	RunCommandResult(context.Context, string, ...string) (CommandResult, error)
}

var commandOutputRedactions = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)(bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)((?:token|password|passwd|secret|credential|api[_-]?key)\s*[=:]\s*)[^\s]+`),
}

var commandEvidenceBlockedSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)authorization:\s*bearer\s+[^\s]*[A-Za-z0-9][^\s]*`),
	regexp.MustCompile(`(?i)\bbearer\s+[^\s]*[A-Za-z0-9][^\s]*`),
	regexp.MustCompile(`(?i)\b(?:token|password|passwd|secret|credential|api[_-]?key)\s*[=:]\s*[^\s]*[A-Za-z0-9][^\s]*`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

var commandEvidenceReviewPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<redacted>`),
	regexp.MustCompile(`(?i)\b(?:token|password|passwd|secret|credential|api[_-]?key|authorization)\b`),
}

// DockerAPICommandSpec returns the only allowed Docker API command probe.
func DockerAPICommandSpec() CommandSpec {
	return CommandSpec{
		ID:          CommandSpecDockerAPI,
		CheckID:     "PRE-DOCKER-API-001",
		Description: "read Docker server API version",
		Command: Command{
			ID:           CommandSpecDockerAPI,
			Name:         "docker",
			Args:         []string{"version", "--format", "{{.Server.APIVersion}}"},
			Timeout:      defaultCommandTimeout,
			RequiresRoot: false,
		},
	}
}

// CgroupFSTypeCommandSpec returns the only allowed cgroup filesystem type probe.
func CgroupFSTypeCommandSpec() CommandSpec {
	return CommandSpec{
		ID:          CommandSpecCgroupFSType,
		CheckID:     "PRE-CGROUP-001",
		Description: "read /sys/fs/cgroup filesystem type",
		Command: Command{
			ID:           CommandSpecCgroupFSType,
			Name:         "stat",
			Args:         []string{"-fc", "%T", "/sys/fs/cgroup"},
			Timeout:      defaultCommandTimeout,
			RequiresRoot: false,
		},
	}
}

// AllowedCommandSpecs returns the complete command execution plan. It does not execute commands.
func AllowedCommandSpecs() []CommandSpec {
	return []CommandSpec{CgroupFSTypeCommandSpec(), DockerAPICommandSpec()}
}

// CommandExecutionPlan returns the JSON-safe plan without executing commands.
func CommandExecutionPlan() []CommandPlanEntry {
	specs := AllowedCommandSpecs()
	plan := make([]CommandPlanEntry, 0, len(specs))
	for _, spec := range specs {
		command := spec.Command
		plan = append(plan, CommandPlanEntry{
			ID:           spec.ID,
			CheckID:      spec.CheckID,
			Executable:   command.Name,
			Args:         append([]string(nil), command.Args...),
			Timeout:      command.Timeout.String(),
			RequiresRoot: command.RequiresRoot,
			Description:  spec.Description,
		})
	}
	return plan
}

// ValidateAllowedCommand rejects commands that are not byte-for-byte equivalent to an allow-list entry.
func ValidateAllowedCommand(command Command) error {
	_, err := allowedCommandSpecForCommand(command)
	return err
}

func allowedCommandSpecForCommand(command Command) (CommandSpec, error) {
	for _, spec := range AllowedCommandSpecs() {
		if command.ID != "" && command.ID != spec.ID {
			continue
		}
		if command.Name == spec.Command.Name && reflect.DeepEqual(command.Args, spec.Command.Args) {
			return spec, nil
		}
	}
	return CommandSpec{}, fmt.Errorf("command %q with args %q is not in the preflight allow-list", command.Name, command.Args)
}

func allowedCommandSpecForSpec(spec CommandSpec) (CommandSpec, error) {
	if spec.ID == "" {
		return allowedCommandSpecForCommand(spec.Command)
	}
	for _, allowed := range AllowedCommandSpecs() {
		if spec.ID != allowed.ID {
			continue
		}
		if spec.CheckID != "" && spec.CheckID != allowed.CheckID {
			return CommandSpec{}, fmt.Errorf("command spec %q check ID %q does not match allow-list check ID %q", spec.ID, spec.CheckID, allowed.CheckID)
		}
		if !reflect.DeepEqual(spec.Command, allowed.Command) {
			return CommandSpec{}, fmt.Errorf("command spec %q does not match the preflight allow-list", spec.ID)
		}
		return allowed, nil
	}
	return CommandSpec{}, fmt.Errorf("command spec %q is not in the preflight allow-list", spec.ID)
}

// EvidenceFromCommandOutput converts already-collected runner output to stable JSON evidence.
func EvidenceFromCommandOutput(spec CommandSpec, stdout []byte, runErr error) (CommandEvidence, error) {
	result := CommandResult{Stdout: stdout, ExitCode: exitCodeForError(runErr)}
	return EvidenceFromCommandResult(spec, result, runErr)
}

// EvidenceFromCommandResult converts already-collected runner output to stable JSON evidence.
func EvidenceFromCommandResult(spec CommandSpec, result CommandResult, runErr error) (CommandEvidence, error) {
	allowed, err := allowedCommandSpecForSpec(spec)
	if err != nil {
		return CommandEvidence{}, err
	}
	command := allowed.Command
	evidence := CommandEvidence{
		Schema:       CommandEvidenceSchemaV1,
		SpecID:       allowed.ID,
		CheckID:      allowed.CheckID,
		Executable:   command.Name,
		Args:         append([]string(nil), command.Args...),
		Timeout:      command.Timeout.String(),
		RequiresRoot: command.RequiresRoot,
		ExitCode:     result.ExitCode,
		Stdout:       redactCommandOutput(string(result.Stdout)),
		Stderr:       redactCommandOutput(string(result.Stderr)),
		TimedOut:     result.TimedOut || errors.Is(runErr, context.DeadlineExceeded),
	}
	if runErr != nil {
		evidence.Error = redactCommandOutput(runErr.Error())
	}
	return evidence, nil
}

// MarshalCommandEvidenceJSON serializes command evidence for installer reports or support bundles.
func MarshalCommandEvidenceJSON(evidence CommandEvidence) ([]byte, error) {
	return json.MarshalIndent(evidence, "", "  ")
}

// RunCommandForEvidence runs an allowed command only when caller wiring provides a runner.
func RunCommandForEvidence(ctx context.Context, spec CommandSpec, runner CommandRunner) (CommandEvidence, error) {
	if runner == nil {
		return CommandEvidence{}, fmt.Errorf("command runner is required")
	}
	allowed, err := allowedCommandSpecForSpec(spec)
	if err != nil {
		return CommandEvidence{}, err
	}
	command := allowed.Command
	if command.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, command.Timeout)
		defer cancel()
	}
	if resultRunner, ok := runner.(CommandResultRunner); ok {
		result, err := resultRunner.RunCommandResult(ctx, command.Name, command.Args...)
		if result.ExitCode == 0 && err != nil {
			result.ExitCode = exitCodeForError(err)
		}
		return EvidenceFromCommandResult(allowed, result, err)
	}
	stdout, err := runner.Run(ctx, command.Name, command.Args...)
	return EvidenceFromCommandResult(allowed, CommandResult{Stdout: stdout, ExitCode: exitCodeForError(err)}, err)
}

// ReviewCommandEvidenceRedaction scans already-redacted evidence text fields for
// obvious remaining secrets and lower-confidence sensitive hints.
func ReviewCommandEvidenceRedaction(evidence []CommandEvidence) CommandEvidenceRedactionReview {
	review := CommandEvidenceRedactionReview{Status: CommandEvidenceRedactionClean}
	for _, item := range evidence {
		reviewCommandEvidenceField(&review, item.SpecID, "stdout", item.Stdout)
		reviewCommandEvidenceField(&review, item.SpecID, "stderr", item.Stderr)
		reviewCommandEvidenceField(&review, item.SpecID, "error", item.Error)
	}
	return review
}

func reviewCommandEvidenceField(review *CommandEvidenceRedactionReview, specID string, field string, text string) {
	if text == "" {
		return
	}
	for _, pattern := range commandEvidenceBlockedSecretPatterns {
		for _, match := range pattern.FindAllString(text, -1) {
			if strings.Contains(strings.ToLower(match), "<redacted>") {
				continue
			}
			addCommandEvidenceRedactionFinding(review, CommandEvidenceRedactionFinding{
				SpecID:  specID,
				Field:   field,
				Status:  CommandEvidenceRedactionBlocked,
				Message: "field contains an obvious unredacted secret pattern",
			})
			return
		}
	}
	for _, pattern := range commandEvidenceReviewPatterns {
		if pattern.MatchString(text) {
			addCommandEvidenceRedactionFinding(review, CommandEvidenceRedactionFinding{
				SpecID:  specID,
				Field:   field,
				Status:  CommandEvidenceRedactionNeedsReview,
				Message: "field contains redaction markers or sensitive-keyword context",
			})
			return
		}
	}
}

func addCommandEvidenceRedactionFinding(review *CommandEvidenceRedactionReview, finding CommandEvidenceRedactionFinding) {
	review.Findings = append(review.Findings, finding)
	if finding.Status == CommandEvidenceRedactionBlocked {
		review.Status = CommandEvidenceRedactionBlocked
		return
	}
	if review.Status == CommandEvidenceRedactionClean {
		review.Status = CommandEvidenceRedactionNeedsReview
	}
}

func exitCodeForError(err error) int {
	if err == nil {
		return 0
	}
	return -1
}

func redactCommandOutput(text string) string {
	if text == "" {
		return ""
	}
	redacted := text
	for _, pattern := range commandOutputRedactions {
		redacted = pattern.ReplaceAllString(redacted, "${1}<redacted>")
	}
	return strings.ReplaceAll(redacted, "\x00", "")
}
