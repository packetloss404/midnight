// Package version centralizes build and compatibility version metadata.
package version

import "strings"

const DevelopmentVersion = "0.0.0-dev"

// Version, Commit, and Date are replaced at build time with linker -X flags.
// Keep these as variables: constants and variables without a string initializer
// cannot be injected by the Go linker.
var (
	Version = DevelopmentVersion
	Commit  = "unknown"
	Date    = "unknown"
)

const APIVersion = "v1alpha"

const SchemaVersion = 1

// Metadata is the immutable build identity presented by a running binary.
// Version remains available as a package variable for existing consumers;
// Current provides the complete linker-injected identity to new callers.
type Metadata struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Current returns a snapshot of the build metadata.
func Current() Metadata {
	return Metadata{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
	}
}

// IsDevelopment reports whether the metadata still carries a development
// version and therefore must not be published as a release artifact.
func (m Metadata) IsDevelopment() bool {
	value := strings.ToLower(strings.TrimSpace(m.Version))
	return value == "" || value == "(devel)" || strings.Contains(value, "-dev")
}

type SchemaVersionDTO struct {
	SchemaVersion int    `json:"schema_version"`
	APIVersion    string `json:"api_version"`
}

type ComponentCompatibility struct {
	Component string `json:"component"`
	Minimum   string `json:"minimum"`
	Maximum   string `json:"maximum,omitempty"`
}

type CompatibilityMatrixRow struct {
	Capability string `json:"capability"`
	Component  string `json:"component"`
	Minimum    string `json:"minimum"`
	Maximum    string `json:"maximum,omitempty"`
	Status     string `json:"status"`
}

type Compatibility struct {
	ServerVersion string                   `json:"server_version"`
	BuildCommit   string                   `json:"build_commit,omitempty"`
	BuildDate     string                   `json:"build_date,omitempty"`
	APIVersion    string                   `json:"api_version"`
	SchemaVersion int                      `json:"schema_version"`
	Components    []ComponentCompatibility `json:"components"`
	Matrix        []CompatibilityMatrixRow `json:"matrix,omitempty"`
}

func Compat() Compatibility {
	return Compatibility{
		ServerVersion: Version,
		BuildCommit:   Commit,
		BuildDate:     Date,
		APIVersion:    APIVersion,
		SchemaVersion: SchemaVersion,
		Components: []ComponentCompatibility{
			{Component: "server", Minimum: Version, Maximum: Version},
			{Component: "agent", Minimum: Version, Maximum: Version},
			{Component: "worker", Minimum: Version, Maximum: Version},
			{Component: "scheduler", Minimum: Version, Maximum: Version},
		},
		Matrix: []CompatibilityMatrixRow{
			{Capability: "stateless-multihost-scheduling", Component: "scheduler", Minimum: Version, Maximum: Version, Status: "planning-only"},
			{Capability: "agent-canary-rollout", Component: "agent", Minimum: Version, Maximum: Version, Status: "planning-only"},
		},
	}
}

func Schema() SchemaVersionDTO {
	return SchemaVersionDTO{SchemaVersion: SchemaVersion, APIVersion: APIVersion}
}
