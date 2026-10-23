package acme

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

	"github.com/midnight/midnight/internal/router"
)

const IngressEvidenceBundleSchemaVersion = "midnight.dev/ingress-evidence-bundle/v1alpha1"

type IngressEvidenceBundleInput struct {
	RoutePublicationEvidence RoutePublicationEvidencePacket
	RouteEvidenceIndex       IngressRouteReleaseEvidenceIndex
	ApplyEvidence            router.RouteArtifactPublicationApplyEvidence
}

// IngressEvidenceBundle keeps the support-safe route publication packet,
// release evidence index, and apply result together for one ingress decision.
type IngressEvidenceBundle struct {
	SchemaVersion            string                                       `json:"schemaVersion"`
	Status                   IngressRouteReleaseEvidenceStatus            `json:"status"`
	RoutePublicationEvidence RoutePublicationEvidencePacket               `json:"routePublicationEvidence"`
	RouteEvidenceIndex       IngressRouteReleaseEvidenceIndex             `json:"routeEvidenceIndex"`
	ApplyEvidence            router.RouteArtifactPublicationApplyEvidence `json:"applyEvidence"`
}

func NewIngressEvidenceBundle(input IngressEvidenceBundleInput) (IngressEvidenceBundle, error) {
	bundle := IngressEvidenceBundle{
		SchemaVersion:            IngressEvidenceBundleSchemaVersion,
		RoutePublicationEvidence: input.RoutePublicationEvidence,
		RouteEvidenceIndex:       input.RouteEvidenceIndex,
		ApplyEvidence:            input.ApplyEvidence,
	}
	bundle.Status = rollupIngressRouteReleaseEvidenceStatus([]IngressRouteReleaseEvidenceStatus{
		routePublicationReleaseStatus(input.RoutePublicationEvidence.Status),
		input.RouteEvidenceIndex.Status,
		routePublicationApplyReleaseStatus(input.ApplyEvidence.Status),
	})
	if err := bundle.Validate(); err != nil {
		return IngressEvidenceBundle{}, err
	}
	return bundle, nil
}

func (bundle IngressEvidenceBundle) Validate() error {
	if bundle.SchemaVersion != IngressEvidenceBundleSchemaVersion {
		return errors.New("ingress evidence bundle schemaVersion is invalid")
	}
	if !validIngressRouteReleaseEvidenceStatus(bundle.Status) {
		return fmt.Errorf("ingress evidence bundle status %q is invalid", bundle.Status)
	}
	if err := validateRoutePublicationEvidencePacket(bundle.RoutePublicationEvidence); err != nil {
		return fmt.Errorf("ingress evidence bundle route publication evidence: %w", err)
	}
	if err := bundle.RouteEvidenceIndex.Validate(); err != nil {
		return fmt.Errorf("ingress evidence bundle route evidence index: %w", err)
	}
	if err := router.ValidateRouteArtifactPublicationApplyEvidence(bundle.ApplyEvidence); err != nil {
		return fmt.Errorf("ingress evidence bundle apply evidence: %w", err)
	}

	packetStatus := routePublicationReleaseStatus(bundle.RoutePublicationEvidence.Status)
	if bundle.RouteEvidenceIndex.RoutePublicationEvidence.Status != packetStatus {
		return fmt.Errorf("ingress evidence bundle route publication status mismatch: packet %q index ref %q", packetStatus, bundle.RouteEvidenceIndex.RoutePublicationEvidence.Status)
	}
	if want := rollupIngressRouteReleaseEvidenceStatus([]IngressRouteReleaseEvidenceStatus{
		packetStatus,
		bundle.RouteEvidenceIndex.Status,
		routePublicationApplyReleaseStatus(bundle.ApplyEvidence.Status),
	}); bundle.Status != want {
		return fmt.Errorf("ingress evidence bundle status %q does not match rollup %q", bundle.Status, want)
	}
	return nil
}

func RenderIngressEvidenceBundleJSON(bundle IngressEvidenceBundle) ([]byte, error) {
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(bundle); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func WriteIngressEvidenceBundleJSON(path string, bundle IngressEvidenceBundle) error {
	if err := validateIngressEvidenceBundleJSONPath(path); err != nil {
		return err
	}
	encoded, err := RenderIngressEvidenceBundleJSON(bundle)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o640)
}

func ReadIngressEvidenceBundleJSON(path string) (IngressEvidenceBundle, error) {
	if err := validateIngressEvidenceBundleJSONPath(path); err != nil {
		return IngressEvidenceBundle{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return IngressEvidenceBundle{}, err
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var bundle IngressEvidenceBundle
	if err := decoder.Decode(&bundle); err != nil {
		return IngressEvidenceBundle{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return IngressEvidenceBundle{}, errors.New("ingress evidence bundle JSON has trailing data")
		}
		return IngressEvidenceBundle{}, err
	}
	if err := bundle.Validate(); err != nil {
		return IngressEvidenceBundle{}, err
	}
	return bundle, nil
}

func routePublicationApplyReleaseStatus(status router.RouteArtifactPublicationApplyStatus) IngressRouteReleaseEvidenceStatus {
	switch status {
	case router.RouteArtifactPublicationApplyStatusReady:
		return IngressRouteReleaseEvidenceReady
	case router.RouteArtifactPublicationApplyStatusRefused, router.RouteArtifactPublicationApplyStatusError:
		return IngressRouteReleaseEvidenceBlocked
	default:
		return ""
	}
}

func validateRoutePublicationEvidencePacket(packet RoutePublicationEvidencePacket) error {
	if !validRoutePublicationEvidenceStatus(packet.Status) {
		return fmt.Errorf("status %q is invalid", packet.Status)
	}
	if packet.BytesToWrite < 0 || packet.Prewrite.Writes < 0 || packet.Prewrite.Skips < 0 || packet.Prewrite.Errors < 0 || packet.Prewrite.BytesToWrite < 0 {
		return errors.New("counts must be non-negative")
	}
	if packet.Publication.Writes < 0 || packet.Publication.Skips < 0 || packet.Publication.Errors < 0 || packet.Publication.TotalBytes < 0 || packet.Publication.StaleCount < 0 {
		return errors.New("publication counts must be non-negative")
	}
	if err := packet.Ingress.Validate(); err != nil {
		return err
	}
	if !routePublicationPrewriteItemsSorted(packet.Prewrite.Items) {
		return errors.New("prewrite items must be sorted")
	}
	for _, item := range packet.Prewrite.Items {
		if item.BytesToWrite < 0 {
			return errors.New("prewrite item bytes must be non-negative")
		}
	}
	if !routePublicationEvidenceIssuesSorted(packet.Issues) {
		return errors.New("issues must be sorted")
	}
	for _, issue := range packet.Issues {
		if strings.TrimSpace(issue.Source) == "" || strings.TrimSpace(issue.Code) == "" || issue.Count <= 0 {
			return errors.New("issues require source, code, and positive count")
		}
	}
	if !sort.StringsAreSorted(packet.NextActions) {
		return errors.New("next actions must be sorted")
	}
	return nil
}

func validRoutePublicationEvidenceStatus(status RoutePublicationEvidenceStatus) bool {
	switch status {
	case RoutePublicationEvidenceReady, RoutePublicationEvidenceNeedsReview, RoutePublicationEvidenceBlocked:
		return true
	default:
		return false
	}
}

func routePublicationPrewriteItemsSorted(items []RoutePublicationPrewriteItemEvidence) bool {
	return sort.SliceIsSorted(items, func(i, j int) bool {
		if items[i].Format != items[j].Format {
			return items[i].Format < items[j].Format
		}
		if items[i].Hash != items[j].Hash {
			return items[i].Hash < items[j].Hash
		}
		if items[i].Action != items[j].Action {
			return items[i].Action < items[j].Action
		}
		return items[i].BytesToWrite < items[j].BytesToWrite
	})
}

func routePublicationEvidenceIssuesSorted(issues []RoutePublicationEvidenceIssue) bool {
	return sort.SliceIsSorted(issues, func(i, j int) bool {
		if issues[i].Source != issues[j].Source {
			return issues[i].Source < issues[j].Source
		}
		return issues[i].Code < issues[j].Code
	})
}

func validateIngressEvidenceBundleJSONPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("ingress evidence bundle path is required")
	}
	if path != strings.TrimSpace(path) {
		return errors.New("ingress evidence bundle path must not have surrounding whitespace")
	}
	if strings.HasSuffix(path, "/") || strings.HasSuffix(path, `\`) {
		return errors.New("ingress evidence bundle path must be an explicit .json file")
	}
	cleaned := filepath.Clean(path)
	if cleaned != path {
		return errors.New("ingress evidence bundle path must be normalized")
	}
	base := filepath.Base(cleaned)
	if base == "." || base == ".." || base == ".json" || filepath.Ext(base) != ".json" {
		return errors.New("ingress evidence bundle path must end with an explicit .json file")
	}
	parent := filepath.Dir(cleaned)
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("ingress evidence bundle path parent must be a directory")
	}
	return nil
}
