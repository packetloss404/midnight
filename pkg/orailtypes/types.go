// Package orailtypes contains public resource and API types shared by clients and the agent.
package orailtypes

import (
	"errors"
	"time"
)

// ID is a public resource identifier. Phase 0 uses ULID-shaped opaque
// strings; the prefix taxonomy is locked in ADR-0001.
type ID string

// ResourceVersion is an opaque optimistic-concurrency token returned in
// resource bodies and ETag headers.
type ResourceVersion string

// IDPrefix is one of the locked ULID prefixes from ADR-0001.
type IDPrefix string

// Locked Phase 0 ULID prefixes. New prefixes require an ADR amendment.
const (
	IDPrefixProject        IDPrefix = "prj"
	IDPrefixEnvironment    IDPrefix = "env"
	IDPrefixService        IDPrefix = "svc"
	IDPrefixDeployment     IDPrefix = "dep"
	IDPrefixOperation      IDPrefix = "op"
	IDPrefixJob            IDPrefix = "job"
	IDPrefixDomain         IDPrefix = "dom"
	IDPrefixVolume         IDPrefix = "vol"
	IDPrefixVariable       IDPrefix = "var"
	IDPrefixToken          IDPrefix = "tok"
	IDPrefixUser           IDPrefix = "usr"
	IDPrefixServiceAccount IDPrefix = "sa"
	IDPrefixTemplate       IDPrefix = "tpl"
	IDPrefixEvent          IDPrefix = "evt"
	IDPrefixIdempotency    IDPrefix = "idem"
	IDPrefixPlan           IDPrefix = "plan"
	IDPrefixInstance       IDPrefix = "inst"
)

type DeploymentKind string

const (
	DeploymentKindWeb            DeploymentKind = "web"
	DeploymentKindService        DeploymentKind = DeploymentKindWeb
	DeploymentKindStatic         DeploymentKind = "static"
	DeploymentKindCronInvocation DeploymentKind = "cron-invocation"
	DeploymentKindPredeploy      DeploymentKind = "predeploy"
	DeploymentKindOneOff         DeploymentKind = "oneoff"
	DeploymentKindRunOnce        DeploymentKind = DeploymentKindOneOff
)

type DeploymentState string

const (
	DeploymentQueued     DeploymentState = "QUEUED"
	DeploymentBuilding   DeploymentState = "BUILDING"
	DeploymentDeploying  DeploymentState = "DEPLOYING"
	DeploymentRunningJob DeploymentState = "RUNNING_JOB"
	DeploymentActive     DeploymentState = "ACTIVE"
	DeploymentSuperseded DeploymentState = "SUPERSEDED"
	DeploymentFailed     DeploymentState = "FAILED"
	DeploymentCanceling  DeploymentState = "CANCELING"
	DeploymentCanceled   DeploymentState = "CANCELED"
	DeploymentRemoving   DeploymentState = "REMOVING"
	DeploymentRemoved    DeploymentState = "REMOVED"
)

type OperationState string

const (
	OperationQueued    OperationState = "QUEUED"
	OperationPending                  = OperationQueued
	OperationRunning   OperationState = "RUNNING"
	OperationSucceeded OperationState = "SUCCEEDED"
	OperationFailed    OperationState = "FAILED"
	OperationCanceled  OperationState = "CANCELED"
)

type FailureCode string

type ErrorEnvelope struct {
	Code      FailureCode    `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	DocsURL   string         `json:"docs_url"`
	Details   map[string]any `json:"details,omitempty"`
}

type ErrorResponse struct {
	Error ErrorEnvelope `json:"error"`
}

const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

var (
	ErrInvalidCursorPage          = errors.New("invalid cursor page")
	ErrPageLimitExceeded          = errors.New("page limit exceeded")
	ErrInvalidDeployment          = errors.New("invalid deployment")
	ErrInvalidOperation           = errors.New("invalid operation")
	ErrInvalidOperationTransition = errors.New("invalid operation transition")
)

func (k DeploymentKind) IsJob() bool {
	return k == DeploymentKindCronInvocation || k == DeploymentKindPredeploy || k == DeploymentKindOneOff
}

func ValidateDeploymentKind(kind DeploymentKind) error {
	switch kind {
	case DeploymentKindWeb, DeploymentKindStatic, DeploymentKindCronInvocation, DeploymentKindPredeploy, DeploymentKindOneOff:
		return nil
	default:
		return ErrInvalidDeployment
	}
}

func ValidateDeploymentState(state DeploymentState) error {
	switch state {
	case DeploymentQueued, DeploymentBuilding, DeploymentDeploying, DeploymentRunningJob, DeploymentActive, DeploymentSuperseded, DeploymentFailed, DeploymentCanceling, DeploymentCanceled, DeploymentRemoving, DeploymentRemoved:
		return nil
	default:
		return ErrInvalidDeployment
	}
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type CursorPageDTO struct {
	NextCursor *string `json:"next_cursor"`
}

type CursorPageResponse[T any] struct {
	Data []T           `json:"data"`
	Page CursorPageDTO `json:"page"`
}

func NormalizePageLimit(limit int) (normalized int, clamped bool, err error) {
	if limit < 0 {
		return 0, false, ErrInvalidCursorPage
	}
	if limit == 0 {
		return DefaultPageLimit, false, nil
	}
	if limit > MaxPageLimit {
		return MaxPageLimit, true, nil
	}
	return limit, false, nil
}

// NormalizePageLimitStrict is the strict variant used by Phase 1+ endpoints
// that surface PAGINATION_LIMIT_EXCEEDED rather than silently clamping. It
// returns ErrPageLimitExceeded when the requested limit is above MaxPageLimit
// and ErrInvalidCursorPage when the requested limit is negative.
func NormalizePageLimitStrict(limit int) (int, error) {
	if limit < 0 {
		return 0, ErrInvalidCursorPage
	}
	if limit == 0 {
		return DefaultPageLimit, nil
	}
	if limit > MaxPageLimit {
		return 0, ErrPageLimitExceeded
	}
	return limit, nil
}

func (p CursorPageDTO) Validate() error {
	if p.NextCursor == nil {
		return nil
	}
	cursor := *p.NextCursor
	if cursor == "" {
		return ErrInvalidCursorPage
	}
	for _, r := range cursor {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return ErrInvalidCursorPage
	}
	return nil
}

func (r CursorPageResponse[T]) Validate() error {
	if r.Data == nil {
		return ErrInvalidCursorPage
	}
	return r.Page.Validate()
}

type Operation struct {
	ID        ID             `json:"id"`
	Kind      string         `json:"kind,omitempty"`
	State     OperationState `json:"state"`
	Code      FailureCode    `json:"code,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	CreatedAt time.Time      `json:"created_at,omitzero"`
	UpdatedAt time.Time      `json:"updated_at,omitzero"`
}

func (o Operation) Validate() error {
	if o.ID == "" {
		return ErrInvalidOperation
	}
	switch o.State {
	case OperationQueued, OperationRunning, OperationSucceeded, OperationFailed, OperationCanceled:
		return nil
	default:
		return ErrInvalidOperation
	}
}

func ValidateOperationTransition(from, to OperationState) error {
	if from == to {
		return nil
	}
	switch from {
	case OperationQueued:
		switch to {
		case OperationRunning, OperationFailed, OperationCanceled:
			return nil
		}
	case OperationRunning:
		switch to {
		case OperationSucceeded, OperationFailed, OperationCanceled:
			return nil
		}
	}
	return ErrInvalidOperationTransition
}

type Job struct {
	ID           ID     `json:"id"`
	Queue        string `json:"queue"`
	State        string `json:"state"`
	OperationID  ID     `json:"operation_id,omitempty"`
	PartitionKey string `json:"partition_key,omitempty"`
}
