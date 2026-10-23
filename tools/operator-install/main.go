// Command operator-install prints the dry-run operator checklist for the
// single-server MVP install path. It emits exact commands for host preflight,
// package build/install preparation, post-install doctor review, and optional
// service enablement without executing them.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEvidenceDir = "artifacts/operator-install"
	defaultDebGlob     = "build/deb/midnight_*.deb"
	defaultDoctorJSON  = "/var/lib/midnight/operator-install-doctor.json"
	defaultDBDSNFile   = "/etc/midnight/db.dsn"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

type inputs struct {
	Format                string
	EvidenceDir           string
	DebGlob               string
	DoctorJSON            string
	DBDSNFile             string
	Distro                string
	IncludeServiceControl bool
}

type installPlan struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   string    `json:"generated_at"`
	DryRun        bool      `json:"dry_run"`
	Target        string    `json:"target"`
	Safety        []string  `json:"safety"`
	Variables     []varDecl `json:"variables"`
	Steps         []step    `json:"steps"`
}

type varDecl struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type step struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Purpose  string   `json:"purpose"`
	Command  string   `json:"command"`
	Evidence string   `json:"evidence"`
	Policy   string   `json:"policy"`
	Requires []string `json:"requires,omitempty"`
}

func run(args []string, stdout, stderr io.Writer) error {
	in, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	plan := buildPlan(in, time.Now().UTC())
	switch in.Format {
	case "shell":
		return writeShellPlan(stdout, plan)
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	default:
		return fmt.Errorf("unsupported --format %q (supported: shell, json)", in.Format)
	}
}

func parseFlags(args []string, stderr io.Writer) (inputs, error) {
	fs := flag.NewFlagSet("operator-install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := inputs{}
	fs.StringVar(&in.Format, "format", "shell", "output format: shell or json")
	fs.StringVar(&in.EvidenceDir, "evidence-dir", defaultEvidenceDir, "local directory where printed commands write install-path evidence")
	fs.StringVar(&in.DebGlob, "deb-glob", defaultDebGlob, "shell glob for the Midnight .deb to inspect/install")
	fs.StringVar(&in.DoctorJSON, "doctor-json", defaultDoctorJSON, "path where the printed live doctor command writes JSON")
	fs.StringVar(&in.DBDSNFile, "db-dsn-file", defaultDBDSNFile, "Postgres DSN file path to check without printing its value")
	fs.StringVar(&in.Distro, "distro", "ubuntu-24.04-or-debian-12", "operator label for the target host profile")
	fs.BoolVar(&in.IncludeServiceControl, "include-service-control", false, "also print explicit opt-in systemctl enable/start commands")
	if err := fs.Parse(args); err != nil {
		return inputs{}, err
	}
	in.Format = strings.ToLower(strings.TrimSpace(in.Format))
	in.EvidenceDir = strings.TrimSpace(in.EvidenceDir)
	in.DebGlob = strings.TrimSpace(in.DebGlob)
	in.DoctorJSON = strings.TrimSpace(in.DoctorJSON)
	in.DBDSNFile = strings.TrimSpace(in.DBDSNFile)
	in.Distro = strings.TrimSpace(in.Distro)
	if in.EvidenceDir == "" {
		return inputs{}, errors.New("--evidence-dir is required")
	}
	if in.DebGlob == "" {
		return inputs{}, errors.New("--deb-glob is required")
	}
	if in.DoctorJSON == "" {
		return inputs{}, errors.New("--doctor-json is required")
	}
	if in.DBDSNFile == "" {
		return inputs{}, errors.New("--db-dsn-file is required")
	}
	if in.Distro == "" {
		return inputs{}, errors.New("--distro is required")
	}
	return in, nil
}

func buildPlan(in inputs, now time.Time) installPlan {
	vars := []varDecl{
		{Name: "MIDNIGHT_INSTALL_EVIDENCE_DIR", Value: in.EvidenceDir},
		{Name: "MIDNIGHT_DEB_GLOB", Value: in.DebGlob},
		{Name: "MIDNIGHT_DOCTOR_JSON", Value: in.DoctorJSON},
		{Name: "MIDNIGHT_DB_DSN_FILE", Value: in.DBDSNFile},
		{Name: "MIDNIGHT_SERVICES", Value: "midnight-server midnight-worker midnight-agent midnight-buildkit midnight-registry"},
	}
	steps := []step{
		{
			ID:       "OIP-001",
			Title:    "Prepare local evidence directory",
			Purpose:  "Create a local transcript directory for the install-path review.",
			Command:  `mkdir -p "$MIDNIGHT_INSTALL_EVIDENCE_DIR"`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/",
			Policy:   "local-write",
		},
		{
			ID:       "OIP-002",
			Title:    "Record host identity",
			Purpose:  "Capture host metadata without reading application secrets.",
			Command:  `date -u +"%Y-%m-%dT%H:%M:%SZ" | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/started-at.txt"; uname -a | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/uname.txt"; cat /etc/os-release | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/os-release.txt"; hostnamectl | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/hostnamectl.txt"`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/started-at.txt, uname.txt, os-release.txt, hostnamectl.txt",
			Policy:   "read-only",
		},
		{
			ID:       "OIP-003",
			Title:    "Verify single-server host baseline",
			Purpose:  "Check the alpha host floor before package installation.",
			Command:  `. /etc/os-release; printf 'ID=%s VERSION_ID=%s\n' "$ID" "$VERSION_ID" | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/distro.txt"; systemctl --version | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/systemd-version.txt"; findmnt -no FSTYPE /sys/fs/cgroup | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/cgroup-fstype.txt"; test "$(findmnt -no FSTYPE /sys/fs/cgroup)" = cgroup2fs`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/distro.txt, systemd-version.txt, cgroup-fstype.txt",
			Policy:   "read-only",
			Requires: []string{"systemd"},
		},
		{
			ID:       "OIP-004",
			Title:    "Verify required operator tools",
			Purpose:  "Ensure package, doctor, and runtime checks can run on the target host.",
			Command:  `for bin in sudo systemctl docker go dpkg dpkg-buildpackage jq curl ss; do command -v "$bin"; done | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/operator-tools.txt"; docker version --format '{{.Server.APIVersion}}' | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/docker-api-version.txt"`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/operator-tools.txt, docker-api-version.txt",
			Policy:   "read-only",
			Requires: []string{"docker", "go"},
		},
		{
			ID:       "OIP-005",
			Title:    "Build unsigned local package",
			Purpose:  "Produce the local .deb using the repository packaging path.",
			Command:  `make package-deb 2>&1 | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/package-deb.log"`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/package-deb.log and build/deb/*.deb",
			Policy:   "local-write",
			Requires: []string{"dpkg-buildpackage", "go", "make"},
		},
		{
			ID:       "OIP-006",
			Title:    "Inspect package contents",
			Purpose:  "Confirm the package carries the expected systemd, config, and doctor assets before install.",
			Command:  `set -- $MIDNIGHT_DEB_GLOB; test "$#" -eq 1; test -f "$1"; dpkg-deb --info "$1" | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/deb-info.txt"; dpkg-deb --contents "$1" | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/deb-contents.txt"; grep -E 'midnight-(server|worker|agent|buildkit|registry)\.service|midnight-doctor|midnight\.env|registry\.yml' "$MIDNIGHT_INSTALL_EVIDENCE_DIR/deb-contents.txt"`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/deb-info.txt, deb-contents.txt",
			Policy:   "read-only",
			Requires: []string{"dpkg-deb"},
		},
		{
			ID:       "OIP-007",
			Title:    "Simulate package install",
			Purpose:  "Ask apt to resolve the install without modifying the host.",
			Command:  `set -- $MIDNIGHT_DEB_GLOB; test "$#" -eq 1; test -f "$1"; sudo apt-get install --dry-run "$1" | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/apt-install-dry-run.txt"`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/apt-install-dry-run.txt",
			Policy:   "read-only-package-simulation",
			Requires: []string{"apt-get", "sudo"},
		},
		{
			ID:       "OIP-008",
			Title:    "Install package when ready",
			Purpose:  "Install the package after reviewing the dry-run; the package postinst does not enable services.",
			Command:  `set -- $MIDNIGHT_DEB_GLOB; test "$#" -eq 1; test -f "$1"; sudo apt-get install -y "$1" 2>&1 | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/apt-install.txt"`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/apt-install.txt",
			Policy:   "operator-package-install",
			Requires: []string{"apt-get", "sudo"},
		},
		{
			ID:       "OIP-009",
			Title:    "Review post-install doctor",
			Purpose:  "Inspect the package postinst doctor result without printing secrets.",
			Command:  `sudo test -s /var/lib/midnight/doctor-postinst.json; sudo jq -r '.results[] | "\(.status)\t\(.id)\t\(.message)"' /var/lib/midnight/doctor-postinst.json | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/doctor-postinst-summary.txt"`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/doctor-postinst-summary.txt",
			Policy:   "read-only",
			Requires: []string{"jq", "sudo"},
		},
		{
			ID:       "OIP-010",
			Title:    "Check service-enable inputs",
			Purpose:  "Verify required config files and external runtime binaries without revealing DSN or key material.",
			Command:  `sudo test -s "$MIDNIGHT_DB_DSN_FILE" && echo "db dsn file present (value not printed)" | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/db-dsn.status"; sudo test -s /etc/midnight/cursor.key && echo "cursor key present (value not printed)" | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/cursor-key.status"; sudo test -s /etc/midnight/registry.yml && echo "registry config present" | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/registry-config.status"; for bin in buildkitd buildctl registry; do command -v "$bin"; done | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/runtime-binaries.txt"`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/db-dsn.status, cursor-key.status, registry-config.status, runtime-binaries.txt",
			Policy:   "read-only",
			Requires: []string{"sudo"},
		},
		{
			ID:       "OIP-011",
			Title:    "Run live doctor before services",
			Purpose:  "Rerun live doctor with captured host facts and secret-safe hints before service enablement.",
			Command:  `docker version --format '{{.Server.APIVersion}}' | sudo tee /tmp/midnight-docker-api.txt >/dev/null; ss -ltn | sudo tee /tmp/midnight-ss-ltn.txt >/dev/null; db_flag=""; if sudo test -s "$MIDNIGHT_DB_DSN_FILE"; then db_flag="--db-dsn-configured"; fi; sudo midnight-doctor --json --live --mountinfo-fixture=/proc/self/mountinfo --daemon-json-fixture=/etc/docker/daemon.json --docker-version-fixture=/tmp/midnight-docker-api.txt --ss-ltn-fixture=/tmp/midnight-ss-ltn.txt $db_flag --firewall-mode=none --mac-mode=apparmor-enforce --time-sync=synchronized --hostname-fqdn="$(hostname -f)" --docker-packages=docker-ce --runtime-binaries=buildkitd,buildctl,registry | sudo tee "$MIDNIGHT_DOCTOR_JSON" >/dev/null; sudo jq -r '.results[] | "\(.status)\t\(.id)\t\(.message)"' "$MIDNIGHT_DOCTOR_JSON" | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/midnight-doctor-live-summary.txt"`,
			Evidence: "$MIDNIGHT_DOCTOR_JSON, $MIDNIGHT_INSTALL_EVIDENCE_DIR/midnight-doctor-live-summary.txt",
			Policy:   "read-only",
			Requires: []string{"docker", "jq", "midnight-doctor", "ss", "sudo"},
		},
		{
			ID:       "OIP-012",
			Title:    "Review installed service state",
			Purpose:  "Confirm units are installed and observe current state without starting or stopping them.",
			Command:  `sudo systemctl status --no-pager $MIDNIGHT_SERVICES | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/systemctl-status.txt" || true; sudo systemctl is-enabled $MIDNIGHT_SERVICES | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/systemctl-is-enabled.txt" || true`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/systemctl-status.txt, systemctl-is-enabled.txt",
			Policy:   "read-only",
			Requires: []string{"systemd", "sudo"},
		},
	}
	if in.IncludeServiceControl {
		steps = append(steps, serviceControlSteps()...)
	}
	return installPlan{
		SchemaVersion: 1,
		GeneratedAt:   now.Format(time.RFC3339),
		DryRun:        true,
		Target:        in.Distro,
		Safety: []string{
			"default output is dry-run: commands are printed, not executed",
			"no secret values are required or printed; DSN and key checks report presence only",
			"default steps do not start, stop, restart, or enable services",
			"service lifecycle commands are printed only when --include-service-control is set",
		},
		Variables: vars,
		Steps:     steps,
	}
}

func serviceControlSteps() []step {
	return []step{
		{
			ID:       "OIP-900",
			Title:    "Opt-in service enablement",
			Purpose:  "Start the single-server MVP services only after the live doctor result has been reviewed.",
			Command:  `sudo systemctl enable --now $MIDNIGHT_SERVICES`,
			Evidence: "operator terminal transcript and systemctl status output",
			Policy:   "explicit-opt-in-service-control",
			Requires: []string{"--include-service-control", "systemd", "sudo"},
		},
		{
			ID:       "OIP-901",
			Title:    "Opt-in post-start status",
			Purpose:  "Capture service state after explicit enablement.",
			Command:  `sudo systemctl status --no-pager $MIDNIGHT_SERVICES | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/systemctl-status-after-enable.txt"; sudo journalctl -u midnight-server -u midnight-worker -u midnight-agent -u midnight-buildkit -u midnight-registry --since "-10 minutes" --no-pager | tee "$MIDNIGHT_INSTALL_EVIDENCE_DIR/journalctl-after-enable.txt"`,
			Evidence: "$MIDNIGHT_INSTALL_EVIDENCE_DIR/systemctl-status-after-enable.txt, journalctl-after-enable.txt",
			Policy:   "explicit-opt-in-service-control",
			Requires: []string{"--include-service-control", "systemd", "sudo"},
		},
	}
}

func writeShellPlan(w io.Writer, plan installPlan) error {
	fmt.Fprintln(w, "# Midnight operator install-path command plan")
	fmt.Fprintf(w, "# Target: %s\n", plan.Target)
	fmt.Fprintln(w, "# Mode: dry-run; review before executing on the single-server host.")
	for _, line := range plan.Safety {
		fmt.Fprintf(w, "# Safety: %s\n", line)
	}
	fmt.Fprintln(w, "set -euo pipefail")
	for _, v := range plan.Variables {
		fmt.Fprintf(w, "export %s=%s\n", v.Name, shellQuote(v.Value))
	}
	fmt.Fprintln(w)
	for _, s := range plan.Steps {
		fmt.Fprintf(w, "# [%s] %s\n", s.ID, s.Title)
		fmt.Fprintf(w, "# Purpose: %s\n", s.Purpose)
		fmt.Fprintf(w, "# Policy: %s\n", s.Policy)
		if len(s.Requires) > 0 {
			requires := append([]string(nil), s.Requires...)
			sort.Strings(requires)
			fmt.Fprintf(w, "# Requires: %s\n", strings.Join(requires, ", "))
		}
		fmt.Fprintf(w, "# Evidence: %s\n", s.Evidence)
		fmt.Fprintln(w, s.Command)
		fmt.Fprintln(w)
	}
	return nil
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return strconv.Quote(value)
}
