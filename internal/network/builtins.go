package network

import (
	"sort"
	"strings"
)

// BuiltinsInput is the data shape required to compute Midnight's auto-
// injected MIDNIGHT_* environment variables. Callers populate every field
// they can; empty values are emitted as empty strings rather than skipped
// so user templates can rely on the variables existing.
type BuiltinsInput struct {
	ServiceSlug     string
	ProjectID       string
	ProjectSlug     string
	EnvironmentID   string
	EnvironmentName string
	PublicDomain    string
	ReplicaID       string
	DeploymentID    string
}

// Built-in env var names. The set mirrors Railway's RAILWAY_* surface so
// dev-portable templates can use `${{this.MIDNIGHT_PRIVATE_DOMAIN}}` and
// the reference-variables resolver track will resolve them naturally.
const (
	EnvServiceName     = "MIDNIGHT_SERVICE_NAME"
	EnvProjectID       = "MIDNIGHT_PROJECT_ID"
	EnvProjectSlug     = "MIDNIGHT_PROJECT_SLUG"
	EnvEnvironmentID   = "MIDNIGHT_ENVIRONMENT_ID"
	EnvEnvironmentName = "MIDNIGHT_ENVIRONMENT_NAME"
	EnvPrivateDomain   = "MIDNIGHT_PRIVATE_DOMAIN"
	EnvPublicDomain    = "MIDNIGHT_PUBLIC_DOMAIN"
	EnvReplicaID       = "MIDNIGHT_REPLICA_ID"
	EnvDeploymentID    = "MIDNIGHT_DEPLOYMENT_ID"
)

// BuiltinKeys returns the deterministic, sorted list of every built-in
// env var Midnight injects. Tests use this to assert no extra keys leak
// in via copy-paste errors.
func BuiltinKeys() []string {
	keys := []string{
		EnvServiceName,
		EnvProjectID,
		EnvProjectSlug,
		EnvEnvironmentID,
		EnvEnvironmentName,
		EnvPrivateDomain,
		EnvPublicDomain,
		EnvReplicaID,
		EnvDeploymentID,
	}
	sort.Strings(keys)
	return keys
}

// BuildBuiltins returns the sorted KEY=VALUE list of MIDNIGHT_* built-in
// env vars derived from the input. The result is deterministic in input
// fields so two callers with the same inputs produce byte-identical env
// slices — important for idempotency tests downstream.
//
// MIDNIGHT_PRIVATE_DOMAIN is computed from ServiceSlug rather than read
// from the input so the alias and the env var stay in sync.
func BuildBuiltins(in BuiltinsInput) []string {
	values := map[string]string{
		EnvServiceName:     strings.TrimSpace(in.ServiceSlug),
		EnvProjectID:       strings.TrimSpace(in.ProjectID),
		EnvProjectSlug:     strings.TrimSpace(in.ProjectSlug),
		EnvEnvironmentID:   strings.TrimSpace(in.EnvironmentID),
		EnvEnvironmentName: strings.TrimSpace(in.EnvironmentName),
		EnvPrivateDomain:   ServiceAlias(in.ServiceSlug),
		EnvPublicDomain:    strings.TrimSpace(in.PublicDomain),
		EnvReplicaID:       strings.TrimSpace(in.ReplicaID),
		EnvDeploymentID:    strings.TrimSpace(in.DeploymentID),
	}
	keys := BuiltinKeys()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+values[k])
	}
	return out
}

// MergeUserOverridesAfter takes a list of built-in env entries and a list
// of user-supplied env entries and returns the combined slice with the
// built-ins FIRST, then the user entries. Docker honors the last
// occurrence of each key, so this ordering lets user-supplied vars
// override MIDNIGHT_* defaults — matching Railway's precedence rule.
//
// Callers MUST place this combined slice before any other appended
// values (e.g., PORT) so the override semantics stay obvious.
func MergeUserOverridesAfter(builtins, userEnv []string) []string {
	out := make([]string, 0, len(builtins)+len(userEnv))
	out = append(out, builtins...)
	out = append(out, userEnv...)
	return out
}
