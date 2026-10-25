//go:build integration

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	miniotc "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/secrets"
)

// TestSealedCredentialEndToEndAgainstMinIO exercises the panel-locked
// sealed-credential boundary end-to-end:
//
//  1. Writes a sealed variable through Lane 4's Vault using an
//     in-memory KeyProvider (the alpha-default substrate).
//  2. Wires a SealedCredentialSource through NewVaultSealedResolver +
//     ChainCredentialSource defaulted to sealed-first.
//  3. Constructs an S3Destination via NewS3DestinationCtx, which
//     runs the resolve path and forwards the audit row to the
//     supplied sink.
//  4. Performs a Put → Get round-trip against a MinIO testcontainer
//     using only the sealed-resolved credentials.
//
// Default `go test ./...` skips this file because the build tag is
// `integration`. The integration gate builds its pinned MinIO image first.
func TestSealedCredentialEndToEndAgainstMinIO(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	const accessKey = "midnight-sealed-test"
	const secretKey = "midnight-sealed-test-secret"
	const bucket = "midnight-sealed-backups"

	mc, err := miniotc.Run(ctx, minioIntegrationImage,
		miniotc.WithUsername(accessKey),
		miniotc.WithPassword(secretKey),
	)
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	t.Cleanup(func() {
		_ = mc.Terminate(context.Background())
	})

	endpoint, err := mc.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}

	// Lane 4 Vault wired with a fresh in-memory KEK.
	material := make([]byte, 32)
	for i := range material {
		material[i] = byte(i*7 + 3)
	}
	kp, err := secrets.NewMemoryKeyProvider(material, 1)
	if err != nil {
		t.Fatalf("NewMemoryKeyProvider: %v", err)
	}
	vault := secrets.NewVault(kp, secrets.AllowAll{})

	// Seal the credential payload through the panel-locked sealed
	// scope so the only way back to plaintext is via Reveal under an
	// injection scope (panel: line 1178).
	payload, err := json.Marshal(sealedCredentialPayload{
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	sealed, err := vault.Set(ctx, secrets.VariableInput{
		Name:      "backup-credentials",
		Workspace: "wsp_demo",
		Scope:     secrets.ScopeSealed,
		Plaintext: payload,
	})
	if err != nil {
		t.Fatalf("vault.Set: %v", err)
	}
	ref := string(sealed.Meta().ID)
	if !strings.HasPrefix(ref, "sealed_") {
		t.Fatalf("variable id prefix unexpected: %s", ref)
	}

	resolver := NewVaultSealedResolver(vault, secrets.AllowAll{}, secrets.Principal{
		ID:    "deploy-coordinator",
		Type:  "system",
		Scope: "wsp_demo",
	})

	envLookup := fakeLookup(map[string]string{
		EnvBackupCredentialRef: ref,
	})
	source := &ChainCredentialSource{
		Sealed: &SealedCredentialSource{
			Resolver:  resolver,
			LookupEnv: envLookup,
		},
		Env:       &EnvCredentialSource{LookupEnv: envLookup},
		LookupEnv: envLookup,
	}

	auditSink := &captureAuditSink{}
	dst, err := NewS3DestinationCtx(ctx, S3Options{
		Endpoint: S3Endpoint{
			Bucket:   bucket,
			Prefix:   "smoke",
			Endpoint: endpoint,
			Region:   "us-east-1",
			UseSSL:   false,
			UsePath:  true,
		},
		CredentialSource: source,
		Audit:            auditSink.Append,
	})
	if err != nil {
		t.Fatalf("NewS3DestinationCtx: %v", err)
	}

	// Audit emit cardinality: exactly one row per resolve.
	auditSink.assertOne(t)
	row := auditSink.first()
	if row.Action != AuditActionBackupCredentialsResolve {
		t.Fatalf("audit action = %q", row.Action)
	}
	if got, _ := row.Attrs["source"].(string); got != auditSourceSealed {
		t.Fatalf("audit source = %v, want sealed", row.Attrs["source"])
	}
	if got, _ := row.Attrs["reference_id"].(string); got != ref {
		t.Fatalf("audit reference_id = %v, want %s", row.Attrs["reference_id"], ref)
	}
	encoded, _ := json.Marshal(row)
	if strings.Contains(string(encoded), secretKey) || strings.Contains(string(encoded), accessKey) {
		t.Fatalf("audit row leaked plaintext: %s", string(encoded))
	}

	makeBucketCtx, makeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer makeCancel()
	if err := dst.cli.MakeBucket(makeBucketCtx, bucket, miniogoMakeOpts()); err != nil {
		t.Fatalf("MakeBucket: %v", err)
	}

	body := []byte("midnight-sealed-credential-roundtrip-payload")
	obj, err := dst.Put(ctx, "demo/sealed.bin", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if obj.Bytes != int64(len(body)) {
		t.Fatalf("Put.Bytes = %d, want %d", obj.Bytes, len(body))
	}

	rc, _, err := dst.Get(ctx, "demo/sealed.bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	gotBytes, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(gotBytes, body) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(gotBytes), len(body))
	}

	if err := dst.Delete(ctx, "demo/sealed.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	t.Logf("sealed-credential end-to-end round-trip succeeded against MinIO at %s (ref=%s)", endpoint, ref)
}

type captureAuditSink struct {
	mu   sync.Mutex
	rows []audit.Row
}

func (c *captureAuditSink) Append(_ context.Context, r audit.Row) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, r)
}

func (c *captureAuditSink) assertOne(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.rows) != 1 {
		t.Fatalf("audit row count = %d, want 1", len(c.rows))
	}
}

func (c *captureAuditSink) first() audit.Row {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.rows) == 0 {
		return audit.Row{}
	}
	return c.rows[0]
}
