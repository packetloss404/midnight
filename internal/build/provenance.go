package build

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Statement is the package-level alias for an in-toto/SLSA v1.0 statement
// produced by BuildStatement. It carries stable JSON key ordering so the
// generator's output is deterministic across runs.
type Statement = ProvenanceStatement

// BuildSubject names the artifact a provenance statement is about. Phase 0
// callers always use a single subject (the produced image).
type BuildSubject struct {
	// Name is the subject name. Conventionally this is the OCI image
	// reference; SLSA permits any non-empty string.
	Name string
	// Digest is the produced artifact's sha256 digest. The value may be
	// supplied with or without the "sha256:" prefix; the generator
	// normalizes either form into a {sha256: <hex>} digest map.
	Digest string
}

// BuildPredicate is the locked SLSA v1.0 predicate body. The fields mirror
// the panel-locked snapshot fields: builder identity, buildType, invocation
// metadata, and resolved materials (typically resolved base images).
type BuildPredicate struct {
	Builder    BuildBuilder
	BuildType  string
	Invocation BuildInvocation
	Materials  []BuildMaterial
}

// BuildBuilder identifies the builder that produced the statement.
type BuildBuilder struct {
	ID string
}

// BuildInvocation records the per-build invocation metadata Midnight stores
// alongside the deployment snapshot.
type BuildInvocation struct {
	InvocationID string
	StartedOn    time.Time
	FinishedOn   time.Time
}

// BuildMaterial is one resolved-dependency entry. URI is the original
// reference (for example a base image tag) and Digest is the sha256 digest
// the build actually consumed.
type BuildMaterial struct {
	URI    string
	Digest string
}

// BuildStatement returns a normalized, validated in-toto/SLSA v1.0 provenance
// statement for the supplied subject and predicate. The returned statement
// has stable key ordering and round-trips through
// MarshalProvenanceStatementJSON / UnmarshalProvenanceStatementJSON.
func BuildStatement(subject BuildSubject, predicate BuildPredicate) (Statement, error) {
	subjectName := strings.TrimSpace(subject.Name)
	if subjectName == "" {
		return Statement{}, errors.New("provenance subject name must not be empty")
	}
	digest := strings.TrimSpace(subject.Digest)
	if digest == "" {
		return Statement{}, errors.New("provenance subject digest must not be empty")
	}
	if !strings.HasPrefix(digest, "sha256:") {
		digest = "sha256:" + digest
	}

	builderID := strings.TrimSpace(predicate.Builder.ID)
	if builderID == "" {
		return Statement{}, errors.New("provenance builder id must not be empty")
	}
	buildType := strings.TrimSpace(predicate.BuildType)
	if buildType == "" {
		return Statement{}, errors.New("provenance buildType must not be empty")
	}

	stmt := NewProvenanceStatement(subjectName, digest, builderID, buildType)

	if err := validateBuildInvocation(predicate.Invocation); err != nil {
		return Statement{}, err
	}
	if predicate.Invocation.InvocationID != "" || !predicate.Invocation.StartedOn.IsZero() || !predicate.Invocation.FinishedOn.IsZero() {
		stmt = stmt.WithMetadata(SLSABuildMetadata{
			InvocationID: predicate.Invocation.InvocationID,
			StartedOn:    predicate.Invocation.StartedOn.UTC(),
			FinishedOn:   predicate.Invocation.FinishedOn.UTC(),
		})
	}

	if len(predicate.Materials) > 0 {
		deps, err := buildResolvedDependencies(predicate.Materials)
		if err != nil {
			return Statement{}, err
		}
		stmt = stmt.WithResolvedDependencies(deps)
	}

	if err := stmt.Validate(); err != nil {
		return Statement{}, err
	}
	return stmt, nil
}

func buildResolvedDependencies(materials []BuildMaterial) ([]SLSAResourceRef, error) {
	out := make([]SLSAResourceRef, 0, len(materials))
	for i, material := range materials {
		uri := strings.TrimSpace(material.URI)
		digest := strings.TrimSpace(material.Digest)
		if uri == "" && digest == "" {
			return nil, fmt.Errorf("provenance material %d must include a uri or digest", i)
		}
		ref := SLSAResourceRef{URI: uri}
		if digest != "" {
			normalized := digest
			if !strings.HasPrefix(normalized, "sha256:") {
				normalized = "sha256:" + normalized
			}
			ref.Digest = sha256DigestMap(normalized)
		}
		out = append(out, ref)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].URI != out[j].URI {
			return out[i].URI < out[j].URI
		}
		return out[i].Digest["sha256"] < out[j].Digest["sha256"]
	})
	return out, nil
}

func validateBuildInvocation(inv BuildInvocation) error {
	if inv.StartedOn.IsZero() && !inv.FinishedOn.IsZero() {
		return errors.New("provenance invocation finishedOn requires startedOn")
	}
	if !inv.StartedOn.IsZero() && !inv.FinishedOn.IsZero() && inv.FinishedOn.Before(inv.StartedOn) {
		return errors.New("provenance invocation finishedOn must not precede startedOn")
	}
	return nil
}
