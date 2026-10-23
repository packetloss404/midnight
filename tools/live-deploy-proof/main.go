// Command live-deploy-proof prints the operator command plan for collecting
// evidence from a live single-VM deploy proof. The default mode is dry-run:
// it prints exact commands and does not execute them.
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

const defaultEvidenceDir = "artifacts/live-deploy-proof"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

type inputs struct {
	Format                string
	EvidenceDir           string
	Since                 string
	APIBase               string
	PublicURL             string
	PublicHost            string
	VMIP                  string
	TraefikDynamicDir     string
	IncludeServiceControl bool
}

type proofPlan struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   string    `json:"generated_at"`
	DryRun        bool      `json:"dry_run"`
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
	fs := flag.NewFlagSet("live-deploy-proof", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := inputs{}
	fs.StringVar(&in.Format, "format", "shell", "output format: shell or json")
	fs.StringVar(&in.EvidenceDir, "evidence-dir", defaultEvidenceDir, "local directory where printed commands write evidence")
	fs.StringVar(&in.Since, "since", "-30 minutes", "journalctl lookback window")
	fs.StringVar(&in.APIBase, "api-base", "http://127.0.0.1:8080", "Midnight API base URL for local read-only probes")
	fs.StringVar(&in.PublicURL, "public-url", "", "optional public HTTPS URL to probe")
	fs.StringVar(&in.PublicHost, "public-host", "", "optional public host for TLS/SNI checks")
	fs.StringVar(&in.VMIP, "vm-ip", "", "optional VM IP used with curl --resolve for HTTPS proof")
	fs.StringVar(&in.TraefikDynamicDir, "traefik-dynamic-dir", "/etc/midnight/traefik/dynamic", "Traefik dynamic file-provider directory")
	fs.BoolVar(&in.IncludeServiceControl, "include-service-control", false, "also print explicit opt-in systemctl enable/restart commands")
	if err := fs.Parse(args); err != nil {
		return inputs{}, err
	}
	in.Format = strings.ToLower(strings.TrimSpace(in.Format))
	in.EvidenceDir = strings.TrimSpace(in.EvidenceDir)
	in.Since = strings.TrimSpace(in.Since)
	in.APIBase = strings.TrimRight(strings.TrimSpace(in.APIBase), "/")
	in.PublicURL = strings.TrimSpace(in.PublicURL)
	in.PublicHost = strings.TrimSpace(in.PublicHost)
	in.VMIP = strings.TrimSpace(in.VMIP)
	in.TraefikDynamicDir = strings.TrimSpace(in.TraefikDynamicDir)
	if in.EvidenceDir == "" {
		return inputs{}, errors.New("--evidence-dir is required")
	}
	if in.Since == "" {
		return inputs{}, errors.New("--since is required")
	}
	if in.APIBase == "" {
		return inputs{}, errors.New("--api-base is required")
	}
	if in.TraefikDynamicDir == "" {
		return inputs{}, errors.New("--traefik-dynamic-dir is required")
	}
	return in, nil
}

func buildPlan(in inputs, now time.Time) proofPlan {
	vars := []varDecl{
		{Name: "LIVE_DEPLOY_PROOF_DIR", Value: in.EvidenceDir},
		{Name: "MIDNIGHT_API_BASE", Value: in.APIBase},
		{Name: "MIDNIGHT_PUBLIC_URL", Value: in.PublicURL},
		{Name: "MIDNIGHT_PUBLIC_HOST", Value: in.PublicHost},
		{Name: "MIDNIGHT_VM_IP", Value: in.VMIP},
		{Name: "MIDNIGHT_TRAEFIK_DYNAMIC_DIR", Value: in.TraefikDynamicDir},
		{Name: "MIDNIGHT_JOURNAL_SINCE", Value: in.Since},
	}
	steps := []step{
		{
			ID:       "LDP-001",
			Title:    "Prepare evidence directory",
			Purpose:  "Create the local collection directory without touching production services.",
			Command:  `mkdir -p "$LIVE_DEPLOY_PROOF_DIR"`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/",
			Policy:   "local-write",
		},
		{
			ID:       "LDP-002",
			Title:    "Record collection context",
			Purpose:  "Capture timestamp and host identity for the evidence packet.",
			Command:  `date -u +"%Y-%m-%dT%H:%M:%SZ" | tee "$LIVE_DEPLOY_PROOF_DIR/collected-at.txt" && uname -a | tee "$LIVE_DEPLOY_PROOF_DIR/uname.txt" && hostnamectl | tee "$LIVE_DEPLOY_PROOF_DIR/hostnamectl.txt"`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/collected-at.txt, uname.txt, hostnamectl.txt",
			Policy:   "read-only",
		},
		{
			ID:       "LDP-003",
			Title:    "Run local release gate drill evidence",
			Purpose:  "Refresh the deterministic Phase 0 drill evidence before comparing it to live proof.",
			Command:  `go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence artifacts/phase0/release-evidence.json --release-evidence-index artifacts/phase0/release-evidence-index.json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files --release-summary artifacts/phase0/release-summary.json | tee "$LIVE_DEPLOY_PROOF_DIR/phase0-gate.json"`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/phase0-gate.json and artifacts/phase0/*.json",
			Policy:   "local-write",
		},
		{
			ID:       "LDP-004",
			Title:    "Run live doctor checks",
			Purpose:  "Collect live host checks without exposing DSN or secret values.",
			Command:  `MIDNIGHT_DOCTOR_LIVE=1 go run ./cmd/midnight-doctor --json --live | tee "$LIVE_DEPLOY_PROOF_DIR/midnight-doctor-live.json"`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/midnight-doctor-live.json",
			Policy:   "read-only",
		},
		{
			ID:       "LDP-005",
			Title:    "Capture service status",
			Purpose:  "Prove package-managed services are present and report their current state without starting or stopping them.",
			Command:  `sudo systemctl status --no-pager docker traefik midnight-server midnight-worker midnight-agent midnight-buildkit midnight-registry | tee "$LIVE_DEPLOY_PROOF_DIR/systemctl-status.txt"; sudo systemctl is-active docker traefik midnight-server midnight-worker midnight-agent midnight-buildkit midnight-registry | tee "$LIVE_DEPLOY_PROOF_DIR/systemctl-is-active.txt"`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/systemctl-status.txt, systemctl-is-active.txt",
			Policy:   "read-only",
			Requires: []string{"systemd", "sudo"},
		},
		{
			ID:       "LDP-006",
			Title:    "Capture recent service logs",
			Purpose:  "Collect support-safe recent logs around the deploy proof window.",
			Command:  `sudo journalctl -u midnight-server -u midnight-worker -u midnight-agent -u traefik -u docker --since "$MIDNIGHT_JOURNAL_SINCE" --no-pager | tee "$LIVE_DEPLOY_PROOF_DIR/journalctl-recent.txt"`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/journalctl-recent.txt",
			Policy:   "read-only",
			Requires: []string{"systemd", "sudo"},
		},
		{
			ID:       "LDP-007",
			Title:    "Capture Docker runtime evidence",
			Purpose:  "Record Docker version, daemon config summary, and Midnight-managed container state.",
			Command:  `docker version | tee "$LIVE_DEPLOY_PROOF_DIR/docker-version.txt"; docker info --format '{{json .}}' | tee "$LIVE_DEPLOY_PROOF_DIR/docker-info.json"; docker ps --filter label=midnight.managed_by=midnight-agent --format 'table {{.ID}}\t{{.Image}}\t{{.Status}}\t{{.Names}}' | tee "$LIVE_DEPLOY_PROOF_DIR/docker-ps-midnight.txt"; containers="$(docker ps -q --filter label=midnight.managed_by=midnight-agent)"; if [ -n "$containers" ]; then docker inspect $containers | jq '[.[] | {id: .Id, name: .Name, image: .Image, state: .State.Status, health: .State.Health.Status, labels: .Config.Labels, networks: .NetworkSettings.Networks}]'; else echo '[]'; fi | tee "$LIVE_DEPLOY_PROOF_DIR/docker-inspect-midnight.json"`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/docker-version.txt, docker-info.json, docker-ps-midnight.txt, docker-inspect-midnight.json",
			Policy:   "read-only",
			Requires: []string{"docker", "jq"},
		},
		{
			ID:       "LDP-008",
			Title:    "Capture Traefik publication evidence",
			Purpose:  "Show whether the worker published file-provider artifacts to the configured dynamic directory.",
			Command:  `sudo find "$MIDNIGHT_TRAEFIK_DYNAMIC_DIR" -maxdepth 3 -type f -name 'traefik.dynamic.yaml' -printf '%p %s bytes\n' | tee "$LIVE_DEPLOY_PROOF_DIR/traefik-dynamic-files.txt"; if command -v traefik >/dev/null 2>&1; then sudo traefik validate --configFile=/etc/traefik/traefik.yaml | tee "$LIVE_DEPLOY_PROOF_DIR/traefik-validate.txt"; else echo "traefik binary not found in PATH" | tee "$LIVE_DEPLOY_PROOF_DIR/traefik-validate.txt"; fi`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/traefik-dynamic-files.txt, traefik-validate.txt",
			Policy:   "read-only",
			Requires: []string{"sudo"},
		},
		{
			ID:       "LDP-009",
			Title:    "Probe local control-plane readiness",
			Purpose:  "Capture local API readiness evidence without requiring external credentials.",
			Command:  `curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/api-data-readiness" | tee "$LIVE_DEPLOY_PROOF_DIR/api-data-readiness.json"; curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/health-target" | tee "$LIVE_DEPLOY_PROOF_DIR/health-target.json"`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/api-data-readiness.json, health-target.json",
			Policy:   "read-only",
			Requires: []string{"curl"},
		},
		{
			ID:       "LDP-010",
			Title:    "Probe public HTTPS route",
			Purpose:  "Verify public route, TLS presentation, and optional VM-IP resolve path when a host is supplied.",
			Command:  `if [ -n "$MIDNIGHT_PUBLIC_URL" ]; then curl -fsS -D "$LIVE_DEPLOY_PROOF_DIR/public-response.headers" "$MIDNIGHT_PUBLIC_URL" -o "$LIVE_DEPLOY_PROOF_DIR/public-response.body"; else echo "set --public-url or MIDNIGHT_PUBLIC_URL to collect public HTTP evidence" | tee "$LIVE_DEPLOY_PROOF_DIR/public-response.skipped"; fi; if [ -n "$MIDNIGHT_PUBLIC_HOST" ]; then openssl s_client -servername "$MIDNIGHT_PUBLIC_HOST" -connect "${MIDNIGHT_VM_IP:-$MIDNIGHT_PUBLIC_HOST}:443" </dev/null 2>/dev/null | openssl x509 -noout -issuer -subject -dates | tee "$LIVE_DEPLOY_PROOF_DIR/public-tls.txt"; fi; if [ -n "$MIDNIGHT_PUBLIC_HOST" ] && [ -n "$MIDNIGHT_VM_IP" ]; then curl -fsS --resolve "$MIDNIGHT_PUBLIC_HOST:443:$MIDNIGHT_VM_IP" "https://$MIDNIGHT_PUBLIC_HOST/" -o "$LIVE_DEPLOY_PROOF_DIR/public-resolve.body"; fi`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/public-response.*, public-tls.txt, public-resolve.body",
			Policy:   "read-only-network",
			Requires: []string{"curl", "openssl"},
		},
		{
			ID:       "LDP-011",
			Title:    "Record operator signoff notes",
			Purpose:  "Create a local note file for exceptions, skipped live checks, and links to attached screenshots or dashboards.",
			Command:  `printf '%s\n' '# Live deploy proof notes' '- operator:' '- release or PR:' '- public URL:' '- skipped checks and why:' '- exceptions:' > "$LIVE_DEPLOY_PROOF_DIR/operator-notes.md"`,
			Evidence: "$LIVE_DEPLOY_PROOF_DIR/operator-notes.md",
			Policy:   "local-write",
		},
	}
	if in.IncludeServiceControl {
		steps = append(steps, serviceControlSteps()...)
	}
	return proofPlan{
		SchemaVersion: 1,
		GeneratedAt:   now.Format(time.RFC3339),
		DryRun:        true,
		Safety: []string{
			"default output is dry-run: commands are printed, not executed",
			"no external secret values are required or printed by this wrapper",
			"default steps do not start, stop, restart, or enable production services",
			"service lifecycle commands are printed only when --include-service-control is set",
		},
		Variables: vars,
		Steps:     steps,
	}
}

func serviceControlSteps() []step {
	return []step{
		{
			ID:       "LDP-900",
			Title:    "Opt-in service startup",
			Purpose:  "Start package-managed services only after the operator explicitly decides this VM is the proof target.",
			Command:  `sudo systemctl enable --now midnight-server.service midnight-worker.service midnight-agent.service`,
			Evidence: "operator terminal transcript",
			Policy:   "explicit-opt-in-service-control",
			Requires: []string{"systemd", "sudo", "--include-service-control"},
		},
		{
			ID:       "LDP-901",
			Title:    "Opt-in worker restart",
			Purpose:  "Restart the worker only when intentionally proving service lifecycle behavior.",
			Command:  `sudo systemctl restart midnight-worker.service && sudo systemctl status --no-pager midnight-worker.service`,
			Evidence: "operator terminal transcript",
			Policy:   "explicit-opt-in-service-control",
			Requires: []string{"systemd", "sudo", "--include-service-control"},
		},
	}
}

func writeShellPlan(w io.Writer, plan proofPlan) error {
	fmt.Fprintln(w, "# Midnight live deploy proof command plan")
	fmt.Fprintln(w, "# Mode: dry-run; review before executing on the proof VM.")
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
