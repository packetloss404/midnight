package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	phase3DrillPromotionReadinessID           = "phase0.beta.b8.phase3-drill-promotion-readiness"
	phase3DrillPromotionReadinessEvidenceName = "b8-phase3-drill-promotion-readiness-contract"
	phase3DrillPromotionReadinessSourcePath   = "test/drills/phase3-promotion-readiness.yaml"
)

type phase3DrillPromotionReadinessContract struct {
	ID                 string                               `json:"id"`
	Sprint             string                               `json:"sprint"`
	Status             string                               `json:"status"`
	Source             string                               `json:"source"`
	LocalOnly          bool                                 `json:"localOnly"`
	SourceReservations string                               `json:"sourceReservations"`
	SourceManifest     string                               `json:"sourceManifest"`
	Execution          string                               `json:"execution"`
	LiveExecution      bool                                 `json:"liveExecution"`
	ReservedIDs        []string                             `json:"reservedIds"`
	RequiredUnblockers []string                             `json:"requiredUnblockers"`
	Checks             []phase3DrillPromotionReadinessCheck `json:"checks"`
}

type phase3DrillPromotionReadinessCheck struct {
	ID                 string   `json:"id"`
	Status             string   `json:"status"`
	Domain             string   `json:"domain"`
	ReservationID      string   `json:"reservationId"`
	CarrierID          string   `json:"carrierId"`
	RequiredUnblockers []string `json:"requiredUnblockers"`
	EvidenceRefs       []string `json:"evidenceRefs"`
}

type phase3DrillPromotionReadinessFile struct {
	Version            string
	Owner              string
	Sprint             string
	Status             string
	LocalOnly          bool
	SourceReservations string
	SourceManifest     string
	Execution          string
	LiveExecution      string
	ReservedIDs        []string
	Unblockers         []string
	EvidenceRefs       []string
	Checks             []phase3DrillPromotionReadinessCheck
}

func buildPhase3DrillPromotionReadinessContract(manifest drillManifest) (phase3DrillPromotionReadinessContract, error) {
	data, err := os.ReadFile(phase3DrillPromotionReadinessSourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return phase3DrillPromotionReadinessContract{}, nil
		}
		return phase3DrillPromotionReadinessContract{}, err
	}
	return buildPhase3DrillPromotionReadinessContractFromData(manifest, data)
}

func buildPhase3DrillPromotionReadinessContractFromData(manifest drillManifest, data []byte) (phase3DrillPromotionReadinessContract, error) {
	readiness, err := parsePhase3DrillPromotionReadiness(data)
	if err != nil {
		return phase3DrillPromotionReadinessContract{}, err
	}
	if err := validatePhase3DrillPromotionReadiness(manifest, readiness); err != nil {
		return phase3DrillPromotionReadinessContract{}, err
	}
	checks := append([]phase3DrillPromotionReadinessCheck(nil), readiness.Checks...)
	reservedIDs := make([]string, 0, len(checks))
	for i := range checks {
		reservedIDs = append(reservedIDs, checks[i].ID)
		checks[i].RequiredUnblockers = append([]string(nil), checks[i].RequiredUnblockers...)
		checks[i].EvidenceRefs = append([]string(nil), checks[i].EvidenceRefs...)
		sort.Strings(checks[i].RequiredUnblockers)
		sort.Strings(checks[i].EvidenceRefs)
	}
	sort.Strings(reservedIDs)
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	return phase3DrillPromotionReadinessContract{
		ID:                 phase3DrillPromotionReadinessID,
		Sprint:             readiness.Sprint,
		Status:             "not-ready",
		Source:             phase3DrillPromotionReadinessSourcePath,
		LocalOnly:          readiness.LocalOnly,
		SourceReservations: readiness.SourceReservations,
		SourceManifest:     readiness.SourceManifest,
		Execution:          "not-run",
		LiveExecution:      false,
		ReservedIDs:        reservedIDs,
		RequiredUnblockers: phase3PromotionReadinessRequiredUnblockers(),
		Checks:             checks,
	}, nil
}

func parsePhase3DrillPromotionReadiness(data []byte) (phase3DrillPromotionReadinessFile, error) {
	var file phase3DrillPromotionReadinessFile
	var current *phase3DrillPromotionReadinessCheck
	listName := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "version:"):
			file.Version = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "owner:"):
			file.Owner = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "sprint:"):
			file.Sprint = valueAfterColon(line)
			listName = ""
		case current == nil && strings.HasPrefix(line, "status:"):
			file.Status = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "local-only:"):
			file.LocalOnly = strings.EqualFold(valueAfterColon(line), "true")
			listName = ""
		case strings.HasPrefix(line, "reservation-index:"):
			file.SourceReservations = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "source-reservations:"):
			file.SourceReservations = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "source-manifest:"):
			file.SourceManifest = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "execution:"):
			file.Execution = valueAfterColon(line)
			listName = ""
		case strings.HasPrefix(line, "live-execution:"):
			file.LiveExecution = valueAfterColon(line)
			listName = ""
		case line == "reserved-ids:":
			listName = "reserved-ids"
		case line == "unblockers:":
			listName = "unblockers"
		case line == "evidence-refs:":
			listName = "evidence-refs"
		case strings.HasPrefix(line, "- id:"):
			if current != nil {
				file.Checks = append(file.Checks, *current)
			}
			current = &phase3DrillPromotionReadinessCheck{ID: strings.TrimSpace(strings.TrimPrefix(line, "- id:"))}
			listName = ""
		case current != nil && strings.HasPrefix(line, "status:"):
			current.Status = valueAfterColon(line)
			listName = ""
		case current != nil && strings.HasPrefix(line, "domain:"):
			current.Domain = valueAfterColon(line)
			listName = ""
		case current != nil && strings.HasPrefix(line, "reservation-id:"):
			current.ReservationID = valueAfterColon(line)
			listName = ""
		case current != nil && strings.HasPrefix(line, "carrier-id:"):
			current.CarrierID = valueAfterColon(line)
			listName = ""
		case current != nil && line == "required-unblockers:":
			listName = "required-unblockers"
		case listName != "" && strings.HasPrefix(line, "- "):
			value := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			switch listName {
			case "reserved-ids":
				file.ReservedIDs = append(file.ReservedIDs, value)
			case "unblockers":
				file.Unblockers = append(file.Unblockers, value)
			case "required-unblockers":
				if current != nil {
					current.RequiredUnblockers = append(current.RequiredUnblockers, value)
				}
			case "evidence-refs":
				if current != nil {
					current.EvidenceRefs = append(current.EvidenceRefs, value)
				} else {
					file.EvidenceRefs = append(file.EvidenceRefs, value)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return phase3DrillPromotionReadinessFile{}, err
	}
	if current != nil {
		file.Checks = append(file.Checks, *current)
	}
	return file, nil
}

func validatePhase3DrillPromotionReadiness(manifest drillManifest, file phase3DrillPromotionReadinessFile) error {
	if file.Version != "1" {
		return errors.New("phase 3 drill promotion readiness version must be 1")
	}
	if strings.TrimSpace(file.Owner) == "" {
		return errors.New("phase 3 drill promotion readiness owner is required")
	}
	if file.Sprint != "Phase 0 Beta Sprint B8" {
		return fmt.Errorf("phase 3 drill promotion readiness sprint = %q, want Phase 0 Beta Sprint B8", file.Sprint)
	}
	if !file.LocalOnly {
		return errors.New("phase 3 drill promotion readiness must be local-only")
	}
	if file.SourceReservations != phase3DrillReservationsSourcePath {
		return fmt.Errorf("phase 3 drill promotion readiness source-reservations = %q, want %s", file.SourceReservations, phase3DrillReservationsSourcePath)
	}
	if err := validateWorkspaceRelativeMetadataRef(file.SourceReservations); err != nil {
		return fmt.Errorf("phase 3 drill promotion readiness source-reservations: %w", err)
	}
	if file.SourceManifest != phase3HostDrillMetadataSourcePath {
		return fmt.Errorf("phase 3 drill promotion readiness source-manifest = %q, want %s", file.SourceManifest, phase3HostDrillMetadataSourcePath)
	}
	if err := validateWorkspaceRelativeMetadataRef(file.SourceManifest); err != nil {
		return fmt.Errorf("phase 3 drill promotion readiness source-manifest: %w", err)
	}
	if file.Execution != "" || file.LiveExecution != "" || file.Status != "" {
		return errors.New("phase 3 drill promotion readiness must not declare top-level execution, live-execution, or ready status")
	}
	if len(file.Checks) == 0 {
		return errors.New("phase 3 drill promotion readiness requires checklist rows")
	}
	if err := validatePhase3PromotionReadinessChecks(manifest, file.Checks); err != nil {
		return err
	}
	return nil
}

func validatePhase3PromotionReadinessChecks(manifest drillManifest, checks []phase3DrillPromotionReadinessCheck) error {
	required := stringSet(phase3ReservationRequiredIDs())
	seen := make(map[string]struct{}, len(checks))
	for i, check := range checks {
		if _, ok := required[check.ID]; !ok {
			return fmt.Errorf("phase 3 drill promotion readiness checks[%d] has unknown id %q", i, check.ID)
		}
		if _, ok := seen[check.ID]; ok {
			return fmt.Errorf("phase 3 drill promotion readiness checks[%d] duplicates id %q", i, check.ID)
		}
		seen[check.ID] = struct{}{}
		if _, ok := findDrillByID(manifest, check.ID); ok {
			return fmt.Errorf("phase 3 drill promotion readiness id %s must remain outside executable manifest rows", check.ID)
		}
		if check.Status != "not-ready" {
			return fmt.Errorf("phase 3 drill promotion readiness checks[%d] status = %q, want not-ready", i, check.Status)
		}
		if check.ReservationID != check.ID {
			return fmt.Errorf("phase 3 drill promotion readiness checks[%d] reservation-id = %q, want %s", i, check.ReservationID, check.ID)
		}
		if want := phase3ReservationRequiredDomainsByID()[check.ID]; check.Domain != want {
			return fmt.Errorf("phase 3 drill promotion readiness checks[%d] domain = %q, want %q", i, check.Domain, want)
		}
		if strings.TrimSpace(check.CarrierID) == "" {
			return fmt.Errorf("phase 3 drill promotion readiness checks[%d] carrier-id is required", i)
		}
		if err := validatePhase3PromotionReadinessUnblockers(i, check.RequiredUnblockers); err != nil {
			return err
		}
		if len(check.EvidenceRefs) == 0 {
			return fmt.Errorf("phase 3 drill promotion readiness checks[%d] requires at least one evidence ref", i)
		}
		for _, ref := range check.EvidenceRefs {
			if err := validateWorkspaceRelativeMetadataRef(ref); err != nil {
				return fmt.Errorf("phase 3 drill promotion readiness checks[%d] evidence-ref: %w", i, err)
			}
		}
	}
	for _, id := range phase3ReservationRequiredIDs() {
		if _, ok := seen[id]; !ok {
			return fmt.Errorf("phase 3 drill promotion readiness missing reserved id %s", id)
		}
	}
	for id := range requiredDrillIDs() {
		if _, ok := required[id]; ok {
			return fmt.Errorf("phase 3 drill promotion readiness id %s must not be an executable manifest id", id)
		}
	}
	return nil
}

func validatePhase3PromotionReadinessUnblockers(index int, unblockers []string) error {
	required := stringSet(phase3PromotionReadinessRequiredUnblockers())
	seen := make(map[string]struct{}, len(unblockers))
	for i, blocker := range unblockers {
		if !isSafePhase3MetadataToken(blocker) {
			return fmt.Errorf("phase 3 drill promotion readiness checks[%d] required-unblockers[%d] value %q must be a stable symbolic id", index, i, blocker)
		}
		if _, ok := required[blocker]; !ok {
			return fmt.Errorf("phase 3 drill promotion readiness checks[%d] required-unblockers[%d] has unknown unblocker %q", index, i, blocker)
		}
		if _, ok := seen[blocker]; ok {
			return fmt.Errorf("phase 3 drill promotion readiness checks[%d] required-unblockers[%d] duplicates unblocker %q", index, i, blocker)
		}
		seen[blocker] = struct{}{}
	}
	for _, blocker := range phase3PromotionReadinessRequiredUnblockers() {
		if _, ok := seen[blocker]; !ok {
			return fmt.Errorf("phase 3 drill promotion readiness missing required unblocker %s", blocker)
		}
	}
	return nil
}

func phase3PromotionReadinessRequiredUnblockers() []string {
	return []string{
		"accepted-id-range",
		"executable-rig",
		"harness-assertion",
		"live-evidence-ref",
		"rollback-boundary",
	}
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}
