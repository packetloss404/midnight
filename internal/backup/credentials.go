package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/secrets"
)

// Panel-locked env-var names. The first three are the alpha-default
// credential triple; MIDNIGHT_BACKUP_CREDENTIAL_REF is the optional
// escalation hook that points the resolver at a sealed variable in
// Lane 4's secret store. See
// agile/handoff/governance/BACKUP_CREDENTIAL_BOUNDARY.md for the
// posture decision.
const (
	EnvBackupAccessKeyID     = "MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID"
	EnvBackupSecretAccessKey = "MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY"
	EnvBackupSessionToken    = "MIDNIGHT_BACKUP_AWS_SESSION_TOKEN"
	EnvBackupCredentialRef   = "MIDNIGHT_BACKUP_CREDENTIAL_REF"
)

// Audit constants for the credential resolve emit. The action string
// is panel-locked at "backup.credentials.resolve"; auditors and the
// `midnight audit verify` log fixtures match on the action prefix.
const (
	AuditActionBackupCredentialsResolve = "backup.credentials.resolve"
	AuditResourceKindBackupDestination  = "backup_destination"

	auditSourceEnv    = "env"
	auditSourceSealed = "sealed"
)

// Credentials is the resolved (plaintext) S3-compatible credential
// triple. Plaintext fields are ephemeral by contract: callers convert
// the value to a *miniocreds.Credentials, hand it to the MinIO client,
// then invoke Zero() to scrub the local copy.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// IsZero reports whether every credential field is empty.
func (c Credentials) IsZero() bool {
	return c.AccessKeyID == "" && c.SecretAccessKey == "" && c.SessionToken == ""
}

// MinioCredentials converts the resolved triple into a
// *miniocreds.Credentials value the MinIO client constructor accepts.
// Returns nil for the IsZero case so callers can detect "no
// credentials" without inspecting fields.
func (c Credentials) MinioCredentials() *miniocreds.Credentials {
	if c.IsZero() {
		return nil
	}
	return miniocreds.NewStaticV4(c.AccessKeyID, c.SecretAccessKey, c.SessionToken)
}

// Zero overwrites each credential field in place. Defensive only —
// Go's GC may move the underlying bytes around freely, so callers
// SHOULD treat plaintext as short-lived even after the call.
func (c *Credentials) Zero() {
	if c == nil {
		return
	}
	c.AccessKeyID = ""
	c.SecretAccessKey = ""
	c.SessionToken = ""
}

// CredentialSource resolves backup credentials for the supplied
// destination URI. Implementations MUST return exactly one audit row
// per successful Resolve describing the source (`env` or `sealed`),
// with no plaintext fields. The audit row is the canonical emit
// shape; the caller is responsible for stamping correlation fields
// (RequestID, Actor) and appending it to the configured audit chain
// — Phase 1 audit emit failure is fail-soft (warn + continue) per
// the panel-locked operational posture.
type CredentialSource interface {
	Resolve(ctx context.Context, destinationURI string) (Credentials, *audit.Row, error)
}

// SealedCredentialResolver is the small Vault subset
// SealedCredentialSource needs. *secrets.Vault wired through
// NewVaultSealedResolver satisfies the contract; tests can stub it.
//
// Implementations MUST enforce Authorizer.CanRead on the consumer
// principal before unsealing — the BACKUP_CREDENTIALS_UNAUTHORIZED
// path is panel-locked through this seam.
type SealedCredentialResolver interface {
	ResolveSealed(ctx context.Context, ref secrets.VariableID) ([]byte, error)
}

// EnvCredentialSource is the alpha-default credential source. It
// reads MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID /
// MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY (plus the optional
// MIDNIGHT_BACKUP_AWS_SESSION_TOKEN) and emits an audit record with
// `source: "env"`.
//
// Per BACKUP_CREDENTIAL_BOUNDARY.md the env-var posture is acceptable
// for v0.1.0-alpha and v0.2.0-alpha; from v0.3.0-alpha onward the
// sealed posture is mandatory and the env source is removed from the
// default chain.
type EnvCredentialSource struct {
	// LookupEnv overrides os.LookupEnv. Tests use it to inject a
	// deterministic environment without mutating the process state.
	LookupEnv func(string) (string, bool)
	// Now overrides the audit-row clock. Defaults to time.Now().UTC().
	Now func() time.Time
}

// Resolve reads the env vars and returns a populated Credentials +
// audit row, or BACKUP_CREDENTIALS_MISSING when either of the two
// required env vars is unset.
func (s *EnvCredentialSource) Resolve(ctx context.Context, uri string) (Credentials, *audit.Row, error) {
	lookup := s.lookup()
	id := strings.TrimSpace(valueOf(lookup, EnvBackupAccessKeyID))
	secret := strings.TrimSpace(valueOf(lookup, EnvBackupSecretAccessKey))
	token := strings.TrimSpace(valueOf(lookup, EnvBackupSessionToken))
	if id == "" || secret == "" {
		return Credentials{}, nil, failurecodes.New(
			failurecodes.BackupCredentialsMissing,
			"env-var backup credentials are not configured (MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID / MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY)",
		)
	}
	creds := Credentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: token}
	row := newCredentialResolveRow(uri, auditSourceEnv, "", s.now())
	return creds, row, nil
}

func (s *EnvCredentialSource) lookup() func(string) (string, bool) {
	if s != nil && s.LookupEnv != nil {
		return s.LookupEnv
	}
	return os.LookupEnv
}

func (s *EnvCredentialSource) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// SealedCredentialSource resolves backup credentials from a sealed
// variable referenced by MIDNIGHT_BACKUP_CREDENTIAL_REF. The
// referenced variable's plaintext MUST be a JSON document of the form
// `{"access_key_id": "...", "secret_access_key": "...",
// "session_token": "..."}` (session_token optional). Plaintext bytes
// are zeroed in place after the structured fields have been copied.
//
// The audit row records the reference id but never the plaintext.
//
// Per the BACKUP_CREDENTIAL_BOUNDARY.md decision, this source is
// optional in v0.1.0-alpha / v0.2.0-alpha (operators may opt in by
// setting the ref env var) and mandatory from v0.3.0-alpha onward.
type SealedCredentialSource struct {
	// Resolver is the seam onto Lane 4's Vault. May be nil — the
	// resolver returns BACKUP_CREDENTIALS_UNAUTHORIZED in that case
	// to surface the misconfiguration loudly rather than falling
	// through to env silently.
	Resolver SealedCredentialResolver
	// LookupEnv overrides os.LookupEnv. Tests use it to inject a
	// deterministic environment without mutating the process state.
	LookupEnv func(string) (string, bool)
	// Now overrides the audit-row clock. Defaults to time.Now().UTC().
	Now func() time.Time
}

// Resolve looks up the sealed-variable reference, decrypts the
// payload, and returns the resolved credentials plus an audit row
// stamped with `source: "sealed"` and the reference id. Returns
// BACKUP_CREDENTIALS_MISSING when the env ref is unset; returns
// BACKUP_CREDENTIALS_UNAUTHORIZED when the consumer principal lacks
// read scope on the referenced variable, when the reference target
// is missing, or when the resolver is nil.
func (s *SealedCredentialSource) Resolve(ctx context.Context, uri string) (Credentials, *audit.Row, error) {
	lookup := s.lookup()
	ref := strings.TrimSpace(valueOf(lookup, EnvBackupCredentialRef))
	if ref == "" {
		return Credentials{}, nil, failurecodes.New(
			failurecodes.BackupCredentialsMissing,
			fmt.Sprintf("%s is not set", EnvBackupCredentialRef),
		)
	}
	if s.Resolver == nil {
		return Credentials{}, nil, failurecodes.New(
			failurecodes.BackupCredentialsUnauthorized,
			"sealed credential source has no Vault resolver wired",
		)
	}
	plaintext, err := s.Resolver.ResolveSealed(ctx, secrets.VariableID(ref))
	if err != nil {
		var failure *failurecodes.FailureError
		if errors.As(err, &failure) {
			return Credentials{}, nil, failure
		}
		// Translate Lane 4's typed errors into the panel-locked
		// failure-code envelope so callers get a consistent reason
		// without hand-parsing strings.
		switch {
		case errors.Is(err, secrets.ErrVariableNotFound):
			return Credentials{}, nil, failurecodes.Wrap(
				failurecodes.BackupCredentialsUnauthorized,
				"sealed credential reference target is missing",
				err,
			)
		case errors.Is(err, secrets.ErrReferenceForbidden):
			return Credentials{}, nil, failurecodes.Wrap(
				failurecodes.BackupCredentialsUnauthorized,
				"consumer principal lacks read scope on the sealed credential",
				err,
			)
		case errors.Is(err, secrets.ErrSealedRevealed):
			return Credentials{}, nil, failurecodes.Wrap(
				failurecodes.BackupCredentialsUnauthorized,
				"sealed credential cannot be revealed in this scope",
				err,
			)
		default:
			return Credentials{}, nil, failurecodes.Wrap(
				failurecodes.BackupCredentialsUnauthorized,
				"sealed credential resolution failed",
				err,
			)
		}
	}
	defer zeroBytes(plaintext)
	creds, err := parseSealedCredentialPayload(plaintext)
	if err != nil {
		return Credentials{}, nil, err
	}
	row := newCredentialResolveRow(uri, auditSourceSealed, ref, s.now())
	return creds, row, nil
}

func (s *SealedCredentialSource) lookup() func(string) (string, bool) {
	if s != nil && s.LookupEnv != nil {
		return s.LookupEnv
	}
	return os.LookupEnv
}

func (s *SealedCredentialSource) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// ChainCredentialSource is the panel-locked composite the s3
// destination consumes by default. When MIDNIGHT_BACKUP_CREDENTIAL_REF
// is set the chain delegates to Sealed and surfaces its error
// verbatim — falling back to env when the operator explicitly opted
// into sealed would silently weaken the security posture. When the
// ref is unset, the chain delegates to Env. When neither path can
// resolve, BACKUP_CREDENTIALS_MISSING is returned.
type ChainCredentialSource struct {
	Sealed *SealedCredentialSource
	Env    *EnvCredentialSource
	// LookupEnv overrides the env-var lookup used to detect whether
	// the sealed reference is set. When nil, the inner sources'
	// LookupEnv hooks are tried (Sealed first, then Env), then
	// os.LookupEnv as a final fallback.
	LookupEnv func(string) (string, bool)
}

// NewDefaultChainCredentialSource returns the panel-locked default
// chain wiring. When resolver is non-nil the sealed escalation path
// is enabled (the operator has wired a Vault); when resolver is nil
// only the env source is consulted, which is the v0.1.0-alpha /
// v0.2.0-alpha posture. The env source is always present so existing
// env-var users are unaffected.
func NewDefaultChainCredentialSource(resolver SealedCredentialResolver) *ChainCredentialSource {
	chain := &ChainCredentialSource{
		Env: &EnvCredentialSource{},
	}
	if resolver != nil {
		chain.Sealed = &SealedCredentialSource{Resolver: resolver}
	}
	return chain
}

// Resolve runs the panel-locked sealed-then-env chain. Returns the
// resolved credentials + audit row, or a failurecodes.FailureError
// describing the missing/unauthorized condition.
func (c *ChainCredentialSource) Resolve(ctx context.Context, uri string) (Credentials, *audit.Row, error) {
	if c == nil {
		return Credentials{}, nil, failurecodes.New(
			failurecodes.BackupCredentialsMissing,
			"no backup credential source is configured",
		)
	}
	lookup := c.lookup()
	refSet := false
	if v, ok := lookup(EnvBackupCredentialRef); ok && strings.TrimSpace(v) != "" {
		refSet = true
	}
	if refSet {
		if c.Sealed == nil {
			return Credentials{}, nil, failurecodes.New(
				failurecodes.BackupCredentialsUnauthorized,
				fmt.Sprintf("%s is set but the sealed credential source is not wired", EnvBackupCredentialRef),
			)
		}
		// Sealed failure is terminal — the operator opted into the
		// sealed posture by setting the ref, so silently falling back
		// to env would defeat the boundary.
		return c.Sealed.Resolve(ctx, uri)
	}
	if c.Env != nil {
		creds, row, err := c.Env.Resolve(ctx, uri)
		if err == nil {
			return creds, row, nil
		}
		var failure *failurecodes.FailureError
		if errors.As(err, &failure) && failure.Code == failurecodes.BackupCredentialsMissing {
			return Credentials{}, nil, failurecodes.New(
				failurecodes.BackupCredentialsMissing,
				"no backup credential source is configured: "+
					fmt.Sprintf("%s is unset and env-var fallback is empty", EnvBackupCredentialRef),
			)
		}
		return Credentials{}, nil, err
	}
	return Credentials{}, nil, failurecodes.New(
		failurecodes.BackupCredentialsMissing,
		"no backup credential source is configured",
	)
}

func (c *ChainCredentialSource) lookup() func(string) (string, bool) {
	if c != nil && c.LookupEnv != nil {
		return c.LookupEnv
	}
	if c != nil && c.Sealed != nil && c.Sealed.LookupEnv != nil {
		return c.Sealed.LookupEnv
	}
	if c != nil && c.Env != nil && c.Env.LookupEnv != nil {
		return c.Env.LookupEnv
	}
	return os.LookupEnv
}

// VaultSealedResolver is the production SealedCredentialResolver that
// wraps Lane 4's *secrets.Vault. It enforces Authorizer.CanRead on
// the consumer principal before unsealing, then decrypts under the
// runtime-injection scope.
type VaultSealedResolver struct {
	vault     vaultMetaReader
	auth      secrets.Authorizer
	principal secrets.Principal
}

// vaultMetaReader is the small subset of *secrets.Vault the resolver
// uses. The interface is unexported because callers should construct
// the resolver via NewVaultSealedResolver, but it makes the
// dependency explicit so tests can substitute a fake without
// importing the real Vault.
type vaultMetaReader interface {
	Get(ctx context.Context, id secrets.VariableID) (*secrets.Variable, error)
	Decrypt(ctx context.Context, id secrets.VariableID, scope secrets.ResolvedScope) ([]byte, secrets.AuditDecryptRecord, error)
}

// NewVaultSealedResolver wires Lane 4's Vault + Authorizer +
// consumer principal into a SealedCredentialResolver. The principal
// MUST be the deploy / backup-coordinator service account; the
// authorizer MUST be the workspace's policy-driven Authorizer (use
// secrets.AllowAll only in tests).
func NewVaultSealedResolver(vault *secrets.Vault, auth secrets.Authorizer, principal secrets.Principal) *VaultSealedResolver {
	if auth == nil {
		auth = secrets.AllowAll{}
	}
	return &VaultSealedResolver{vault: vault, auth: auth, principal: principal}
}

// ResolveSealed implements SealedCredentialResolver.
func (r *VaultSealedResolver) ResolveSealed(ctx context.Context, id secrets.VariableID) ([]byte, error) {
	if r == nil || r.vault == nil {
		return nil, failurecodes.New(
			failurecodes.BackupCredentialsUnauthorized,
			"vault sealed resolver is not initialised",
		)
	}
	v, err := r.vault.Get(ctx, id)
	if err != nil {
		if errors.Is(err, secrets.ErrVariableNotFound) {
			return nil, failurecodes.Wrap(
				failurecodes.BackupCredentialsUnauthorized,
				"sealed credential reference target is missing",
				err,
			)
		}
		return nil, err
	}
	if err := r.auth.CanRead(ctx, r.principal, v.Meta()); err != nil {
		return nil, failurecodes.Wrap(
			failurecodes.BackupCredentialsUnauthorized,
			"consumer principal lacks read scope on the sealed credential",
			err,
		)
	}
	plaintext, _, err := r.vault.Decrypt(ctx, id, secrets.ScopeRuntimeInject)
	if err != nil {
		if errors.Is(err, secrets.ErrSealedRevealed) {
			return nil, failurecodes.Wrap(
				failurecodes.BackupCredentialsUnauthorized,
				"sealed credential cannot be revealed in this scope",
				err,
			)
		}
		return nil, err
	}
	return plaintext, nil
}

// sealedCredentialPayload is the on-the-wire shape of a sealed
// backup credential. JSON keys are panel-locked; operators write
// this exact shape via `midnight admin variable set --sealed
// --scope=workspace --name=backup-credentials --plaintext '{...}'`.
type sealedCredentialPayload struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
}

func parseSealedCredentialPayload(plaintext []byte) (Credentials, error) {
	var payload sealedCredentialPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return Credentials{}, failurecodes.Wrap(
			failurecodes.BackupCredentialsUnauthorized,
			"sealed credential payload is not valid JSON",
			err,
		)
	}
	id := strings.TrimSpace(payload.AccessKeyID)
	secret := strings.TrimSpace(payload.SecretAccessKey)
	token := strings.TrimSpace(payload.SessionToken)
	if id == "" || secret == "" {
		return Credentials{}, failurecodes.New(
			failurecodes.BackupCredentialsUnauthorized,
			"sealed credential payload is missing access_key_id or secret_access_key",
		)
	}
	return Credentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: token}, nil
}

// newCredentialResolveRow builds the canonical audit row for a single
// resolve call. The Attrs map carries `source` and (for sealed)
// `reference_id`; plaintext NEVER appears.
func newCredentialResolveRow(uri, source, referenceID string, now time.Time) *audit.Row {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	attrs := map[string]any{"source": source}
	if referenceID != "" {
		attrs["reference_id"] = referenceID
	}
	if uri != "" {
		attrs["destination_uri"] = uri
	}
	return &audit.Row{
		OccurredAt:   now,
		RecordedAt:   now,
		ActorType:    "system",
		ResourceKind: AuditResourceKindBackupDestination,
		ResourceID:   uri,
		Action:       AuditActionBackupCredentialsResolve,
		Attrs:        attrs,
	}
}

func valueOf(lookup func(string) (string, bool), key string) string {
	v, _ := lookup(key)
	return v
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
