package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"
)

type drillManifest struct {
	Version string
	Owner   string
	Drills  []drill
}

type drill struct {
	ID               string
	Title            string
	Status           string
	ReasonCode       string
	Event            string
	Diagnostic       string
	Runbook          string
	MetadataTags     []string
	MetadataNotes    []string
	MetadataSubcases []string
}

func parseDrillManifest(data []byte) (drillManifest, error) {
	var manifest drillManifest
	var current *drill
	inMetadata := false
	metadataList := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "version:"):
			manifest.Version = valueAfterColon(line)
		case strings.HasPrefix(line, "owner:"):
			manifest.Owner = valueAfterColon(line)
		case strings.HasPrefix(line, "- id:"):
			if current != nil {
				manifest.Drills = append(manifest.Drills, *current)
			}
			current = &drill{ID: strings.TrimSpace(strings.TrimPrefix(line, "- id:"))}
			inMetadata = false
			metadataList = ""
		case current != nil && strings.HasPrefix(line, "title:"):
			current.Title = valueAfterColon(line)
			inMetadata = false
			metadataList = ""
		case current != nil && strings.HasPrefix(line, "status:"):
			current.Status = valueAfterColon(line)
			inMetadata = false
			metadataList = ""
		case current != nil && strings.HasPrefix(line, "reason-code:"):
			current.ReasonCode = valueAfterColon(line)
			inMetadata = false
			metadataList = ""
		case current != nil && strings.HasPrefix(line, "event:"):
			current.Event = valueAfterColon(line)
			inMetadata = false
			metadataList = ""
		case current != nil && strings.HasPrefix(line, "diagnostic:"):
			current.Diagnostic = valueAfterColon(line)
			inMetadata = false
			metadataList = ""
		case current != nil && strings.HasPrefix(line, "runbook:"):
			current.Runbook = valueAfterColon(line)
			inMetadata = false
			metadataList = ""
		case current != nil && line == "metadata:":
			inMetadata = true
			metadataList = ""
		case current != nil && inMetadata && line == "subcases:":
			metadataList = strings.TrimSuffix(line, ":")
		case current != nil && inMetadata && line == "tags:":
			metadataList = strings.TrimSuffix(line, ":")
		case current != nil && inMetadata && line == "notes:":
			metadataList = strings.TrimSuffix(line, ":")
		case current != nil && inMetadata && metadataList != "" && strings.HasPrefix(line, "- "):
			value := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			switch metadataList {
			case "subcases":
				current.MetadataSubcases = append(current.MetadataSubcases, value)
			case "tags":
				current.MetadataTags = append(current.MetadataTags, value)
			case "notes":
				current.MetadataNotes = append(current.MetadataNotes, value)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return drillManifest{}, err
	}
	if current != nil {
		manifest.Drills = append(manifest.Drills, *current)
	}
	return manifest, nil
}

func validateDrillManifest(data []byte) error {
	manifest, err := parseDrillManifest(data)
	if err != nil {
		return err
	}
	return validateParsedDrillManifest(manifest)
}

func validateParsedDrillManifest(manifest drillManifest) error {
	if manifest.Version != "1" {
		return errors.New("drill manifest version must be 1")
	}
	if strings.TrimSpace(manifest.Owner) == "" {
		return errors.New("drill manifest owner is required")
	}
	want := requiredDrillIDs()
	seen := make(map[string]struct{}, len(manifest.Drills))
	for i, drill := range manifest.Drills {
		if _, ok := want[drill.ID]; !ok {
			return fmt.Errorf("drills[%d] has unknown id %q", i, drill.ID)
		}
		if _, ok := seen[drill.ID]; ok {
			return fmt.Errorf("drills[%d] duplicates id %q", i, drill.ID)
		}
		if strings.TrimSpace(drill.Title) == "" || strings.TrimSpace(drill.Status) == "" {
			return fmt.Errorf("drills[%d] requires title and status", i)
		}
		if strings.TrimSpace(drill.ReasonCode) == "" || strings.TrimSpace(drill.Event) == "" || strings.TrimSpace(drill.Diagnostic) == "" || strings.TrimSpace(drill.Runbook) == "" {
			return fmt.Errorf("drills[%d] requires reason-code, event, diagnostic, and runbook", i)
		}
		if drill.ID == "D-14" && len(drill.MetadataSubcases) == 0 {
			return fmt.Errorf("drills[%d] D-14 requires metadata subcases", i)
		}
		seen[drill.ID] = struct{}{}
	}
	for id := range want {
		if _, ok := seen[id]; !ok {
			return fmt.Errorf("drill manifest missing required id %s", id)
		}
	}
	if err := validatePhase3HostDrillMetadata(manifest); err != nil {
		return err
	}
	return nil
}

func requiredDrillIDs() map[string]struct{} {
	ids := make(map[string]struct{}, 19)
	for i := 1; i <= 19; i++ {
		ids[fmt.Sprintf("D-%02d", i)] = struct{}{}
	}
	return ids
}

func valueAfterColon(line string) string {
	_, value, _ := strings.Cut(line, ":")
	return strings.TrimSpace(value)
}

func hasMetadataValue(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

func validateWorkspaceRelativeMetadataRef(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("ref is required")
	}
	if strings.Contains(value, `\`) {
		return fmt.Errorf("%q must use portable forward slashes", value)
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(normalized, "/") || strings.Contains(normalized, ":") {
		return fmt.Errorf("%q must be workspace-relative", value)
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." {
			return fmt.Errorf("%q must not escape workspace", value)
		}
	}
	return nil
}
