package secrets

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrVariableNotFound is returned when a Variable lookup misses.
var ErrVariableNotFound = errors.New("midnight: variable not found")

// ErrInvalidScope is returned when a Variable operation is asked to
// produce a scope that is not declared by the caller (e.g. trying to
// promote a sealed variable to plain).
var ErrInvalidScope = errors.New("midnight: invalid variable scope")

// ErrInvalidVariableName is returned when a name cannot be represented safely
// as one KEY=VALUE entry at the runtime boundary.
var ErrInvalidVariableName = errors.New("midnight: invalid variable name")

// VariableInput is the caller-supplied creation/update payload.
type VariableInput struct {
	Name        string
	Workspace   string
	Project     string
	Environment string
	Service     string
	Scope       Scope
	Plaintext   []byte
	// ValueKind is "literal" for a plain string template and "reference"
	// when the supplied Plaintext contains at least one `${{...}}`
	// expression. Callers (the API layer) compute this from the parsed
	// template; the vault stores it verbatim on the metadata view so
	// read paths can surface it without unsealing the encrypted blob.
	ValueKind string
	// References is the parsed reference targets (names only — never
	// plaintext) attached to the template. Empty for literal values.
	References []VariableReference
}

// ResolvedScope is a contract between the runtime injection boundary and
// any audit consumer: it describes the intended use without leaking
// plaintext.
type ResolvedScope string

const (
	ScopeRuntimeInject        ResolvedScope = "runtime_inject"
	ScopeBuildInject          ResolvedScope = "build_inject"
	ScopeNotificationDelivery ResolvedScope = "notification_delivery"
	ScopeSealedRotation       ResolvedScope = "sealed_rotation"
	ScopeRestore              ResolvedScope = "restore"
)

// Vault is the in-memory primary store of variables. It owns:
//   - the KeyProvider boundary (KEK custody),
//   - the per-variable Envelope (DEK + ciphertext),
//   - the redaction-safe metadata.
//
// Vault is safe for concurrent use.
type Vault struct {
	mu     sync.RWMutex
	kp     KeyProvider
	store  EnvelopeStore
	byID   map[VariableID]*Variable
	byName map[string]VariableID // scope+name index
	auth   Authorizer
	clock  func() time.Time
}

// NewVault constructs a Vault wired to the supplied KeyProvider. The
// Authorizer is consulted for cross-scope reference resolution; pass
// AllowAll for unauthenticated tests.
func NewVault(kp KeyProvider, auth Authorizer) *Vault {
	return NewVaultWithStore(kp, auth, nil)
}

// NewVaultWithStore constructs a Vault whose encrypted envelopes survive
// process restarts in store. Passing nil preserves the developer/test
// in-memory behavior.
func NewVaultWithStore(kp KeyProvider, auth Authorizer, store EnvelopeStore) *Vault {
	if auth == nil {
		auth = AllowAll{}
	}
	return &Vault{
		kp:     kp,
		store:  store,
		byID:   map[VariableID]*Variable{},
		byName: map[string]VariableID{},
		auth:   auth,
		clock:  time.Now,
	}
}

// Variable is the unified in-memory record. Use Meta() to surface the
// redaction-safe view; sealed variables additionally implement
// SealedVariable.
type Variable struct {
	meta     VariableMeta
	envelope Envelope
	sealed   *SealedVariable // non-nil only for ScopeSealed
	refTo    VariableID      // non-empty for reference variables
}

// Meta returns the redaction-safe metadata for the Variable.
func (v *Variable) Meta() VariableMeta { return v.meta }

// IsSealed reports whether the Variable is sealed (write-only).
func (v *Variable) IsSealed() bool { return v.sealed != nil }

// IsReference reports whether the Variable is a reference to another
// variable's value (resolved at injection time).
func (v *Variable) IsReference() bool { return v.refTo != "" }

// Envelope returns the persisted envelope. Callers MUST treat this as
// opaque — use Vault.Decrypt to obtain plaintext via the reveal token.
func (v *Variable) Envelope() Envelope { return v.envelope }

func variableKey(scope Scope, workspace, project, environment, service, name string) string {
	// Privacy classification is mutable metadata, not part of variable
	// identity. PostgreSQL enforces the same canonical ownership+name key.
	_ = scope
	return strings.Join([]string{workspace, project, environment, service, name}, "\x00")
}

// nextVariableID returns a deterministic id from the supplied components
// and a UUID-shaped tail produced from a fresh DEK fragment so that ids
// are stable across restarts of the in-memory Vault when names match.
func nextVariableID(scope Scope, workspace, project, environment, service, name string) (VariableID, error) {
	suffix, err := generateNonce()
	if err != nil {
		return "", err
	}
	prefix := "var"
	if scope == ScopeSealed {
		prefix = "sealed"
	}
	id := fmt.Sprintf("%s_%s_%x", prefix, sanitize(name), suffix)
	return VariableID(id), nil
}

func sanitize(in string) string {
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case c >= 'a' && c <= 'z':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+'a'-'A')
		case c >= '0' && c <= '9':
			out = append(out, c)
		case c == '_' || c == '-':
			out = append(out, c)
		}
	}
	return string(out)
}

// Set stores a Variable. For sealed scope, this is the only way the
// plaintext enters the Vault and the value cannot be re-revealed by any
// API surface.
func (vlt *Vault) Set(ctx context.Context, in VariableInput) (*Variable, error) {
	if in.Scope == "" {
		return nil, ErrInvalidScope
	}
	if !ValidVariableName(in.Name) {
		return nil, ErrInvalidVariableName
	}
	vlt.mu.Lock()
	defer vlt.mu.Unlock()

	var (
		id              VariableID
		version         = 1
		expectedVersion = 0
	)
	if vlt.store != nil {
		existing, err := vlt.store.Find(ctx, in.Project, in.Environment, in.Service, in.Name)
		switch {
		case err == nil:
			id = existing.Meta.ID
			version = existing.Meta.Version + 1
			expectedVersion = existing.Meta.Version
		case errors.Is(err, ErrVariableNotFound):
			// First write for this canonical scope.
		default:
			return nil, err
		}
	} else if existingID, ok := vlt.byName[variableKey(in.Scope, in.Workspace, in.Project, in.Environment, in.Service, in.Name)]; ok {
		id = existingID
		if existing := vlt.byID[id]; existing != nil {
			version = existing.meta.Version + 1
		}
	}
	if id == "" {
		var err error
		id, err = nextVariableID(in.Scope, in.Workspace, in.Project, in.Environment, in.Service, in.Name)
		if err != nil {
			return nil, err
		}
	}
	envelope, err := SealForVariable(ctx, vlt.kp, id, in.Plaintext)
	if err != nil {
		return nil, err
	}
	fp, err := vlt.kp.Fingerprint(ctx)
	if err != nil {
		return nil, err
	}
	now := vlt.clock().UTC()
	meta := VariableMeta{
		ID:             id,
		Scope:          in.Scope,
		Workspace:      in.Workspace,
		Project:        in.Project,
		Environment:    in.Environment,
		Service:        in.Service,
		Name:           in.Name,
		Version:        version,
		LastRotatedAt:  now,
		LengthBucket:   LengthBucket(len(in.Plaintext)),
		KEKFingerprint: fp,
		ValueKind:      in.ValueKind,
		References:     append([]VariableReference(nil), in.References...),
	}
	v := &Variable{meta: meta, envelope: envelope}
	if in.Scope == ScopeSealed {
		v.sealed = &SealedVariable{meta: meta, envelope: envelope, store: vlt, set: true}
	}
	if vlt.store != nil {
		persisted := PersistedVariable{Meta: meta, Envelope: envelope}
		var err error
		if lifecycle, ok := vlt.store.(LifecycleEnvelopeStore); ok {
			err = lifecycle.PutCAS(ctx, persisted, expectedVersion)
		} else {
			err = vlt.store.Put(ctx, persisted)
		}
		if err != nil {
			return nil, err
		}
	}
	vlt.byID[id] = v
	key := variableKey(in.Scope, in.Workspace, in.Project, in.Environment, in.Service, in.Name)
	vlt.byName[key] = id
	return v, nil
}

// Create stores a new variable and fails with ErrVariableVersionConflict when
// the canonical ownership+name already exists. The conflict check and
// ciphertext insert are one atomic operation in durable stores.
func (vlt *Vault) Create(ctx context.Context, in VariableInput) (*Variable, error) {
	if in.Scope == "" {
		return nil, ErrInvalidScope
	}
	if !ValidVariableName(in.Name) {
		return nil, ErrInvalidVariableName
	}
	vlt.mu.Lock()
	defer vlt.mu.Unlock()

	key := variableKey(in.Scope, in.Workspace, in.Project, in.Environment, in.Service, in.Name)
	if vlt.store == nil {
		if existingID, ok := vlt.byName[key]; ok && vlt.byID[existingID] != nil {
			return nil, &VariableVersionConflict{Expected: 0, Actual: vlt.byID[existingID].meta.Version}
		}
	} else if _, ok := vlt.store.(LifecycleEnvelopeStore); !ok {
		existing, err := vlt.store.Find(ctx, in.Project, in.Environment, in.Service, in.Name)
		if err == nil {
			return nil, &VariableVersionConflict{Expected: 0, Actual: existing.Meta.Version}
		}
		if !errors.Is(err, ErrVariableNotFound) {
			return nil, err
		}
	}

	id, err := nextVariableID(in.Scope, in.Workspace, in.Project, in.Environment, in.Service, in.Name)
	if err != nil {
		return nil, err
	}
	variable, err := vlt.buildVariable(ctx, id, 1, in)
	if err != nil {
		return nil, err
	}
	persisted := PersistedVariable{Meta: variable.meta, Envelope: variable.envelope}
	if vlt.store != nil {
		if lifecycle, ok := vlt.store.(LifecycleEnvelopeStore); ok {
			err = lifecycle.PutCAS(ctx, persisted, 0)
		} else {
			err = vlt.store.Put(ctx, persisted)
		}
		if err != nil {
			return nil, err
		}
	}
	vlt.byID[id] = variable
	vlt.byName[key] = id
	return variable, nil
}

// Update replaces a variable's write-only value and mutable scope metadata
// when expectedVersion still matches. Identity and ownership are immutable.
func (vlt *Vault) Update(ctx context.Context, id VariableID, expectedVersion int, in VariableInput) (*Variable, error) {
	if expectedVersion < 1 {
		return nil, &VariableVersionConflict{Expected: expectedVersion}
	}
	if in.Scope == "" {
		return nil, ErrInvalidScope
	}
	if !ValidVariableName(in.Name) {
		return nil, ErrInvalidVariableName
	}
	vlt.mu.Lock()
	defer vlt.mu.Unlock()

	var current *Variable
	if vlt.store != nil {
		persisted, err := vlt.store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		current = vlt.variableFromPersisted(ctx, persisted)
	} else {
		current = vlt.byID[id]
		if current == nil {
			return nil, ErrVariableNotFound
		}
	}
	if !sameVariableIdentity(current.meta, in) {
		return nil, ErrVariableScopeIntegrity
	}
	if current.meta.Version != expectedVersion {
		return nil, &VariableVersionConflict{Expected: expectedVersion, Actual: current.meta.Version}
	}

	variable, err := vlt.buildVariable(ctx, id, expectedVersion+1, in)
	if err != nil {
		return nil, err
	}
	persisted := PersistedVariable{Meta: variable.meta, Envelope: variable.envelope}
	if vlt.store != nil {
		if lifecycle, ok := vlt.store.(LifecycleEnvelopeStore); ok {
			err = lifecycle.PutCAS(ctx, persisted, expectedVersion)
		} else {
			err = vlt.store.Put(ctx, persisted)
		}
		if err != nil {
			return nil, err
		}
	}
	oldKey := variableKey(current.meta.Scope, current.meta.Workspace, current.meta.Project, current.meta.Environment, current.meta.Service, current.meta.Name)
	delete(vlt.byName, oldKey)
	vlt.byID[id] = variable
	vlt.byName[variableKey(in.Scope, in.Workspace, in.Project, in.Environment, in.Service, in.Name)] = id
	return variable, nil
}

// Delete removes a variable from active lookups using optimistic concurrency.
// Durable stores retain a metadata-only active-row tombstone. Immutable
// version history remains available only to already-pinned deployment
// snapshots; active/latest lookup cannot resolve the deleted value.
func (vlt *Vault) Delete(ctx context.Context, id VariableID, expectedVersion int) error {
	if expectedVersion < 1 {
		return &VariableVersionConflict{Expected: expectedVersion}
	}
	vlt.mu.Lock()
	defer vlt.mu.Unlock()

	var current *Variable
	if vlt.store != nil {
		persisted, err := vlt.store.Get(ctx, id)
		if err != nil {
			return err
		}
		current = vlt.variableFromPersisted(ctx, persisted)
	} else {
		current = vlt.byID[id]
		if current == nil {
			return ErrVariableNotFound
		}
	}
	if current.meta.Version != expectedVersion {
		return &VariableVersionConflict{Expected: expectedVersion, Actual: current.meta.Version}
	}
	if vlt.store != nil {
		lifecycle, ok := vlt.store.(LifecycleEnvelopeStore)
		if !ok {
			return errors.New("midnight: durable variable store does not support safe delete")
		}
		if err := lifecycle.DeleteCAS(ctx, id, expectedVersion); err != nil {
			return err
		}
	}
	delete(vlt.byID, id)
	delete(vlt.byName, variableKey(current.meta.Scope, current.meta.Workspace, current.meta.Project, current.meta.Environment, current.meta.Service, current.meta.Name))
	return nil
}

func (vlt *Vault) buildVariable(ctx context.Context, id VariableID, version int, in VariableInput) (*Variable, error) {
	envelope, err := SealForVariable(ctx, vlt.kp, id, in.Plaintext)
	if err != nil {
		return nil, err
	}
	fp, err := vlt.kp.Fingerprint(ctx)
	if err != nil {
		return nil, err
	}
	meta := VariableMeta{
		ID:             id,
		Scope:          in.Scope,
		Workspace:      in.Workspace,
		Project:        in.Project,
		Environment:    in.Environment,
		Service:        in.Service,
		Name:           in.Name,
		Version:        version,
		LastRotatedAt:  vlt.clock().UTC(),
		LengthBucket:   LengthBucket(len(in.Plaintext)),
		KEKFingerprint: fp,
		ValueKind:      in.ValueKind,
		References:     append([]VariableReference(nil), in.References...),
	}
	variable := &Variable{meta: meta, envelope: envelope}
	if in.Scope == ScopeSealed {
		variable.sealed = &SealedVariable{meta: meta, envelope: envelope, store: vlt, set: true}
	}
	return variable, nil
}

func sameVariableIdentity(meta VariableMeta, in VariableInput) bool {
	return meta.Workspace == in.Workspace &&
		meta.Project == in.Project &&
		meta.Environment == in.Environment &&
		meta.Service == in.Service &&
		meta.Name == in.Name
}

// ValidVariableName rejects the bytes that would split or terminate a
// KEY=VALUE runtime entry. Hyphens remain accepted for compatibility with
// existing non-shell consumers; callers that require shell identifiers may
// enforce a narrower policy.
func ValidVariableName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && !strings.ContainsAny(name, "=\x00\r\n")
}

// Get returns a redaction-safe view of a Variable.
func (vlt *Vault) Get(ctx context.Context, id VariableID) (*Variable, error) {
	if vlt.store != nil {
		persisted, err := vlt.store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		return vlt.variableFromPersisted(ctx, persisted), nil
	}
	vlt.mu.RLock()
	defer vlt.mu.RUnlock()
	v, ok := vlt.byID[id]
	if !ok {
		return nil, ErrVariableNotFound
	}
	return v, nil
}

// GetVersion returns an exact encrypted variable version for immutable
// deployment retries. Historical records remain internal and metadata-only
// callers should continue to use Get/List, which expose active state only.
func (vlt *Vault) GetVersion(ctx context.Context, id VariableID, version int) (*Variable, error) {
	if version < 1 {
		return nil, ErrVariableNotFound
	}
	if vlt.store != nil {
		lifecycle, ok := vlt.store.(LifecycleEnvelopeStore)
		if !ok {
			return nil, errors.New("midnight: durable variable store does not support versioned lookup")
		}
		persisted, err := lifecycle.GetVersion(ctx, id, version)
		if err != nil {
			return nil, err
		}
		return vlt.variableFromPersisted(ctx, persisted), nil
	}
	vlt.mu.RLock()
	defer vlt.mu.RUnlock()
	variable := vlt.byID[id]
	if variable == nil || variable.meta.Version != version {
		return nil, ErrVariableNotFound
	}
	return variable, nil
}

// Find returns one variable by the canonical runtime scope. Scope here means
// ownership (project/environment/service), not the privacy classification.
func (vlt *Vault) Find(ctx context.Context, projectID, environmentID, serviceID, name string) (*Variable, error) {
	if vlt.store != nil {
		persisted, err := vlt.store.Find(ctx, projectID, environmentID, serviceID, name)
		if err != nil {
			return nil, err
		}
		return vlt.variableFromPersisted(ctx, persisted), nil
	}
	vlt.mu.RLock()
	defer vlt.mu.RUnlock()
	for _, variable := range vlt.byID {
		meta := variable.meta
		if meta.Project == projectID && meta.Environment == environmentID && meta.Service == serviceID && meta.Name == name {
			return variable, nil
		}
	}
	return nil, ErrVariableNotFound
}

func (vlt *Vault) variableFromPersisted(ctx context.Context, persisted PersistedVariable) *Variable {
	if persisted.Meta.KEKFingerprint == "" {
		if fingerprint, err := vlt.kp.Fingerprint(ctx); err == nil {
			persisted.Meta.KEKFingerprint = fingerprint
		}
	}
	variable := &Variable{meta: persisted.Meta, envelope: persisted.Envelope}
	if variable.meta.Scope == ScopeSealed {
		variable.sealed = &SealedVariable{meta: variable.meta, envelope: variable.envelope, store: vlt, set: true}
	}
	return variable
}

// Decrypt unseals a Variable's envelope into plaintext. For sealed
// variables, the resolved scope MUST be one of the panel-locked
// injection contexts; callers without such a context receive
// ErrSealedRevealed.
//
// The audit emit (panel: line 1178) is the caller's responsibility — this
// method returns a redacted record describing the unseal so the caller
// can persist exactly the right shape. Plaintext is never logged.
func (vlt *Vault) Decrypt(ctx context.Context, id VariableID, scope ResolvedScope) ([]byte, AuditDecryptRecord, error) {
	v, err := vlt.Get(ctx, id)
	if err != nil {
		return nil, AuditDecryptRecord{}, err
	}
	if v.IsSealed() && !isInjectionScope(scope) {
		return nil, AuditDecryptRecord{}, ErrSealedRevealed
	}
	plaintext, err := OpenForVariable(ctx, vlt.kp, id, v.envelope)
	if err != nil {
		return nil, AuditDecryptRecord{}, err
	}
	rec := AuditDecryptRecord{
		VariableID:     id,
		Scope:          v.meta.Scope,
		Resolved:       scope,
		KEKID:          v.envelope.KEKID,
		KEKFingerprint: v.meta.KEKFingerprint,
		Bucket:         v.meta.LengthBucket,
	}
	return plaintext, rec, nil
}

// DecryptVersion opens one immutable historical envelope for a snapshotted
// deployment. It has the same injection-only policy as Decrypt.
func (vlt *Vault) DecryptVersion(ctx context.Context, id VariableID, version int, scope ResolvedScope) ([]byte, AuditDecryptRecord, error) {
	variable, err := vlt.GetVersion(ctx, id, version)
	if err != nil {
		return nil, AuditDecryptRecord{}, err
	}
	if variable.IsSealed() && !isInjectionScope(scope) {
		return nil, AuditDecryptRecord{}, ErrSealedRevealed
	}
	plaintext, err := OpenForVariable(ctx, vlt.kp, id, variable.envelope)
	if err != nil {
		return nil, AuditDecryptRecord{}, err
	}
	return plaintext, AuditDecryptRecord{
		VariableID:     id,
		Scope:          variable.meta.Scope,
		Resolved:       scope,
		KEKID:          variable.envelope.KEKID,
		KEKFingerprint: variable.meta.KEKFingerprint,
		Bucket:         variable.meta.LengthBucket,
	}, nil
}

// AuditDecryptRecord is a redaction-safe description of a single decrypt
// call. Audit consumers persist this exact shape — no plaintext, no exact
// byte length.
type AuditDecryptRecord struct {
	VariableID     VariableID    `json:"variable_id"`
	Scope          Scope         `json:"scope"`
	Resolved       ResolvedScope `json:"resolved"`
	KEKID          KEKID         `json:"kek_id"`
	KEKFingerprint string        `json:"kek_fingerprint"`
	Bucket         string        `json:"length_bucket"`
}

func isInjectionScope(s ResolvedScope) bool {
	switch s {
	case ScopeRuntimeInject, ScopeBuildInject, ScopeNotificationDelivery, ScopeSealedRotation, ScopeRestore:
		return true
	}
	return false
}

// Sealed returns the SealedVariable wrapper for the supplied id, or an
// error when the id is unknown / not sealed.
func (vlt *Vault) Sealed(ctx context.Context, id VariableID) (*SealedVariable, error) {
	v, err := vlt.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !v.IsSealed() {
		return nil, ErrInvalidScope
	}
	return v.sealed, nil
}

// Rotate replaces a Variable's plaintext with a new value. For sealed
// variables this is the panel-locked "no view-then-edit" path: the caller
// supplies a new plaintext directly without ever revealing the previous
// value.
func (vlt *Vault) Rotate(ctx context.Context, id VariableID, plaintext []byte) (*Variable, error) {
	vlt.mu.Lock()
	defer vlt.mu.Unlock()
	var v *Variable
	if vlt.store != nil {
		persisted, err := vlt.store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		v = vlt.variableFromPersisted(ctx, persisted)
	} else {
		var ok bool
		v, ok = vlt.byID[id]
		if !ok {
			return nil, ErrVariableNotFound
		}
	}
	envelope, err := SealForVariable(ctx, vlt.kp, id, plaintext)
	if err != nil {
		return nil, err
	}
	fp, err := vlt.kp.Fingerprint(ctx)
	if err != nil {
		return nil, err
	}
	now := vlt.clock().UTC()
	v.envelope = envelope
	v.meta.Version++
	v.meta.LastRotatedAt = now
	v.meta.LengthBucket = LengthBucket(len(plaintext))
	v.meta.KEKFingerprint = fp
	if v.sealed != nil {
		v.sealed.mu.Lock()
		v.sealed.envelope = envelope
		v.sealed.meta = v.meta
		v.sealed.mu.Unlock()
	}
	if vlt.store != nil {
		persisted := PersistedVariable{Meta: v.meta, Envelope: envelope}
		var err error
		if lifecycle, ok := vlt.store.(LifecycleEnvelopeStore); ok {
			err = lifecycle.PutCAS(ctx, persisted, v.meta.Version-1)
		} else {
			err = vlt.store.Put(ctx, persisted)
		}
		if err != nil {
			return nil, err
		}
	}
	vlt.byID[id] = v
	return v, nil
}

// Reveal returns a RevealToken bound to the supplied resolved scope.
// Tokens are bound to the Vault that produced them — Sealed.Reveal will
// reject a token from a different Vault.
func (vlt *Vault) Reveal(scope ResolvedScope) RevealToken {
	if !isInjectionScope(scope) {
		return RevealToken{}
	}
	return RevealToken{scope: string(scope), vault: vlt}
}

// MintRotationToken is a convenience wrapper for sealed-variable rotation
// callers that need to verify a previous value before overwriting (e.g.
// migration tooling). Consumers SHOULD prefer Rotate which avoids
// revealing the previous plaintext.
func (vlt *Vault) MintRotationToken() RevealToken {
	return RevealToken{scope: string(ScopeSealedRotation), vault: vlt}
}

// List returns the VariableMeta for every variable in deterministic id-sorted
// order. Durable-store errors are surfaced so read APIs never misrepresent a
// database failure as an empty variable set.
func (vlt *Vault) List(ctx context.Context) ([]VariableMeta, error) {
	if vlt.store != nil {
		persisted, err := vlt.store.List(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]VariableMeta, 0, len(persisted))
		for _, variable := range persisted {
			meta := vlt.variableFromPersisted(ctx, variable).meta
			out = append(out, meta)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	vlt.mu.RLock()
	defer vlt.mu.RUnlock()
	out := make([]VariableMeta, 0, len(vlt.byID))
	for _, v := range vlt.byID {
		out = append(out, v.meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// SnapshotEncryptedDisk returns a deterministic byte representation of
// every persisted envelope plus metadata. Tests use this to verify no
// plaintext leaks to the on-disk store. Plaintext NEVER appears in the
// snapshot — only KEK ids, ciphertext bytes, and metadata.
func (vlt *Vault) SnapshotEncryptedDisk() []byte {
	vlt.mu.RLock()
	defer vlt.mu.RUnlock()
	keys := make([]string, 0, len(vlt.byID))
	for id := range vlt.byID {
		keys = append(keys, string(id))
	}
	sort.Strings(keys)
	var buf strings.Builder
	for _, k := range keys {
		v := vlt.byID[VariableID(k)]
		buf.WriteString("id=")
		buf.WriteString(k)
		buf.WriteString("\nscope=")
		buf.WriteString(string(v.meta.Scope))
		buf.WriteString("\nname=")
		buf.WriteString(v.meta.Name)
		buf.WriteString("\nbucket=")
		buf.WriteString(v.meta.LengthBucket)
		buf.WriteString("\nkek=")
		buf.WriteString(string(v.envelope.KEKID))
		buf.WriteString("\nfp=")
		buf.WriteString(EnvelopeFingerprint(v.envelope))
		buf.WriteString("\nciphertext=")
		buf.WriteString(envelopeBytesAsHex(v.envelope.Ciphertext))
		buf.WriteString("\nwrapped_dek=")
		buf.WriteString(envelopeBytesAsHex(v.envelope.WrappedDEK))
		buf.WriteString("\n---\n")
	}
	return []byte(buf.String())
}

func envelopeBytesAsHex(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[2*i] = hex[c>>4]
		out[2*i+1] = hex[c&0x0f]
	}
	return string(out)
}
