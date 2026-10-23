package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestCollectCommandEvidenceBatchMapsEvidenceToReport(t *testing.T) {
	t.Parallel()

	batch, err := CollectCommandEvidenceBatch(context.Background(), batchEvidenceRunner{
		"stat -fc %T /sys/fs/cgroup":                     {Stdout: []byte("cgroup2fs\n")},
		"docker version --format {{.Server.APIVersion}}": {Stdout: []byte("1.44\n")},
	})
	if err != nil {
		t.Fatalf("CollectCommandEvidenceBatch() error = %v", err)
	}
	if batch.Schema != CommandEvidenceBatchSchemaV1 {
		t.Fatalf("Schema = %q, want %q", batch.Schema, CommandEvidenceBatchSchemaV1)
	}
	if batch.Report.Status != StatusPass || batch.Report.ExitCode != 0 {
		t.Fatalf("Report = %+v, want pass report", batch.Report)
	}
	if len(batch.Evidence) != 2 || batch.Evidence[0].SpecID != CommandSpecCgroupFSType || batch.Evidence[1].SpecID != CommandSpecDockerAPI {
		t.Fatalf("Evidence order = %+v, want allow-list order", batch.Evidence)
	}
	for _, check := range batch.Report.Checks {
		if check.Status != StatusPass || check.Reason == "" {
			t.Fatalf("check = %+v, want pass with reason", check)
		}
	}
}

func TestCollectCommandEvidenceBatchRequiresInjectedRunner(t *testing.T) {
	t.Parallel()

	batch, err := CollectCommandEvidenceBatch(context.Background(), nil)
	if err == nil {
		t.Fatalf("CollectCommandEvidenceBatch(nil) error = nil, want error with batch %+v", batch)
	}
}

func TestCollectCommandEvidenceBatchRecordsRunnerFailures(t *testing.T) {
	t.Parallel()

	batch, err := CollectCommandEvidenceBatch(context.Background(), batchEvidenceRunner{
		"stat -fc %T /sys/fs/cgroup":                     {Stdout: []byte("cgroup2fs\n")},
		"docker version --format {{.Server.APIVersion}}": {Stderr: []byte("Authorization: Bearer secret\n"), ExitCode: 17, Err: errors.New("docker failed token=abc")},
	})
	if err != nil {
		t.Fatalf("CollectCommandEvidenceBatch() error = %v", err)
	}
	if batch.Report.Status != StatusFail || batch.Report.ExitCode != 2 {
		t.Fatalf("Report = %+v, want fail report", batch.Report)
	}
	if got := batch.Evidence[1].Stderr; got != "Authorization: Bearer <redacted>\n" {
		t.Fatalf("Stderr = %q, want redacted bearer", got)
	}
	if got := batch.Evidence[1].Error; got != "docker failed token=<redacted>" {
		t.Fatalf("Error = %q, want redacted token", got)
	}
	if batch.Report.Checks[1].Reason != "docker failed token=<redacted>" {
		t.Fatalf("report reason = %q, want redacted runner error", batch.Report.Checks[1].Reason)
	}
	if batch.RedactionReview.Status != CommandEvidenceRedactionNeedsReview || len(batch.RedactionReview.Findings) != 2 {
		t.Fatalf("RedactionReview = %+v, want stderr and error review findings", batch.RedactionReview)
	}
}

func TestCollectCommandEvidenceBatchMapsUnsupportedDockerAPI(t *testing.T) {
	t.Parallel()

	batch, err := CollectCommandEvidenceBatch(context.Background(), batchEvidenceRunner{
		"stat -fc %T /sys/fs/cgroup":                     {Stdout: []byte("cgroup2fs\n")},
		"docker version --format {{.Server.APIVersion}}": {Stdout: []byte("1.42\n")},
	})
	if err != nil {
		t.Fatalf("CollectCommandEvidenceBatch() error = %v", err)
	}
	if batch.Report.Status != StatusFail {
		t.Fatalf("Report.Status = %q, want fail", batch.Report.Status)
	}
	if got := batch.Report.Checks[1].Reason; got != "Docker API 1.42 is below required 1.43" {
		t.Fatalf("Docker check reason = %q, want unsupported version reason", got)
	}
}

func TestMarshalCommandEvidenceBatchJSONIsDeterministic(t *testing.T) {
	t.Parallel()

	batch, err := CollectCommandEvidenceBatch(context.Background(), batchEvidenceRunner{
		"stat -fc %T /sys/fs/cgroup":                     {Stdout: []byte("cgroup2fs\n")},
		"docker version --format {{.Server.APIVersion}}": {Stdout: []byte("1.44\n")},
	})
	if err != nil {
		t.Fatalf("CollectCommandEvidenceBatch() error = %v", err)
	}
	data, err := MarshalCommandEvidenceBatchJSON(batch)
	if err != nil {
		t.Fatalf("MarshalCommandEvidenceBatchJSON() error = %v", err)
	}
	const want = "{\n  \"schema\": \"midnight.preflight.command_evidence_batch.v1\",\n  \"report\": {\n    \"status\": \"pass\",\n    \"severity\": \"healthy\",\n    \"exit_code\": 0,\n    \"checks\": [\n      {\n        \"id\": \"PRE-CGROUP-001\",\n        \"status\": \"pass\",\n        \"reason\": \"cgroup v2 filesystem type found\"\n      },\n      {\n        \"id\": \"PRE-DOCKER-API-001\",\n        \"status\": \"pass\",\n        \"reason\": \"Docker API 1.44 satisfies required 1.43\"\n      }\n    ]\n  },\n  \"evidence\": [\n    {\n      \"schema\": \"midnight.preflight.command_evidence.v1\",\n      \"spec_id\": \"preflight.cgroup_fs_type\",\n      \"check_id\": \"PRE-CGROUP-001\",\n      \"executable\": \"stat\",\n      \"args\": [\n        \"-fc\",\n        \"%T\",\n        \"/sys/fs/cgroup\"\n      ],\n      \"timeout\": \"3s\",\n      \"requires_root\": false,\n      \"exit_code\": 0,\n      \"stdout\": \"cgroup2fs\\n\"\n    },\n    {\n      \"schema\": \"midnight.preflight.command_evidence.v1\",\n      \"spec_id\": \"preflight.docker_api_version\",\n      \"check_id\": \"PRE-DOCKER-API-001\",\n      \"executable\": \"docker\",\n      \"args\": [\n        \"version\",\n        \"--format\",\n        \"{{.Server.APIVersion}}\"\n      ],\n      \"timeout\": \"3s\",\n      \"requires_root\": false,\n      \"exit_code\": 0,\n      \"stdout\": \"1.44\\n\"\n    }\n  ],\n  \"redaction_review\": {\n    \"status\": \"clean\"\n  }\n}"
	if string(data) != want {
		t.Fatalf("MarshalCommandEvidenceBatchJSON() = %s, want %s", data, want)
	}
	var decoded CommandEvidenceBatch
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(batch) error = %v", err)
	}
}

type batchCommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	TimedOut bool
	Err      error
}

type batchEvidenceRunner map[string]batchCommandResult

func (r batchEvidenceRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	result, err := r.RunCommandResult(ctx, name, args...)
	return result.Stdout, err
}

func (r batchEvidenceRunner) RunCommandResult(_ context.Context, name string, args ...string) (CommandResult, error) {
	key := name
	for _, arg := range args {
		key += " " + arg
	}
	result, ok := r[key]
	if !ok {
		return CommandResult{ExitCode: -1}, os.ErrNotExist
	}
	exitCode := result.ExitCode
	if exitCode == 0 && result.Err != nil {
		exitCode = -1
	}
	return CommandResult{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: exitCode, TimedOut: result.TimedOut}, result.Err
}
