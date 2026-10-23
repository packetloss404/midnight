package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/secrets"
)

// fakeLookup builds a deterministic env-var lookup function backed by
// the supplied map. Tests use it to avoid mutating os.Environ.
func fakeLookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

const fixedDestinationURI = "s3://midnight-backups/proj-foo"

// fakeSealedResolver lets tests stub SealedCredentialResolver
// without reaching into Lane 4's Vault.
type fakeSealedResolver struct {
	plaintext map[secrets.VariableID][]byte
	err       error
}

func (f *fakeSealedResolver) ResolveSealed(_ context.Context, id secrets.VariableID) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.plaintext[id]
	if !ok {
		return nil, secrets.ErrVariableNotFound
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

func TestEnvCredentialSourceResolvesWhenSet(t *testing.T) {
	t.Parallel()
	src := &EnvCredentialSource{
		LookupEnv: fakeLookup(map[string]string{
			EnvBackupAccessKeyID:     "AKIAEXAMPLE",
			EnvBackupSecretAccessKey: "supersecret",
			EnvBackupSessionToken:    "tok",
		}),
		Now: fixedClock(time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)),
	}
	creds, row, err := src.Resolve(context.Background(), fixedDestinationURI)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.AccessKeyID != "AKIAEXAMPLE" || creds.SecretAccessKey != "supersecret" || creds.SessionToken != "tok" {
		t.Fatalf("creds = %+v", creds)
	}
	if row == nil {
		t.Fatal("expected audit row, got nil")
	}
	if row.Action != AuditActionBackupCredentialsResolve {
		t.Fatalf("Action = %q", row.Action)
	}
	if got, _ := row.Attrs["source"].(string); got != auditSourceEnv {
		t.Fatalf("Attrs.source = %v", row.Attrs["source"])
	}
	if _, ok := row.Attrs["reference_id"]; ok {
		t.Fatal("env source must not emit reference_id")
	}
	if got, _ := row.Attrs["destination_uri"].(string); got != fixedDestinationURI {
		t.Fatalf("Attrs.destination_uri = %v", row.Attrs["destination_uri"])
	}
	plaintext := row.Action + " " + (mustString(row.Attrs))
	if strings.Contains(plaintext, "supersecret") || strings.Contains(plaintext, "AKIAEXAMPLE") {
		t.Fatalf("audit row leaked plaintext: %s", plaintext)
	}
}

func mustString(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func TestEnvCredentialSourceReturnsMissingWhenUnset(t *testing.T) {
	t.Parallel()
	src := &EnvCredentialSource{LookupEnv: fakeLookup(map[string]string{})}
	_, _, err := src.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsMissing)
}

func TestEnvCredentialSourceReturnsMissingWhenOnlyOneSet(t *testing.T) {
	t.Parallel()
	src := &EnvCredentialSource{LookupEnv: fakeLookup(map[string]string{
		EnvBackupAccessKeyID: "AKIA",
	})}
	_, _, err := src.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsMissing)
}

func TestSealedCredentialSourceResolvesPlaintext(t *testing.T) {
	t.Parallel()
	payload := mustJSON(t, sealedCredentialPayload{
		AccessKeyID:     "AKIASEALED",
		SecretAccessKey: "sealedsecret",
		SessionToken:    "tok",
	})
	resolver := &fakeSealedResolver{
		plaintext: map[secrets.VariableID][]byte{
			"var_backup-credentials_abcd": payload,
		},
	}
	src := &SealedCredentialSource{
		Resolver: resolver,
		LookupEnv: fakeLookup(map[string]string{
			EnvBackupCredentialRef: "var_backup-credentials_abcd",
		}),
		Now: fixedClock(time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)),
	}
	creds, row, err := src.Resolve(context.Background(), fixedDestinationURI)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.AccessKeyID != "AKIASEALED" || creds.SecretAccessKey != "sealedsecret" || creds.SessionToken != "tok" {
		t.Fatalf("creds = %+v", creds)
	}
	if row == nil {
		t.Fatal("expected audit row, got nil")
	}
	if got, _ := row.Attrs["source"].(string); got != auditSourceSealed {
		t.Fatalf("Attrs.source = %v", row.Attrs["source"])
	}
	if got, _ := row.Attrs["reference_id"].(string); got != "var_backup-credentials_abcd" {
		t.Fatalf("Attrs.reference_id = %v", row.Attrs["reference_id"])
	}
	encoded := mustString(row.Attrs)
	if strings.Contains(encoded, "sealedsecret") || strings.Contains(encoded, "AKIASEALED") {
		t.Fatalf("audit row leaked plaintext: %s", encoded)
	}
}

func TestSealedCredentialSourceMissingWhenRefUnset(t *testing.T) {
	t.Parallel()
	src := &SealedCredentialSource{
		Resolver:  &fakeSealedResolver{},
		LookupEnv: fakeLookup(map[string]string{}),
	}
	_, _, err := src.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsMissing)
}

func TestSealedCredentialSourceUnauthorizedWhenTargetMissing(t *testing.T) {
	t.Parallel()
	src := &SealedCredentialSource{
		Resolver: &fakeSealedResolver{plaintext: map[secrets.VariableID][]byte{}},
		LookupEnv: fakeLookup(map[string]string{
			EnvBackupCredentialRef: "var_does-not-exist",
		}),
	}
	_, _, err := src.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsUnauthorized)
}

func TestSealedCredentialSourceUnauthorizedWhenAuthDenies(t *testing.T) {
	t.Parallel()
	src := &SealedCredentialSource{
		Resolver: &fakeSealedResolver{err: secrets.ErrReferenceForbidden},
		LookupEnv: fakeLookup(map[string]string{
			EnvBackupCredentialRef: "var_some-ref",
		}),
	}
	_, _, err := src.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsUnauthorized)
}

func TestSealedCredentialSourceUnauthorizedWhenScopeReveal(t *testing.T) {
	t.Parallel()
	src := &SealedCredentialSource{
		Resolver: &fakeSealedResolver{err: secrets.ErrSealedRevealed},
		LookupEnv: fakeLookup(map[string]string{
			EnvBackupCredentialRef: "var_some-ref",
		}),
	}
	_, _, err := src.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsUnauthorized)
}

func TestSealedCredentialSourceUnauthorizedWhenResolverNil(t *testing.T) {
	t.Parallel()
	src := &SealedCredentialSource{
		Resolver: nil,
		LookupEnv: fakeLookup(map[string]string{
			EnvBackupCredentialRef: "var_x",
		}),
	}
	_, _, err := src.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsUnauthorized)
}

func TestSealedCredentialSourceRejectsInvalidPayload(t *testing.T) {
	t.Parallel()
	resolver := &fakeSealedResolver{
		plaintext: map[secrets.VariableID][]byte{
			"var_garbage": []byte("not-json"),
		},
	}
	src := &SealedCredentialSource{
		Resolver: resolver,
		LookupEnv: fakeLookup(map[string]string{
			EnvBackupCredentialRef: "var_garbage",
		}),
	}
	_, _, err := src.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsUnauthorized)
}

func TestSealedCredentialSourceRejectsPayloadMissingFields(t *testing.T) {
	t.Parallel()
	resolver := &fakeSealedResolver{
		plaintext: map[secrets.VariableID][]byte{
			"var_partial": mustJSON(t, sealedCredentialPayload{
				AccessKeyID: "AKIA",
			}),
		},
	}
	src := &SealedCredentialSource{
		Resolver: resolver,
		LookupEnv: fakeLookup(map[string]string{
			EnvBackupCredentialRef: "var_partial",
		}),
	}
	_, _, err := src.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsUnauthorized)
}

func TestChainCredentialSourceTriesSealedFirst(t *testing.T) {
	t.Parallel()
	resolver := &fakeSealedResolver{
		plaintext: map[secrets.VariableID][]byte{
			"var_sealed": mustJSON(t, sealedCredentialPayload{
				AccessKeyID:     "AKIASEALED",
				SecretAccessKey: "sealed",
			}),
		},
	}
	env := map[string]string{
		EnvBackupAccessKeyID:     "AKIAENV",
		EnvBackupSecretAccessKey: "envsecret",
		EnvBackupCredentialRef:   "var_sealed",
	}
	chain := &ChainCredentialSource{
		Sealed:    &SealedCredentialSource{Resolver: resolver, LookupEnv: fakeLookup(env)},
		Env:       &EnvCredentialSource{LookupEnv: fakeLookup(env)},
		LookupEnv: fakeLookup(env),
	}
	creds, row, err := chain.Resolve(context.Background(), fixedDestinationURI)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.AccessKeyID != "AKIASEALED" {
		t.Fatalf("AccessKeyID = %q, expected sealed source to win when ref is set", creds.AccessKeyID)
	}
	if got, _ := row.Attrs["source"].(string); got != auditSourceSealed {
		t.Fatalf("Attrs.source = %v, want sealed", row.Attrs["source"])
	}
}

func TestChainCredentialSourceFallsBackToEnvWhenRefUnset(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		EnvBackupAccessKeyID:     "AKIAENV",
		EnvBackupSecretAccessKey: "envsecret",
	}
	chain := &ChainCredentialSource{
		Sealed:    &SealedCredentialSource{Resolver: &fakeSealedResolver{}, LookupEnv: fakeLookup(env)},
		Env:       &EnvCredentialSource{LookupEnv: fakeLookup(env)},
		LookupEnv: fakeLookup(env),
	}
	creds, row, err := chain.Resolve(context.Background(), fixedDestinationURI)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.AccessKeyID != "AKIAENV" {
		t.Fatalf("AccessKeyID = %q, expected env source to win when ref unset", creds.AccessKeyID)
	}
	if got, _ := row.Attrs["source"].(string); got != auditSourceEnv {
		t.Fatalf("Attrs.source = %v, want env", row.Attrs["source"])
	}
}

func TestChainCredentialSourceMissingWhenNeitherConfigured(t *testing.T) {
	t.Parallel()
	empty := fakeLookup(map[string]string{})
	chain := &ChainCredentialSource{
		Sealed:    &SealedCredentialSource{Resolver: &fakeSealedResolver{}, LookupEnv: empty},
		Env:       &EnvCredentialSource{LookupEnv: empty},
		LookupEnv: empty,
	}
	_, _, err := chain.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsMissing)
}

func TestChainCredentialSourceSealedFailureIsTerminal(t *testing.T) {
	t.Parallel()
	// When the operator opts into the sealed path, a sealed failure
	// MUST NOT silently fall back to env — the boundary requires a
	// loud BACKUP_CREDENTIALS_UNAUTHORIZED.
	resolver := &fakeSealedResolver{err: secrets.ErrReferenceForbidden}
	env := map[string]string{
		EnvBackupAccessKeyID:     "AKIAENV",
		EnvBackupSecretAccessKey: "envsecret",
		EnvBackupCredentialRef:   "var_forbidden",
	}
	chain := &ChainCredentialSource{
		Sealed:    &SealedCredentialSource{Resolver: resolver, LookupEnv: fakeLookup(env)},
		Env:       &EnvCredentialSource{LookupEnv: fakeLookup(env)},
		LookupEnv: fakeLookup(env),
	}
	_, _, err := chain.Resolve(context.Background(), fixedDestinationURI)
	assertFailureCode(t, err, failurecodes.BackupCredentialsUnauthorized)
}

func TestNewDefaultChainCredentialSourceWithoutResolverIsEnvOnly(t *testing.T) {
	t.Parallel()
	chain := NewDefaultChainCredentialSource(nil)
	if chain.Sealed != nil {
		t.Fatal("expected Sealed to be nil when resolver is nil")
	}
	if chain.Env == nil {
		t.Fatal("expected Env to be present in the alpha-default chain")
	}
}

func TestNewDefaultChainCredentialSourceWithResolverWiresSealed(t *testing.T) {
	t.Parallel()
	chain := NewDefaultChainCredentialSource(&fakeSealedResolver{})
	if chain.Sealed == nil {
		t.Fatal("expected Sealed to be wired when resolver is non-nil")
	}
	if chain.Env == nil {
		t.Fatal("expected Env to remain wired when resolver is non-nil")
	}
}

// TestEachResolveEmitsExactlyOneAuditRecord asserts the panel-locked
// audit emit cardinality: each successful Resolve produces exactly one
// audit row, and each failed Resolve produces none. This bounds the
// audit-chain row growth and matches the Lane 5 emit pattern.
func TestEachResolveEmitsExactlyOneAuditRecord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source func() CredentialSource
		want   int
	}{
		{
			name: "env success",
			source: func() CredentialSource {
				return &EnvCredentialSource{LookupEnv: fakeLookup(map[string]string{
					EnvBackupAccessKeyID:     "k",
					EnvBackupSecretAccessKey: "s",
				})}
			},
			want: 1,
		},
		{
			name: "env missing",
			source: func() CredentialSource {
				return &EnvCredentialSource{LookupEnv: fakeLookup(map[string]string{})}
			},
			want: 0,
		},
		{
			name: "sealed success",
			source: func() CredentialSource {
				return &SealedCredentialSource{
					Resolver: &fakeSealedResolver{plaintext: map[secrets.VariableID][]byte{
						"var_x": mustJSON(t, sealedCredentialPayload{AccessKeyID: "k", SecretAccessKey: "s"}),
					}},
					LookupEnv: fakeLookup(map[string]string{EnvBackupCredentialRef: "var_x"}),
				}
			},
			want: 1,
		},
		{
			name: "sealed unauthorized",
			source: func() CredentialSource {
				return &SealedCredentialSource{
					Resolver:  &fakeSealedResolver{err: secrets.ErrReferenceForbidden},
					LookupEnv: fakeLookup(map[string]string{EnvBackupCredentialRef: "var_x"}),
				}
			},
			want: 0,
		},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, row, err := tt.source().Resolve(context.Background(), fixedDestinationURI)
			emit := 0
			if row != nil {
				emit = 1
			}
			if emit != tt.want {
				t.Fatalf("emitted rows = %d, want %d (err=%v)", emit, tt.want, err)
			}
		})
	}
}

// TestVaultSealedResolverEnforcesCanRead verifies the Authorizer.CanRead
// hop fires before the unseal — the BACKUP_CREDENTIALS_UNAUTHORIZED
// path is panel-locked through this seam.
func TestVaultSealedResolverEnforcesCanRead(t *testing.T) {
	t.Parallel()

	kp := newTestKeyProvider(t)
	vault := secrets.NewVault(kp, secrets.AllowAll{})
	v, err := vault.Set(context.Background(), secrets.VariableInput{
		Name:      "backup-credentials",
		Workspace: "wsp_demo",
		Scope:     secrets.ScopeSealed,
		Plaintext: mustJSON(t, sealedCredentialPayload{AccessKeyID: "k", SecretAccessKey: "s"}),
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	deny := secrets.DenyByDefault{Reads: map[string]bool{}}
	resolver := NewVaultSealedResolver(vault, deny, secrets.Principal{ID: "deploy", Type: "system"})
	_, err = resolver.ResolveSealed(context.Background(), v.Meta().ID)
	assertFailureCode(t, err, failurecodes.BackupCredentialsUnauthorized)
}

func TestVaultSealedResolverHappyPath(t *testing.T) {
	t.Parallel()

	kp := newTestKeyProvider(t)
	vault := secrets.NewVault(kp, secrets.AllowAll{})
	v, err := vault.Set(context.Background(), secrets.VariableInput{
		Name:      "backup-credentials",
		Workspace: "wsp_demo",
		Scope:     secrets.ScopeSealed,
		Plaintext: mustJSON(t, sealedCredentialPayload{AccessKeyID: "AKIA-VAULT", SecretAccessKey: "sec"}),
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	resolver := NewVaultSealedResolver(vault, secrets.AllowAll{}, secrets.Principal{ID: "deploy", Type: "system"})
	plaintext, err := resolver.ResolveSealed(context.Background(), v.Meta().ID)
	if err != nil {
		t.Fatalf("ResolveSealed: %v", err)
	}
	defer zeroBytes(plaintext)
	var payload sealedCredentialPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.AccessKeyID != "AKIA-VAULT" || payload.SecretAccessKey != "sec" {
		t.Fatalf("payload = %+v", payload)
	}
}

// TestNewS3DestinationForwardsAuditRow verifies the s3 destination
// constructor calls the supplied Audit hook exactly once per
// successful CredentialSource.Resolve.
func TestNewS3DestinationForwardsAuditRow(t *testing.T) {
	t.Parallel()

	src := &EnvCredentialSource{LookupEnv: fakeLookup(map[string]string{
		EnvBackupAccessKeyID:     "AKIA",
		EnvBackupSecretAccessKey: "secret",
	})}
	var rows []audit.Row
	_, err := NewS3Destination(S3Options{
		Endpoint:         S3Endpoint{Bucket: "x", Endpoint: "s3.us-east-1.amazonaws.com", Region: "us-east-1"},
		CredentialSource: src,
		Audit:            func(_ context.Context, r audit.Row) { rows = append(rows, r) },
	})
	if err != nil {
		t.Fatalf("NewS3Destination: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	if rows[0].Action != AuditActionBackupCredentialsResolve {
		t.Fatalf("Action = %q", rows[0].Action)
	}
}

// TestNewS3DestinationDoesNotEmitWhenCredentialsExplicit asserts the
// explicit-credentials short-circuit: callers passing pre-built
// *miniocreds.Credentials skip the resolve path entirely.
func TestNewS3DestinationDoesNotEmitWhenCredentialsExplicit(t *testing.T) {
	t.Parallel()

	emitted := 0
	_, err := NewS3Destination(S3Options{
		Endpoint:    S3Endpoint{Bucket: "x", Endpoint: "s3.us-east-1.amazonaws.com", Region: "us-east-1"},
		Credentials: CredentialsFromEnv(),
		Audit:       func(_ context.Context, _ audit.Row) { emitted++ },
	})
	// Credentials nil is OK — the test just asserts no audit row was
	// emitted because the resolve path was not entered.
	_ = err
	if emitted != 0 {
		t.Fatalf("emitted = %d, want 0 when Credentials is supplied directly", emitted)
	}
}

// newTestKeyProvider constructs a deterministic in-memory KeyProvider
// over fresh KEK material.
func newTestKeyProvider(t *testing.T) secrets.KeyProvider {
	t.Helper()
	material := make([]byte, 32)
	for i := range material {
		material[i] = byte(i)
	}
	kp, err := secrets.NewMemoryKeyProvider(material, 1)
	if err != nil {
		t.Fatalf("NewMemoryKeyProvider: %v", err)
	}
	return kp
}

func assertFailureCode(t *testing.T, err error, want failurecodes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var failure *failurecodes.FailureError
	if !errors.As(err, &failure) {
		t.Fatalf("expected *failurecodes.FailureError with code %s, got %T: %v", want, err, err)
	}
	if failure.Code != want {
		t.Fatalf("failure code = %s, want %s (err=%v)", failure.Code, want, err)
	}
}
