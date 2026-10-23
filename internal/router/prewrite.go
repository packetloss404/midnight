package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type RouteArtifactPrewriteAction string

type RouteArtifactPublicationApplyStatus string

const (
	RouteArtifactPrewriteActionWrite RouteArtifactPrewriteAction = "write"
	RouteArtifactPrewriteActionSkip  RouteArtifactPrewriteAction = "skip"
	RouteArtifactPrewriteActionError RouteArtifactPrewriteAction = "error"

	RouteArtifactPublicationApplyStatusReady   RouteArtifactPublicationApplyStatus = "ready"
	RouteArtifactPublicationApplyStatusRefused RouteArtifactPublicationApplyStatus = "refused"
	RouteArtifactPublicationApplyStatusError   RouteArtifactPublicationApplyStatus = "error"
)

// RouteArtifactPrewriteInput describes one already-rendered route group artifact
// being checked before any filesystem write is attempted.
type RouteArtifactPrewriteInput struct {
	RouteGroup string
	// RouteName, when non-empty, identifies a single route within the group so
	// the writer emits one file per route (`<group>/<routeName>-<hash>.<format>`)
	// instead of one shared file per group. Leaving it empty preserves the
	// legacy single-file-per-group layout for backward compatibility.
	RouteName    string
	Format       string
	File         TraefikFile
	Rendered     string
	ExpectedHash string
	ExistingHash string
}

type RouteArtifactPrewritePlan struct {
	Items        []RouteArtifactPrewriteItem
	Writes       int
	Skips        int
	Errors       int
	BytesToWrite int
}

type RouteArtifactPrewriteItem struct {
	RouteGroup   string
	RouteName    string
	Format       string
	Hash         string
	Action       RouteArtifactPrewriteAction
	BytesToWrite int
	Diagnostics  []string
	rendered     string
}

// RoutePublicationSummary is a support-safe, operator-facing summary of a
// planned route artifact publication. It is derived from the prewrite plan and
// intentionally excludes rendered config, hosts, service URLs, and route names.
type RoutePublicationSummary struct {
	Writes      int      `json:"writes"`
	Skips       int      `json:"skips"`
	Errors      int      `json:"errors"`
	TotalBytes  int      `json:"totalBytes"`
	StaleCount  int      `json:"staleCount"`
	NextActions []string `json:"nextActions"`
}

// RouteArtifactPublicationWriter is the injected side-effect seam for route
// artifact publication. Implementations may map route groups and formats to
// filesystem paths, but errors returned here must be safe for operators to see.
type RouteArtifactPublicationWriter interface {
	WriteRouteArtifact(RouteArtifactPublicationWrite) (int, error)
}

type RouteArtifactPublicationWrite struct {
	RouteGroup string
	RouteName  string
	Format     string
	Hash       string
	Content    []byte
}

type RouteArtifactPublicationApplyEvidence struct {
	Status       RouteArtifactPublicationApplyStatus `json:"status"`
	Writes       int                                 `json:"writes"`
	Skips        int                                 `json:"skips"`
	Errors       int                                 `json:"errors"`
	BytesWritten int                                 `json:"bytesWritten"`
	Diagnostics  []string                            `json:"diagnostics,omitempty"`
}

// PlanRouteArtifactPrewrite validates rendered route group artifacts and returns
// the deterministic write/skip/error decisions needed by the caller. It does not
// write files or inspect the filesystem.
func PlanRouteArtifactPrewrite(inputs []RouteArtifactPrewriteInput) RouteArtifactPrewritePlan {
	items := make([]RouteArtifactPrewriteInput, len(inputs))
	copy(items, inputs)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].RouteGroup != items[j].RouteGroup {
			return items[i].RouteGroup < items[j].RouteGroup
		}
		if items[i].RouteName != items[j].RouteName {
			return items[i].RouteName < items[j].RouteName
		}
		if items[i].Format != items[j].Format {
			return items[i].Format < items[j].Format
		}
		return RenderedConfigHash(items[i].Rendered) < RenderedConfigHash(items[j].Rendered)
	})

	plan := RouteArtifactPrewritePlan{Items: make([]RouteArtifactPrewriteItem, 0, len(items))}
	for _, input := range items {
		format := strings.TrimSpace(input.Format)
		if format == "" {
			format = "traefik.dynamic.yaml"
		}

		hash := RenderedConfigHash(input.Rendered)
		validation := ValidateRenderedRouteGroup(input.File, input.Rendered, strings.TrimSpace(input.ExpectedHash))
		item := RouteArtifactPrewriteItem{
			RouteGroup:  input.RouteGroup,
			RouteName:   input.RouteName,
			Format:      format,
			Hash:        hash,
			Diagnostics: validation.SupportSafeDiagnostics(),
			rendered:    input.Rendered,
		}

		if !validation.OK() {
			item.Action = RouteArtifactPrewriteActionError
			plan.Errors++
		} else if strings.TrimSpace(input.ExistingHash) == hash {
			item.Action = RouteArtifactPrewriteActionSkip
			plan.Skips++
		} else {
			item.Action = RouteArtifactPrewriteActionWrite
			item.BytesToWrite = len([]byte(input.Rendered))
			plan.Writes++
			plan.BytesToWrite += item.BytesToWrite
		}

		plan.Items = append(plan.Items, item)
	}

	return plan
}

// ApplyRouteArtifactPublication writes only planned write items through the
// injected writer. Skip items are counted but not written. Error items refuse the
// entire publication so callers cannot partially apply a failed prewrite plan.
func ApplyRouteArtifactPublication(plan RouteArtifactPrewritePlan, writer RouteArtifactPublicationWriter) (RouteArtifactPublicationApplyEvidence, error) {
	evidence := RouteArtifactPublicationApplyEvidence{Status: RouteArtifactPublicationApplyStatusReady, Skips: plan.Skips, Errors: plan.Errors}

	for _, item := range plan.Items {
		if item.Action == RouteArtifactPrewriteActionError {
			evidence.Diagnostics = append(evidence.Diagnostics, item.Diagnostics...)
		}
	}
	sort.Strings(evidence.Diagnostics)
	if plan.Errors > 0 || hasRouteArtifactPrewriteErrorItem(plan.Items) {
		evidence.Status = RouteArtifactPublicationApplyStatusRefused
		if len(evidence.Diagnostics) == 0 {
			evidence.Diagnostics = append(evidence.Diagnostics, "code=route_publication_prewrite_errors")
		}
		return evidence, errors.New("route publication refused prewrite errors")
	}

	if writer == nil {
		evidence.Status = RouteArtifactPublicationApplyStatusError
		evidence.Diagnostics = append(evidence.Diagnostics, "code=route_publication_writer_missing")
		return evidence, errors.New("route publication writer missing")
	}

	for _, item := range plan.Items {
		switch item.Action {
		case RouteArtifactPrewriteActionSkip:
			continue
		case RouteArtifactPrewriteActionWrite:
			written, err := writer.WriteRouteArtifact(RouteArtifactPublicationWrite{
				RouteGroup: item.RouteGroup,
				RouteName:  item.RouteName,
				Format:     item.Format,
				Hash:       item.Hash,
				Content:    []byte(item.rendered),
			})
			if err != nil {
				evidence.Status = RouteArtifactPublicationApplyStatusError
				evidence.Diagnostics = append(evidence.Diagnostics, "code=route_publication_write_failed")
				return evidence, errors.New("route publication write failed")
			}
			evidence.Writes++
			evidence.BytesWritten += written
		}
	}

	return evidence, nil
}

// WriteRouteArtifactPublicationApplyEvidenceJSON writes apply evidence to an
// explicitly named .json file. It validates status/count consistency before any
// filesystem write so refused or errored publications cannot be recorded ready.
func WriteRouteArtifactPublicationApplyEvidenceJSON(path string, evidence RouteArtifactPublicationApplyEvidence) error {
	if err := validateRoutePublicationEvidenceJSONPath(path); err != nil {
		return err
	}
	if err := ValidateRouteArtifactPublicationApplyEvidence(evidence); err != nil {
		return err
	}

	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(evidence); err != nil {
		return err
	}
	return WriteFileAtomic(path, out.Bytes(), 0o640)
}

// ReadRouteArtifactPublicationApplyEvidenceJSON reads apply evidence from an
// explicitly named .json file using a strict decoder: unknown fields and trailing
// JSON values are rejected.
func ReadRouteArtifactPublicationApplyEvidenceJSON(path string) (RouteArtifactPublicationApplyEvidence, error) {
	if err := validateRoutePublicationEvidenceJSONPath(path); err != nil {
		return RouteArtifactPublicationApplyEvidence{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return RouteArtifactPublicationApplyEvidence{}, err
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var evidence RouteArtifactPublicationApplyEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return RouteArtifactPublicationApplyEvidence{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return RouteArtifactPublicationApplyEvidence{}, errors.New("route publication evidence JSON has trailing data")
		}
		return RouteArtifactPublicationApplyEvidence{}, err
	}
	if err := ValidateRouteArtifactPublicationApplyEvidence(evidence); err != nil {
		return RouteArtifactPublicationApplyEvidence{}, err
	}
	return evidence, nil
}

func ValidateRouteArtifactPublicationApplyEvidence(evidence RouteArtifactPublicationApplyEvidence) error {
	switch evidence.Status {
	case RouteArtifactPublicationApplyStatusReady:
		if evidence.Errors > 0 {
			return errors.New("route publication apply evidence cannot be ready with errors")
		}
		if len(evidence.Diagnostics) > 0 {
			return errors.New("route publication apply evidence cannot be ready with diagnostics")
		}
	case RouteArtifactPublicationApplyStatusRefused, RouteArtifactPublicationApplyStatusError:
		// valid terminal statuses for refused prewrite plans and failed writer calls
	default:
		return fmt.Errorf("unknown route publication apply evidence status %q", evidence.Status)
	}
	if evidence.Writes < 0 || evidence.Skips < 0 || evidence.Errors < 0 || evidence.BytesWritten < 0 {
		return errors.New("route publication apply evidence counts must be non-negative")
	}
	return nil
}

func validateRoutePublicationEvidenceJSONPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("route publication evidence path is required")
	}
	if filepath.Ext(path) != ".json" {
		return errors.New("route publication evidence path must end with .json")
	}
	return nil
}

// SummarizeRoutePublication converts prewrite decisions into a compact
// support-safe publication summary without performing any publication work.
func SummarizeRoutePublication(plan RouteArtifactPrewritePlan) RoutePublicationSummary {
	summary := RoutePublicationSummary{
		Writes:     plan.Writes,
		Skips:      plan.Skips,
		Errors:     plan.Errors,
		TotalBytes: plan.BytesToWrite,
	}

	for _, item := range plan.Items {
		for _, diagnostic := range item.Diagnostics {
			if strings.Contains(diagnostic, "code="+string(RouteArtifactStaleHash)) {
				summary.StaleCount++
			}
		}
	}

	if summary.Errors > 0 {
		if summary.StaleCount > 0 {
			summary.NextActions = append(summary.NextActions, "regenerate stale route artifacts before publishing")
		}
		summary.NextActions = append(summary.NextActions, "review support-safe route artifact diagnostics")
	} else if summary.Writes > 0 {
		summary.NextActions = append(summary.NextActions, "publish planned route artifact writes")
	} else {
		summary.NextActions = append(summary.NextActions, "no publication needed; artifacts already current")
	}

	return summary
}

func hasRouteArtifactPrewriteErrorItem(items []RouteArtifactPrewriteItem) bool {
	for _, item := range items {
		if item.Action == RouteArtifactPrewriteActionError {
			return true
		}
	}
	return false
}

// RenderRoutePublicationSummaryJSON renders the summary with deterministic
// struct-field ordering for logs, CLIs, and support bundles.
func RenderRoutePublicationSummaryJSON(summary RoutePublicationSummary) (string, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(summary); err != nil {
		return "", err
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}
