package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

const DeploymentSnapshotSchemaVersion = "midnight.dev/deployment-snapshot/v1alpha1"

const (
	InTotoStatementType       = "https://in-toto.io/Statement/v1"
	SLSAProvenancePredicateV1 = "https://slsa.dev/provenance/v1"
)

// DeploymentSnapshot records the immutable inputs needed to redeploy by digest.
type DeploymentSnapshot struct {
	SchemaVersion string
	CreatedAt     time.Time
	Build         BuildSnapshot
	Registry      RegistrySnapshot
	Images        []ImageSnapshot
}

// BuildSnapshot captures the builder identity and build-time provenance refs
// stored alongside a deployment snapshot.
type BuildSnapshot struct {
	BuilderID        string
	BuildKitVersion  string
	SourceRef        string
	ProvenanceDigest string
	ProvenanceRef    ArtifactRef
	SBOMRef          ArtifactRef
}

// ArtifactRef identifies an immutable build artifact such as provenance or an SBOM.
type ArtifactRef struct {
	Name      string `json:"name"`
	Digest    string `json:"digest"`
	MediaType string `json:"mediaType,omitempty"`
}

// ProvenanceStatement is the Phase 0 in-toto/SLSA v1.0 provenance shape. The
// JSON encoding uses stable key ordering so two equivalent statements always
// hash identically.
type ProvenanceStatement struct {
	Type          string              `json:"_type"`
	Subject       []ProvenanceSubject `json:"subject"`
	PredicateType string              `json:"predicateType"`
	Predicate     SLSAProvenanceV1    `json:"predicate"`
}

// ProvenanceSubject names the artifact a provenance statement attests.
type ProvenanceSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// SLSAProvenanceV1 is the SLSA v1.0 predicate body used by Midnight's
// generated provenance statements.
type SLSAProvenanceV1 struct {
	BuildDefinition SLSABuildDefinition `json:"buildDefinition"`
	RunDetails      SLSARunDetails      `json:"runDetails"`
}

// SLSABuildDefinition carries the SLSA buildDefinition predicate fields.
type SLSABuildDefinition struct {
	BuildType            string                 `json:"buildType"`
	ExternalParameters   map[string]interface{} `json:"externalParameters,omitempty"`
	InternalParameters   map[string]interface{} `json:"internalParameters,omitempty"`
	ResolvedDependencies []SLSAResourceRef      `json:"resolvedDependencies,omitempty"`
}

// SLSAResourceRef refers to a resolved dependency (typically a base image).
type SLSAResourceRef struct {
	URI    string            `json:"uri,omitempty"`
	Digest map[string]string `json:"digest,omitempty"`
}

// SLSARunDetails carries the SLSA runDetails predicate fields.
type SLSARunDetails struct {
	Builder  SLSABuilder       `json:"builder"`
	Metadata SLSABuildMetadata `json:"metadata,omitempty"`
}

// SLSABuilder identifies the builder that produced a provenance statement.
type SLSABuilder struct {
	ID string `json:"id"`
}

// SLSABuildMetadata carries optional per-invocation metadata.
type SLSABuildMetadata struct {
	InvocationID string    `json:"invocationID,omitempty"`
	StartedOn    time.Time `json:"startedOn,omitempty"`
	FinishedOn   time.Time `json:"finishedOn,omitempty"`
}

// NewProvenanceStatement returns a minimal, valid in-toto/SLSA v1.0
// provenance statement. ResolvedDependencies and metadata are left empty;
// callers fill them in via the helper setters below.
func NewProvenanceStatement(subjectName, subjectDigest, builderID, buildType string) ProvenanceStatement {
	return ProvenanceStatement{
		Type:          InTotoStatementType,
		PredicateType: SLSAProvenancePredicateV1,
		Subject: []ProvenanceSubject{{
			Name:   subjectName,
			Digest: sha256DigestMap(subjectDigest),
		}},
		Predicate: SLSAProvenanceV1{
			BuildDefinition: SLSABuildDefinition{BuildType: buildType},
			RunDetails:      SLSARunDetails{Builder: SLSABuilder{ID: builderID}},
		},
	}
}

// WithResolvedDependencies appends sorted resolved-dependency entries to the
// statement's buildDefinition. The returned statement is a copy.
func (s ProvenanceStatement) WithResolvedDependencies(deps []SLSAResourceRef) ProvenanceStatement {
	out := s
	resolved := append([]SLSAResourceRef(nil), deps...)
	sort.SliceStable(resolved, func(i, j int) bool {
		return resolved[i].URI < resolved[j].URI
	})
	out.Predicate.BuildDefinition.ResolvedDependencies = resolved
	return out
}

// WithMetadata sets the SLSA runDetails metadata fields. The returned
// statement is a copy.
func (s ProvenanceStatement) WithMetadata(meta SLSABuildMetadata) ProvenanceStatement {
	out := s
	out.Predicate.RunDetails.Metadata = meta
	return out
}

// Validate enforces the structural rules a provenance statement must satisfy
// for Phase 0: required type, predicate type, builder id, and at least one
// subject with a sha256 digest.
func (s ProvenanceStatement) Validate() error {
	if s.Type != InTotoStatementType {
		return fmt.Errorf("provenance statement _type must be %q", InTotoStatementType)
	}
	if s.PredicateType != SLSAProvenancePredicateV1 {
		return fmt.Errorf("provenance statement predicateType must be %q", SLSAProvenancePredicateV1)
	}
	if len(s.Subject) == 0 {
		return errors.New("provenance statement subject must include at least one entry")
	}
	for _, subject := range s.Subject {
		if subject.Name == "" {
			return errors.New("provenance subject name must not be empty")
		}
		digest, ok := subject.Digest["sha256"]
		if !ok || digest == "" {
			return fmt.Errorf("provenance subject %q must include a sha256 digest", subject.Name)
		}
	}
	if s.Predicate.RunDetails.Builder.ID == "" {
		return errors.New("provenance builder id must not be empty")
	}
	if s.Predicate.BuildDefinition.BuildType == "" {
		return errors.New("provenance buildType must not be empty")
	}
	return nil
}

// MarshalProvenanceStatementJSON returns a stable, human-readable JSON
// encoding of the supplied statement. It validates first and uses sorted
// digest map keys so two equivalent statements always serialize to the same
// bytes.
func MarshalProvenanceStatementJSON(statement ProvenanceStatement) ([]byte, error) {
	if err := statement.Validate(); err != nil {
		return nil, err
	}
	encoded := normalizeProvenanceStatement(statement)
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(encoded); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// UnmarshalProvenanceStatementJSON decodes and validates a provenance JSON
// document. Round-tripping a marshaled statement through this function must
// return a value byte-equal to the original.
func UnmarshalProvenanceStatementJSON(data []byte) (ProvenanceStatement, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var statement ProvenanceStatement
	if err := decoder.Decode(&statement); err != nil {
		return ProvenanceStatement{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("provenance JSON must contain a single object")
		}
		return ProvenanceStatement{}, err
	}
	if err := statement.Validate(); err != nil {
		return ProvenanceStatement{}, err
	}
	return statement, nil
}

func normalizeProvenanceStatement(statement ProvenanceStatement) ProvenanceStatement {
	out := statement
	out.Subject = make([]ProvenanceSubject, len(statement.Subject))
	for i, subject := range statement.Subject {
		out.Subject[i] = ProvenanceSubject{
			Name:   subject.Name,
			Digest: sortedDigestMap(subject.Digest),
		}
	}
	deps := make([]SLSAResourceRef, len(statement.Predicate.BuildDefinition.ResolvedDependencies))
	for i, dep := range statement.Predicate.BuildDefinition.ResolvedDependencies {
		deps[i] = SLSAResourceRef{URI: dep.URI, Digest: sortedDigestMap(dep.Digest)}
	}
	sort.SliceStable(deps, func(i, j int) bool {
		return deps[i].URI < deps[j].URI
	})
	out.Predicate.BuildDefinition.ResolvedDependencies = deps
	return out
}

func sortedDigestMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(in))
	for _, k := range keys {
		out[k] = in[k]
	}
	return out
}

// RegistrySnapshot is the snapshot's view of the embedded registry endpoint
// and namespace where the produced image lives.
type RegistrySnapshot struct {
	Endpoint  string
	Namespace string
}

// ImageSnapshot pairs a built image's coordinates with its produced digest
// and an optional rollback reference.
type ImageSnapshot struct {
	Name        string
	Tag         string
	Digest      string
	Built       bool
	RollbackRef string
}

// ImagePin is the immutable deployment reference used for no-build redeploys.
type ImagePin struct {
	Name        string
	Tag         string
	Digest      string
	RollbackRef string
}

// RollbackTargetKind classifies the user-supplied rollback selector.
type RollbackTargetKind string

const (
	// RollbackPrevious selects the snapshot just before the current
	// deployment.
	RollbackPrevious RollbackTargetKind = "previous"
	// RollbackToDeployment selects an explicit deployment id.
	RollbackToDeployment RollbackTargetKind = "to-deployment"
	// RollbackToRevision selects an explicit revision string.
	RollbackToRevision RollbackTargetKind = "to-revision"
)

// DeploymentSnapshotRecord pairs a snapshot with its addressable
// deployment+revision identifiers.
type DeploymentSnapshotRecord struct {
	Deployment string
	Revision   string
	Snapshot   DeploymentSnapshot
}

// RejectSnapshotMutation is the canonical refusal raised by any caller that
// receives a write request against an existing deployment snapshot row.
// Snapshots are immutable by panel decision: rolling back creates a NEW
// deployment row that pins the same digest, never an in-place edit. Callers
// (the API write path, audit replay tools) must surface this exact failure
// code so operators get a consistent recovery hint.
func RejectSnapshotMutation(deployment, field string) error {
	return failurecodes.New(failurecodes.SnapshotImmutable,
		fmt.Sprintf("deployment snapshot %q is immutable; refused mutation of field %q", deployment, field))
}

// RollbackTarget is the parsed rollback selector understood by
// SelectRollbackSnapshot and the planner.
type RollbackTarget struct {
	Kind       RollbackTargetKind
	Deployment string
	Revision   string
}

// SelectRollbackSnapshot resolves a rollback target against a deployment
// history. It returns the matched record and a boolean ok flag.
func SelectRollbackSnapshot(history []DeploymentSnapshotRecord, target RollbackTarget) (DeploymentSnapshotRecord, bool) {
	switch target.Kind {
	case RollbackPrevious:
		return selectPreviousRollbackSnapshot(history, target.Deployment)
	case RollbackToDeployment:
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Deployment == target.Deployment {
				return history[i], true
			}
		}
	case RollbackToRevision:
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Revision == target.Revision {
				return history[i], true
			}
		}
	}

	return DeploymentSnapshotRecord{}, false
}

func selectPreviousRollbackSnapshot(history []DeploymentSnapshotRecord, deployment string) (DeploymentSnapshotRecord, bool) {
	seenCurrent := false
	for i := len(history) - 1; i >= 0; i-- {
		if deployment != "" && history[i].Deployment != deployment {
			continue
		}
		if seenCurrent {
			return history[i], true
		}
		seenCurrent = true
	}

	return DeploymentSnapshotRecord{}, false
}

// RequiresBuild reports whether the snapshot lacks digest pins for any image.
func (s DeploymentSnapshot) RequiresBuild() bool {
	for _, image := range s.Images {
		if image.Digest == "" {
			return true
		}
	}

	return false
}

// ImagePins returns digest pins for images that can be redeployed without a build.
func (s DeploymentSnapshot) ImagePins() []ImagePin {
	pins := make([]ImagePin, 0, len(s.Images))
	for _, image := range s.Images {
		if image.Digest == "" {
			continue
		}

		pins = append(pins, ImagePin{
			Name:        image.Name,
			Tag:         image.Tag,
			Digest:      image.Digest,
			RollbackRef: image.RollbackRef,
		})
	}

	return pins
}

// ValidateDraft enforces the structural rules a build planner must satisfy
// before BuildKit is invoked. It does not require all snapshot fields to be
// populated; that happens after the build completes.
func (s DeploymentSnapshot) ValidateDraft() error {
	if s.SchemaVersion != "" && s.SchemaVersion != DeploymentSnapshotSchemaVersion {
		return fmt.Errorf("deployment snapshot schema version %q is not supported", s.SchemaVersion)
	}
	if s.Build.ProvenanceDigest != "" && !isSHA256Digest(s.Build.ProvenanceDigest) {
		return fmt.Errorf("deployment snapshot provenance digest must be a sha256 digest")
	}
	if err := validateArtifactRef("provenance", s.Build.ProvenanceRef); err != nil {
		return err
	}
	if err := validateArtifactRef("sbom", s.Build.SBOMRef); err != nil {
		return err
	}

	return nil
}

// SecretFingerprint returns a stable digest of a build secret without retaining
// the secret value itself.
func SecretFingerprint(name string, secret []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(name))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(secret)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func validateArtifactRef(name string, ref ArtifactRef) error {
	if ref.Name == "" && ref.Digest == "" && ref.MediaType == "" {
		return nil
	}
	if !isSHA256Digest(ref.Digest) {
		return fmt.Errorf("deployment snapshot %s ref digest must be a sha256 digest", name)
	}
	return nil
}

func isSHA256Digest(digest string) bool {
	return strings.HasPrefix(digest, "sha256:")
}

func sha256DigestMap(digest string) map[string]string {
	if strings.HasPrefix(digest, "sha256:") {
		return map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}
	}
	return map[string]string{"sha256": digest}
}
