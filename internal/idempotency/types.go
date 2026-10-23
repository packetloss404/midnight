// Package idempotency contains the request-reservation and replay primitives
// used by every mutating /api/v1alpha endpoint.
//
// The contract is panel-locked in
// agile/engineering/contracts/OPERATION_AND_IDEMPOTENCY_CONTRACT.md. This
// package implements the shared state matrix for the in-memory and Postgres
// stores; production wiring uses Postgres through the same Store interface.
package idempotency

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Key is an opaque client-supplied idempotency token (1..255 visible ASCII).
type Key string

// Actor is the stable server-derived principal namespace for an idempotency
// key. It must never contain a bearer/session credential or another
// client-supplied authentication secret.
type Actor string

// Tenant is the server-resolved authorization boundary for a mutation. Human
// writes use a canonical workspace ID. Installation-wide operator/developer
// workflows use TenantInstallation until they are migrated to a narrower
// product scope.
type Tenant string

const (
	// ActorLegacyOperator preserves the pre-tenant namespace for internal
	// callers that have not attached an authenticated principal to context.
	ActorLegacyOperator Actor = "legacy:operator"
	// ActorRecoveryOperator is the installation-wide recovery principal.
	ActorRecoveryOperator Actor = "operator:recovery"
	// ActorLocalDeveloper is the unauthenticated memory-mode development
	// principal. Production mode never assigns it.
	ActorLocalDeveloper Actor = "developer:local"
	// TenantInstallation is the explicit legacy/install-wide namespace.
	TenantInstallation Tenant = "installation:default"
)

type actorContextKey struct{}
type tenantContextKey struct{}

// WithActor binds a server-derived principal to all idempotency operations
// performed with ctx. Empty actors are ignored so stores retain their explicit
// legacy namespace instead of creating an unscoped row.
func WithActor(ctx context.Context, actor Actor) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if actor == "" {
		return ctx
	}
	return context.WithValue(ctx, actorContextKey{}, actor)
}

// UserActor returns the durable namespace for a verified human user ID.
func UserActor(userID string) Actor {
	if userID == "" {
		return ""
	}
	return Actor("user:" + userID)
}

// ActorFromContext returns the bound actor or the explicit legacy namespace.
// The fallback keeps existing internal/operator workflows compatible while
// ensuring PostgreSQL rows never use an empty actor.
func ActorFromContext(ctx context.Context) Actor {
	if ctx != nil {
		if actor, ok := ctx.Value(actorContextKey{}).(Actor); ok && actor != "" {
			return actor
		}
	}
	return ActorLegacyOperator
}

// WithTenant binds a canonical, server-resolved tenant to idempotency work.
func WithTenant(ctx context.Context, tenant Tenant) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tenant == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantContextKey{}, tenant)
}

// TenantFromContext returns the canonical tenant or the explicit
// installation-wide compatibility namespace.
func TenantFromContext(ctx context.Context) Tenant {
	if ctx != nil {
		if tenant, ok := ctx.Value(tenantContextKey{}).(Tenant); ok && tenant != "" {
			return tenant
		}
	}
	return TenantInstallation
}

// ErrInvalidKey is returned when the supplied Idempotency-Key is empty,
// over-long, or contains non-printable / whitespace characters.
var ErrInvalidKey = errors.New("invalid idempotency key")

// ErrKeyReused is returned when the same key is replayed against the
// scope but with a different body hash while the original is still in
// flight.
var ErrKeyReused = errors.New("idempotency key reused with different request")

// ErrKeyReusedDifferentBody is returned when a terminal-state replay is
// attempted with a different body hash than the original request.
var ErrKeyReusedDifferentBody = errors.New("idempotency key reused with different body")

// ErrRequestInProgress is returned for concurrent duplicate requests
// (same scope, same key) while the original is still pending.
var ErrRequestInProgress = errors.New("idempotency request in progress")

// ErrReservationNotFound is returned by Load/AttachOperation when the
// supplied key is unknown or has expired.
var ErrReservationNotFound = errors.New("idempotency reservation not found")

// ErrReservationFenced is returned when a transition presents an expired,
// superseded, or otherwise mismatched generation reference. Callers must never
// retry the transition against a freshly loaded generation: the original
// request no longer owns that reservation.
var ErrReservationFenced = fmt.Errorf("idempotency reservation fenced: %w", ErrReservationNotFound)

// ErrInvalidFingerprint is returned when a caller does not supply the stable
// HTTP method, route template, or request-body digest that forms the server
// owned portion of the idempotency scope.
var ErrInvalidFingerprint = errors.New("invalid idempotency fingerprint")

// ErrSensitiveResponseUnavailable is returned when an encrypted terminal
// response cannot be sealed or authenticated with the configured key
// provider. Callers must fail closed; they must never retry completion through
// the plaintext response path or treat an unavailable replay as a fresh
// mutation.
var ErrSensitiveResponseUnavailable = errors.New("sensitive idempotency response is unavailable")

// State enumerates the lifecycle states of an idempotency reservation. The
// transitions follow the panel-locked state matrix:
//
//	StateReserved -> StateRunning -> {StateSucceeded, StateFailed}
//	StateExpired is retained attempt history; a fresh generation may reuse the
//	same logical scope while late holders of the expired generation are fenced.
type State string

const (
	// StateReserved is the initial state — the row exists but no work has
	// completed.
	StateReserved State = "RESERVED"
	// StateRunning indicates an operation has been attached and the server
	// has acknowledged the work.
	StateRunning State = "RUNNING"
	// StateSucceeded is a terminal state for 2xx outcomes.
	StateSucceeded State = "SUCCEEDED"
	// StateFailed is a terminal state for stored 4xx/5xx outcomes.
	StateFailed State = "FAILED"
	// StateExpired is the post-TTL historical state. Reserve ignores it when
	// creating a fresh generation, while transitions against it fail fenced.
	StateExpired State = "EXPIRED"
)

// RequestFingerprint captures the (method, path_template, body_hash) tuple
// that scopes a key. The token id is supplied separately by the caller.
type RequestFingerprint struct {
	Method     string
	Path       string
	BodySHA256 string
}

// Equal reports whether two fingerprints match exactly.
func (f RequestFingerprint) Equal(other RequestFingerprint) bool {
	return f.Method == other.Method && f.Path == other.Path && f.BodySHA256 == other.BodySHA256
}

// ValidateFingerprint rejects dynamic/empty scopes before they can collapse
// otherwise independent requests into the same reservation namespace.
func ValidateFingerprint(fingerprint RequestFingerprint) error {
	if fingerprint.Method == "" || fingerprint.Path == "" || len(fingerprint.BodySHA256) != sha256.Size*2 {
		return ErrInvalidFingerprint
	}
	for _, r := range fingerprint.BodySHA256 {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return ErrInvalidFingerprint
		}
	}
	return nil
}

// ValidateKey enforces the visible-ASCII, 1..255-character contract.
func ValidateKey(key Key) error {
	if len(key) == 0 || len(key) > 255 {
		return ErrInvalidKey
	}
	for _, r := range string(key) {
		if r <= ' ' || r > '~' {
			return ErrInvalidKey
		}
	}
	return nil
}

// BodyHashSHA256 hashes the supplied bytes. Phase 0 uses raw bytes; Phase 1+
// must canonicalize the body via RFC 8785 JCS before calling this helper.
func BodyHashSHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// BodyHashSHA256Reader streams a (typically very large) request body
// through SHA-256 without buffering. The reader is consumed; the caller
// must rewind if the bytes are needed again.
func BodyHashSHA256Reader(r io.Reader) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// StoredResponse is a captured terminal response replayed verbatim on a
// matching idempotent retry.
type StoredResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Clone returns a deep copy so callers cannot mutate stored bytes.
func (r StoredResponse) Clone() StoredResponse {
	cloned := StoredResponse{
		StatusCode: r.StatusCode,
		Header:     make(http.Header, len(r.Header)),
		Body:       append([]byte(nil), r.Body...),
	}
	for key, values := range r.Header {
		cloned.Header[key] = append([]string(nil), values...)
	}
	return cloned
}

// Reservation is the in-memory representation of an idempotency-key row.
type Reservation struct {
	Key         Key
	Actor       Actor
	Tenant      Tenant
	Generation  string
	State       State
	Fingerprint RequestFingerprint
	OperationID string
	Response    *StoredResponse
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Reference is the immutable fencing identity returned by Reserve. Every
// state transition must present it so a late request cannot mutate a newer
// reservation that reused the same client key after expiry.
type Reference struct {
	Key        Key
	Actor      Actor
	Tenant     Tenant
	Method     string
	Path       string
	BodySHA256 string
	Generation string
}

// Reference returns the transition fence for this reservation.
func (r Reservation) Reference() Reference {
	return Reference{
		Key:        r.Key,
		Actor:      r.Actor,
		Tenant:     r.Tenant,
		Method:     r.Fingerprint.Method,
		Path:       r.Fingerprint.Path,
		BodySHA256: r.Fingerprint.BodySHA256,
		Generation: r.Generation,
	}
}

// ValidateReference verifies both the immutable fence shape and the current
// server-derived actor/tenant binding. A caller cannot transition a
// reservation merely by presenting a reference obtained in another principal
// or tenant context.
func ValidateReference(ctx context.Context, ref Reference) error {
	if err := ValidateKey(ref.Key); err != nil {
		return err
	}
	if ref.Actor == "" || ref.Tenant == "" || ref.Generation == "" {
		return ErrReservationFenced
	}
	if err := ValidateFingerprint(RequestFingerprint{
		Method: ref.Method, Path: ref.Path, BodySHA256: ref.BodySHA256,
	}); err != nil {
		return ErrReservationFenced
	}
	if ref.Actor != ActorFromContext(ctx) || ref.Tenant != TenantFromContext(ctx) {
		return ErrReservationFenced
	}
	return nil
}

// IsTerminal reports whether the reservation has stored a final response.
func (r Reservation) IsTerminal() bool {
	switch r.State {
	case StateSucceeded, StateFailed:
		return true
	default:
		return false
	}
}

// HasOperation reports whether an operation id has been attached.
func (r Reservation) HasOperation() bool { return r.OperationID != "" }

// Store is the panel-locked Phase 1.5 contract for idempotency
// reservations. The interface is implementation-agnostic so the in-memory
// MemoryStore can be swapped for a Postgres-backed equivalent without any
// caller-side change. The verb pairing
// `Get`/`Put`/`CompleteSuccess`/`CompleteFailure` mirrors the S03 brief
// while preserving the original panel-locked
// `Reserve`/`Load`/`AttachOperation` names that capture the precise state
// transition each call performs.
type Store interface {
	// Get is a thin alias for Load surfaced for the S03 Phase 1.5
	// `Get/Put/CompleteSuccess/CompleteFailure` interface vocabulary.
	Get(ctx context.Context, key Key, fingerprint RequestFingerprint) (Reservation, error)
	// Put is a thin alias for Reserve surfaced for the S03 Phase 1.5
	// `Get/Put/CompleteSuccess/CompleteFailure` interface vocabulary.
	Put(ctx context.Context, key Key, fingerprint RequestFingerprint, ttl time.Duration) (Reservation, error)
	// Reserve atomically inserts or surfaces an existing reservation. The
	// returned Reservation reflects the post-call state.
	Reserve(ctx context.Context, key Key, fingerprint RequestFingerprint, ttl time.Duration) (Reservation, error)
	// Load returns the current reservation for the supplied key.
	Load(ctx context.Context, key Key, fingerprint RequestFingerprint) (Reservation, error)
	// AttachOperation transitions a reservation from RESERVED to RUNNING and
	// records the bound operation id.
	AttachOperation(ctx context.Context, ref Reference, operationID string) (Reservation, error)
	// CompleteSuccess transitions a reservation to SUCCEEDED and stores the
	// final response for verbatim replay.
	CompleteSuccess(ctx context.Context, ref Reference, response StoredResponse) (Reservation, error)
	// CompleteFailure transitions a reservation to FAILED and stores the
	// final response for verbatim replay.
	CompleteFailure(ctx context.Context, ref Reference, response StoredResponse) (Reservation, error)
}

// SensitiveResponseStore is the opt-in extension for responses containing
// one-time credentials or other secret material. Implementations persist only
// an authenticated envelope and return plaintext solely to the authorized
// caller that supplied the exact actor/tenant reservation scope.
type SensitiveResponseStore interface {
	Store
	CompleteSensitiveSuccess(ctx context.Context, ref Reference, response StoredResponse) (Reservation, error)
	CompleteSensitiveFailure(ctx context.Context, ref Reference, response StoredResponse) (Reservation, error)
}

func newGeneration() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
