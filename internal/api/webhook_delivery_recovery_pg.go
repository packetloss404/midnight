// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/webhook"
)

var ErrWebhookDeliveryRecoveryUnavailable = errors.New(
	"webhook delivery recovery transaction is unavailable",
)

type WebhookDeliveryRedriveRequest struct {
	DeliveryID  string
	ProjectID   string
	WorkspaceID string
	Key         idempotency.Key
	Actor       idempotency.Actor
	Tenant      idempotency.Tenant
	Principal   identity.Principal
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

type WebhookDeliveryRedriveResult struct {
	Response idempotency.StoredResponse
	Replayed bool
}

// WebhookDeliveryRedriveCoordinator is the mutation-only boundary used by the
// operator handler. Read handlers continue to receive the narrower,
// metadata-only inbox projection.
type WebhookDeliveryRedriveCoordinator interface {
	RedriveWebhookDelivery(
		context.Context,
		WebhookDeliveryRedriveRequest,
	) (WebhookDeliveryRedriveResult, error)
}

// PostgresWebhookDeliveryRedriveCoordinator commits the inbox transition,
// exact idempotency response, and recovery audit row in one SERIALIZABLE
// transaction. A process exit before commit leaves all three untouched; a
// retry after commit replays the stored response without mutating the inbox.
type PostgresWebhookDeliveryRedriveCoordinator struct {
	Pool        *pg.Pool
	Inbox       *webhook.PostgresDeliveryStore
	Idempotency *idempotency.PostgresStore
	Audit       *audit.PostgresChain
	Now         func() time.Time
}

func NewPostgresWebhookDeliveryRedriveCoordinator(
	pool *pg.Pool,
	inbox *webhook.PostgresDeliveryStore,
	idempotencyStore *idempotency.PostgresStore,
	auditChain *audit.PostgresChain,
	now func() time.Time,
) *PostgresWebhookDeliveryRedriveCoordinator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PostgresWebhookDeliveryRedriveCoordinator{
		Pool:        pool,
		Inbox:       inbox,
		Idempotency: idempotencyStore,
		Audit:       auditChain,
		Now:         now,
	}
}

func (c *PostgresWebhookDeliveryRedriveCoordinator) RedriveWebhookDelivery(
	ctx context.Context,
	request WebhookDeliveryRedriveRequest,
) (WebhookDeliveryRedriveResult, error) {
	if err := c.validate(request); err != nil {
		return WebhookDeliveryRedriveResult{}, err
	}
	mutationCtx := idempotency.WithActor(ctx, request.Actor)
	mutationCtx = idempotency.WithTenant(mutationCtx, request.Tenant)

	var result WebhookDeliveryRedriveResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(
			mutationCtx,
			pgx.TxOptions{IsoLevel: pgx.Serializable},
			func(tx *pg.Tx) error {
				var txErr error
				result, txErr = c.redriveInTx(mutationCtx, tx, request)
				return txErr
			},
		)
		if err == nil {
			result.Response = result.Response.Clone()
			return result, nil
		}
		if !isTenantMutationRetryable(err) ||
			attempt == tenantMutationSerializableAttempts-1 {
			return WebhookDeliveryRedriveResult{}, err
		}
	}
	return WebhookDeliveryRedriveResult{}, ErrWebhookDeliveryRecoveryUnavailable
}

func (c *PostgresWebhookDeliveryRedriveCoordinator) redriveInTx(
	ctx context.Context,
	tx *pg.Tx,
	request WebhookDeliveryRedriveRequest,
) (WebhookDeliveryRedriveResult, error) {
	reservation, err := c.Idempotency.ReserveInTx(
		ctx,
		tx,
		request.Key,
		request.Fingerprint,
		24*time.Hour,
	)
	if err != nil {
		return WebhookDeliveryRedriveResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return WebhookDeliveryRedriveResult{},
				errors.New("webhook delivery redrive replay has no stored response")
		}
		return WebhookDeliveryRedriveResult{
			Response: reservation.Response.Clone(),
			Replayed: true,
		}, nil
	}

	before, after, err := c.Inbox.RedriveProjectDeliveryInTx(ctx, tx, request.DeliveryID, request.ProjectID)
	if err != nil {
		return WebhookDeliveryRedriveResult{}, err
	}
	response, err := webhookDeliveryStoredResponse(http.StatusAccepted, after, request.ProjectID)
	if err != nil {
		return WebhookDeliveryRedriveResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		ctx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return WebhookDeliveryRedriveResult{}, err
	}
	now := c.Now().UTC().Truncate(time.Microsecond)
	if _, err := c.Audit.AppendInTx(
		ctx,
		tx,
		webhookDeliveryRedriveAuditRow(request, before, after, now),
	); err != nil {
		return WebhookDeliveryRedriveResult{},
			fmt.Errorf("webhook delivery redrive audit append: %w", err)
	}
	return WebhookDeliveryRedriveResult{Response: response}, nil
}

func (c *PostgresWebhookDeliveryRedriveCoordinator) validate(
	request WebhookDeliveryRedriveRequest,
) error {
	if c == nil ||
		c.Pool == nil ||
		c.Inbox == nil ||
		c.Inbox.Pool != c.Pool ||
		c.Inbox.Audit != c.Audit ||
		c.Idempotency == nil ||
		c.Idempotency.Pool != c.Pool ||
		c.Audit == nil ||
		c.Audit.Pool != c.Pool ||
		c.Now == nil {
		return ErrWebhookDeliveryRecoveryUnavailable
	}
	if request.DeliveryID == "" {
		return webhook.ErrInboxDeliveryNotFound
	}
	if request.ProjectID == "" || request.Actor == "" || request.Tenant == "" {
		return ErrWebhookDeliveryRecoveryUnavailable
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	return nil
}

func webhookDeliveryRedriveAuditRow(
	request WebhookDeliveryRedriveRequest,
	before webhook.DeliveryDetail,
	after webhook.DeliveryDetail,
	now time.Time,
) audit.Row {
	actorID := string(request.Actor)
	actorType := "system"
	authMethod := "operator-bearer"
	if request.Principal.UserID != "" {
		actorID = request.Principal.UserID
		actorType = "user"
		authMethod = request.Principal.AuthMethod
	} else if request.Actor == idempotency.ActorLocalDeveloper {
		authMethod = "local-developer"
	}
	return audit.Row{
		RequestID:      request.RequestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        actorID,
		ActorType:      actorType,
		AuthMethod:     authMethod,
		SourceIP:       normalizedMutationSourceIP(request.SourceIP),
		UserAgent:      request.UserAgent,
		ResourceKind:   "webhook_delivery",
		ResourceID:     after.ID,
		WorkspaceID:    request.WorkspaceID,
		ProjectID:      request.ProjectID,
		Action:         "webhook.delivery.redrive",
		PolicyDecision: "allowed",
		Before:         webhookDeliveryAuditMetadata(before.DeliverySummary),
		After:          webhookDeliveryAuditMetadata(after.DeliverySummary),
		Attrs: map[string]any{
			"provider":             string(after.Provider),
			"endpoint_id":          after.EndpointID,
			"provider_delivery_id": after.ProviderDeliveryID,
			"event_type":           after.EventType,
		},
	}
}

func webhookDeliveryAuditMetadata(delivery webhook.DeliverySummary) map[string]any {
	return map[string]any{
		"state":              string(delivery.State),
		"route_attempt":      delivery.RouteAttempt,
		"max_route_attempts": delivery.MaxRouteAttempts,
		"manual_retries":     delivery.ManualRetries,
		"reason_code":        delivery.ReasonCode,
		"available_at":       delivery.AvailableAt.UTC(),
		"terminal_at":        delivery.TerminalAt,
	}
}
