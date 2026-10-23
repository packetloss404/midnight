package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultPlanIsDryRunAndNonDestructive(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := run(nil, stdout, stderr); err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Mode: dry-run",
		"go run ./cmd/midnight-doctor --json --live",
		"sudo systemctl status --no-pager",
		"curl -fsS \"$MIDNIGHT_API_BASE/api/v1alpha/_demo/api-data-readiness\"",
		"set --public-url or MIDNIGHT_PUBLIC_URL",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("default plan missing %q\n%s", want, out)
		}
	}
	for _, forbidden := range []string{
		"systemctl start ",
		"systemctl stop ",
		"systemctl restart ",
		"systemctl enable --now",
		"docker run ",
		"docker stop ",
	} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("default plan unexpectedly contains %q\n%s", forbidden, out)
		}
	}
}

func TestJSONPlanIncludesVariablesAndSteps(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{
		"--format", "json",
		"--evidence-dir", "artifacts/custom-live-proof",
		"--public-url", "https://app.example.test/",
		"--public-host", "app.example.test",
		"--vm-ip", "203.0.113.10",
	}, stdout, stderr)
	if err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	var plan proofPlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, stdout.String())
	}
	if !plan.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if len(plan.Steps) < 10 {
		t.Fatalf("Steps len = %d, want at least 10", len(plan.Steps))
	}
	if got := varValue(plan, "LIVE_DEPLOY_PROOF_DIR"); got != "artifacts/custom-live-proof" {
		t.Fatalf("LIVE_DEPLOY_PROOF_DIR = %q", got)
	}
	if got := varValue(plan, "MIDNIGHT_PUBLIC_HOST"); got != "app.example.test" {
		t.Fatalf("MIDNIGHT_PUBLIC_HOST = %q", got)
	}
	if !hasStep(plan, "LDP-010", "read-only-network") {
		t.Fatalf("expected public route probe step in plan: %+v", plan.Steps)
	}
}

func TestServiceControlRequiresExplicitOptIn(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := run([]string{"--include-service-control"}, stdout, stderr); err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"# [LDP-900] Opt-in service startup",
		"sudo systemctl enable --now midnight-server.service midnight-worker.service midnight-agent.service",
		"# [LDP-901] Opt-in worker restart",
		"sudo systemctl restart midnight-worker.service",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("opt-in plan missing %q\n%s", want, out)
		}
	}
}

func TestRejectsUnsupportedFormat(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{"--format", "yaml"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected unsupported format to fail")
	}
	if !strings.Contains(err.Error(), "unsupported --format") {
		t.Fatalf("error = %v", err)
	}
}

func varValue(plan proofPlan, name string) string {
	for _, v := range plan.Variables {
		if v.Name == name {
			return v.Value
		}
	}
	return ""
}

func hasStep(plan proofPlan, id, policy string) bool {
	for _, s := range plan.Steps {
		if s.ID == id && s.Policy == policy {
			return true
		}
	}
	return false
}
