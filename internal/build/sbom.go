package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CycloneDXSpecVersion pins the CycloneDX SBOM spec version Lane 3 emits.
// 1.5 is the most recent CycloneDX revision Phase 1 is willing to commit to;
// later upgrades require a deliberate, audited change because external SBOM
// consumers parse spec-version-sensitively.
const CycloneDXSpecVersion = "1.5"

// CycloneDXSBOM is the small subset of CycloneDX 1.5 we emit. It is
// intentionally minimal: subject component (the produced image) plus
// pinned base-image components from the Dockerfile FROM lines. Operators
// who want a full BOM can replace the file via a custom builder.
type CycloneDXSBOM struct {
	BOMFormat    string               `json:"bomFormat"`
	SpecVersion  string               `json:"specVersion"`
	SerialNumber string               `json:"serialNumber,omitempty"`
	Version      int                  `json:"version"`
	Metadata     CycloneDXMetadata    `json:"metadata"`
	Components   []CycloneDXComponent `json:"components,omitempty"`
}

// CycloneDXMetadata identifies the produced subject artifact and the tool
// that emitted the SBOM.
type CycloneDXMetadata struct {
	Tools     []CycloneDXTool    `json:"tools,omitempty"`
	Component CycloneDXComponent `json:"component"`
}

// CycloneDXTool identifies the producer.
type CycloneDXTool struct {
	Vendor  string `json:"vendor,omitempty"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// CycloneDXComponent is one line in the SBOM.
type CycloneDXComponent struct {
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	Version string          `json:"version,omitempty"`
	PURL    string          `json:"purl,omitempty"`
	Hashes  []CycloneDXHash `json:"hashes,omitempty"`
}

// CycloneDXHash is the algorithm/value pair CycloneDX uses to record digests.
type CycloneDXHash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

// EmitCycloneDXSBOM produces a deterministic CycloneDX 1.5 SBOM for the
// supplied service+image+resolved-FROM set. It is safe to call before the
// final image digest is known: passing an empty digest omits the subject
// hash. Two calls with equal inputs produce byte-equal outputs (no random
// serialNumber, no timestamps).
func EmitCycloneDXSBOM(serviceID, image, digest string, froms []DockerfileFromDigest) ([]byte, error) {
	if strings.TrimSpace(image) == "" {
		return nil, errors.New("sbom: image reference is required")
	}
	if strings.TrimSpace(serviceID) == "" {
		return nil, errors.New("sbom: service id is required")
	}

	subject := CycloneDXComponent{
		Type:   "container",
		Name:   image,
		PURL:   purlForImage(image, digest),
		Hashes: hashesForDigest(digest),
	}
	bom := CycloneDXSBOM{
		BOMFormat:   "CycloneDX",
		SpecVersion: CycloneDXSpecVersion,
		Version:     1,
		Metadata: CycloneDXMetadata{
			Tools: []CycloneDXTool{{
				Vendor:  "midnight",
				Name:    "midnight-buildkit",
				Version: RequiredBuildKitVersion,
			}},
			Component: subject,
		},
	}

	deps := make([]CycloneDXComponent, 0, len(froms))
	for _, from := range froms {
		comp := CycloneDXComponent{
			Type: "container",
			Name: from.Image,
			PURL: purlForImage(from.Image, from.Digest),
		}
		if from.Digest != "" {
			comp.Hashes = hashesForDigest(from.Digest)
		}
		deps = append(deps, comp)
	}
	sort.SliceStable(deps, func(i, j int) bool {
		if deps[i].Name == deps[j].Name {
			return deps[i].PURL < deps[j].PURL
		}
		return deps[i].Name < deps[j].Name
	})
	bom.Components = deps

	// Synthesize a stable serialNumber from the inputs so different builds
	// produce different SBOMs while two identical builds produce identical
	// SBOMs (CycloneDX requires "urn:uuid:<uuid>", but tools accept a
	// content-derived UUID; stable inputs produce stable URN).
	bom.SerialNumber = stableURN(serviceID, image, digest, froms)

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(bom); err != nil {
		return nil, fmt.Errorf("sbom: encode CycloneDX: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func hashesForDigest(digest string) []CycloneDXHash {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return nil
	}
	if strings.HasPrefix(digest, "sha256:") {
		return []CycloneDXHash{{Alg: "SHA-256", Content: strings.TrimPrefix(digest, "sha256:")}}
	}
	return []CycloneDXHash{{Alg: "SHA-256", Content: digest}}
}

func purlForImage(image, digest string) string {
	purl := "pkg:oci/" + image
	if digest != "" {
		if !strings.HasPrefix(digest, "sha256:") {
			digest = "sha256:" + digest
		}
		purl += "@" + digest
	}
	return purl
}

func stableURN(serviceID, image, digest string, froms []DockerfileFromDigest) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(serviceID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(image))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(digest))
	_, _ = hash.Write([]byte{0})
	for _, from := range froms {
		_, _ = hash.Write([]byte(from.Image))
		_, _ = hash.Write([]byte{1})
		_, _ = hash.Write([]byte(from.Digest))
		_, _ = hash.Write([]byte{2})
	}
	sum := hash.Sum(nil)
	hexstr := hex.EncodeToString(sum[:16])
	return "urn:uuid:" + hexstr[0:8] + "-" + hexstr[8:12] + "-" + hexstr[12:16] + "-" + hexstr[16:20] + "-" + hexstr[20:32]
}
