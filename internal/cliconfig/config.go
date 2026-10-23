// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package cliconfig owns the local state the `midnight` developer CLI reads
// and writes for `login` and `link`.
//
//   - ~/.midnight/config.json — global, per-api-base credential metadata.
//     Current files contain opaque secret references, never bearer tokens.
//   - the operating-system keyring — bearer-token storage used by default.
//   - .midnight/project.json — a per-cwd link record written by
//     `midnight link` so `midnight up` knows which project, environment,
//     and service to deploy to.
//
// Setting MIDNIGHT_CONFIG_HOME explicitly selects a headless/test fallback:
// secrets are stored in a separate owner-only file beneath that exact
// directory. This is intentionally not an automatic response to a keyring
// failure; normal desktop use fails closed instead of silently writing a
// plaintext bearer token. Legacy v1/v2 plaintext configs remain readable and
// migrate transactionally on their next successful mutation.
//
// Metadata writes are atomic (tmpfile + rename) and read-modify-write
// mutations hold a per-config cross-process lock. The package uses
// os.UserHomeDir, not $HOME, so Windows works.
package cliconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// GlobalConfigDirName is the directory under the operator's home dir that
// stores the user-level config.json.
const GlobalConfigDirName = ".midnight"

// GlobalConfigFileName is the filename of the user-level config.json.
const GlobalConfigFileName = "config.json"

// FallbackCredentialFileName is used only when MIDNIGHT_CONFIG_HOME is
// explicitly set. It contains secrets and must remain owner-readable only.
const FallbackCredentialFileName = "credentials.json"

// ProjectLinkDirName is the per-cwd directory `midnight link` creates.
const ProjectLinkDirName = ".midnight"

// ProjectLinkFileName is the per-cwd link file. It travels with the repo
// (so CI clones inherit the link) and uses the cwd as its scope (so a
// developer who clones the same repo twice gets two separate links).
const ProjectLinkFileName = "project.json"

const (
	// EnvAPIBase is the canonical CLI API endpoint override.
	EnvAPIBase = "MIDNIGHT_API_BASE"
	// EnvBaseURL is the compatibility alias used by older scripts.
	EnvBaseURL = "MIDNIGHT_BASE_URL"
	// EnvBearerToken is the Railway-shaped project/CI credential.
	EnvBearerToken = "MIDNIGHT_TOKEN"
	// EnvAPIToken is the compatibility alias used by older product commands.
	EnvAPIToken = "MIDNIGHT_API_TOKEN"
)

// Global is the user-level credential metadata file. Servers are keyed by API
// base URL so an operator can hold credentials for several Midnight servers.
type Global struct {
	Version int                    `json:"version"`
	Servers map[string]ServerEntry `json:"servers"`
	Updated time.Time              `json:"updated,omitempty"`
}

// CredentialKind prevents a durable human session from being confused with
// the installation-wide operator recovery credential.
type CredentialKind string

const (
	CredentialKindHumanSession     CredentialKind = "human_session"
	CredentialKindOperatorRecovery CredentialKind = "operator_recovery"
	CredentialKindExternal         CredentialKind = "external"
)

// CredentialSource records how a resolved credential entered the CLI. It is
// safe to display; it never contains credential material.
type CredentialSource string

const (
	CredentialSourceExplicit       CredentialSource = "explicit"
	CredentialSourceEnvironment    CredentialSource = "environment"
	CredentialSourceStoredSession  CredentialSource = "stored_human_session"
	CredentialSourceStoredRecovery CredentialSource = "stored_operator_recovery"
	CredentialSourceStoredLegacy   CredentialSource = "stored_legacy"
)

// APIBaseSource records which trust boundary supplied the resolved endpoint.
// In particular, a project link can be committed to a repository and is not
// trusted to select the destination for an ambient environment credential.
type APIBaseSource string

const (
	APIBaseSourceExplicit    APIBaseSource = "explicit"
	APIBaseSourceEnvironment APIBaseSource = "environment"
	APIBaseSourceProject     APIBaseSource = "project_link"
	APIBaseSourceStored      APIBaseSource = "stored_credential"
)

// HumanSessionMetadata is the non-secret subset of the login response needed
// for identity-aware status and remote logout.
type HumanSessionMetadata struct {
	SessionID   string    `json:"session_id,omitempty"`
	UserID      string    `json:"user_id,omitempty"`
	Email       string    `json:"email,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

// HumanSessionCredential is accepted by SetHumanSession. Token is persisted
// by the selected secret backend; metadata remains separately inspectable.
type HumanSessionCredential struct {
	Token string
	HumanSessionMetadata
}

// ServerEntry retains the plaintext fields in memory and for v1/v2 read
// compatibility. Current writes clear them and persist only CredentialRef and
// OperatorRecoveryCredentialRef. A human login moves any active non-human
// token into the recovery slot. DeleteHumanSession intentionally does not
// promote recovery into the default slot: that would silently elevate later
// developer commands.
type ServerEntry struct {
	APIBase                       string               `json:"api_base"`
	Token                         string               `json:"token,omitempty"`
	CredentialRef                 string               `json:"credential_ref,omitempty"`
	CredentialKind                CredentialKind       `json:"credential_kind,omitempty"`
	HumanSession                  HumanSessionMetadata `json:"human_session,omitempty"`
	OperatorRecoveryToken         string               `json:"operator_recovery_token,omitempty"`
	OperatorRecoveryCredentialRef string               `json:"operator_recovery_credential_ref,omitempty"`
	Updated                       time.Time            `json:"updated,omitempty"`
}

// ProjectLink is the v1 shape of the per-cwd link file.
type ProjectLink struct {
	Version         int       `json:"version"`
	APIBase         string    `json:"api_base"`
	ProjectID       string    `json:"project_id"`
	ProjectSlug     string    `json:"project_slug,omitempty"`
	EnvironmentID   string    `json:"environment_id"`
	EnvironmentName string    `json:"environment_name,omitempty"`
	ServiceID       string    `json:"service_id"`
	ServiceSlug     string    `json:"service_slug,omitempty"`
	Updated         time.Time `json:"updated,omitempty"`
}

// CurrentVersion is the global credential-config version stamped onto fresh
// writes.
const CurrentVersion = 3

// ProjectLinkCurrentVersion remains v1 because the per-project link schema did
// not change with the credential migration.
const ProjectLinkCurrentVersion = 1

var globalConfigWriteJSON = atomicWriteJSON

// MissingCredentialError identifies configuration that cannot be resolved
// without guessing. Callers can use errors.Is with ErrNoAPIBase or ErrNoToken
// and still surface the contextual message to the operator.
type MissingCredentialError struct {
	Kind    string
	APIBase string
	Reason  string
}

func (e *MissingCredentialError) Error() string {
	switch e.Kind {
	case "api_base":
		if e.Reason != "" {
			return "No Midnight server could be chosen: " + e.Reason + " (or set $" + EnvAPIBase + ")."
		}
		return "No Midnight server configured. Run `midnight login --api-base https://your-server` (or set " + EnvAPIBase + ")."
	case "token":
		if e.APIBase != "" {
			return fmt.Sprintf("no token for %s; pass --token, set $MIDNIGHT_TOKEN, or run `midnight login`", e.APIBase)
		}
		return "token is required; pass --token, set $MIDNIGHT_TOKEN, or run `midnight login`"
	default:
		return "required credential is missing"
	}
}

func (e *MissingCredentialError) Is(target error) bool {
	other, ok := target.(*MissingCredentialError)
	return ok && e.Kind == other.Kind
}

var (
	// ErrNotLinked is returned when LoadProjectLink finds no
	// .midnight/project.json under the supplied directory.
	ErrNotLinked = errors.New("This directory isn't linked to a service yet. Run `midnight link` (after `midnight login`).")
	// ErrNoAPIBase is returned when no endpoint can be resolved without
	// inventing or arbitrarily selecting one.
	ErrNoAPIBase = &MissingCredentialError{Kind: "api_base"}
	// ErrNoToken is returned when no explicit, environment, or stored token is
	// available for the resolved API base.
	ErrNoToken = &MissingCredentialError{Kind: "token"}
	// ErrNoHumanSession is returned when an endpoint has no locally stored
	// durable human session. Operator recovery credentials never satisfy it.
	ErrNoHumanSession = errors.New("no stored human session")
	// ErrNoOperatorRecovery is returned when no explicitly stored operator
	// recovery credential exists for an endpoint.
	ErrNoOperatorRecovery = errors.New("no stored operator recovery credential")
	// ErrUnsupportedGlobalConfigVersion prevents an older CLI from
	// destructively rewriting credential fields introduced by a newer one.
	ErrUnsupportedGlobalConfigVersion = errors.New("unsupported global config version")
	// ErrUnsupportedProjectLinkVersion prevents an older CLI from silently
	// interpreting a project-link schema introduced by a newer CLI.
	ErrUnsupportedProjectLinkVersion = errors.New("unsupported project link version")
	// ErrInvalidAPIBase identifies endpoints that cannot be used as an
	// authentication boundary. Error details intentionally never repeat the
	// supplied URL because it may contain userinfo or other secret material.
	ErrInvalidAPIBase = errors.New("invalid API base")
	// ErrUnsafeCredentialMix prevents repository-controlled project metadata
	// from choosing the host that receives an ambient CI or environment token.
	ErrUnsafeCredentialMix = errors.New("refusing environment token with project-link API base")
	// ErrCredentialStore identifies an OS keyring or explicitly selected
	// headless fallback failure. Its public text never includes secret material.
	ErrCredentialStore = errors.New("credential store unavailable")
	// ErrPlaintextCredentialInCurrentConfig rejects plaintext credential fields
	// in schema versions that require reference-only metadata.
	ErrPlaintextCredentialInCurrentConfig = errors.New("plaintext credential in current config")
)

// ResolveOptions are the explicit and project-scoped inputs to the shared CLI
// credential resolver. Environment variables and the per-server credential
// store are consulted internally so every caller observes the same order.
type ResolveOptions struct {
	APIBase        string
	Token          string
	ProjectAPIBase string
	// OperatorRecovery opts an explicitly operator-scoped command into the
	// separately stored recovery credential. Ordinary developer commands must
	// leave this false and never silently elevate.
	OperatorRecovery bool
}

// Credentials are a complete endpoint/token pair ready for an authenticated
// API request.
type Credentials struct {
	APIBase       string
	APIBaseSource APIBaseSource
	Token         string
	Kind          CredentialKind
	Source        CredentialSource
}

// GlobalConfigPath returns the absolute path to the user-level config
// file. It honours $MIDNIGHT_CONFIG_HOME for tests and air-gapped CI.
func GlobalConfigPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("MIDNIGHT_CONFIG_HOME")); override != "" {
		return filepath.Join(override, GlobalConfigFileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, GlobalConfigDirName, GlobalConfigFileName), nil
}

// LoadGlobal reads the user-level config file. A missing file is not an
// error and yields the zero value (so callers can write the first token
// without a pre-existing file).
func LoadGlobal() (Global, error) {
	path, err := GlobalConfigPath()
	if err != nil {
		return Global{}, err
	}
	return loadGlobalPath(path)
}

func loadGlobalPath(path string) (Global, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Global{Version: CurrentVersion, Servers: map[string]ServerEntry{}}, nil
	}
	if err != nil {
		return Global{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Global
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Global{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Version == 0 {
		// Pre-versioned files have the same token tuple as v1 and migrate
		// through the classification below.
		cfg.Version = 1
	}
	if cfg.Version > CurrentVersion {
		return Global{}, fmt.Errorf(
			"%w: found version %d, this CLI supports through version %d",
			ErrUnsupportedGlobalConfigVersion,
			cfg.Version,
			CurrentVersion,
		)
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]ServerEntry{}
	}
	normalizedServers := make(map[string]ServerEntry, len(cfg.Servers))
	_, credentialRefPrefix, backendErr := selectedSecretBackend()
	if backendErr != nil {
		return Global{}, backendErr
	}
	for key, entry := range cfg.Servers {
		apiBase, err := ValidateAPIBase(firstNonEmpty(entry.APIBase, key))
		if err != nil {
			return Global{}, fmt.Errorf("invalid stored server endpoint: %w", err)
		}
		entry.APIBase = apiBase
		entry.Token = strings.TrimSpace(entry.Token)
		entry.OperatorRecoveryToken = strings.TrimSpace(entry.OperatorRecoveryToken)
		entry.CredentialRef = strings.TrimSpace(entry.CredentialRef)
		entry.OperatorRecoveryCredentialRef = strings.TrimSpace(entry.OperatorRecoveryCredentialRef)
		if cfg.Version >= CurrentVersion && (entry.Token != "" || entry.OperatorRecoveryToken != "") {
			return Global{}, fmt.Errorf("%w: server metadata must contain only credential references", ErrPlaintextCredentialInCurrentConfig)
		}
		if cfg.Version >= CurrentVersion {
			if entry.CredentialRef != "" && entry.CredentialRef != credentialReference(apiBase, "default", credentialRefPrefix) {
				return Global{}, fmt.Errorf("%w: invalid default credential reference", ErrCredentialStore)
			}
			if entry.OperatorRecoveryCredentialRef != "" && entry.OperatorRecoveryCredentialRef != credentialReference(apiBase, "recovery", credentialRefPrefix) {
				return Global{}, fmt.Errorf("%w: invalid recovery credential reference", ErrCredentialStore)
			}
		}
		if entry.CredentialKind == "" && entry.Token != "" {
			entry.CredentialKind = classifyStoredToken(entry.Token)
		}
		if _, exists := normalizedServers[apiBase]; exists {
			return Global{}, errors.New("multiple stored server entries normalize to the same API base")
		}
		normalizedServers[apiBase] = entry
	}
	cfg.Servers = normalizedServers
	return cfg, nil
}

// SaveGlobal transactionally migrates legacy plaintext fields to the selected
// secret backend, then atomically writes reference-only metadata. The parent
// directory is mode 0700 and the metadata file is mode 0600.
func SaveGlobal(cfg Global) error {
	path, err := GlobalConfigPath()
	if err != nil {
		return err
	}
	return withConfigLock(path, func() error {
		if cfg.Version > CurrentVersion {
			return fmt.Errorf(
				"%w: refusing to replace version %d with version %d",
				ErrUnsupportedGlobalConfigVersion,
				cfg.Version,
				CurrentVersion,
			)
		}
		if cfg.Version >= CurrentVersion {
			for _, entry := range cfg.Servers {
				if entry.Token != "" || entry.OperatorRecoveryToken != "" {
					return fmt.Errorf("%w: use credential references", ErrPlaintextCredentialInCurrentConfig)
				}
			}
		}
		normalizedServers := make(map[string]ServerEntry, len(cfg.Servers))
		for key, entry := range cfg.Servers {
			apiBase, err := ValidateAPIBase(firstNonEmpty(entry.APIBase, key))
			if err != nil {
				return fmt.Errorf("invalid stored server endpoint: %w", err)
			}
			if _, exists := normalizedServers[apiBase]; exists {
				return errors.New("multiple stored server entries normalize to the same API base")
			}
			entry.APIBase = apiBase
			normalizedServers[apiBase] = entry
		}
		cfg.Servers = normalizedServers
		backend, prefix, err := selectedSecretBackend()
		if err != nil {
			return err
		}
		stages := make([]stagedSecret, 0, len(cfg.Servers)*2)
		if err := migrateLegacyEntries(&cfg, backend, prefix, &stages); err != nil {
			rollbackStagedSecrets(stages)
			return err
		}
		if err := saveGlobalPath(path, cfg); err != nil {
			rollbackStagedSecrets(stages)
			return fmt.Errorf("save credential metadata: %v", redactCredentialError(err))
		}
		return nil
	})
}

func saveGlobalPath(path string, cfg Global) error {
	if cfg.Version > CurrentVersion {
		return fmt.Errorf(
			"%w: refusing to replace version %d with version %d",
			ErrUnsupportedGlobalConfigVersion,
			cfg.Version,
			CurrentVersion,
		)
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]ServerEntry{}
	}
	normalizedServers := make(map[string]ServerEntry, len(cfg.Servers))
	for key, entry := range cfg.Servers {
		apiBase, err := ValidateAPIBase(firstNonEmpty(entry.APIBase, key))
		if err != nil {
			return fmt.Errorf("invalid stored server endpoint: %w", err)
		}
		if _, exists := normalizedServers[apiBase]; exists {
			return errors.New("multiple stored server entries normalize to the same API base")
		}
		entry.APIBase = apiBase
		if strings.TrimSpace(entry.Token) != "" || strings.TrimSpace(entry.OperatorRecoveryToken) != "" {
			return fmt.Errorf("%w: use SetToken, SetHumanSession, or SetOperatorRecoveryToken", ErrPlaintextCredentialInCurrentConfig)
		}
		normalizedServers[apiBase] = entry
	}
	cfg.Servers = normalizedServers
	cfg.Version = CurrentVersion
	cfg.Updated = time.Now().UTC()
	return globalConfigWriteJSON(path, cfg, 0o600, 0o700)
}

// SetToken upserts the active token for the supplied API base. It is retained
// for older package callers; new identity-aware code should use
// SetHumanSession or SetOperatorRecoveryToken. Auxiliary split-credential
// fields are preserved so a legacy caller cannot erase a separately stored
// recovery credential.
func SetToken(apiBase, token string) error {
	normalized, err := ValidateAPIBase(apiBase)
	if err != nil {
		return err
	}
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken == "" {
		return errors.New("token is required")
	}
	return mutateGlobal(func(cfg *Global, backend secretBackend, prefix string, stages *[]stagedSecret) error {
		entry := cfg.Servers[normalized]
		previousToken, previousErr := credentialForEntry(entry, false)
		if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
			return previousErr
		}
		nextKind := classifyStoredToken(trimmedToken)
		ref := credentialReference(normalized, "default", prefix)
		stage, err := stageSecret(backend, ref, trimmedToken)
		if err != nil {
			return err
		}
		*stages = append(*stages, stage)
		entry.APIBase = normalized
		entry.Token = ""
		entry.CredentialRef = ref
		entry.CredentialKind = nextKind
		if nextKind != CredentialKindHumanSession || previousToken != trimmedToken {
			entry.HumanSession = HumanSessionMetadata{}
		}
		entry.Updated = time.Now().UTC()
		cfg.Servers[normalized] = entry
		return nil
	})
}

// SetHumanSession stores a durable user session without overwriting a
// previously active operator credential. The latter is moved into the
// separately addressable recovery slot.
func SetHumanSession(apiBase string, credential HumanSessionCredential) error {
	normalized, err := ValidateAPIBase(apiBase)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(credential.Token)
	if !isHumanSessionToken(token) {
		return errors.New("human session token must use the midn_user_ credential class")
	}
	return mutateGlobal(func(cfg *Global, backend secretBackend, prefix string, stages *[]stagedSecret) error {
		entry := cfg.Servers[normalized]
		current, currentErr := credentialForEntry(entry, false)
		if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
			return currentErr
		}
		if current != "" && entry.CredentialKind != CredentialKindHumanSession && !isHumanSessionToken(current) {
			recoveryRef := credentialReference(normalized, "recovery", prefix)
			stage, err := stageSecret(backend, recoveryRef, current)
			if err != nil {
				return err
			}
			*stages = append(*stages, stage)
			entry.OperatorRecoveryCredentialRef = recoveryRef
			entry.OperatorRecoveryToken = ""
		}
		ref := credentialReference(normalized, "default", prefix)
		stage, err := stageSecret(backend, ref, token)
		if err != nil {
			return err
		}
		*stages = append(*stages, stage)
		entry.APIBase = normalized
		entry.Token = ""
		entry.CredentialRef = ref
		entry.CredentialKind = CredentialKindHumanSession
		entry.HumanSession = credential.HumanSessionMetadata
		entry.Updated = time.Now().UTC()
		cfg.Servers[normalized] = entry
		return nil
	})
}

// SetOperatorRecoveryToken stores the installation-wide recovery credential
// separately when a human session is active. It is selected only by an
// operator-scoped resolver.
func SetOperatorRecoveryToken(apiBase, token string) error {
	normalized, err := ValidateAPIBase(apiBase)
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("operator recovery token is required")
	}
	if isHumanSessionToken(token) {
		return errors.New("a human session cannot be stored as operator recovery")
	}
	return mutateGlobal(func(cfg *Global, backend secretBackend, prefix string, stages *[]stagedSecret) error {
		entry := cfg.Servers[normalized]
		entry.APIBase = normalized
		current, currentErr := credentialForEntry(entry, false)
		if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
			return currentErr
		}
		slot := "default"
		if entry.CredentialKind == CredentialKindHumanSession || isHumanSessionToken(current) {
			slot = "recovery"
		}
		ref := credentialReference(normalized, slot, prefix)
		stage, err := stageSecret(backend, ref, token)
		if err != nil {
			return err
		}
		*stages = append(*stages, stage)
		if slot == "recovery" {
			entry.OperatorRecoveryToken = ""
			entry.OperatorRecoveryCredentialRef = ref
		} else {
			entry.Token = ""
			entry.CredentialRef = ref
			entry.CredentialKind = CredentialKindOperatorRecovery
			entry.HumanSession = HumanSessionMetadata{}
		}
		entry.Updated = time.Now().UTC()
		cfg.Servers[normalized] = entry
		return nil
	})
}

// HumanSession returns the stored human credential for an API base. Recovery
// and externally supplied credentials never satisfy this lookup.
func HumanSession(apiBase string) (HumanSessionCredential, error) {
	normalized, err := ValidateAPIBase(apiBase)
	if err != nil {
		return HumanSessionCredential{}, err
	}
	cfg, err := LoadGlobal()
	if err != nil {
		return HumanSessionCredential{}, err
	}
	entry, ok := cfg.Servers[normalized]
	if !ok || entry.CredentialKind != CredentialKindHumanSession {
		return HumanSessionCredential{}, ErrNoHumanSession
	}
	token, err := credentialForEntry(entry, false)
	if errors.Is(err, os.ErrNotExist) {
		return HumanSessionCredential{}, ErrNoHumanSession
	}
	if err != nil {
		return HumanSessionCredential{}, err
	}
	if !isHumanSessionToken(token) {
		return HumanSessionCredential{}, ErrNoHumanSession
	}
	return HumanSessionCredential{
		Token:                strings.TrimSpace(token),
		HumanSessionMetadata: entry.HumanSession,
	}, nil
}

// DeleteHumanSession forgets only the active human session. A separately
// preserved operator recovery credential remains stored but is not promoted
// into the default slot.
func DeleteHumanSession(apiBase string) error {
	normalized, err := ValidateAPIBase(apiBase)
	if err != nil {
		return err
	}
	return mutateGlobal(func(cfg *Global, backend secretBackend, _ string, stages *[]stagedSecret) error {
		entry, ok := cfg.Servers[normalized]
		if !ok || entry.CredentialKind != CredentialKindHumanSession {
			return nil
		}
		ref := entry.CredentialRef
		entry.Token = ""
		entry.CredentialRef = ""
		entry.CredentialKind = ""
		entry.HumanSession = HumanSessionMetadata{}
		entry.Updated = time.Now().UTC()
		if entry.OperatorRecoveryToken == "" && entry.OperatorRecoveryCredentialRef == "" {
			delete(cfg.Servers, normalized)
		} else {
			cfg.Servers[normalized] = entry
		}
		if ref != "" {
			stage, err := stageSecretDelete(backend, ref)
			if err != nil {
				return err
			}
			*stages = append(*stages, stage)
		}
		return nil
	})
}

// OperatorRecoveryToken returns only a credential explicitly classified or
// preserved as operator recovery.
func OperatorRecoveryToken(apiBase string) (string, error) {
	normalized, err := ValidateAPIBase(apiBase)
	if err != nil {
		return "", err
	}
	cfg, err := LoadGlobal()
	if err != nil {
		return "", err
	}
	entry, ok := cfg.Servers[normalized]
	if !ok {
		return "", ErrNoOperatorRecovery
	}
	if entry.OperatorRecoveryToken != "" || entry.OperatorRecoveryCredentialRef != "" {
		token, err := credentialForEntry(entry, true)
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoOperatorRecovery
		}
		return token, err
	}
	if entry.CredentialKind == CredentialKindOperatorRecovery {
		token, err := credentialForEntry(entry, false)
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoOperatorRecovery
		}
		return token, err
	}
	return "", ErrNoOperatorRecovery
}

// DeleteToken removes the token entry for the supplied API base. Missing
// entries are a no-op so `midnight logout` is idempotent.
func DeleteToken(apiBase string) error {
	normalized, err := ValidateAPIBase(apiBase)
	if err != nil {
		return err
	}
	return mutateGlobal(func(cfg *Global, backend secretBackend, _ string, stages *[]stagedSecret) error {
		entry, ok := cfg.Servers[normalized]
		if !ok {
			return nil
		}
		delete(cfg.Servers, normalized)
		for _, ref := range []string{entry.CredentialRef, entry.OperatorRecoveryCredentialRef} {
			if ref != "" {
				stage, err := stageSecretDelete(backend, ref)
				if err != nil {
					return err
				}
				*stages = append(*stages, stage)
			}
		}
		return nil
	})
}

func mutateGlobal(mutate func(*Global, secretBackend, string, *[]stagedSecret) error) error {
	path, err := GlobalConfigPath()
	if err != nil {
		return err
	}
	return withConfigLock(path, func() error {
		cfg, err := loadGlobalPath(path)
		if err != nil {
			return err
		}
		backend, prefix, err := selectedSecretBackend()
		if err != nil {
			return err
		}
		stages := make([]stagedSecret, 0, 2)
		if err := mutate(&cfg, backend, prefix, &stages); err != nil {
			rollbackStagedSecrets(stages)
			return err
		}
		if err := migrateLegacyEntries(&cfg, backend, prefix, &stages); err != nil {
			rollbackStagedSecrets(stages)
			return err
		}
		if err := saveGlobalPath(path, cfg); err != nil {
			rollbackStagedSecrets(stages)
			return fmt.Errorf("save credential metadata: %v", redactCredentialError(err))
		}
		return nil
	})
}

func migrateLegacyEntries(cfg *Global, backend secretBackend, prefix string, stages *[]stagedSecret) error {
	for apiBase, entry := range cfg.Servers {
		if token := strings.TrimSpace(entry.Token); token != "" {
			ref := credentialReference(apiBase, "default", prefix)
			stage, err := stageSecret(backend, ref, token)
			if err != nil {
				return err
			}
			*stages = append(*stages, stage)
			entry.Token = ""
			entry.CredentialRef = ref
		}
		if token := strings.TrimSpace(entry.OperatorRecoveryToken); token != "" {
			ref := credentialReference(apiBase, "recovery", prefix)
			stage, err := stageSecret(backend, ref, token)
			if err != nil {
				return err
			}
			*stages = append(*stages, stage)
			entry.OperatorRecoveryToken = ""
			entry.OperatorRecoveryCredentialRef = ref
		}
		cfg.Servers[apiBase] = entry
	}
	return nil
}

func rollbackStagedSecrets(stages []stagedSecret) {
	for i := len(stages) - 1; i >= 0; i-- {
		_ = stages[i].rollback()
	}
}

// ResolveCredentials applies the CLI contract:
//
//   - endpoint: explicit flag, endpoint env, project link, one stored server
//   - token: explicit flag, project/CI env, legacy env, stored server token
//
// Missing or ambiguous values fail with typed errors. No localhost, default
// environment, or arbitrary first-server fallback is fabricated.
func ResolveCredentials(options ResolveOptions) (Credentials, error) {
	endpoint, err := resolveAPIBase(options.APIBase, options.ProjectAPIBase)
	if err != nil {
		return Credentials{}, err
	}
	token, kind, source, err := resolveToken(endpoint.APIBase, options.Token, options.OperatorRecovery)
	if err != nil {
		return Credentials{}, err
	}
	if endpoint.Source == APIBaseSourceProject && source == CredentialSourceEnvironment {
		return Credentials{}, fmt.Errorf(
			"%w; pass --api-base, set $%s, or pass --token explicitly",
			ErrUnsafeCredentialMix,
			EnvAPIBase,
		)
	}
	return Credentials{
		APIBase:       endpoint.APIBase,
		APIBaseSource: endpoint.Source,
		Token:         token,
		Kind:          kind,
		Source:        source,
	}, nil
}

// ResolveAPIBase resolves an endpoint without guessing. A linked project wins
// over the stored-server fallback because it is scoped to the current working
// directory. Stored credentials supply an endpoint only when exactly one
// non-empty server credential exists.
func ResolveAPIBase(explicit, projectAPIBase string) (string, error) {
	endpoint, err := resolveAPIBase(explicit, projectAPIBase)
	return endpoint.APIBase, err
}

type resolvedAPIBase struct {
	APIBase string
	Source  APIBaseSource
}

func resolveAPIBase(explicit, projectAPIBase string) (resolvedAPIBase, error) {
	candidates := []struct {
		value  string
		source APIBaseSource
	}{
		{explicit, APIBaseSourceExplicit},
		{os.Getenv(EnvAPIBase), APIBaseSourceEnvironment},
		{os.Getenv(EnvBaseURL), APIBaseSourceEnvironment},
		{projectAPIBase, APIBaseSourceProject},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.value) == "" {
			continue
		}
		normalized, err := ValidateAPIBase(candidate.value)
		if err != nil {
			return resolvedAPIBase{}, err
		}
		return resolvedAPIBase{APIBase: normalized, Source: candidate.source}, nil
	}

	cfg, err := LoadGlobal()
	if err != nil {
		return resolvedAPIBase{}, err
	}
	var stored string
	for key, entry := range cfg.Servers {
		if !entryHasCredential(entry) {
			continue
		}
		candidate, err := ValidateAPIBase(firstNonEmpty(entry.APIBase, key))
		if err != nil {
			return resolvedAPIBase{}, fmt.Errorf("invalid stored server endpoint: %w", err)
		}
		if stored != "" && stored != candidate {
			return resolvedAPIBase{}, &MissingCredentialError{
				Kind:   "api_base",
				Reason: "multiple stored servers exist; pass --api-base",
			}
		}
		stored = candidate
	}
	if stored != "" {
		return resolvedAPIBase{APIBase: stored, Source: APIBaseSourceStored}, nil
	}
	return resolvedAPIBase{}, &MissingCredentialError{Kind: "api_base"}
}

// ResolveToken applies explicit -> project/CI env -> compatibility env ->
// stored-server precedence for a resolved endpoint.
func ResolveToken(apiBase, explicit string) (string, error) {
	token, _, _, err := resolveToken(apiBase, explicit, false)
	return token, err
}

// Token retains the original package API while using the shared precedence
// contract. New command code should generally call ResolveCredentials.
func Token(apiBase string) (string, error) {
	return ResolveToken(apiBase, "")
}

func resolveToken(apiBase, explicit string, operatorRecovery bool) (string, CredentialKind, CredentialSource, error) {
	normalized, err := ValidateAPIBase(apiBase)
	if err != nil {
		return "", "", "", err
	}
	if token := strings.TrimSpace(explicit); token != "" {
		if operatorRecovery && isHumanSessionToken(token) {
			return "", "", "", errors.New("a human session cannot satisfy an operator recovery credential request")
		}
		return token, classifyResolvedToken(token), CredentialSourceExplicit, nil
	}
	for _, candidate := range []string{os.Getenv(EnvBearerToken), os.Getenv(EnvAPIToken)} {
		if token := strings.TrimSpace(candidate); token != "" {
			if operatorRecovery && isHumanSessionToken(token) {
				return "", "", "", errors.New("a human session cannot satisfy an operator recovery credential request")
			}
			return token, classifyResolvedToken(token), CredentialSourceEnvironment, nil
		}
	}

	cfg, err := LoadGlobal()
	if err != nil {
		return "", "", "", err
	}
	entry, ok := cfg.Servers[normalized]
	if ok && operatorRecovery {
		if entry.OperatorRecoveryToken != "" || entry.OperatorRecoveryCredentialRef != "" {
			token, err := credentialForEntry(entry, true)
			if errors.Is(err, os.ErrNotExist) {
				return "", "", "", ErrNoOperatorRecovery
			}
			if err != nil {
				return "", "", "", err
			}
			return token, CredentialKindOperatorRecovery, CredentialSourceStoredRecovery, nil
		}
		if entry.CredentialKind == CredentialKindOperatorRecovery {
			token, err := credentialForEntry(entry, false)
			if errors.Is(err, os.ErrNotExist) {
				return "", "", "", ErrNoOperatorRecovery
			}
			if err != nil {
				return "", "", "", err
			}
			return token, CredentialKindOperatorRecovery, CredentialSourceStoredRecovery, nil
		}
		return "", "", "", ErrNoOperatorRecovery
	}
	if !ok || (entry.Token == "" && entry.CredentialRef == "") {
		return "", "", "", &MissingCredentialError{Kind: "token", APIBase: normalized}
	}
	token, err := credentialForEntry(entry, false)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", "", &MissingCredentialError{Kind: "token", APIBase: normalized}
	}
	if err != nil {
		return "", "", "", err
	}
	kind := entry.CredentialKind
	if kind == "" {
		kind = classifyStoredToken(token)
	}
	source := CredentialSourceStoredLegacy
	if kind == CredentialKindHumanSession {
		source = CredentialSourceStoredSession
	} else if kind == CredentialKindOperatorRecovery {
		source = CredentialSourceStoredRecovery
	}
	return token, kind, source, nil
}

func entryHasCredential(entry ServerEntry) bool {
	return strings.TrimSpace(entry.Token) != "" || strings.TrimSpace(entry.CredentialRef) != "" ||
		strings.TrimSpace(entry.OperatorRecoveryToken) != "" || strings.TrimSpace(entry.OperatorRecoveryCredentialRef) != ""
}

func classifyStoredToken(token string) CredentialKind {
	if isHumanSessionToken(token) {
		return CredentialKindHumanSession
	}
	if strings.TrimSpace(token) != "" {
		// The v1 OAuth shim only persisted the installation-wide operator
		// token, so non-human legacy entries migrate as recovery credentials.
		return CredentialKindOperatorRecovery
	}
	return ""
}

func classifyResolvedToken(token string) CredentialKind {
	if isHumanSessionToken(token) {
		return CredentialKindHumanSession
	}
	if strings.TrimSpace(token) != "" {
		return CredentialKindExternal
	}
	return ""
}

func isHumanSessionToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), "midn_user_")
}

// ValidateAPIBase validates and canonicalizes an API authentication boundary.
// API bases are origins, not arbitrary URL prefixes: paths other than "/",
// userinfo, queries, and fragments are rejected. Plain HTTP is accepted only
// for numeric loopback addresses and localhost so local development remains
// possible without weakening remote credential transport.
func ValidateAPIBase(apiBase string) (string, error) {
	raw := strings.TrimSpace(apiBase)
	if raw == "" {
		return "", fmt.Errorf("%w: API base is required", ErrInvalidAPIBase)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: malformed URL", ErrInvalidAPIBase)
	}
	if !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" {
		return "", fmt.Errorf("%w: an absolute URL with a host is required", ErrInvalidAPIBase)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", fmt.Errorf("%w: scheme must be https or http", ErrInvalidAPIBase)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: userinfo is not allowed", ErrInvalidAPIBase)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("%w: query parameters are not allowed", ErrInvalidAPIBase)
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", fmt.Errorf("%w: fragments are not allowed", ErrInvalidAPIBase)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("%w: API base must not contain a path", ErrInvalidAPIBase)
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("%w: host is required", ErrInvalidAPIBase)
	}
	// url.Parse accepts an empty port suffix. Reject it so equivalent origins
	// cannot acquire distinct credential-store keys.
	if strings.HasSuffix(parsed.Host, ":") {
		return "", fmt.Errorf("%w: port must not be empty", ErrInvalidAPIBase)
	}
	if scheme == "http" && !isLoopbackAPIHost(hostname) {
		return "", fmt.Errorf("%w: https is required except for loopback hosts", ErrInvalidAPIBase)
	}
	parsed.Scheme = scheme
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String(), nil
}

func isLoopbackAPIHost(hostname string) bool {
	if strings.EqualFold(strings.TrimSuffix(hostname, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

// NormalizeAPIBase retains the original non-validating helper for source
// compatibility. Security-sensitive reads, writes, and resolution use
// ValidateAPIBase and never rely on this function for acceptance.
func NormalizeAPIBase(apiBase string) string {
	return strings.TrimRight(strings.TrimSpace(apiBase), "/")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ProjectLinkPath returns the absolute path to the per-cwd link file.
func ProjectLinkPath(workdir string) (string, error) {
	cwd := strings.TrimSpace(workdir)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("abs cwd: %w", err)
	}
	return filepath.Join(abs, ProjectLinkDirName, ProjectLinkFileName), nil
}

// LoadProjectLink reads the per-cwd link file. The caller may pass an
// empty workdir to mean `os.Getwd()`. Returns ErrNotLinked if the file is
// missing.
func LoadProjectLink(workdir string) (ProjectLink, error) {
	path, err := ProjectLinkPath(workdir)
	if err != nil {
		return ProjectLink{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ProjectLink{}, ErrNotLinked
	}
	if err != nil {
		return ProjectLink{}, fmt.Errorf("read %s: %w", path, err)
	}
	var link ProjectLink
	if err := json.Unmarshal(data, &link); err != nil {
		return ProjectLink{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if link.Version == 0 {
		link.Version = ProjectLinkCurrentVersion
	}
	if link.Version > ProjectLinkCurrentVersion {
		return ProjectLink{}, fmt.Errorf(
			"%w: found version %d, this CLI supports through version %d",
			ErrUnsupportedProjectLinkVersion,
			link.Version,
			ProjectLinkCurrentVersion,
		)
	}
	if strings.TrimSpace(link.APIBase) != "" {
		link.APIBase, err = ValidateAPIBase(link.APIBase)
		if err != nil {
			return ProjectLink{}, fmt.Errorf("unsafe API base in %s: %w", path, err)
		}
	}
	return link, nil
}

// SaveProjectLink atomically writes the per-cwd link file.
func SaveProjectLink(workdir string, link ProjectLink) error {
	path, err := ProjectLinkPath(workdir)
	if err != nil {
		return err
	}
	if link.Version > ProjectLinkCurrentVersion {
		return fmt.Errorf(
			"%w: refusing to replace version %d with version %d",
			ErrUnsupportedProjectLinkVersion,
			link.Version,
			ProjectLinkCurrentVersion,
		)
	}
	if strings.TrimSpace(link.APIBase) != "" {
		link.APIBase, err = ValidateAPIBase(link.APIBase)
		if err != nil {
			return err
		}
	}
	link.Version = ProjectLinkCurrentVersion
	link.Updated = time.Now().UTC()
	return atomicWriteJSON(path, link, 0o644, 0o755)
}

// DeleteProjectLink removes the per-cwd link file. Missing files are a
// no-op.
func DeleteProjectLink(workdir string) error {
	path, err := ProjectLinkPath(workdir)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	// Try to remove the directory if it ends up empty.
	dir := filepath.Dir(path)
	_ = os.Remove(dir)
	return nil
}

func atomicWriteJSON(path string, value any, fileMode, dirMode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := io.Copy(tmp, strings.NewReader(string(data))); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if runtime.GOOS != "windows" {
		// Windows ignores chmod on most filesystems; everywhere else we
		// lock down the file mode before renaming.
		if err := os.Chmod(tmpName, fileMode); err != nil {
			cleanup()
			return fmt.Errorf("chmod %s: %w", tmpName, err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	return nil
}
