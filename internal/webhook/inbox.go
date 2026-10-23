// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package webhook

import (
	"context"
	"errors"
	"time"
)

const (
	DefaultInboxLease               = 30 * time.Second
	DefaultInboxMaxAttempts         = 8
	DefaultTargetMaxAttempts        = 8
	DefaultTerminalPayloadRetention = 30 * 24 * time.Hour
	DefaultPayloadPurgeBatch        = 500
)

var (
	ErrInboxNoWork                = errors.New("webhook inbox: no work")
	ErrInboxLeaseLost             = errors.New("webhook inbox: lease lost")
	ErrDeliveryIdentityReuse      = errors.New("webhook inbox: delivery identity reused with different payload")
	ErrInboxDeliveryNotFound      = errors.New("webhook inbox: delivery not found")
	ErrInboxDeliveryTerminal      = errors.New("webhook inbox: delivery is terminal")
	ErrInboxPayloadCorrupt        = errors.New("webhook inbox: retained payload hash mismatch")
	ErrInboxDeliveryScopeMismatch = errors.New("webhook inbox: delivery targets are not exclusive to project")
)

// InboxState is durable delivery or target lifecycle truth.
type InboxState string

const (
	InboxPending    InboxState = "pending"
	InboxProcessing InboxState = "processing"
	InboxRetryWait  InboxState = "retry_wait"
	InboxSucceeded  InboxState = "succeeded"
	InboxIgnored    InboxState = "ignored"
	InboxDeadLetter InboxState = "dead_letter"
)

// InboxAcceptance is the verified, bounded envelope persisted before HTTP
// acknowledgement. Payload is deliberately absent from every list/detail
// projection; only the store and claimed worker lease can read it.
type InboxAcceptance struct {
	Provider           Provider
	EndpointID         string
	ProviderDeliveryID string
	EventType          string
	EventID            string
	EventHash          string
	PayloadSHA256      string
	Payload            []byte
	SignatureKeyID     string
}

type AcceptDisposition string

const (
	AcceptCreated  AcceptDisposition = "created"
	AcceptReplayed AcceptDisposition = "replayed"
)

type AcceptResult struct {
	ID                 string
	ProviderDeliveryID string
	ReceivedAt         time.Time
	Disposition        AcceptDisposition
}

// DeliverySummary is the redaction-safe operator projection.
type DeliverySummary struct {
	ID                 string     `json:"id"`
	Provider           Provider   `json:"provider"`
	EndpointID         string     `json:"endpoint_id"`
	ProviderDeliveryID string     `json:"provider_delivery_id"`
	EventType          string     `json:"event_type"`
	EventID            string     `json:"event_id,omitempty"`
	EventHash          string     `json:"event_hash"`
	PayloadSHA256      string     `json:"payload_sha256"`
	PayloadBytes       int64      `json:"payload_bytes"`
	State              InboxState `json:"state"`
	RouteAttempt       int        `json:"route_attempt"`
	MaxRouteAttempts   int        `json:"max_route_attempts"`
	ManualRetries      int        `json:"manual_retries"`
	ReasonCode         string     `json:"reason_code,omitempty"`
	LastError          string     `json:"last_error,omitempty"`
	ReplayCount        int64      `json:"replay_count"`
	ReceivedAt         time.Time  `json:"received_at"`
	LastReceivedAt     time.Time  `json:"last_received_at"`
	AvailableAt        time.Time  `json:"available_at"`
	RoutingCompletedAt *time.Time `json:"routing_completed_at,omitempty"`
	TerminalAt         *time.Time `json:"terminal_at,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type DeliveryDetail struct {
	DeliverySummary
	Targets []TargetSummary `json:"targets"`
}

type TargetSummary struct {
	ID                               string     `json:"id"`
	TargetKey                        string     `json:"target_key"`
	WorkspaceID                      string     `json:"workspace_id"`
	ProjectID                        string     `json:"project_id"`
	ServiceID                        string     `json:"service_id"`
	InstallationID                   int64      `json:"installation_id"`
	InstallationGrantResourceVersion int64      `json:"installation_grant_resource_version"`
	RepositoryID                     int64      `json:"repository_id"`
	Repository                       string     `json:"repository"`
	RepositoryGrantResourceVersion   int64      `json:"repository_grant_resource_version"`
	LinkResourceVersion              int64      `json:"link_resource_version"`
	Branch                           string     `json:"branch"`
	CommitSHA                        string     `json:"commit_sha"`
	State                            InboxState `json:"state"`
	Attempt                          int        `json:"attempt"`
	MaxAttempts                      int        `json:"max_attempts"`
	ManualRetries                    int        `json:"manual_retries"`
	ReasonCode                       string     `json:"reason_code,omitempty"`
	LastError                        string     `json:"last_error,omitempty"`
	OperationID                      string     `json:"operation_id,omitempty"`
	DeploymentID                     string     `json:"deployment_id,omitempty"`
	StagedOutboxID                   string     `json:"staged_outbox_id,omitempty"`
	AvailableAt                      time.Time  `json:"available_at"`
	TerminalAt                       *time.Time `json:"terminal_at,omitempty"`
	CreatedAt                        time.Time  `json:"created_at"`
	UpdatedAt                        time.Time  `json:"updated_at"`
}

type DeliveryFilter struct {
	State     InboxState
	Provider  Provider
	ProjectID string
	Limit     int
}

type PayloadPurgeResult struct {
	Deliveries int
	Targets    int
}

// TerminalPayloadPurger releases retained source/dispatch bytes after the
// recovery window while preserving delivery identity, hashes, outcomes, and
// metadata-only operator evidence.
type TerminalPayloadPurger interface {
	PurgeTerminalPayloads(
		context.Context,
		time.Time,
		int,
	) (PayloadPurgeResult, error)
}

type ClaimedDelivery struct {
	DeliverySummary
	LeaseToken string
	Payload    []byte
}

func (d ClaimedDelivery) ReceivedDelivery() ReceivedDelivery {
	return ReceivedDelivery{
		Provider:   d.Provider,
		EndpointID: d.EndpointID,
		DeliveryID: d.ProviderDeliveryID,
		EventHash:  d.EventHash,
		EventType:  d.EventType,
		Body:       append([]byte(nil), d.Payload...),
		ReceivedAt: d.ReceivedAt,
	}
}

// DispatchTarget is an immutable, redaction-safe target snapshot plus an
// opaque bounded payload understood only by the planner/dispatcher pair.
type DispatchTarget struct {
	TargetKey                        string
	WorkspaceID                      string
	ProjectID                        string
	ServiceID                        string
	InstallationID                   int64
	InstallationGrantResourceVersion int64
	RepositoryID                     int64
	Repository                       string
	RepositoryGrantResourceVersion   int64
	LinkResourceVersion              int64
	LinkCreatedAt                    time.Time
	LinkUpdatedAt                    time.Time
	RepositoryGrantCreatedAt         time.Time
	RepositoryGrantUpdatedAt         time.Time
	RepositoryVerifiedAt             time.Time
	LinkRepositoryVerifiedAt         time.Time
	InstallationGrantCreatedAt       time.Time
	InstallationGrantUpdatedAt       time.Time
	Branch                           string
	Ref                              string
	CommitSHA                        string
	CloneURL                         string
	RootDirectory                    string
	ConfigPath                       string
	TriggerReason                    string
	Payload                          []byte
}

type DispatchPlan struct {
	IgnoredReason string
	Targets       []DispatchTarget
}

type ClaimedTarget struct {
	TargetSummary
	LeaseToken                 string
	Payload                    []byte
	PayloadSHA256              string
	InstallationGrantCreatedAt time.Time
	InstallationGrantUpdatedAt time.Time
	RepositoryGrantCreatedAt   time.Time
	RepositoryGrantUpdatedAt   time.Time
	RepositoryVerifiedAt       time.Time
	LinkCreatedAt              time.Time
	LinkUpdatedAt              time.Time
	LinkRepositoryVerifiedAt   time.Time
	Ref                        string
	CloneURL                   string
	RootDirectory              string
	ConfigPath                 string
	TriggerReason              string
}

func (t ClaimedTarget) DispatchSnapshot() DispatchTarget {
	return DispatchTarget{
		TargetKey:                        t.TargetKey,
		WorkspaceID:                      t.WorkspaceID,
		ProjectID:                        t.ProjectID,
		ServiceID:                        t.ServiceID,
		InstallationID:                   t.InstallationID,
		InstallationGrantResourceVersion: t.InstallationGrantResourceVersion,
		RepositoryID:                     t.RepositoryID,
		Repository:                       t.Repository,
		RepositoryGrantResourceVersion:   t.RepositoryGrantResourceVersion,
		LinkResourceVersion:              t.LinkResourceVersion,
		LinkCreatedAt:                    t.LinkCreatedAt,
		LinkUpdatedAt:                    t.LinkUpdatedAt,
		RepositoryGrantCreatedAt:         t.RepositoryGrantCreatedAt,
		RepositoryGrantUpdatedAt:         t.RepositoryGrantUpdatedAt,
		RepositoryVerifiedAt:             t.RepositoryVerifiedAt,
		LinkRepositoryVerifiedAt:         t.LinkRepositoryVerifiedAt,
		InstallationGrantCreatedAt:       t.InstallationGrantCreatedAt,
		InstallationGrantUpdatedAt:       t.InstallationGrantUpdatedAt,
		Branch:                           t.Branch,
		Ref:                              t.Ref,
		CommitSHA:                        t.CommitSHA,
		CloneURL:                         t.CloneURL,
		RootDirectory:                    t.RootDirectory,
		ConfigPath:                       t.ConfigPath,
		TriggerReason:                    t.TriggerReason,
		Payload:                          append([]byte(nil), t.Payload...),
	}
}

type TargetResult struct {
	OperationID    string
	DeploymentID   string
	StagedOutboxID string
}

// DurableInbox is the persist-before-ack, leased retry boundary.
type DurableInbox interface {
	Accept(ctx context.Context, request InboxAcceptance) (AcceptResult, error)
	ClaimDelivery(ctx context.Context) (ClaimedDelivery, error)
	StorePlan(ctx context.Context, delivery ClaimedDelivery, plan DispatchPlan) error
	ClaimTarget(ctx context.Context, delivery ClaimedDelivery) (ClaimedTarget, error)
	CompleteTarget(ctx context.Context, delivery ClaimedDelivery, target ClaimedTarget, result TargetResult) error
	IgnoreTarget(ctx context.Context, delivery ClaimedDelivery, target ClaimedTarget, reasonCode string) error
	DeadLetterTarget(ctx context.Context, delivery ClaimedDelivery, target ClaimedTarget, reasonCode string, cause error) error
	RetryTarget(ctx context.Context, delivery ClaimedDelivery, target ClaimedTarget, reasonCode string, cause error) error
	AggregateDelivery(ctx context.Context, delivery ClaimedDelivery) error
	IgnoreDelivery(ctx context.Context, delivery ClaimedDelivery, reasonCode string) error
	DeadLetterDelivery(ctx context.Context, delivery ClaimedDelivery, reasonCode string, cause error) error
	RetryDelivery(ctx context.Context, delivery ClaimedDelivery, reasonCode string, cause error) error
	ListDeliveries(ctx context.Context, filter DeliveryFilter) ([]DeliverySummary, error)
	GetDelivery(ctx context.Context, id string) (DeliveryDetail, error)
	RedriveDelivery(ctx context.Context, id string) (DeliveryDetail, error)
}

// InboxLeaseRenewer is the optional heartbeat seam used by durable workers.
// Renewals are fenced by the exact delivery and target lease tokens; losing a
// lease must return ErrInboxLeaseLost and must not mutate the replacement
// owner's lease.
type InboxLeaseRenewer interface {
	LeaseHeartbeatInterval() time.Duration
	RenewDeliveryLease(ctx context.Context, delivery ClaimedDelivery) error
	RenewTargetLease(ctx context.Context, delivery ClaimedDelivery, target ClaimedTarget) error
}

type Planner interface {
	Plan(ctx context.Context, delivery ReceivedDelivery) (DispatchPlan, error)
}

type TargetDispatcher interface {
	DispatchTarget(ctx context.Context, delivery ReceivedDelivery, target DispatchTarget) (TargetResult, error)
}

// PermanentError marks a signed payload or immutable target invariant that
// retries cannot repair. Transient errors use ordinary errors.
type PermanentError struct {
	Code string
	Err  error
}

func (e *PermanentError) Error() string {
	if e == nil || e.Err == nil {
		return "webhook permanent error"
	}
	return e.Err.Error()
}

func (e *PermanentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IgnoredTargetError means current source authority was deliberately revoked
// or changed. It is terminal for this historical target, not an infrastructure
// failure.
type IgnoredTargetError struct {
	Code string
	Err  error
}

func (e *IgnoredTargetError) Error() string {
	if e == nil || e.Err == nil {
		return "webhook target ignored"
	}
	return e.Err.Error()
}

func (e *IgnoredTargetError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
