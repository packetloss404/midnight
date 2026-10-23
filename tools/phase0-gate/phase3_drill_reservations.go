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
	phase3DrillReservationsID           = "phase0.beta.b7.phase3-drill-reservations"
	phase3DrillReservationsEvidenceName = "b7-phase3-drill-reservations-contract"
	phase3DrillReservationsSourcePath   = "test/drills/phase3-reservations.yaml"
)

type phase3DrillReservationsContract struct {
	ID                   string                   `json:"id"`
	Sprint               string                   `json:"sprint"`
	Status               string                   `json:"status"`
	Source               string                   `json:"source"`
	LocalOnly            bool                     `json:"localOnly"`
	SourceManifest       string                   `json:"sourceManifest"`
	RequiredReservations []string                 `json:"requiredReservations"`
	RequiredDomains      []string                 `json:"requiredDomains"`
	Reservations         []phase3DrillReservation `json:"reservations"`
}

type phase3DrillReservation struct {
	ID                string   `json:"id"`
	Status            string   `json:"status"`
	Domain            string   `json:"domain"`
	CarrierID         string   `json:"carrierId"`
	CarrierReasonCode string   `json:"carrierReasonCode"`
	PlannedDrill      string   `json:"plannedDrill"`
	HostScope         string   `json:"hostScope"`
	Execution         string   `json:"execution"`
	Rig               string   `json:"rig"`
	Note              string   `json:"note"`
	EvidenceRefs      []string `json:"evidenceRefs"`
}

type phase3DrillReservationsFile struct {
	Version        string
	Owner          string
	Sprint         string
	LocalOnly      bool
	SourceManifest string
	Reservations   []phase3DrillReservation
}

func buildPhase3DrillReservationsContract(manifest drillManifest) (phase3DrillReservationsContract, error) {
	data, err := os.ReadFile(phase3DrillReservationsSourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return phase3DrillReservationsContract{}, nil
		}
		return phase3DrillReservationsContract{}, err
	}
	return buildPhase3DrillReservationsContractFromData(manifest, data)
}

func buildPhase3DrillReservationsContractFromData(manifest drillManifest, data []byte) (phase3DrillReservationsContract, error) {
	reservations, err := parsePhase3DrillReservations(data)
	if err != nil {
		return phase3DrillReservationsContract{}, err
	}
	if err := validatePhase3DrillReservations(manifest, reservations); err != nil {
		return phase3DrillReservationsContract{}, err
	}
	out := append([]phase3DrillReservation(nil), reservations.Reservations...)
	for i := range out {
		sort.Strings(out[i].EvidenceRefs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return phase3DrillReservationsContract{
		ID:                   phase3DrillReservationsID,
		Sprint:               reservations.Sprint,
		Status:               "pass",
		Source:               phase3DrillReservationsSourcePath,
		LocalOnly:            reservations.LocalOnly,
		SourceManifest:       reservations.SourceManifest,
		RequiredReservations: phase3ReservationRequiredIDs(),
		RequiredDomains:      phase3HostMetadataRequiredDomains(),
		Reservations:         out,
	}, nil
}

func parsePhase3DrillReservations(data []byte) (phase3DrillReservationsFile, error) {
	var file phase3DrillReservationsFile
	var current *phase3DrillReservation
	inEvidenceRefs := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "version:"):
			file.Version = valueAfterColon(line)
		case strings.HasPrefix(line, "owner:"):
			file.Owner = valueAfterColon(line)
		case strings.HasPrefix(line, "sprint:"):
			file.Sprint = valueAfterColon(line)
		case strings.HasPrefix(line, "local-only:"):
			file.LocalOnly = strings.EqualFold(valueAfterColon(line), "true")
		case strings.HasPrefix(line, "source-manifest:"):
			file.SourceManifest = valueAfterColon(line)
		case strings.HasPrefix(line, "- id:"):
			if current != nil {
				file.Reservations = append(file.Reservations, *current)
			}
			current = &phase3DrillReservation{ID: strings.TrimSpace(strings.TrimPrefix(line, "- id:"))}
			inEvidenceRefs = false
		case current != nil && strings.HasPrefix(line, "status:"):
			current.Status = valueAfterColon(line)
			inEvidenceRefs = false
		case current != nil && strings.HasPrefix(line, "domain:"):
			current.Domain = valueAfterColon(line)
			inEvidenceRefs = false
		case current != nil && strings.HasPrefix(line, "carrier-id:"):
			current.CarrierID = valueAfterColon(line)
			inEvidenceRefs = false
		case current != nil && strings.HasPrefix(line, "carrier-reason-code:"):
			current.CarrierReasonCode = valueAfterColon(line)
			inEvidenceRefs = false
		case current != nil && strings.HasPrefix(line, "planned-drill:"):
			current.PlannedDrill = valueAfterColon(line)
			inEvidenceRefs = false
		case current != nil && strings.HasPrefix(line, "host-scope:"):
			current.HostScope = valueAfterColon(line)
			inEvidenceRefs = false
		case current != nil && strings.HasPrefix(line, "execution:"):
			current.Execution = valueAfterColon(line)
			inEvidenceRefs = false
		case current != nil && strings.HasPrefix(line, "rig:"):
			current.Rig = valueAfterColon(line)
			inEvidenceRefs = false
		case current != nil && strings.HasPrefix(line, "note:"):
			current.Note = valueAfterColon(line)
			inEvidenceRefs = false
		case current != nil && line == "evidence-refs:":
			inEvidenceRefs = true
		case current != nil && inEvidenceRefs && strings.HasPrefix(line, "- "):
			current.EvidenceRefs = append(current.EvidenceRefs, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
		}
	}
	if err := scanner.Err(); err != nil {
		return phase3DrillReservationsFile{}, err
	}
	if current != nil {
		file.Reservations = append(file.Reservations, *current)
	}
	return file, nil
}

func validatePhase3DrillReservations(manifest drillManifest, file phase3DrillReservationsFile) error {
	if file.Version != "1" {
		return errors.New("phase 3 drill reservations version must be 1")
	}
	if strings.TrimSpace(file.Owner) == "" {
		return errors.New("phase 3 drill reservations owner is required")
	}
	if file.Sprint != "Phase 0 Beta Sprint B7" {
		return fmt.Errorf("phase 3 drill reservations sprint = %q, want Phase 0 Beta Sprint B7", file.Sprint)
	}
	if !file.LocalOnly {
		return errors.New("phase 3 drill reservations must be local-only")
	}
	if file.SourceManifest != phase3HostDrillMetadataSourcePath {
		return fmt.Errorf("phase 3 drill reservations source-manifest = %q, want %s", file.SourceManifest, phase3HostDrillMetadataSourcePath)
	}
	if err := validateWorkspaceRelativeMetadataRef(file.SourceManifest); err != nil {
		return fmt.Errorf("phase 3 drill reservations source-manifest: %w", err)
	}

	required := phase3ReservationRequiredDomainsByID()
	seen := make(map[string]struct{}, len(file.Reservations))
	for i, reservation := range file.Reservations {
		domain, ok := required[reservation.ID]
		if !ok {
			return fmt.Errorf("phase 3 drill reservations[%d] has unknown id %q", i, reservation.ID)
		}
		if _, ok := seen[reservation.ID]; ok {
			return fmt.Errorf("phase 3 drill reservations[%d] duplicates id %q", i, reservation.ID)
		}
		seen[reservation.ID] = struct{}{}
		if reservation.Status != "reserved" {
			return fmt.Errorf("phase 3 drill reservations[%d] status = %q, want reserved", i, reservation.Status)
		}
		if reservation.Domain != domain {
			return fmt.Errorf("phase 3 drill reservations[%d] domain = %q, want %q", i, reservation.Domain, domain)
		}
		if err := validatePhase3DrillReservationCarrier(manifest, reservation); err != nil {
			return fmt.Errorf("phase 3 drill reservations[%d]: %w", i, err)
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{name: "planned-drill", value: reservation.PlannedDrill},
			{name: "host-scope", value: reservation.HostScope},
			{name: "execution", value: reservation.Execution},
			{name: "rig", value: reservation.Rig},
		} {
			if !isSafePhase3MetadataToken(field.value) {
				return fmt.Errorf("phase 3 drill reservations[%d] %s value %q must be a stable symbolic id", i, field.name, field.value)
			}
		}
		if reservation.Execution != "planner-only" || reservation.Rig != "future" {
			return fmt.Errorf("phase 3 drill reservations[%d] must reserve future planner-only drills without claiming execution", i)
		}
		if strings.TrimSpace(reservation.Note) == "" {
			return fmt.Errorf("phase 3 drill reservations[%d] note is required", i)
		}
		if len(reservation.EvidenceRefs) == 0 {
			return fmt.Errorf("phase 3 drill reservations[%d] requires at least one evidence ref", i)
		}
		for _, ref := range reservation.EvidenceRefs {
			if err := validateWorkspaceRelativeMetadataRef(ref); err != nil {
				return fmt.Errorf("phase 3 drill reservations[%d] evidence-ref: %w", i, err)
			}
		}
	}
	for _, id := range phase3ReservationRequiredIDs() {
		if _, ok := seen[id]; !ok {
			return fmt.Errorf("phase 3 drill reservations missing required id %s", id)
		}
	}
	return nil
}

func validatePhase3DrillReservationCarrier(manifest drillManifest, reservation phase3DrillReservation) error {
	carrier, ok := findDrillByID(manifest, reservation.CarrierID)
	if !ok {
		return fmt.Errorf("carrier-id %q does not exist in %s", reservation.CarrierID, phase3HostDrillMetadataSourcePath)
	}
	if carrier.ReasonCode != reservation.CarrierReasonCode {
		return fmt.Errorf("carrier-reason-code %q does not match carrier %s reason-code %q", reservation.CarrierReasonCode, reservation.CarrierID, carrier.ReasonCode)
	}
	return nil
}

func phase3ReservationRequiredIDs() []string {
	return []string{"D-20", "D-21", "D-22", "D-23"}
}

func phase3ReservationRequiredDomainsByID() map[string]string {
	return map[string]string{
		"D-20": "networking",
		"D-21": "runtime_adapter",
		"D-22": "registry_cache",
		"D-23": "volume_attach",
	}
}

func findDrillByID(manifest drillManifest, id string) (drill, bool) {
	for _, candidate := range manifest.Drills {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return drill{}, false
}
