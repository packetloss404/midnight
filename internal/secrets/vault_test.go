package secrets

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func newTestVault(t *testing.T) *Vault {
	t.Helper()
	provider := newTestProvider(t)
	return NewVault(provider, AllowAll{})
}

func TestVaultSetGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	vlt := newTestVault(t)
	v, err := vlt.Set(ctx, VariableInput{
		Name:        "DATABASE_URL",
		Workspace:   "wsp_a",
		Project:     "prj_a",
		Environment: "env_prod",
		Service:     "svc_api",
		Scope:       ScopeSecret,
		Plaintext:   []byte("postgres://user:hunter2@host:5432/db"),
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if v.Meta().LengthBucket != "17-64" {
		t.Fatalf("bucket = %s want 17-64", v.Meta().LengthBucket)
	}
	plaintext, _, err := vlt.Decrypt(ctx, v.Meta().ID, ScopeRuntimeInject)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plaintext) != "postgres://user:hunter2@host:5432/db" {
		t.Fatalf("plaintext mismatch")
	}
}

func TestSealedVariableNoReveal(t *testing.T) {
	ctx := context.Background()
	vlt := newTestVault(t)
	const plain = "super-secret-passphrase-VALUE"
	v, err := vlt.Set(ctx, VariableInput{
		Name:      "PROD_DB_PASSWORD",
		Workspace: "wsp_a",
		Project:   "prj_a",
		Scope:     ScopeSealed,
		Plaintext: []byte(plain),
	})
	if err != nil {
		t.Fatalf("set sealed: %v", err)
	}
	// Public read must surface only metadata.
	meta := v.Meta()
	if meta.LengthBucket == "" {
		t.Fatalf("metadata missing length bucket")
	}
	// Stringer must NOT include plaintext.
	if strings.Contains(v.sealed.String(), plain) {
		t.Fatalf("sealed.String() leaks plaintext")
	}
	// Decrypt without an injection scope must fail.
	if _, _, err := vlt.Decrypt(ctx, meta.ID, ResolvedScope("")); !errors.Is(err, ErrSealedRevealed) {
		t.Fatalf("decrypt without injection scope = %v want ErrSealedRevealed", err)
	}
	// Decrypt for runtime injection must succeed and roundtrip plaintext.
	plaintext, rec, err := vlt.Decrypt(ctx, meta.ID, ScopeRuntimeInject)
	if err != nil {
		t.Fatalf("decrypt runtime: %v", err)
	}
	if string(plaintext) != plain {
		t.Fatalf("runtime decrypt mismatch")
	}
	if rec.Resolved != ScopeRuntimeInject {
		t.Fatalf("audit record resolved = %s", rec.Resolved)
	}
	// Reveal via SealedVariable (with empty token) must fail.
	if _, err := v.sealed.Reveal(ctx, RevealToken{}); !errors.Is(err, ErrSealedRevealed) {
		t.Fatalf("Reveal{} = %v want ErrSealedRevealed", err)
	}
	// Reveal with vault-issued runtime injection token succeeds.
	tok := vlt.Reveal(ScopeRuntimeInject)
	got, err := v.sealed.Reveal(ctx, tok)
	if err != nil {
		t.Fatalf("Reveal(runtime token) = %v", err)
	}
	if string(got) != plain {
		t.Fatalf("Reveal(runtime token) mismatch")
	}
	otherVault := newTestVault(t)
	if _, err := v.sealed.Reveal(ctx, otherVault.Reveal(ScopeRuntimeInject)); !errors.Is(err, ErrSealedRevealed) {
		t.Fatalf("Reveal(token from another vault) = %v want ErrSealedRevealed", err)
	}
}

func TestSealedVariableRotateNoView(t *testing.T) {
	ctx := context.Background()
	vlt := newTestVault(t)
	v, err := vlt.Set(ctx, VariableInput{
		Name:      "API_KEY",
		Workspace: "wsp_a",
		Scope:     ScopeSealed,
		Plaintext: []byte("alpha-key-value"),
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	updated, err := vlt.Rotate(ctx, v.Meta().ID, []byte("beta-key-value"))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if updated.Meta().Version != 2 {
		t.Fatalf("rotation version = %d want 2", updated.Meta().Version)
	}
	plaintext, _, err := vlt.Decrypt(ctx, updated.Meta().ID, ScopeRuntimeInject)
	if err != nil {
		t.Fatalf("decrypt after rotate: %v", err)
	}
	if string(plaintext) != "beta-key-value" {
		t.Fatalf("plaintext after rotate = %q", plaintext)
	}
}

func TestVaultEncryptedDiskHasNoPlaintext(t *testing.T) {
	ctx := context.Background()
	vlt := newTestVault(t)
	plaintexts := [][]byte{
		[]byte("very-private-token-VALUE"),
		[]byte("hunter2-correct-horse-battery-staple"),
		[]byte("postgres://user:hunter2@host:5432/db"),
	}
	for i, pt := range plaintexts {
		_, err := vlt.Set(ctx, VariableInput{
			Name:      "VAR" + string(rune('A'+i)),
			Workspace: "wsp",
			Scope:     ScopeSecret,
			Plaintext: pt,
		})
		if err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	disk := vlt.SnapshotEncryptedDisk()
	for _, pt := range plaintexts {
		if bytes.Contains(disk, pt) {
			t.Fatalf("plaintext %q leaked into encrypted disk snapshot", pt)
		}
	}
}

func TestKEKRotationProducesNewWrap(t *testing.T) {
	ctx := context.Background()
	provider := newTestProvider(t)
	vlt := NewVault(provider, AllowAll{})

	plaintexts := [][]byte{
		[]byte("rotation-plaintext-A"),
		[]byte("rotation-plaintext-B"),
		[]byte("rotation-plaintext-C-which-is-very-long-indeed"),
	}
	var ids []VariableID
	for i, pt := range plaintexts {
		v, err := vlt.Set(ctx, VariableInput{
			Name:      "ROT" + string(rune('A'+i)),
			Workspace: "wsp",
			Scope:     ScopeSecret,
			Plaintext: pt,
		})
		if err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
		ids = append(ids, v.Meta().ID)
	}

	preActive, err := provider.ActiveKEK(ctx)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	for _, id := range ids {
		v, _ := vlt.Get(ctx, id)
		if v.envelope.KEKID != preActive {
			t.Fatalf("variable %s wrapped under %s, want %s", id, v.envelope.KEKID, preActive)
		}
	}

	report, err := vlt.RotateKEK(ctx)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if report.Rewrapped != len(ids) {
		t.Fatalf("rotation rewrapped %d, want %d", report.Rewrapped, len(ids))
	}
	if report.PreviousKEK != preActive {
		t.Fatalf("rotation previous KEK = %s want %s", report.PreviousKEK, preActive)
	}
	if report.NewKEK == preActive {
		t.Fatalf("rotation produced same KEK id")
	}
	for i, id := range ids {
		v, _ := vlt.Get(ctx, id)
		if v.envelope.KEKID == preActive {
			t.Fatalf("variable %s still wrapped under previous KEK", id)
		}
		plaintext, _, err := vlt.Decrypt(ctx, id, ScopeRuntimeInject)
		if err != nil {
			t.Fatalf("decrypt after rotate %d: %v", i, err)
		}
		if !bytes.Equal(plaintext, plaintexts[i]) {
			t.Fatalf("decrypt after rotate %d mismatch", i)
		}
	}
	// On-disk snapshot still contains zero plaintext after rotation.
	disk := vlt.SnapshotEncryptedDisk()
	for _, pt := range plaintexts {
		if bytes.Contains(disk, pt) {
			t.Fatalf("plaintext %q leaked after rotation", pt)
		}
	}
}

func TestReferenceCrossScopeAuthzDeniedByDefault(t *testing.T) {
	ctx := context.Background()
	auth := DenyByDefault{
		Reads: map[string]bool{
			"alice::svc:svc_consumer": true, // alice can read consumer scope only
		},
		Edges: map[string]bool{
			"alice::dev->dev": true,
		},
	}
	provider := newTestProvider(t)
	vlt := NewVault(provider, auth)

	target, err := vlt.Set(ctx, VariableInput{
		Name:        "PROD_SECRET",
		Workspace:   "wsp_a",
		Project:     "prj_a",
		Environment: "env_prod",
		Service:     "svc_target",
		Scope:       ScopeSealed,
		Plaintext:   []byte("prod-only-secret"),
	})
	if err != nil {
		t.Fatalf("set target: %v", err)
	}

	in := VariableInput{
		Name:        "REF_PROD_SECRET",
		Workspace:   "wsp_a",
		Project:     "prj_a",
		Environment: "env_dev",
		Service:     "svc_consumer",
		Scope:       ScopeSealed,
	}
	creator := Principal{ID: "alice", Type: "user", Scope: "svc:svc_consumer"}

	// Cross-tier dev->prod is denied by default Authorizer.
	if _, err := vlt.CreateReference(ctx, in, target.Meta().ID, creator, TierDev, TierProd); !errors.Is(err, ErrReferenceCrossTier) {
		t.Fatalf("dev->prod cross-tier got %v want ErrReferenceCrossTier", err)
	}

	// preview -> prod is permanently denied.
	if _, err := vlt.CreateReference(ctx, in, target.Meta().ID, creator, TierPreview, TierProd); !errors.Is(err, ErrReferenceCrossTier) {
		t.Fatalf("preview->prod permanent deny = %v", err)
	}

	// Cross-scope read forbidden: alice cannot read svc:svc_target.
	authAllowEdge := DenyByDefault{
		Reads: map[string]bool{},
		Edges: map[string]bool{"alice::dev->prod": true},
	}
	vlt2 := NewVault(provider, authAllowEdge)
	target2, err := vlt2.Set(ctx, VariableInput{
		Name:        "PROD_SECRET",
		Workspace:   "wsp_a",
		Project:     "prj_a",
		Environment: "env_prod",
		Service:     "svc_target",
		Scope:       ScopeSealed,
		Plaintext:   []byte("prod-only"),
	})
	if err != nil {
		t.Fatalf("set in vlt2: %v", err)
	}
	if _, err := vlt2.CreateReference(ctx, in, target2.Meta().ID, creator, TierDev, TierProd); !errors.Is(err, ErrReferenceForbidden) {
		t.Fatalf("cross-scope read got %v want ErrReferenceForbidden", err)
	}
}

func TestReferenceResolutionRequiresConsumerRead(t *testing.T) {
	ctx := context.Background()
	// Authorizer permits creation by alice but at resolution time the
	// consumer principal "deploy-bot" has NO read access.
	provider := newTestProvider(t)
	auth := DenyByDefault{
		Reads: map[string]bool{
			"alice::svc:svc_target": true,
		},
		Edges: map[string]bool{
			"alice::staging->staging": true,
		},
	}
	vlt := NewVault(provider, auth)

	target, err := vlt.Set(ctx, VariableInput{
		Name:        "STAGING_SECRET",
		Workspace:   "wsp",
		Project:     "prj",
		Environment: "env_staging",
		Service:     "svc_target",
		Scope:       ScopeSealed,
		Plaintext:   []byte("staging-secret"),
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	creator := Principal{ID: "alice", Type: "user", Scope: "svc:svc_target"}
	in := VariableInput{
		Name:        "REF_STAGING",
		Workspace:   "wsp",
		Project:     "prj",
		Environment: "env_staging",
		Service:     "svc_consumer",
		Scope:       ScopeSealed,
	}
	ref, err := vlt.CreateReference(ctx, in, target.Meta().ID, creator, TierStaging, TierStaging)
	if err != nil {
		t.Fatalf("create reference: %v", err)
	}

	// Consumer principal lacks any read permission.
	consumer := Principal{ID: "deploy-bot", Type: "service_account", Scope: "svc:svc_consumer"}
	if _, _, err := vlt.ResolveReference(ctx, ref.Meta().ID, consumer, ScopeRuntimeInject); !errors.Is(err, ErrReferenceForbidden) {
		t.Fatalf("resolve without consumer read = %v want ErrReferenceForbidden", err)
	}
}

func TestReferenceCycleDetected(t *testing.T) {
	ctx := context.Background()
	provider := newTestProvider(t)
	vlt := NewVault(provider, AllowAll{})

	// Create variables A and B.
	a, err := vlt.Set(ctx, VariableInput{Name: "A", Workspace: "w", Scope: ScopeSecret, Plaintext: []byte("a")})
	if err != nil {
		t.Fatalf("set a: %v", err)
	}
	b, err := vlt.Set(ctx, VariableInput{Name: "B", Workspace: "w", Scope: ScopeSecret, Plaintext: []byte("b")})
	if err != nil {
		t.Fatalf("set b: %v", err)
	}
	// Build a->b reference and forge a cycle by mutating b's refTo.
	ref, err := vlt.CreateReference(ctx, VariableInput{Name: "REF_A", Workspace: "w", Scope: ScopeSecret}, b.Meta().ID, Principal{ID: "sys"}, TierProd, TierProd)
	if err != nil {
		t.Fatalf("ref: %v", err)
	}
	// Manually rewire b -> ref to create a cycle (simulate corrupted state).
	vlt.mu.Lock()
	vlt.byID[b.Meta().ID].refTo = ref.Meta().ID
	vlt.mu.Unlock()
	_ = a
	if _, _, err := vlt.ResolveReference(ctx, ref.Meta().ID, Principal{ID: "sys"}, ScopeRuntimeInject); !errors.Is(err, ErrReferenceCycle) {
		t.Fatalf("expected cycle, got %v", err)
	}
}

func TestVaultOptimisticLifecycle(t *testing.T) {
	ctx := context.Background()
	vlt := newTestVault(t)
	input := VariableInput{
		Name:      "API_TOKEN",
		Workspace: "wsp_1",
		Project:   "prj_1",
		Service:   "svc_1",
		Scope:     ScopeSealed,
		Plaintext: []byte("alpha"),
		ValueKind: "literal",
	}
	created, err := vlt.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vlt.Create(ctx, input); !errors.Is(err, ErrVariableVersionConflict) {
		t.Fatalf("duplicate create error = %v, want ErrVariableVersionConflict", err)
	}

	input.Plaintext = []byte("bravo")
	updated, err := vlt.Update(ctx, created.Meta().ID, 1, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Meta().Version != 2 {
		t.Fatalf("version = %d, want 2", updated.Meta().Version)
	}
	input.Plaintext = []byte("stale")
	if _, err := vlt.Update(ctx, created.Meta().ID, 1, input); !errors.Is(err, ErrVariableVersionConflict) {
		t.Fatalf("stale update error = %v, want ErrVariableVersionConflict", err)
	}
	value, _, err := vlt.Decrypt(ctx, created.Meta().ID, ScopeRuntimeInject)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "bravo" {
		t.Fatalf("decrypted value = %q, want winning update", value)
	}
	if err := vlt.Delete(ctx, created.Meta().ID, 1); !errors.Is(err, ErrVariableVersionConflict) {
		t.Fatalf("stale delete error = %v, want ErrVariableVersionConflict", err)
	}
	if err := vlt.Delete(ctx, created.Meta().ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := vlt.Get(ctx, created.Meta().ID); !errors.Is(err, ErrVariableNotFound) {
		t.Fatalf("get after delete error = %v, want ErrVariableNotFound", err)
	}
}
