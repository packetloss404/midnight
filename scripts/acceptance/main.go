// Command acceptance runs explicitly authorized, credential-ready live checks.
// It never turns an absent credential, preflight, or skipped lane into a pass.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

const receiptSchema = "midnight.live-acceptance/v1"

type check struct {
	Code   string `json:"code"`
	Passed bool   `json:"passed"`
}
type cleanupReceipt struct {
	Attempted               bool     `json:"attempted"`
	Verified                bool     `json:"verified"`
	RetainedEvidence        []string `json:"retained_evidence_kinds,omitempty"`
	RetainedResources       []string `json:"retained_resource_kinds,omitempty"`
	ResidualJournalID       string   `json:"residual_journal_id,omitempty"`
	ResidualJournalLocation string   `json:"residual_journal_location,omitempty"`
	PrivateStateRemoved     bool     `json:"private_state_removed,omitempty"`
}
type receipt struct {
	Schema           string         `json:"schema_version"`
	Lane             string         `json:"lane"`
	Mode             string         `json:"mode"`
	State            string         `json:"state"`
	RunID            string         `json:"run_id"`
	ScopeSHA256      string         `json:"scope_sha256,omitempty"`
	CandidateCommit  string         `json:"candidate_commit,omitempty"`
	ExpectedProvider string         `json:"expected_provider,omitempty"`
	StartedAt        time.Time      `json:"started_at"`
	CompletedAt      time.Time      `json:"completed_at"`
	Checks           []check        `json:"checks"`
	Cleanup          cleanupReceipt `json:"cleanup"`
	Unverified       []string       `json:"unverified"`
	Reason           string         `json:"reason_code,omitempty"`
}
type outcomeError struct {
	code    string
	blocked bool
}

func (e *outcomeError) Error() string { return e.code }
func blocked(code string) error       { return &outcomeError{code: code, blocked: true} }
func failed(code string) error        { return &outcomeError{code: code} }

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, output io.Writer) int {
	fs := flag.NewFlagSet("acceptance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	lane := fs.String("lane", "", "agent, diagnosis, github, or dns-acme")
	envFile := fs.String("env-file", "", "private literal KEY=value configuration file")
	execute := fs.Bool("execute", false, "perform configured live calls and QA-only mutations")
	childDNS := fs.String("internal-dns-state", "", "internal bounded child state directory")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		_, _ = io.WriteString(output, "{\"state\":\"BLOCKED\",\"reason_code\":\"INVALID_ARGUMENTS\"}\n")
		return 64
	}
	r := receipt{Schema: receiptSchema, Lane: *lane, Mode: "preflight", State: "BLOCKED", RunID: nonce(), StartedAt: time.Now().UTC(), Checks: []check{}, Unverified: []string{}}
	if *execute {
		r.Mode = "execute"
	}
	finish := func(err error) int {
		if err == nil && *execute && !r.Cleanup.Verified {
			err = failed("CLEANUP_UNVERIFIED")
		}
		code := 0
		if err != nil {
			var problem *outcomeError
			r.Reason = "CHECK_FAILED"
			r.State = "FAILED"
			code = 1
			if errors.As(err, &problem) {
				r.Reason = problem.code
				if problem.blocked {
					r.State = "BLOCKED"
					code = 78
				}
			}
		} else if !*execute {
			r.State = "READY_NOT_EXECUTED"
			code = 3
		} else {
			r.State = "PASSED"
		}
		r.CompletedAt = time.Now().UTC()
		_ = json.NewEncoder(output).Encode(r)
		return code
	}
	if *lane != "agent" && *lane != "diagnosis" && *lane != "github" && *lane != "dns-acme" {
		r.Lane = ""
		return finish(blocked("UNKNOWN_LANE"))
	}
	if runtime.GOOS == "windows" {
		return finish(blocked("UNIX_PRIVATE_FILE_PERMISSIONS_REQUIRED"))
	}
	cfg, err := loadConfig(*envFile, *lane)
	if err != nil {
		return finish(err)
	}
	r.ScopeSHA256 = scopeHash(cfg, *lane)
	r.CandidateCommit = cfg.values["QA_EXPECTED_COMMIT"]
	if *lane == "agent" || *lane == "diagnosis" {
		r.ExpectedProvider = cfg.values["PROVIDER"]
	}
	if *childDNS != "" {
		if *lane != "dns-acme" || !*execute {
			return 64
		}
		if err := dnsChild(cfg, *childDNS); err != nil {
			return 1
		}
		return 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, cfg.deadline)
	defer deadline()
	runner := newRunner(cfg, &r)
	if err := runner.preflight(ctx, *lane); err != nil {
		return finish(err)
	}
	if !*execute {
		return finish(nil)
	}
	if err := cfg.authorize(*lane); err != nil {
		return finish(err)
	}
	switch *lane {
	case "agent":
		err = runner.agent(ctx)
	case "diagnosis":
		err = runner.diagnosis(ctx)
	case "github":
		err = runner.github(ctx)
	case "dns-acme":
		err = runner.dnsACME(ctx, *envFile)
	default:
		err = blocked("UNKNOWN_LANE")
	}
	return finish(executionResult(ctx, err))
}

// Lane cleanup intentionally has its own context. A signal or execution
// deadline during that cleanup must not convert an interrupted run to PASS.
func executionResult(ctx context.Context, err error) error {
	if err != nil || ctx.Err() == nil {
		return err
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return failed("EXECUTION_DEADLINE_EXCEEDED")
	}
	return failed("EXECUTION_CANCELED")
}

func nonce() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("secure random source unavailable")
	}
	return hex.EncodeToString(raw[:])
}
func scopeHash(cfg config, lane string) string {
	value := lane
	for _, key := range []string{"QA_API_BASE", "QA_EXPECTED_COMMIT", "QA_WORKSPACE_ID", "QA_PROJECT_ID", "QA_SERVICE_ID", "QA_ENVIRONMENT_ID", "PROVIDER", "MODEL", "DIAGNOSIS_IMAGE", "GITHUB_APP_ID", "GITHUB_INSTALLATION_ID", "GITHUB_REPOSITORY_ID", "GITHUB_REPOSITORY", "GITHUB_QA_BRANCH", "GITHUB_EXPECTED_HEAD", "DNS_ZONE_ID", "DNS_ZONE_NAME", "DNS_QA_SUFFIX", "QA_RUNTIME_SSH_TARGET", "QA_RUNTIME_SSH_PORT", "QA_RUNTIME_SSH_SUDO"} {
		value += "\x00" + cfg.values[key]
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (r *receipt) pass(code string)       { r.Checks = append(r.Checks, check{code, true}) }
func (r *receipt) incomplete(code string) { r.Unverified = append(r.Unverified, code) }
func require(ok bool, code string) error {
	if !ok {
		return failed(code)
	}
	return nil
}
func cleanupError(existing error, cleanup error) error {
	if cleanup != nil {
		return failed("CLEANUP_UNVERIFIED")
	}
	return existing
}
