// Command release-verify is the operator-facing smoke tool that
// asserts a Midnight release artifact tree carries the panel-locked
// signed-release evidence:
//
//   - The .deb is GPG-signed (or explicitly waived by the
//     v0.1.0-alpha-1 unsigned-alpha policy).
//   - Every required OCI image is recorded in image-digests.txt and carries
//     cosign verification evidence under <artifacts>/cosign/.
//   - The SBOM + SLSA-style provenance attestations are present.
//
// The tool is the testable counterpart to the docs/release-verify.md
// runbook: docs ship the operator-facing instructions, this binary
// runs them through CI.
//
// Supply-chain pin: the tool loads the cosign + buildkit pins from
// versions.yaml (via internal/build.LoadVersions) instead of trusting
// process env. This enforces the pin at build time per the cosign SDK
// boundary doc (agile/handoff/governance/COSIGN_SDK_BOUNDARY.md). The
// path to versions.yaml defaults to "versions.yaml" relative to the
// working directory and can be overridden with --versions-file for
// reproducible-from-tarball verification runs.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/build"
)

var requiredOCIComponents = []string{"server", "worker", "agent", "buildkit", "registry"}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("release-verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("artifacts-dir", "artifacts/release", "directory containing the release artifacts")
	tag := fs.String("tag", "", "release tag (e.g., v0.1.0-alpha)")
	report := fs.String("report", "", "optional JSON report output path")
	allowUnsigned := fs.Bool("allow-unsigned-deb", false, "permit an unsigned .deb (matches the v0.1.0-alpha-1 alpha policy)")
	allowMissingSupplychain := fs.Bool("allow-missing-supplychain", false, "permit missing cosign/SBOM/provenance evidence for workflow_dispatch and fork smoke runs")
	versionsFile := fs.String("versions-file", "versions.yaml", "path to versions.yaml (cosign + buildkit pin source-of-truth)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*dir) == "" {
		return errors.New("--artifacts-dir is required")
	}
	if strings.TrimSpace(*tag) == "" {
		return errors.New("--tag is required")
	}

	rep := Report{
		SchemaVersion: 1,
		Tag:           *tag,
		ArtifactsDir:  *dir,
		EvaluatedAt:   time.Now().UTC().Format(time.RFC3339),
		Findings:      []Finding{},
	}

	rep.PinnedCosignVersion = build.RequiredCosignVersion
	rep.PinnedCosignLinuxAMD64Digest = build.RequiredCosignLinuxAMD64Digest
	rep.PinnedCosignLinuxARM64Digest = build.RequiredCosignLinuxARM64Digest
	resolvedVersionsFile := *versionsFile
	if found, ok := locateVersionsFile(*versionsFile); ok {
		resolvedVersionsFile = found
	}
	versions, versionsErr := build.LoadVersions(resolvedVersionsFile)
	*versionsFile = resolvedVersionsFile
	switch {
	case versionsErr != nil && os.IsNotExist(versionsErr):
		rep.Findings = append(rep.Findings, Finding{
			Category: "versions-pin",
			Path:     *versionsFile,
			Status:   "warn",
			Detail:   fmt.Sprintf("versions.yaml not found at %s; falling back to embedded pin cosign=%s (use --versions-file to point at the canonical file)", *versionsFile, build.RequiredCosignVersion),
		})
	case versionsErr != nil:
		rep.Findings = append(rep.Findings, Finding{
			Category: "versions-pin",
			Path:     *versionsFile,
			Status:   "fail",
			Detail:   fmt.Sprintf("failed to load versions.yaml: %v", versionsErr),
		})
	case versions.Cosign == "":
		rep.Findings = append(rep.Findings, Finding{
			Category: "versions-pin",
			Path:     *versionsFile,
			Status:   "fail",
			Detail:   "versions.yaml missing cosign: pin (see agile/handoff/governance/COSIGN_SDK_BOUNDARY.md)",
		})
	case versions.Cosign != build.RequiredCosignVersion:
		rep.Findings = append(rep.Findings, Finding{
			Category: "versions-pin",
			Path:     *versionsFile,
			Status:   "fail",
			Detail:   fmt.Sprintf("cosign pin drift: versions.yaml=%s vs internal/build.RequiredCosignVersion=%s", versions.Cosign, build.RequiredCosignVersion),
		})
	default:
		rep.PinnedCosignVersion = versions.Cosign
		rep.PinnedCosignLinuxAMD64Digest = versions.CosignLinuxAMD64Digest
		rep.PinnedCosignLinuxARM64Digest = versions.CosignLinuxARM64Digest
		rep.Findings = append(rep.Findings, Finding{
			Category: "versions-pin",
			Path:     *versionsFile,
			Status:   "pass",
			Detail:   fmt.Sprintf("cosign pinned to %s (linux-amd64 %s; linux-arm64 %s)", versions.Cosign, versions.CosignLinuxAMD64Digest, versions.CosignLinuxARM64Digest),
		})
	}

	debs := mustGlob(*dir, "*.deb")
	rep.DebFiles = debs
	for _, deb := range debs {
		hasSig := fileExists(deb+".asc") || fileExists(strings.TrimSuffix(deb, ".deb")+".changes")
		f := Finding{Category: "deb-signature", Path: deb}
		if hasSig {
			f.Status = "pass"
			f.Detail = "GPG signature present"
		} else if *allowUnsigned {
			f.Status = "warn"
			f.Detail = "unsigned .deb permitted under --allow-unsigned-deb policy"
		} else {
			f.Status = "fail"
			f.Detail = "no GPG signature for .deb (rerun release with MIDNIGHT_RELEASE_GPG_KEYID configured)"
		}
		rep.Findings = append(rep.Findings, f)
	}
	if len(debs) == 0 {
		status := "fail"
		detail := "no .deb files in the artifacts dir; release package is missing"
		if *allowUnsigned {
			status = "warn"
			detail = "no .deb files in the artifacts dir; permitted for unsigned dispatch smoke runs by --allow-unsigned-deb"
		}
		rep.Findings = append(rep.Findings, Finding{
			Category: "deb-signature",
			Status:   status,
			Detail:   detail,
		})
	}

	imageDigestsPath := filepath.Join(*dir, "image-digests.txt")
	imageDigests, digestFindings := readImageDigests(imageDigestsPath, *allowMissingSupplychain)
	rep.ImageDigests = imageDigests
	rep.Findings = append(rep.Findings, digestFindings...)

	cosignDir := filepath.Join(*dir, "cosign")
	cosignEvidence := append(mustGlob(cosignDir, "*.sig"), mustGlob(cosignDir, "*.verify.json")...)
	rep.CosignSignatures = cosignEvidence
	if len(cosignEvidence) == 0 {
		status := "fail"
		detail := "no cosign signature or verification evidence found under artifacts/release/cosign/"
		if *allowMissingSupplychain {
			status = "warn"
			detail = "no cosign signature or verification evidence found under artifacts/release/cosign/; permitted by --allow-missing-supplychain"
		}
		rep.Findings = append(rep.Findings, Finding{
			Category: "cosign",
			Status:   status,
			Detail:   detail,
		})
	} else {
		for _, evidence := range cosignEvidence {
			rep.Findings = append(rep.Findings, Finding{Category: "cosign", Path: evidence, Status: "pass", Detail: "cosign signature verification evidence present"})
		}
	}
	rep.Findings = append(rep.Findings, requiredCosignEvidenceFindings(cosignDir, *allowMissingSupplychain)...)

	sboms := mustGlob(*dir, "*.sbom.json")
	rep.SBOMFiles = sboms
	for _, sbom := range sboms {
		rep.Findings = append(rep.Findings, Finding{Category: "sbom", Path: sbom, Status: "pass", Detail: "SBOM present (CycloneDX from BuildKit)"})
	}
	if len(sboms) == 0 {
		status := "fail"
		detail := "no SBOM (.sbom.json) attached"
		if *allowMissingSupplychain {
			status = "warn"
			detail = "no SBOM (.sbom.json) attached; permitted by --allow-missing-supplychain"
		}
		rep.Findings = append(rep.Findings, Finding{Category: "sbom", Status: status, Detail: detail})
	}

	provenance := mustGlob(*dir, "*.provenance.json")
	rep.ProvenanceFiles = provenance
	for _, p := range provenance {
		rep.Findings = append(rep.Findings, Finding{Category: "provenance", Path: p, Status: "pass", Detail: "SLSA-style provenance present"})
	}
	if len(provenance) == 0 {
		status := "fail"
		detail := "no SLSA-style provenance attached"
		if *allowMissingSupplychain {
			status = "warn"
			detail = "no SLSA-style provenance attached; permitted by --allow-missing-supplychain"
		}
		rep.Findings = append(rep.Findings, Finding{Category: "provenance", Status: status, Detail: detail})
	}

	rep.OverallStatus = aggregate(rep.Findings)

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return err
	}

	if *report != "" {
		f, err := os.Create(*report)
		if err != nil {
			return fmt.Errorf("open report: %w", err)
		}
		defer f.Close()
		fileEnc := json.NewEncoder(f)
		fileEnc.SetIndent("", "  ")
		if err := fileEnc.Encode(rep); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}

	if rep.OverallStatus == "fail" {
		return fmt.Errorf("release-verify reported a fail finding")
	}
	return nil
}

// Report is the JSON shape consumed by the release pipeline + the
// tools/release-verify smoke harness.
type Report struct {
	SchemaVersion                int       `json:"schema_version"`
	Tag                          string    `json:"tag"`
	ArtifactsDir                 string    `json:"artifacts_dir"`
	EvaluatedAt                  string    `json:"evaluated_at"`
	OverallStatus                string    `json:"overall_status"`
	PinnedCosignVersion          string    `json:"pinned_cosign_version,omitempty"`
	PinnedCosignLinuxAMD64Digest string    `json:"pinned_cosign_linux_amd64_digest,omitempty"`
	PinnedCosignLinuxARM64Digest string    `json:"pinned_cosign_linux_arm64_digest,omitempty"`
	DebFiles                     []string  `json:"deb_files,omitempty"`
	CosignSignatures             []string  `json:"cosign_signatures,omitempty"`
	ImageDigests                 []string  `json:"image_digests,omitempty"`
	SBOMFiles                    []string  `json:"sbom_files,omitempty"`
	ProvenanceFiles              []string  `json:"provenance_files,omitempty"`
	Findings                     []Finding `json:"findings"`
}

// Finding is a single classified observation.
type Finding struct {
	Category string `json:"category"`
	Path     string `json:"path,omitempty"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
}

func aggregate(findings []Finding) string {
	out := "pass"
	for _, f := range findings {
		switch f.Status {
		case "fail":
			return "fail"
		case "warn":
			if out == "pass" {
				out = "warn"
			}
		}
	}
	return out
}

func mustGlob(dir, pattern string) []string {
	entries, _ := filepath.Glob(filepath.Join(dir, pattern))
	if entries == nil {
		return []string{}
	}
	return entries
}

func fileExists(path string) bool {
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

func readImageDigests(path string, allowMissing bool) ([]string, []Finding) {
	data, err := os.ReadFile(path)
	if err != nil {
		status := "fail"
		detail := fmt.Sprintf("image digest manifest missing: %v", err)
		if allowMissing {
			status = "warn"
			detail = fmt.Sprintf("image digest manifest missing: %v; permitted by --allow-missing-supplychain", err)
		}
		return nil, []Finding{{Category: "image-digest", Path: path, Status: status, Detail: detail}}
	}

	seen := map[string]string{}
	var digests []string
	for lineNo, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		component, digest, ok := strings.Cut(line, "=")
		component = strings.TrimSpace(component)
		digest = strings.TrimSpace(digest)
		if !ok || component == "" || !strings.Contains(digest, "@sha256:") {
			return digests, []Finding{{
				Category: "image-digest",
				Path:     path,
				Status:   "fail",
				Detail:   fmt.Sprintf("invalid image digest manifest line %d: expected component=repository@sha256:digest", lineNo+1),
			}}
		}
		seen[component] = digest
		digests = append(digests, line)
	}

	findings := []Finding{}
	for _, component := range requiredOCIComponents {
		digest, ok := seen[component]
		if !ok {
			status := "fail"
			detail := fmt.Sprintf("required OCI image %q missing from image-digests.txt", component)
			if allowMissing {
				status = "warn"
				detail += "; permitted by --allow-missing-supplychain"
			}
			findings = append(findings, Finding{Category: "image-digest", Path: path, Status: status, Detail: detail})
			continue
		}
		findings = append(findings, Finding{Category: "image-digest", Path: path, Status: "pass", Detail: fmt.Sprintf("%s image digest recorded: %s", component, digest)})
	}
	return digests, findings
}

func requiredCosignEvidenceFindings(cosignDir string, allowMissing bool) []Finding {
	findings := []Finding{}
	for _, component := range requiredOCIComponents {
		verifyPath := filepath.Join(cosignDir, component+".verify.json")
		sigPath := filepath.Join(cosignDir, component+".sig")
		evidencePath := ""
		switch {
		case fileExists(verifyPath):
			evidencePath = verifyPath
		case fileExists(sigPath):
			evidencePath = sigPath
		}
		if evidencePath != "" {
			findings = append(findings, Finding{Category: "cosign", Path: evidencePath, Status: "pass", Detail: fmt.Sprintf("%s cosign verification evidence present", component)})
			continue
		}
		status := "fail"
		detail := fmt.Sprintf("required OCI image %q has no cosign verification evidence", component)
		if allowMissing {
			status = "warn"
			detail += "; permitted by --allow-missing-supplychain"
		}
		findings = append(findings, Finding{Category: "cosign", Path: cosignDir, Status: status, Detail: detail})
	}
	return findings
}

// locateVersionsFile resolves the supplied versions.yaml path. When the path
// is absolute or already exists relative to cwd it is returned as-is.
// Otherwise the function walks the cwd ancestry looking for the file,
// matching how `go mod` resolves go.mod from a sub-package. This lets `go
// run ./tools/release-verify` work from any working directory under the
// repo root and lets unit tests under tools/release-verify discover the
// repository's canonical versions.yaml without an explicit flag.
func locateVersionsFile(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	if filepath.IsAbs(path) {
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
		return "", false
	}
	if _, err := os.Stat(path); err == nil {
		return path, true
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, path)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
