package main

import (
	"fmt"
	"sort"
	"strings"
)

const (
	phase3HostDrillMetadataID           = "phase0.beta.b6.phase3-host-drill-candidate-catalog"
	phase3HostDrillMetadataEvidenceName = "b6-phase3-host-drill-candidate-catalog-contract"
	phase3HostDrillMetadataSourcePath   = "test/drills/manifest.yaml"
)

type phase3HostDrillMetadataContract struct {
	ID                    string                          `json:"id"`
	Sprint                string                          `json:"sprint"`
	Status                string                          `json:"status"`
	Source                string                          `json:"source"`
	OfflineByDefault      bool                            `json:"offlineByDefault"`
	ManifestSchemaVersion string                          `json:"manifestSchemaVersion"`
	RequiredTags          []string                        `json:"requiredTags"`
	RequiredNotes         []string                        `json:"requiredNotes"`
	RequiredSubcases      []string                        `json:"requiredSubcases"`
	RequiredDomains       []string                        `json:"requiredDomains"`
	HostScopedDrills      []phase3HostScopedDrillMetadata `json:"hostScopedDrills"`
}

type phase3HostScopedDrillMetadata struct {
	DrillID     string   `json:"drillId"`
	HostScope   string   `json:"hostScope"`
	Domain      string   `json:"domain"`
	Phase3Drill string   `json:"phase3Drill"`
	Execution   string   `json:"execution"`
	EvidenceRef string   `json:"evidenceRef"`
	Tags        []string `json:"tags"`
	Notes       []string `json:"notes"`
	Subcases    []string `json:"subcases"`
}

func buildPhase3HostDrillMetadataContract(manifest drillManifest) (phase3HostDrillMetadataContract, error) {
	hostScopedDrills, err := parsePhase3HostScopedDrillMetadata(manifest)
	if err != nil {
		return phase3HostDrillMetadataContract{}, err
	}
	return phase3HostDrillMetadataContract{
		ID:                    phase3HostDrillMetadataID,
		Sprint:                "Phase 0 Beta Sprint B6",
		Status:                "pass",
		Source:                phase3HostDrillMetadataSourcePath,
		OfflineByDefault:      true,
		ManifestSchemaVersion: manifest.Version,
		RequiredTags:          phase3HostMetadataRequiredTags(),
		RequiredNotes:         phase3HostMetadataRequiredNotes(),
		RequiredSubcases:      phase3HostMetadataRequiredSubcases(),
		RequiredDomains:       phase3HostMetadataRequiredDomains(),
		HostScopedDrills:      hostScopedDrills,
	}, nil
}

func validatePhase3HostDrillMetadata(manifest drillManifest) error {
	_, err := buildPhase3HostDrillMetadataContract(manifest)
	return err
}

func phase3HostMetadataRequiredTags() []string {
	return []string{"phase3", "host-scoped", "local-only"}
}

func phase3HostMetadataRequiredNotes() []string {
	return []string{"phase3-drill", "host-scope", "domain", "evidence-ref", "execution"}
}

func phase3HostMetadataRequiredSubcases() []string {
	return []string{"phase3.host", "phase3.domain", "phase3.planner", "phase3.evidence"}
}

func phase3HostMetadataRequiredDomains() []string {
	return []string{"networking", "runtime_adapter", "registry_cache", "volume_attach"}
}

func parsePhase3HostScopedDrillMetadata(manifest drillManifest) ([]phase3HostScopedDrillMetadata, error) {
	var out []phase3HostScopedDrillMetadata
	for _, drill := range manifest.Drills {
		if !hasMetadataValue(drill.MetadataTags, "phase3") && !hasMetadataValue(drill.MetadataTags, "host-scoped") {
			continue
		}
		for _, tag := range phase3HostMetadataRequiredTags() {
			if !hasMetadataValue(drill.MetadataTags, tag) {
				return nil, fmt.Errorf("drill %s phase 3 host metadata missing tag %q", drill.ID, tag)
			}
		}

		notes := keyValueMetadata(drill.MetadataNotes)
		for _, key := range phase3HostMetadataRequiredNotes() {
			if notes[key] == "" {
				return nil, fmt.Errorf("drill %s phase 3 host metadata missing note %q", drill.ID, key)
			}
		}

		subcases := keyValueMetadata(drill.MetadataSubcases)
		for _, key := range phase3HostMetadataRequiredSubcases() {
			if subcases[key] == "" {
				return nil, fmt.Errorf("drill %s phase 3 host metadata missing subcase %q", drill.ID, key)
			}
		}

		if !isSafePhase3MetadataToken(notes["host-scope"]) {
			return nil, fmt.Errorf("drill %s phase 3 host scope %q must be a stable symbolic id", drill.ID, notes["host-scope"])
		}
		if !validPhase3ReadinessDomain(notes["domain"]) {
			return nil, fmt.Errorf("drill %s phase 3 domain %q is not a required operator-readiness domain", drill.ID, notes["domain"])
		}
		for _, key := range []string{"phase3-drill", "execution"} {
			if !isSafePhase3MetadataToken(notes[key]) {
				return nil, fmt.Errorf("drill %s %s value %q must be a stable symbolic id", drill.ID, key, notes[key])
			}
		}
		if err := validateWorkspaceRelativeMetadataRef(notes["evidence-ref"]); err != nil {
			return nil, fmt.Errorf("drill %s phase 3 evidence-ref: %w", drill.ID, err)
		}
		if err := validateWorkspaceRelativeMetadataRef(subcases["phase3.evidence"]); err != nil {
			return nil, fmt.Errorf("drill %s phase3.evidence subcase: %w", drill.ID, err)
		}

		tags := append([]string(nil), drill.MetadataTags...)
		noteValues := append([]string(nil), drill.MetadataNotes...)
		subcaseValues := append([]string(nil), drill.MetadataSubcases...)
		sort.Strings(tags)
		sort.Strings(noteValues)
		sort.Strings(subcaseValues)
		out = append(out, phase3HostScopedDrillMetadata{
			DrillID:     drill.ID,
			HostScope:   notes["host-scope"],
			Domain:      notes["domain"],
			Phase3Drill: notes["phase3-drill"],
			Execution:   notes["execution"],
			EvidenceRef: notes["evidence-ref"],
			Tags:        tags,
			Notes:       noteValues,
			Subcases:    subcaseValues,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("phase 3 host drill candidate catalog requires at least one host-scoped drill using tags %s", strings.Join(phase3HostMetadataRequiredTags(), ", "))
	}
	if err := validatePhase3HostDrillDomainCoverage(out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DrillID == out[j].DrillID {
			return out[i].HostScope < out[j].HostScope
		}
		return out[i].DrillID < out[j].DrillID
	})
	return out, nil
}

func validatePhase3HostDrillDomainCoverage(drills []phase3HostScopedDrillMetadata) error {
	if len(drills) <= 1 {
		return nil
	}
	covered := make(map[string]struct{}, len(drills))
	for _, drill := range drills {
		covered[drill.Domain] = struct{}{}
	}
	var missing []string
	for _, domain := range phase3HostMetadataRequiredDomains() {
		if _, ok := covered[domain]; !ok {
			missing = append(missing, domain)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("phase 3 host drill candidate catalog with multiple host-scoped drills must cover readiness domains %s; missing %s", strings.Join(phase3HostMetadataRequiredDomains(), ", "), strings.Join(missing, ", "))
	}
	return nil
}

func keyValueMetadata(values []string) map[string]string {
	out := make(map[string]string, len(values))
	for _, raw := range values {
		key, value, ok := strings.Cut(strings.TrimSpace(raw), "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

func validPhase3ReadinessDomain(domain string) bool {
	for _, required := range phase3HostMetadataRequiredDomains() {
		if domain == required {
			return true
		}
	}
	return false
}

func isSafePhase3MetadataToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
