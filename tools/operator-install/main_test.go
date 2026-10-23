package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultPlanIsDryRunAndServiceSafe(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := run(nil, stdout, stderr); err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Mode: dry-run",
		"make package-deb",
		"sudo apt-get install --dry-run",
		"sudo apt-get install -y \"$1\"",
		"sudo midnight-doctor --json --live",
		"db dsn file present (value not printed)",
		"sudo systemctl status --no-pager $MIDNIGHT_SERVICES",
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
		"MIDNIGHT_DB_DSN=",
		"bootstrap-token",
	} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("default plan unexpectedly contains %q\n%s", forbidden, out)
		}
	}
}

func TestJSONPlanIncludesOperatorInputs(t *testing.T) {
	t.Parallel()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{
		"--format", "json",
		"--evidence-dir", "artifacts/custom-install",
		"--deb-glob", "build/deb/midnight_0.1.0_amd64.deb",
		"--doctor-json", "/tmp/midnight-doctor.json",
		"--db-dsn-file", "/secure/db.dsn",
		"--distro", "ubuntu-24.04",
	}, stdout, stderr)
	if err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	var plan installPlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, stdout.String())
	}
	if !plan.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if plan.Target != "ubuntu-24.04" {
		t.Fatalf("Target = %q, want ubuntu-24.04", plan.Target)
	}
	if len(plan.Steps) < 12 {
		t.Fatalf("Steps len = %d, want at least 12", len(plan.Steps))
	}
	if got := varValue(plan, "MIDNIGHT_INSTALL_EVIDENCE_DIR"); got != "artifacts/custom-install" {
		t.Fatalf("MIDNIGHT_INSTALL_EVIDENCE_DIR = %q", got)
	}
	if got := varValue(plan, "MIDNIGHT_DEB_GLOB"); got != "build/deb/midnight_0.1.0_amd64.deb" {
		t.Fatalf("MIDNIGHT_DEB_GLOB = %q", got)
	}
	if got := varValue(plan, "MIDNIGHT_DB_DSN_FILE"); got != "/secure/db.dsn" {
		t.Fatalf("MIDNIGHT_DB_DSN_FILE = %q", got)
	}
	if !hasStep(plan, "OIP-011", "read-only") {
		t.Fatalf("expected live doctor step in plan: %+v", plan.Steps)
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
		"# [OIP-900] Opt-in service enablement",
		"sudo systemctl enable --now $MIDNIGHT_SERVICES",
		"# [OIP-901] Opt-in post-start status",
		"journalctl-after-enable.txt",
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

func TestRejectsMissingRequiredInputs(t *testing.T) {
	t.Parallel()

	for _, tc := range [][]string{
		{"--evidence-dir", ""},
		{"--deb-glob", ""},
		{"--doctor-json", ""},
		{"--db-dsn-file", ""},
		{"--distro", ""},
	} {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		if err := run(tc, stdout, stderr); err == nil {
			t.Fatalf("run(%v) succeeded, want error", tc)
		}
	}
}

func varValue(plan installPlan, name string) string {
	for _, v := range plan.Variables {
		if v.Name == name {
			return v.Value
		}
	}
	return ""
}

func hasStep(plan installPlan, id, policy string) bool {
	for _, s := range plan.Steps {
		if s.ID == id && s.Policy == policy {
			return true
		}
	}
	return false
}
