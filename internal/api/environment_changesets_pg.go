// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/metering"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/variableintent"
	"github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type preparedEnvironmentChangesetItem struct {
	View           EnvironmentChangesetItem
	Envelope       secrets.Envelope
	ValueKind      string
	References     []secrets.VariableReference
	LengthBucket   string
	KEKFingerprint string
	VariableID     secrets.VariableID
	Payload        []byte
}

func (c *PostgresTenantMutationCoordinator) GetEnvironmentChangeset(ctx context.Context, request EnvironmentChangesetReadRequest) (EnvironmentChangesetView, error) {
	if err := c.validateEnvironmentChangesetRead(request); err != nil {
		return EnvironmentChangesetView{}, err
	}
	var view EnvironmentChangesetView
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx *pg.Tx) error {
		// The response always includes per-service configuration revision state,
		// including deployment identifiers. Require service visibility even when
		// there is no active draft whose item kinds could otherwise narrow access.
		mutationCtx, authorization, err := c.authorizeEnvironmentChangesetInTx(ctx, tx, request, identity.CapabilityServiceRead, c.now().Truncate(time.Microsecond))
		if err != nil {
			return err
		}
		changeset, err := loadActiveEnvironmentChangesetInTx(mutationCtx, tx, request.EnvironmentID, false)
		if errors.Is(err, errEnvironmentChangesetNotFound) {
			view = emptyEnvironmentChangesetView()
			view.ConfigurationState, err = loadEnvironmentConfigurationStateInTx(mutationCtx, tx, authorization.ProjectID, request.EnvironmentID)
			return err
		}
		if err != nil {
			return err
		}
		if err := authorizeEnvironmentChangesetKinds(authorization, changeset.Items, false); err != nil {
			return err
		}
		validation, err := c.validateLoadedChangesetInTx(mutationCtx, tx, changeset)
		if err != nil {
			return err
		}
		view = environmentChangesetView(changeset, validation)
		view.ConfigurationState, err = loadEnvironmentConfigurationStateInTx(mutationCtx, tx, authorization.ProjectID, request.EnvironmentID)
		return nil
	})
	return view, err
}

func (c *PostgresTenantMutationCoordinator) ValidateEnvironmentChangeset(ctx context.Context, request EnvironmentChangesetReadRequest, expectedRevision int64) (EnvironmentChangesetView, error) {
	if err := c.validateEnvironmentChangesetRead(request); err != nil {
		return EnvironmentChangesetView{}, err
	}
	var view EnvironmentChangesetView
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx *pg.Tx) error {
		mutationCtx, authorization, err := c.authorizeEnvironmentChangesetInTx(ctx, tx, request, identity.CapabilityServiceRead, c.now().Truncate(time.Microsecond))
		if err != nil {
			return err
		}
		changeset, err := loadActiveEnvironmentChangesetInTx(mutationCtx, tx, request.EnvironmentID, false)
		if err != nil {
			return err
		}
		if err := authorizeEnvironmentChangesetKinds(authorization, changeset.Items, false); err != nil {
			return err
		}
		if changeset.Revision != expectedRevision {
			return errEnvironmentChangesetConflict
		}
		validation, err := c.validateLoadedChangesetInTx(mutationCtx, tx, changeset)
		if err != nil {
			return err
		}
		view = environmentChangesetView(changeset, validation)
		view.ConfigurationState, err = loadEnvironmentConfigurationStateInTx(mutationCtx, tx, authorization.ProjectID, request.EnvironmentID)
		return nil
	})
	return view, err
}

func (c *PostgresTenantMutationCoordinator) PutEnvironmentChangeset(ctx context.Context, request EnvironmentChangesetMutationRequest) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	defer clearEnvironmentChangesetInputValues(request.Changes)
	if err := c.validateEnvironmentChangesetMutation(request, true); err != nil {
		return TenantMutationResult{}, err
	}
	if err := c.bindEnvironmentChangesetFingerprint(&request); err != nil {
		return TenantMutationResult{}, err
	}
	if replay, ok, err := c.preflightEnvironmentChangesetPut(ctx, request); ok || err != nil {
		return replay, err
	}
	prepared, err := c.prepareEnvironmentChangesetItems(ctx, request.Changes)
	if err != nil {
		return TenantMutationResult{}, err
	}
	defer clearPreparedEnvironmentChangesetItems(prepared)
	changesetID := string(c.IDFactory(orailtypes.IDPrefix("chg")))
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		now := c.now().Truncate(time.Microsecond)
		err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			mutationCtx, authorization, err := c.authorizeEnvironmentChangesetInTx(ctx, tx, request.EnvironmentChangesetReadRequest, identity.CapabilityProjectRead, now)
			if err != nil {
				return err
			}
			if err := authorizeEnvironmentChangesetKinds(authorization, preparedEnvironmentChangesetViews(prepared), true); err != nil {
				return err
			}
			if err := authorizeEnvironmentChangesetServicesInTx(mutationCtx, tx, authorization.ProjectID, request.EnvironmentID, prepared); err != nil {
				return err
			}
			reservation, err := c.reserveEnvironmentChangesetInTx(mutationCtx, tx, request)
			if err != nil {
				return err
			}
			if replay, ok, err := environmentChangesetReplay(reservation); ok || err != nil {
				result = replay
				return err
			}

			current, loadErr := loadActiveEnvironmentChangesetInTx(mutationCtx, tx, request.EnvironmentID, true)
			if errors.Is(loadErr, errEnvironmentChangesetNotFound) {
				if request.ExpectedRevision != 0 {
					response, err := environmentChangesetProblemResponse(http.StatusPreconditionFailed, failurecodes.PreconditionFailed, "environment changeset revision changed", request.RequestID, map[string]any{"expected_revision": request.ExpectedRevision, "current_revision": 0})
					if err != nil {
						return err
					}
					if err := c.completeEnvironmentChangesetFailureInTx(mutationCtx, tx, reservation, request, authorization, EnvironmentChangeset{EnvironmentID: request.EnvironmentID}, response, "changeset.replace", failurecodes.PreconditionFailed, now); err != nil {
						return err
					}
					result = TenantMutationResult{Response: response}
					return nil
				}
				current = EnvironmentChangeset{ID: changesetID, WorkspaceID: authorization.WorkspaceID, ProjectID: authorization.ProjectID, EnvironmentID: request.EnvironmentID, CreatedBy: request.Principal.UserID, State: "draft", Revision: 1, CreatedAt: now, UpdatedAt: now}
				if _, err := tx.Exec(mutationCtx, `INSERT INTO environment_changesets (id,workspace_id,project_id,environment_id,created_by,note,state,revision,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,'draft',1,$7,$7)`, current.ID, current.WorkspaceID, current.ProjectID, current.EnvironmentID, current.CreatedBy, strings.TrimSpace(request.Note), now); err != nil {
					return fmt.Errorf("insert environment changeset: %w", err)
				}
			} else if loadErr != nil {
				return loadErr
			} else {
				if err := authorizeEnvironmentChangesetKinds(authorization, current.Items, true); err != nil {
					return err
				}
				if current.Revision != request.ExpectedRevision {
					response, err := environmentChangesetProblemResponse(http.StatusPreconditionFailed, failurecodes.PreconditionFailed, "environment changeset revision changed", request.RequestID, map[string]any{"expected_revision": request.ExpectedRevision, "current_revision": current.Revision})
					if err != nil {
						return err
					}
					if err := c.completeEnvironmentChangesetFailureInTx(mutationCtx, tx, reservation, request, authorization, current, response, "changeset.replace", failurecodes.PreconditionFailed, now); err != nil {
						return err
					}
					result = TenantMutationResult{Response: response}
					return nil
				}
				current.Revision++
				current.UpdatedAt, current.Note = now, strings.TrimSpace(request.Note)
				if _, err := tx.Exec(mutationCtx, `UPDATE environment_changesets SET note=$2, revision=$3, updated_at=$4 WHERE id=$1 AND state='draft'`, current.ID, current.Note, current.Revision, now); err != nil {
					return fmt.Errorf("update environment changeset: %w", err)
				}
				if _, err := tx.Exec(mutationCtx, `DELETE FROM environment_changeset_items WHERE changeset_id=$1`, current.ID); err != nil {
					return fmt.Errorf("replace environment changeset items: %w", err)
				}
			}
			current.Note = strings.TrimSpace(request.Note)
			current.Items = make([]EnvironmentChangesetItem, 0, len(prepared))
			for ordinal, item := range prepared {
				refs, err := json.Marshal(item.References)
				if err != nil {
					return err
				}
				_, err = tx.Exec(mutationCtx, `INSERT INTO environment_changeset_items (id,changeset_id,project_id,environment_id,ordinal,kind,operation,service_id,target_id,variable_name,variable_scope,expected_version,value_kind,"references",value_ciphertext,kek_id,wrapped_dek,canonical_form,length_bucket,kek_fingerprint,payload,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,COALESCE($15,''::bytea),$16,COALESCE($17,''::bytea),$18,$19,$20,$21::jsonb,$22)`, item.View.ID, current.ID, current.ProjectID, current.EnvironmentID, ordinal, item.View.Kind, item.View.Operation, item.View.ServiceID, item.View.TargetID, item.View.Name, item.View.Scope, item.View.ExpectedVersion, item.ValueKind, refs, item.Envelope.Ciphertext, item.Envelope.KEKID, item.Envelope.WrappedDEK, item.Envelope.CanonicalForm, item.LengthBucket, item.KEKFingerprint, string(item.Payload), now)
				if err != nil {
					return fmt.Errorf("insert environment changeset item: %w", err)
				}
				current.Items = append(current.Items, item.View)
			}
			validation, err := c.validateLoadedChangesetInTx(mutationCtx, tx, current)
			if err != nil {
				return err
			}
			view := environmentChangesetView(current, validation)
			view.ConfigurationState, err = loadEnvironmentConfigurationStateInTx(mutationCtx, tx, authorization.ProjectID, request.EnvironmentID)
			if err != nil {
				return err
			}
			response, err := environmentChangesetStoredResponse(http.StatusOK, view)
			if err != nil {
				return err
			}
			if _, err := c.Idempotency.CompleteSuccessInTx(mutationCtx, tx, reservation.Reference(), response); err != nil {
				return err
			}
			if err := c.auditEnvironmentChangesetInTx(mutationCtx, tx, request, authorization, current, "changeset.replace", now); err != nil {
				return err
			}
			result = TenantMutationResult{Response: response}
			return nil
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("environment changeset serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) ApplyEnvironmentChangeset(ctx context.Context, request EnvironmentChangesetMutationRequest) (TenantMutationResult, error) {
	return c.transitionEnvironmentChangeset(ctx, request, true)
}

func (c *PostgresTenantMutationCoordinator) ApplyAndDeployEnvironmentChangeset(ctx context.Context, request EnvironmentChangesetMutationRequest) (TenantMutationResult, error) {
	return c.applyAndDeployEnvironmentChangeset(ctx, request)
}

func (c *PostgresTenantMutationCoordinator) DiscardEnvironmentChangeset(ctx context.Context, request EnvironmentChangesetMutationRequest) (TenantMutationResult, error) {
	return c.transitionEnvironmentChangeset(ctx, request, false)
}

func (c *PostgresTenantMutationCoordinator) transitionEnvironmentChangeset(ctx context.Context, request EnvironmentChangesetMutationRequest, apply bool) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	if err := c.validateEnvironmentChangesetMutation(request, false); err != nil {
		return TenantMutationResult{}, err
	}
	if err := c.bindEnvironmentChangesetFingerprint(&request); err != nil {
		return TenantMutationResult{}, err
	}
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		now := c.now().Truncate(time.Microsecond)
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			mutationCtx, authorization, err := c.authorizeEnvironmentChangesetInTx(ctx, tx, request.EnvironmentChangesetReadRequest, identity.CapabilityProjectRead, now)
			if err != nil {
				return err
			}
			reservation, err := c.reserveEnvironmentChangesetInTx(mutationCtx, tx, request)
			if err != nil {
				return err
			}
			if replay, ok, err := environmentChangesetReplay(reservation); ok || err != nil {
				result = replay
				return err
			}
			changeset, err := loadActiveEnvironmentChangesetInTx(mutationCtx, tx, request.EnvironmentID, true)
			if err != nil {
				return err
			}
			if err := authorizeEnvironmentChangesetKinds(authorization, changeset.Items, true); err != nil {
				return err
			}
			if changeset.Revision != request.ExpectedRevision {
				response, err := environmentChangesetProblemResponse(http.StatusPreconditionFailed, failurecodes.PreconditionFailed, "environment changeset revision changed", request.RequestID, map[string]any{"expected_revision": request.ExpectedRevision, "current_revision": changeset.Revision})
				if err != nil {
					return err
				}
				action := "changeset.discard"
				if apply {
					action = "changeset.apply"
				}
				if err := c.completeEnvironmentChangesetFailureInTx(mutationCtx, tx, reservation, request, authorization, changeset, response, action, failurecodes.PreconditionFailed, now); err != nil {
					return err
				}
				result = TenantMutationResult{Response: response}
				return nil
			}
			if err := authorizeEnvironmentChangesetViewServicesInTx(mutationCtx, tx, authorization.ProjectID, request.EnvironmentID, changeset.Items); err != nil {
				return err
			}
			validation, err := c.validateLoadedChangesetInTx(mutationCtx, tx, changeset)
			if err != nil {
				return err
			}
			if apply && !validation.Valid {
				response, err := environmentChangesetProblemResponse(http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed, "environment changeset validation failed", request.RequestID, map[string]any{"errors": validation.Errors})
				if err != nil {
					return err
				}
				if err := c.completeEnvironmentChangesetFailureInTx(mutationCtx, tx, reservation, request, authorization, changeset, response, "changeset.apply", failurecodes.ResourceValidationFailed, now); err != nil {
					return err
				}
				result = TenantMutationResult{Response: response}
				return nil
			}
			volumeDelta, storageChanged := environmentChangesetVolumeDelta(changeset.Items)
			var volumeReservation metering.Reservation
			var volumeLimitEnforced bool
			if apply {
				if storageChanged && (c.Metering == nil || c.Metering.Pool != c.Pool) {
					return errors.New("environment changeset usage admission authority is unavailable")
				}
				if volumeDelta > 0 {
					volumeReservation, volumeLimitEnforced, err = c.Metering.ReserveIfEnforcedInTx(mutationCtx, tx, metering.ReservationRequest{
						WorkspaceID:    authorization.WorkspaceID,
						ActorUserID:    request.Principal.UserID,
						Resource:       metering.ResourceVolumeCount,
						Quantity:       volumeDelta,
						IdempotencyKey: "changeset-volume:" + string(request.Key),
						IntentHash:     request.Fingerprint.BodySHA256,
						TTL:            10 * time.Minute,
					})
					var limitErr *metering.LimitError
					if errors.As(err, &limitErr) {
						response, responseErr := environmentChangesetProblemResponse(http.StatusConflict, failurecodes.ResourceConflict, "workspace volume limit exceeded", request.RequestID, map[string]any{
							"dimension": limitErr.Resource,
							"used":      limitErr.Used,
							"reserved":  limitErr.Reserved,
							"requested": limitErr.Requested,
							"limit":     limitErr.Limit,
						})
						if responseErr != nil {
							return responseErr
						}
						if err := c.completeEnvironmentChangesetFailureInTx(mutationCtx, tx, reservation, request, authorization, changeset, response, "changeset.apply", failurecodes.ResourceConflict, now); err != nil {
							return err
						}
						result = TenantMutationResult{Response: response}
						return nil
					}
					if err != nil {
						return fmt.Errorf("reserve environment volume capacity: %w", err)
					}
				}
				if err := c.applyEnvironmentChangesetInTx(mutationCtx, tx, authorization, changeset, now); err != nil {
					return err
				}
				appliedRevision, err := advanceEnvironmentConfigurationRevisionInTx(mutationCtx, tx, authorization, changeset, validation, now)
				if err != nil {
					return err
				}
				changeset.AppliedRevision = &appliedRevision
				if storageChanged {
					if volumeLimitEnforced {
						if err := c.Metering.CommitReservationInTx(mutationCtx, tx, volumeReservation, now); err != nil {
							return fmt.Errorf("commit environment volume reservation: %w", err)
						}
					} else if _, err := c.Metering.RecordResourceGaugeInTx(mutationCtx, tx, authorization.WorkspaceID, metering.ResourceVolumeCount, now); err != nil {
						return fmt.Errorf("record environment volume count: %w", err)
					}
				}
				changeset.State, changeset.AppliedAt = "applied", &now
			} else {
				changeset.State, changeset.DiscardedAt = "discarded", &now
			}
			changeset.Revision++
			changeset.UpdatedAt = now
			_, err = tx.Exec(mutationCtx, `UPDATE environment_changesets SET state=$2, revision=$3, updated_at=$4, applied_at=$5, discarded_at=$6, applied_configuration_revision=$7 WHERE id=$1 AND state='draft'`, changeset.ID, changeset.State, changeset.Revision, now, changeset.AppliedAt, changeset.DiscardedAt, changeset.AppliedRevision)
			if err != nil {
				return fmt.Errorf("transition environment changeset: %w", err)
			}
			if _, err := tx.Exec(mutationCtx, `UPDATE environment_changeset_items SET value_ciphertext=''::bytea,kek_id='',wrapped_dek=''::bytea,canonical_form='',"references"='[]'::jsonb WHERE changeset_id=$1`, changeset.ID); err != nil {
				return fmt.Errorf("erase environment changeset envelopes: %w", err)
			}
			view := environmentChangesetView(changeset, EnvironmentChangesetValidation{Valid: true, AffectedServices: validation.AffectedServices, AffectedResources: validation.AffectedResources, Errors: []string{}, Warnings: validation.Warnings})
			view.ConfigurationState, err = loadEnvironmentConfigurationStateInTx(mutationCtx, tx, authorization.ProjectID, request.EnvironmentID)
			if err != nil {
				return err
			}
			response, err := environmentChangesetStoredResponse(http.StatusOK, view)
			if err != nil {
				return err
			}
			if _, err := c.Idempotency.CompleteSuccessInTx(mutationCtx, tx, reservation.Reference(), response); err != nil {
				return err
			}
			action := "changeset.discard"
			if apply {
				action = "changeset.apply"
			}
			if err := c.auditEnvironmentChangesetInTx(mutationCtx, tx, request, authorization, changeset, action, now); err != nil {
				return err
			}
			result = TenantMutationResult{Response: response}
			return nil
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("environment changeset transition retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) prepareEnvironmentChangesetItems(ctx context.Context, inputs []EnvironmentChangesetInput) ([]preparedEnvironmentChangesetItem, error) {
	if len(inputs) == 0 || len(inputs) > environmentChangesetMaxItems {
		return nil, fmt.Errorf("%w: changes must contain 1-%d items", errEnvironmentChangesetInvalid, environmentChangesetMaxItems)
	}
	seen := make(map[string]struct{}, len(inputs))
	out := make([]preparedEnvironmentChangesetItem, 0, len(inputs))
	for _, input := range inputs {
		input.Kind, input.Operation, input.ServiceID, input.TargetID, input.Name = strings.TrimSpace(input.Kind), strings.TrimSpace(input.Operation), strings.TrimSpace(input.ServiceID), strings.TrimSpace(input.TargetID), strings.TrimSpace(input.Name)
		if input.ServiceID == "" {
			return nil, fmt.Errorf("%w: each change needs a service_id", errEnvironmentChangesetInvalid)
		}
		key := input.ServiceID + "\x00" + input.Kind + "\x00" + input.Name
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%w: duplicate %s change", errEnvironmentChangesetInvalid, input.Kind)
		}
		seen[key] = struct{}{}
		var prepared preparedEnvironmentChangesetItem
		var err error
		switch input.Kind {
		case "variable":
			prepared, err = c.prepareEnvironmentVariableItem(ctx, input)
		case "runtime":
			prepared, err = c.prepareEnvironmentRuntimeItem(input)
		case "networking":
			prepared, err = c.prepareEnvironmentNetworkingItem(input)
		case "storage":
			prepared, err = c.prepareEnvironmentStorageItem(input)
		default:
			err = fmt.Errorf("%w: unsupported change kind %q", errEnvironmentChangesetInvalid, input.Kind)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, prepared)
	}
	return out, nil
}

func (c *PostgresTenantMutationCoordinator) prepareEnvironmentVariableItem(ctx context.Context, input EnvironmentChangesetInput) (preparedEnvironmentChangesetItem, error) {
	if !validTenantVariableName(input.Name) || input.Runtime != nil || input.Networking != nil || input.Storage != nil {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: each variable change needs a portable name and no non-variable payload", errEnvironmentChangesetInvalid)
	}
	if input.Operation != "add" && input.Operation != "edit" && input.Operation != "remove" {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: variable operation must be add, edit, or remove", errEnvironmentChangesetInvalid)
	}
	itemID := string(c.IDFactory(orailtypes.IDPrefix("csi")))
	variableID := secrets.VariableID(input.TargetID)
	if input.Operation == "add" {
		if input.TargetID != "" || input.ExpectedVersion != nil {
			return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: added variables cannot carry target_id or an existing version", errEnvironmentChangesetInvalid)
		}
		variableID = secrets.VariableID(c.IDFactory(orailtypes.IDPrefixVariable))
	} else if variableID == "" || input.ExpectedVersion == nil || *input.ExpectedVersion < 1 || *input.ExpectedVersion > math.MaxInt32 {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: edited and removed variables need target_id and expected_version", errEnvironmentChangesetInvalid)
	}
	if input.Scope == nil || !validVariableScope(*input.Scope) {
		return preparedEnvironmentChangesetItem{}, secrets.ErrInvalidScope
	}
	view := EnvironmentChangesetItem{ID: itemID, Kind: "variable", Operation: input.Operation, ServiceID: input.ServiceID, TargetID: string(variableID), Name: input.Name, Scope: string(*input.Scope), ExpectedVersion: input.ExpectedVersion}
	prepared := preparedEnvironmentChangesetItem{View: view, VariableID: variableID, Payload: []byte(`{}`)}
	if input.Operation == "remove" {
		if input.Value != nil {
			return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: removed variables cannot carry a value", errEnvironmentChangesetInvalid)
		}
		prepared.View.ValueChange = "value removed"
		return prepared, nil
	}
	if input.Value == nil {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: variable value is required", errEnvironmentChangesetInvalid)
	}
	valueKind, refs, err := parseVariableValue(*input.Value)
	if err != nil {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: variable reference syntax is invalid", errEnvironmentChangesetInvalid)
	}
	plaintext := []byte(*input.Value)
	envelope, err := secrets.SealForVariable(ctx, c.VariableKeyProvider, secrets.VariableID(itemID), plaintext)
	clear(plaintext)
	if err != nil {
		return preparedEnvironmentChangesetItem{}, err
	}
	fingerprint, err := c.VariableKeyProvider.Fingerprint(ctx)
	if err != nil {
		return preparedEnvironmentChangesetItem{}, err
	}
	prepared.Envelope, prepared.ValueKind, prepared.References = envelope, valueKind, refs
	prepared.LengthBucket, prepared.KEKFingerprint = secrets.LengthBucket(len(*input.Value)), fingerprint
	if input.Operation == "add" {
		prepared.View.ValueChange = "value set"
	} else {
		prepared.View.ValueChange = "value replaced"
	}
	return prepared, nil
}

func (c *PostgresTenantMutationCoordinator) prepareEnvironmentRuntimeItem(input EnvironmentChangesetInput) (preparedEnvironmentChangesetItem, error) {
	if input.Runtime == nil || input.Scope != nil || input.Value != nil || input.Networking != nil || input.Storage != nil {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: runtime changes require exactly one runtime payload", errEnvironmentChangesetInvalid)
	}
	if input.Operation != "add" && input.Operation != "edit" {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: runtime operation must be add or edit", errEnvironmentChangesetInvalid)
	}
	if input.Operation == "add" {
		if input.TargetID != "" || input.ExpectedVersion != nil {
			return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: added runtime config cannot carry a target or existing version", errEnvironmentChangesetInvalid)
		}
	} else if input.TargetID != input.ServiceID || input.ExpectedVersion == nil || *input.ExpectedVersion < 1 {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: edited runtime config needs the service target and expected_version", errEnvironmentChangesetInvalid)
	}
	cfg := serviceconfig.Normalize(environmentRuntimeConfig("project", input.ServiceID, "environment", *input.Runtime))
	if err := serviceconfig.Validate(cfg); err != nil {
		return preparedEnvironmentChangesetItem{}, err
	}
	normalized := environmentRuntimeSpecFromConfig(cfg)
	payload, err := json.Marshal(normalized)
	if err != nil {
		return preparedEnvironmentChangesetItem{}, err
	}
	return preparedEnvironmentChangesetItem{View: EnvironmentChangesetItem{ID: string(c.IDFactory(orailtypes.IDPrefix("csi"))), Kind: "runtime", Operation: input.Operation, ServiceID: input.ServiceID, TargetID: input.ServiceID, Name: "service configuration", ExpectedVersion: input.ExpectedVersion, ValueChange: "configuration " + map[bool]string{true: "created", false: "updated"}[input.Operation == "add"], Runtime: &normalized}, Payload: payload}, nil
}

func (c *PostgresTenantMutationCoordinator) prepareEnvironmentNetworkingItem(input EnvironmentChangesetInput) (preparedEnvironmentChangesetItem, error) {
	if input.Networking == nil || input.Scope != nil || input.Value != nil || input.Runtime != nil || input.Storage != nil || input.Networking.Kind != "generated" || !input.Networking.Public {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: networking changes support only the generated public domain", errEnvironmentChangesetInvalid)
	}
	if input.Operation != "add" && input.Operation != "remove" {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: networking operation must be add or remove", errEnvironmentChangesetInvalid)
	}
	targetID := input.TargetID
	if input.Operation == "add" {
		if targetID != "" || input.ExpectedVersion != nil {
			return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: added networking intent cannot carry a target or existing version", errEnvironmentChangesetInvalid)
		}
		targetID = string(c.IDFactory(orailtypes.IDPrefixDomain))
	} else if targetID == "" || input.ExpectedVersion == nil || *input.ExpectedVersion < 1 {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: removed networking intent needs target_id and expected_version", errEnvironmentChangesetInvalid)
	}
	payload, err := json.Marshal(input.Networking)
	if err != nil {
		return preparedEnvironmentChangesetItem{}, err
	}
	change := "public domain added"
	if input.Operation == "remove" {
		change = "public domain removed"
	}
	return preparedEnvironmentChangesetItem{View: EnvironmentChangesetItem{ID: string(c.IDFactory(orailtypes.IDPrefix("csi"))), Kind: "networking", Operation: input.Operation, ServiceID: input.ServiceID, TargetID: targetID, Name: "generated public domain", ExpectedVersion: input.ExpectedVersion, ValueChange: change, Networking: input.Networking}, Payload: payload}, nil
}

func (c *PostgresTenantMutationCoordinator) prepareEnvironmentStorageItem(input EnvironmentChangesetInput) (preparedEnvironmentChangesetItem, error) {
	if input.Scope != nil || input.Value != nil || input.Runtime != nil || input.Networking != nil {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: storage changes cannot carry variable, runtime, or networking fields", errEnvironmentChangesetInvalid)
	}
	if input.Operation != "add" && input.Operation != "edit" && input.Operation != "remove" {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: storage operation must be add, edit, or remove", errEnvironmentChangesetInvalid)
	}
	if input.Name == "" {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: storage name is required", errEnvironmentChangesetInvalid)
	}
	targetID := input.TargetID
	if input.Operation == "add" {
		if targetID != "" || input.ExpectedVersion != nil || input.Storage == nil || input.Storage.SizeBytes < 0 || !validChangesetMountPath(input.Storage.MountPath) {
			return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: added storage needs a non-negative size and absolute clean mount_path", errEnvironmentChangesetInvalid)
		}
		targetID = string(c.IDFactory(orailtypes.IDPrefixVolume))
	} else if targetID == "" || input.ExpectedVersion == nil || *input.ExpectedVersion < 1 {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: edited and removed storage need target_id and expected_version", errEnvironmentChangesetInvalid)
	}
	if input.Operation == "edit" && (input.Storage == nil || input.Storage.SizeBytes < 0 || !validChangesetMountPath(input.Storage.MountPath)) {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: edited storage needs a non-negative size and current mount_path", errEnvironmentChangesetInvalid)
	}
	if input.Operation == "remove" && input.Storage != nil {
		return preparedEnvironmentChangesetItem{}, fmt.Errorf("%w: removed storage cannot carry a storage payload", errEnvironmentChangesetInvalid)
	}
	payload := []byte(`{"remove":true}`)
	if input.Storage != nil {
		var err error
		payload, err = json.Marshal(input.Storage)
		if err != nil {
			return preparedEnvironmentChangesetItem{}, err
		}
	}
	change := map[string]string{"add": "volume created", "edit": "volume resized", "remove": "volume removed"}[input.Operation]
	return preparedEnvironmentChangesetItem{View: EnvironmentChangesetItem{ID: string(c.IDFactory(orailtypes.IDPrefix("csi"))), Kind: "storage", Operation: input.Operation, ServiceID: input.ServiceID, TargetID: targetID, Name: input.Name, ExpectedVersion: input.ExpectedVersion, ValueChange: change, Storage: input.Storage}, Payload: payload}, nil
}

func validChangesetMountPath(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "/") && value != "/" && !strings.Contains(value, "//") && !strings.Contains(value, "..") && !strings.ContainsRune(value, '\x00')
}

func environmentRuntimeConfig(projectID, serviceID, environmentID string, spec EnvironmentRuntimeSpec) serviceconfig.Config {
	return serviceconfig.Config{ProjectID: projectID, ServiceID: serviceID, EnvironmentID: environmentID, RootDirectory: spec.RootDirectory, Builder: spec.Builder, DockerfilePath: spec.DockerfilePath, BuildCommand: spec.BuildCommand, StartCommand: spec.StartCommand, PredeployCommand: spec.PredeployCommand, TargetPort: spec.TargetPort, HealthKind: spec.HealthKind, HealthPath: spec.HealthPath, HealthPort: spec.HealthPort, HealthTimeout: time.Duration(spec.HealthTimeoutSeconds) * time.Second, HealthInterval: time.Duration(spec.HealthIntervalSeconds) * time.Second, RestartPolicy: spec.RestartPolicy, RestartRetries: spec.RestartRetries, Replicas: spec.Replicas, OverlapEnabled: spec.OverlapEnabled, DrainGrace: time.Duration(spec.DrainGraceSeconds) * time.Second, RuntimeKind: spec.RuntimeKind, CronSchedule: spec.CronSchedule, CronTimeout: time.Duration(spec.CronTimeoutSeconds) * time.Second, CronMaxAttempts: spec.CronMaxAttempts, CronRetryDelay: time.Duration(spec.CronRetryDelaySeconds) * time.Second}
}

func environmentRuntimeSpecFromConfig(config serviceconfig.Config) EnvironmentRuntimeSpec {
	return EnvironmentRuntimeSpec{RootDirectory: config.RootDirectory, Builder: config.Builder, DockerfilePath: config.DockerfilePath, BuildCommand: config.BuildCommand, StartCommand: config.StartCommand, PredeployCommand: config.PredeployCommand, TargetPort: config.TargetPort, HealthKind: config.HealthKind, HealthPath: config.HealthPath, HealthPort: config.HealthPort, HealthTimeoutSeconds: int64(config.HealthTimeout / time.Second), HealthIntervalSeconds: int64(config.HealthInterval / time.Second), RestartPolicy: config.RestartPolicy, RestartRetries: config.RestartRetries, Replicas: config.Replicas, OverlapEnabled: config.OverlapEnabled, DrainGraceSeconds: int64(config.DrainGrace / time.Second), RuntimeKind: config.RuntimeKind, CronSchedule: config.CronSchedule, CronTimeoutSeconds: int64(config.CronTimeout / time.Second), CronMaxAttempts: config.CronMaxAttempts, CronRetryDelaySeconds: int64(config.CronRetryDelay / time.Second)}
}

func (c *PostgresTenantMutationCoordinator) applyEnvironmentChangesetInTx(ctx context.Context, tx *pg.Tx, authorization tenantDeployAuthorization, changeset EnvironmentChangeset, now time.Time) error {
	for _, item := range changeset.Items {
		if item.Kind != "runtime" {
			continue
		}
		if item.Runtime == nil || c.ServiceConfigs == nil || c.ServiceConfigs.Pool != c.Pool {
			return errors.New("environment changeset runtime authority is unavailable")
		}
		expected := int64(0)
		if item.ExpectedVersion != nil {
			expected = *item.ExpectedVersion
		}
		if _, err := c.ServiceConfigs.PutInTx(ctx, tx, environmentRuntimeConfig(changeset.ProjectID, item.ServiceID, changeset.EnvironmentID, *item.Runtime), expected); err != nil {
			return fmt.Errorf("apply environment runtime change: %w", err)
		}
	}
	if err := c.applyEnvironmentChangesetVariablesInTx(ctx, tx, changeset, now); err != nil {
		return err
	}
	// Removals precede additions so a limit-neutral swap never trips the
	// database count trigger while the serializable reservation protects only
	// the positive net delta.
	for _, operation := range []string{"remove", "edit", "add"} {
		for _, item := range changeset.Items {
			if item.Kind != "storage" || item.Operation != operation {
				continue
			}
			if c.Volumes == nil || c.Volumes.Pool != c.Pool {
				return errors.New("environment changeset volume authority is unavailable")
			}
			switch item.Operation {
			case "add":
				if item.Storage == nil {
					return errEnvironmentChangesetInvalid
				}
				_, err := c.Volumes.CreateWithIDInTx(ctx, tx, item.TargetID, volume.VolumeCreateInput{ProjectID: changeset.ProjectID, EnvironmentID: changeset.EnvironmentID, ServiceID: item.ServiceID, Name: item.Name, SizeBytes: item.Storage.SizeBytes, MountPath: item.Storage.MountPath})
				if err != nil {
					return fmt.Errorf("apply environment volume create: %w", err)
				}
			case "edit":
				if item.Storage == nil || item.ExpectedVersion == nil {
					return errEnvironmentChangesetInvalid
				}
				if _, err := c.Volumes.ResizeCASInTx(ctx, tx, item.TargetID, *item.ExpectedVersion, item.Storage.SizeBytes); err != nil {
					return fmt.Errorf("apply environment volume resize: %w", err)
				}
			case "remove":
				if item.ExpectedVersion == nil {
					return errEnvironmentChangesetInvalid
				}
				if err := c.Volumes.DeleteCASInTx(ctx, tx, item.TargetID, *item.ExpectedVersion); err != nil {
					return fmt.Errorf("apply environment volume remove: %w", err)
				}
			}
		}
	}
	for _, item := range changeset.Items {
		if item.Kind == "networking" {
			if err := c.applyEnvironmentNetworkingChangeInTx(ctx, tx, authorization, changeset, item, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func environmentChangesetVolumeDelta(items []EnvironmentChangesetItem) (int64, bool) {
	var added, removed int64
	changed := false
	for _, item := range items {
		if item.Kind != "storage" {
			continue
		}
		changed = true
		switch item.Operation {
		case "add":
			added++
		case "remove":
			removed++
		}
	}
	if added <= removed {
		return 0, changed
	}
	return added - removed, changed
}

func (c *PostgresTenantMutationCoordinator) applyEnvironmentNetworkingChangeInTx(ctx context.Context, tx *pg.Tx, authorization tenantDeployAuthorization, changeset EnvironmentChangeset, item EnvironmentChangesetItem, now time.Time) error {
	if c.Routes == nil || c.Routes.Pool != c.Pool || strings.TrimSpace(c.AppsDomain) == "" {
		return errors.New("environment changeset generated-domain authority is unavailable")
	}
	authorization.Service = Service{ID: orailtypes.ID(item.ServiceID), ProjectID: orailtypes.ID(changeset.ProjectID)}
	if err := router.AcquireRouteEnvironmentMutationLock(ctx, tx, changeset.ProjectID, changeset.EnvironmentID); err != nil {
		return err
	}
	values, err := listTenantGeneratedDomainViewsInTx(ctx, tx, authorization, changeset.EnvironmentID, "", true)
	if err != nil {
		return err
	}
	if item.Operation == "remove" {
		if len(values) != 1 || values[0].ID != item.TargetID || item.ExpectedVersion == nil || values[0].Version != *item.ExpectedVersion {
			return errEnvironmentChangesetConflict
		}
		before := values[0]
		if _, err := tx.Exec(ctx, `INSERT INTO retired_product_domain_ids (domain_id,project_id,environment_id,service_id,retired_at) VALUES ($1,$2,$3,$4,$5)`, before.ID, before.ProjectID, before.EnvironmentID, before.ServiceID, now); err != nil {
			return fmt.Errorf("environment changeset generated domain retire: %w", err)
		}
		if _, err := advanceTenantGeneratedRouteGroupInTx(ctx, tx, changeset.ProjectID, changeset.EnvironmentID, now); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM domains WHERE id=$1 AND project_id=$2 AND environment_id=$3 AND service_id=$4 AND kind='generated' AND tls_desired=FALSE AND resource_version=$5 AND EXISTS (SELECT 1 FROM service_domain_bindings b WHERE b.domain_id=domains.id AND b.project_id=domains.project_id AND b.environment_id=domains.environment_id AND b.service_id=domains.service_id)`, before.ID, before.ProjectID, before.EnvironmentID, before.ServiceID, *item.ExpectedVersion)
		if err != nil {
			return fmt.Errorf("environment changeset generated domain delete: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return errEnvironmentChangesetConflict
		}
		return nil
	}
	if len(values) != 0 {
		return errEnvironmentChangesetConflict
	}
	activeDeploymentID, targetPort, err := c.resolveTenantGeneratedDomainTargetInTx(ctx, tx, authorization, changeset.EnvironmentID, now)
	if err != nil {
		return err
	}
	scope := router.RouteScope{ProjectID: changeset.ProjectID, EnvironmentID: changeset.EnvironmentID, ServiceID: item.ServiceID}
	domainValue, routeValue, err := router.BuildDomainRouteIntent(router.DomainRouteIntentInput{Scope: scope, Kind: router.DomainKindGenerated, AppsDomain: c.AppsDomain, PathPrefix: "/", TargetPort: targetPort, Public: true, TLSDesired: false})
	if err != nil {
		return fmt.Errorf("environment changeset generated domain intent: %w", err)
	}
	domainValue, routeValue, err = prepareProductGeneratedDomainInTx(ctx, tx, domainValue, routeValue, item.TargetID)
	if err != nil {
		return err
	}
	if domainValue, err = c.Routes.PutDomainInTx(ctx, tx, domainValue); err != nil {
		return fmt.Errorf("environment changeset generated domain persist: %w", err)
	}
	if err := c.Routes.EnsureDomainLifecycleInTx(ctx, tx, domainValue); err != nil {
		return fmt.Errorf("environment changeset generated domain lifecycle: %w", err)
	}
	if routeValue, err = c.Routes.PutHTTPRouteInTx(ctx, tx, routeValue); err != nil {
		return fmt.Errorf("environment changeset generated route persist: %w", err)
	}
	if err := c.Routes.BindServiceDomainInTx(ctx, tx, domainValue.ID, routeValue.ID, scope, now); err != nil {
		return fmt.Errorf("environment changeset generated service binding: %w", err)
	}
	if _, err := bindTenantGeneratedDomainOpenDeploymentsInTx(ctx, tx, scope, now); err != nil {
		return err
	}
	readyBackends, err := tenantGeneratedDomainReadyBackendsInTx(ctx, tx, activeDeploymentID, routeValue, now)
	if err != nil {
		return err
	}
	revision, err := advanceTenantGeneratedRouteGroupInTx(ctx, tx, changeset.ProjectID, changeset.EnvironmentID, now)
	if err != nil {
		return err
	}
	return replaceTenantGeneratedDomainBackendsInTx(ctx, tx, routeValue, readyBackends, revision)
}

func (c *PostgresTenantMutationCoordinator) applyEnvironmentChangesetVariablesInTx(ctx context.Context, tx *pg.Tx, changeset EnvironmentChangeset, now time.Time) error {
	rows, err := tx.Query(ctx, `SELECT id,operation,service_id,target_id,variable_name,variable_scope,expected_version,value_kind,"references",value_ciphertext,kek_id,wrapped_dek,canonical_form FROM environment_changeset_items WHERE changeset_id=$1 AND kind='variable' ORDER BY ordinal`, changeset.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type stored struct {
		id, op, service, target, name, scope, valueKind string
		expected                                        *int64
		refs                                            []byte
		envelope                                        secrets.Envelope
	}
	items := []stored{}
	for rows.Next() {
		var item stored
		if err := rows.Scan(&item.id, &item.op, &item.service, &item.target, &item.name, &item.scope, &item.expected, &item.valueKind, &item.refs, &item.envelope.Ciphertext, &item.envelope.KEKID, &item.envelope.WrappedDEK, &item.envelope.CanonicalForm); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	store := secrets.NewPostgresEnvelopeStore(c.Pool)
	for _, item := range items {
		scope := secrets.VariableOwnershipScope{Workspace: changeset.WorkspaceID, Project: changeset.ProjectID, Environment: changeset.EnvironmentID, Service: item.service}
		id := secrets.VariableID(item.target)
		if item.op == "remove" {
			if err := store.TombstoneCASInTx(ctx, tx, scope, id, int(*item.expected)); err != nil {
				return err
			}
			continue
		}
		plaintext, err := secrets.OpenForVariable(ctx, c.VariableKeyProvider, secrets.VariableID(item.id), item.envelope)
		if err != nil {
			return err
		}
		var refs []secrets.VariableReference
		if len(item.refs) > 0 {
			if err := json.Unmarshal(item.refs, &refs); err != nil {
				clear(plaintext)
				return err
			}
		}
		version := 1
		if item.expected != nil {
			version = int(*item.expected) + 1
		}
		prepared, err := c.prepareTenantVariable(ctx, id, scope, item.name, secrets.Scope(item.scope), version, plaintext, item.valueKind, refs, now)
		clear(plaintext)
		if err != nil {
			return err
		}
		if item.op == "add" {
			err = store.InsertPreparedInTx(ctx, tx, scope, prepared)
		} else {
			err = store.UpdatePreparedCASInTx(ctx, tx, scope, prepared, int(*item.expected))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func loadActiveEnvironmentChangesetInTx(ctx context.Context, tx *pg.Tx, environmentID string, lock bool) (EnvironmentChangeset, error) {
	query := `SELECT id,workspace_id,project_id,environment_id,created_by,note,state,revision,applied_configuration_revision,created_at,updated_at,applied_at,discarded_at FROM environment_changesets WHERE environment_id=$1 AND state='draft'`
	if lock {
		query += ` FOR UPDATE`
	}
	var out EnvironmentChangeset
	err := tx.QueryRow(ctx, query, environmentID).Scan(&out.ID, &out.WorkspaceID, &out.ProjectID, &out.EnvironmentID, &out.CreatedBy, &out.Note, &out.State, &out.Revision, &out.AppliedRevision, &out.CreatedAt, &out.UpdatedAt, &out.AppliedAt, &out.DiscardedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return EnvironmentChangeset{}, errEnvironmentChangesetNotFound
	}
	if err != nil {
		return EnvironmentChangeset{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id,kind,operation,service_id,target_id,variable_name,variable_scope,expected_version,payload FROM environment_changeset_items WHERE changeset_id=$1 ORDER BY ordinal`, out.ID)
	if err != nil {
		return EnvironmentChangeset{}, err
	}
	defer rows.Close()
	out.Items = []EnvironmentChangesetItem{}
	for rows.Next() {
		var item EnvironmentChangesetItem
		var payload []byte
		if err := rows.Scan(&item.ID, &item.Kind, &item.Operation, &item.ServiceID, &item.TargetID, &item.Name, &item.Scope, &item.ExpectedVersion, &payload); err != nil {
			return EnvironmentChangeset{}, err
		}
		if err := decodeEnvironmentChangesetItemPayload(&item, payload); err != nil {
			return EnvironmentChangeset{}, err
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}

func decodeEnvironmentChangesetItemPayload(item *EnvironmentChangesetItem, payload []byte) error {
	switch item.Kind {
	case "variable":
		item.ValueChange = map[string]string{"add": "value set", "edit": "value replaced", "remove": "value removed"}[item.Operation]
	case "runtime":
		var spec EnvironmentRuntimeSpec
		if err := json.Unmarshal(payload, &spec); err != nil {
			return fmt.Errorf("decode changeset runtime payload: %w", err)
		}
		item.Runtime = &spec
		item.ValueChange = map[string]string{"add": "configuration created", "edit": "configuration updated"}[item.Operation]
	case "networking":
		var spec EnvironmentNetworkingSpec
		if err := json.Unmarshal(payload, &spec); err != nil {
			return fmt.Errorf("decode changeset networking payload: %w", err)
		}
		item.Networking = &spec
		item.ValueChange = map[string]string{"add": "public domain added", "remove": "public domain removed"}[item.Operation]
	case "storage":
		if item.Operation != "remove" {
			var spec EnvironmentStorageSpec
			if err := json.Unmarshal(payload, &spec); err != nil {
				return fmt.Errorf("decode changeset storage payload: %w", err)
			}
			item.Storage = &spec
		}
		item.ValueChange = map[string]string{"add": "volume created", "edit": "volume resized", "remove": "volume removed"}[item.Operation]
	default:
		return fmt.Errorf("unknown changeset kind %q", item.Kind)
	}
	return nil
}

func (c *PostgresTenantMutationCoordinator) validateLoadedChangesetInTx(ctx context.Context, tx *pg.Tx, changeset EnvironmentChangeset) (EnvironmentChangesetValidation, error) {
	result := EnvironmentChangesetValidation{Valid: true, AffectedServices: []string{}, AffectedResources: []EnvironmentAffectedResource{}, Errors: []string{}, Warnings: []string{}}
	type serviceImpact struct {
		kinds  map[string]struct{}
		action string
	}
	services := map[string]*serviceImpact{}
	store := secrets.NewPostgresEnvelopeStore(c.Pool)
	for _, item := range changeset.Items {
		if services[item.ServiceID] == nil {
			services[item.ServiceID] = &serviceImpact{kinds: map[string]struct{}{}, action: environmentDeploymentActionNone}
		}
		services[item.ServiceID].kinds[item.Kind] = struct{}{}
		var itemErrors []string
		var err error
		switch item.Kind {
		case "variable":
			itemErrors, err = validateEnvironmentVariableChangeInTx(ctx, tx, store, changeset, item)
		case "runtime":
			itemErrors, err = c.validateEnvironmentRuntimeChangeInTx(ctx, tx, changeset, item)
		case "networking":
			itemErrors, err = c.validateEnvironmentNetworkingChangeInTx(ctx, tx, changeset, item)
		case "storage":
			itemErrors, err = c.validateEnvironmentStorageChangeInTx(ctx, tx, changeset, item)
		default:
			itemErrors = []string{fmt.Sprintf("unsupported changeset kind %q", item.Kind)}
		}
		if err != nil {
			return result, err
		}
		result.Errors = append(result.Errors, itemErrors...)
		if len(itemErrors) == 0 {
			action, err := c.environmentChangeDeploymentActionInTx(ctx, tx, changeset, item)
			if err != nil {
				return result, err
			}
			services[item.ServiceID].action = higherEnvironmentDeploymentAction(services[item.ServiceID].action, action)
		}
	}
	for service, impact := range services {
		result.AffectedServices = append(result.AffectedServices, service)
		kinds := make([]string, 0, len(impact.kinds))
		for kind := range impact.kinds {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		result.AffectedResources = append(result.AffectedResources, EnvironmentAffectedResource{
			ServiceID: service, ResourceKinds: kinds,
			RequiresDeploy:   impact.action != environmentDeploymentActionNone,
			DeploymentAction: impact.action,
		})
	}
	sort.Strings(result.AffectedServices)
	sort.Slice(result.AffectedResources, func(i, j int) bool {
		return result.AffectedResources[i].ServiceID < result.AffectedResources[j].ServiceID
	})
	result.Valid = len(result.Errors) == 0
	return result, nil
}

const (
	environmentDeploymentActionNone     = "none"
	environmentDeploymentActionRedeploy = "redeploy"
	environmentDeploymentActionRebuild  = "rebuild"
)

func higherEnvironmentDeploymentAction(current, candidate string) string {
	priority := map[string]int{
		environmentDeploymentActionNone:     0,
		environmentDeploymentActionRedeploy: 1,
		environmentDeploymentActionRebuild:  2,
	}
	if priority[candidate] > priority[current] {
		return candidate
	}
	return current
}

func (c *PostgresTenantMutationCoordinator) environmentChangeDeploymentActionInTx(
	ctx context.Context,
	tx *pg.Tx,
	changeset EnvironmentChangeset,
	item EnvironmentChangesetItem,
) (string, error) {
	switch item.Kind {
	case "networking":
		return environmentDeploymentActionNone, nil
	case "variable", "storage":
		return environmentDeploymentActionRedeploy, nil
	case "runtime":
		if item.Runtime == nil {
			return environmentDeploymentActionNone, errEnvironmentChangesetInvalid
		}
		before, err := c.ServiceConfigs.GetInTx(ctx, tx, changeset.ProjectID, changeset.EnvironmentID, item.ServiceID)
		if errors.Is(err, serviceconfig.ErrNotFound) && item.Operation == "add" {
			before = serviceconfig.Default(changeset.ProjectID, item.ServiceID, changeset.EnvironmentID)
		} else if err != nil {
			return environmentDeploymentActionNone, err
		}
		after := serviceconfig.Normalize(environmentRuntimeConfig(changeset.ProjectID, item.ServiceID, changeset.EnvironmentID, *item.Runtime))
		before = serviceconfig.Normalize(before)
		if environmentRuntimeBuildFieldsChanged(before, after) {
			return environmentDeploymentActionRebuild, nil
		}
		return environmentDeploymentActionRedeploy, nil
	default:
		return environmentDeploymentActionNone, errEnvironmentChangesetInvalid
	}
}

func environmentRuntimeBuildFieldsChanged(before, after serviceconfig.Config) bool {
	return before.RootDirectory != after.RootDirectory ||
		before.Builder != after.Builder ||
		before.DockerfilePath != after.DockerfilePath ||
		before.BuildCommand != after.BuildCommand
}

func validateEnvironmentVariableChangeInTx(ctx context.Context, tx *pg.Tx, store *secrets.PostgresEnvelopeStore, changeset EnvironmentChangeset, item EnvironmentChangesetItem) ([]string, error) {
	if item.Operation == "add" {
		var count int
		err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM service_variables WHERE project_id=$1 AND environment_id=$2 AND service_id=$3 AND name=$4 AND deleted_at IS NULL`, changeset.ProjectID, changeset.EnvironmentID, item.ServiceID, item.Name).Scan(&count)
		if err != nil {
			return nil, err
		}
		if count > 0 {
			return []string{fmt.Sprintf("%s already exists", item.Name)}, nil
		}
		return nil, nil
	}
	scope := secrets.VariableOwnershipScope{Workspace: changeset.WorkspaceID, Project: changeset.ProjectID, Environment: changeset.EnvironmentID, Service: item.ServiceID}
	current, err := store.GetActiveInTx(ctx, tx, scope, secrets.VariableID(item.TargetID))
	if errors.Is(err, secrets.ErrVariableNotFound) {
		return []string{fmt.Sprintf("%s no longer exists", item.Name)}, nil
	}
	if err != nil {
		return nil, err
	}
	errorsFound := []string{}
	if item.ExpectedVersion == nil || int64(current.Meta.Version) != *item.ExpectedVersion {
		errorsFound = append(errorsFound, fmt.Sprintf("%s changed since this review was prepared", item.Name))
	}
	if current.Meta.Name != item.Name {
		errorsFound = append(errorsFound, fmt.Sprintf("%s no longer matches its target", item.Name))
	}
	if current.Meta.Scope == secrets.ScopeSealed && item.Operation == "edit" && item.Scope != string(secrets.ScopeSealed) {
		errorsFound = append(errorsFound, fmt.Sprintf("%s cannot downgrade sealed scope", item.Name))
	}
	return errorsFound, nil
}

func (c *PostgresTenantMutationCoordinator) validateEnvironmentRuntimeChangeInTx(ctx context.Context, tx *pg.Tx, changeset EnvironmentChangeset, item EnvironmentChangesetItem) ([]string, error) {
	if c.ServiceConfigs == nil || c.ServiceConfigs.Pool != c.Pool || item.Runtime == nil {
		return []string{"runtime configuration authority is unavailable"}, nil
	}
	current, err := c.ServiceConfigs.GetInTx(ctx, tx, changeset.ProjectID, changeset.EnvironmentID, item.ServiceID)
	if item.Operation == "add" {
		if err == nil {
			return []string{"service configuration already exists"}, nil
		}
		if !errors.Is(err, serviceconfig.ErrNotFound) {
			return nil, err
		}
	} else {
		if errors.Is(err, serviceconfig.ErrNotFound) {
			return []string{"service configuration no longer exists"}, nil
		}
		if err != nil {
			return nil, err
		}
		if item.ExpectedVersion == nil || current.Version != *item.ExpectedVersion {
			return []string{"service configuration changed since this review was prepared"}, nil
		}
	}
	if err := serviceconfig.Validate(environmentRuntimeConfig(changeset.ProjectID, item.ServiceID, changeset.EnvironmentID, *item.Runtime)); err != nil {
		return []string{"runtime configuration is no longer valid"}, nil
	}
	return nil, nil
}

func (c *PostgresTenantMutationCoordinator) validateEnvironmentNetworkingChangeInTx(ctx context.Context, tx *pg.Tx, changeset EnvironmentChangeset, item EnvironmentChangesetItem) ([]string, error) {
	if c.Routes == nil || c.Routes.Pool != c.Pool || strings.TrimSpace(c.AppsDomain) == "" {
		return []string{"generated public domain authority is unavailable"}, nil
	}
	authorization := tenantDeployAuthorization{ProjectID: changeset.ProjectID, WorkspaceID: changeset.WorkspaceID, Service: Service{ID: orailtypes.ID(item.ServiceID), ProjectID: orailtypes.ID(changeset.ProjectID)}}
	values, err := listTenantGeneratedDomainViewsInTx(ctx, tx, authorization, changeset.EnvironmentID, "", false)
	if err != nil {
		return nil, err
	}
	if item.Operation == "add" {
		if len(values) > 0 {
			return []string{"generated public domain already exists"}, nil
		}
		return nil, nil
	}
	if len(values) != 1 || values[0].ID != item.TargetID {
		return []string{"generated public domain no longer exists"}, nil
	}
	if item.ExpectedVersion == nil || values[0].Version != *item.ExpectedVersion {
		return []string{"generated public domain changed since this review was prepared"}, nil
	}
	return nil, nil
}

func (c *PostgresTenantMutationCoordinator) validateEnvironmentStorageChangeInTx(ctx context.Context, tx *pg.Tx, changeset EnvironmentChangeset, item EnvironmentChangesetItem) ([]string, error) {
	if c.Volumes == nil || c.Volumes.Pool != c.Pool {
		return []string{"volume authority is unavailable"}, nil
	}
	if item.Operation == "add" {
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM volumes WHERE project_id=$1 AND environment_id=$2 AND service_id=$3 AND name=$4 AND status NOT IN ('soft_deleted','deleted','restore_retained')`, changeset.ProjectID, changeset.EnvironmentID, item.ServiceID, item.Name).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			return []string{fmt.Sprintf("volume %s already exists", item.Name)}, nil
		}
		return nil, nil
	}
	current, err := c.Volumes.GetInTx(ctx, tx, item.TargetID, false)
	if errors.Is(err, volume.ErrVolumeNotFound) {
		return []string{fmt.Sprintf("volume %s no longer exists", item.Name)}, nil
	}
	if err != nil {
		return nil, err
	}
	errorsFound := []string{}
	if current.ProjectID != changeset.ProjectID || current.EnvironmentID != changeset.EnvironmentID || current.ServiceID != item.ServiceID || current.Name != item.Name {
		errorsFound = append(errorsFound, fmt.Sprintf("volume %s no longer matches its target", item.Name))
	}
	if item.ExpectedVersion == nil || current.ResourceVersion != *item.ExpectedVersion {
		errorsFound = append(errorsFound, fmt.Sprintf("volume %s changed since this review was prepared", item.Name))
	}
	if item.Operation == "edit" && item.Storage != nil {
		if item.Storage.SizeBytes < current.SizeBytes {
			errorsFound = append(errorsFound, fmt.Sprintf("volume %s cannot be resized down", item.Name))
		}
		if strings.TrimSpace(item.Storage.MountPath) != current.MountPath {
			errorsFound = append(errorsFound, fmt.Sprintf("volume %s mount path is immutable", item.Name))
		}
	}
	if item.Operation == "remove" {
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM volume_attachments WHERE volume_id=$1 AND status IN ('pending','active')`, item.TargetID).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			errorsFound = append(errorsFound, fmt.Sprintf("volume %s is attached", item.Name))
		}
	}
	return errorsFound, nil
}

func (c *PostgresTenantMutationCoordinator) authorizeEnvironmentChangesetInTx(ctx context.Context, tx *pg.Tx, request EnvironmentChangesetReadRequest, capability identity.Capability, now time.Time) (context.Context, tenantDeployAuthorization, error) {
	projectID, workspaceID, err := resolveTenantEnvironmentUpdateLineage(ctx, tx, request.EnvironmentID)
	if err != nil {
		return ctx, tenantDeployAuthorization{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
		return ctx, tenantDeployAuthorization{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-members:"+projectID); err != nil {
		return ctx, tenantDeployAuthorization{}, err
	}
	mutationCtx := idempotency.WithActor(ctx, idempotency.UserActor(request.Principal.UserID))
	mutationCtx = idempotency.WithTenant(mutationCtx, projectScopedIdempotencyTenant(workspaceID, projectID))
	if err := revalidateTenantMutationSessionTuple(mutationCtx, tx, request.Principal, request.Session, now); err != nil {
		return mutationCtx, tenantDeployAuthorization{}, err
	}
	canonical, workspaceRole, projectRole, err := lockTenantServiceProjectAccess(mutationCtx, tx, projectID, workspaceID, request.Principal.UserID)
	if err != nil {
		return mutationCtx, tenantDeployAuthorization{}, err
	}
	if canonical != workspaceID {
		return mutationCtx, tenantDeployAuthorization{}, identity.ErrAccessNotFound
	}
	_, restricted, err := lockTenantEnvironmentForUpdate(mutationCtx, tx, request.EnvironmentID, projectID)
	if err != nil {
		return mutationCtx, tenantDeployAuthorization{}, err
	}
	decision := identity.EvaluateAccess(identity.AccessRequest{WorkspaceRole: workspaceRole, ProjectRole: projectRole, Capability: capability, EnvironmentRestricted: restricted})
	if !decision.Allowed {
		if decision.Reason == "environment-restricted" {
			return mutationCtx, tenantDeployAuthorization{}, identity.ErrAccessNotFound
		}
		return mutationCtx, tenantDeployAuthorization{}, identity.ErrAuthorizationDenied
	}
	return mutationCtx, tenantDeployAuthorization{ProjectID: projectID, WorkspaceID: workspaceID, WorkspaceRole: workspaceRole, ProjectRole: projectRole, EnvironmentRestricted: restricted}, nil
}

func preparedEnvironmentChangesetViews(items []preparedEnvironmentChangesetItem) []EnvironmentChangesetItem {
	views := make([]EnvironmentChangesetItem, 0, len(items))
	for _, item := range items {
		views = append(views, item.View)
	}
	return views
}

func authorizeEnvironmentChangesetKinds(authorization tenantDeployAuthorization, items []EnvironmentChangesetItem, write bool) error {
	capabilities := map[identity.Capability]struct{}{}
	for _, item := range items {
		var capability identity.Capability
		switch item.Kind {
		case "variable":
			capability = identity.CapabilityVariablesRead
			if write {
				capability = identity.CapabilityVariablesWrite
			}
		case "runtime":
			capability = identity.CapabilityServiceRead
			if write {
				capability = identity.CapabilityServiceWrite
			}
		case "networking":
			capability = identity.CapabilityDomainsRead
			if write {
				capability = identity.CapabilityDomainsWrite
			}
		case "storage":
			capability = identity.CapabilityServiceRead
			if write {
				capability = identity.CapabilityVolumeWrite
				if item.Operation == "remove" {
					capability = identity.CapabilityVolumeDelete
				}
			}
		default:
			return errEnvironmentChangesetInvalid
		}
		capabilities[capability] = struct{}{}
	}
	for capability := range capabilities {
		decision := identity.EvaluateAccess(identity.AccessRequest{
			WorkspaceRole:         authorization.WorkspaceRole,
			ProjectRole:           authorization.ProjectRole,
			Capability:            capability,
			EnvironmentRestricted: authorization.EnvironmentRestricted,
		})
		if !decision.Allowed {
			if decision.Reason == "environment-restricted" {
				return identity.ErrAccessNotFound
			}
			return identity.ErrAuthorizationDenied
		}
	}
	return nil
}

func authorizeEnvironmentChangesetServicesInTx(ctx context.Context, tx *pg.Tx, projectID, environmentID string, items []preparedEnvironmentChangesetItem) error {
	return authorizeEnvironmentChangesetViewServicesInTx(ctx, tx, projectID, environmentID, preparedEnvironmentChangesetViews(items))
}
func authorizeEnvironmentChangesetViewServicesInTx(ctx context.Context, tx *pg.Tx, projectID, environmentID string, items []EnvironmentChangesetItem) error {
	seen := map[string]struct{}{}
	for _, i := range items {
		if _, ok := seen[i.ServiceID]; ok {
			continue
		}
		seen[i.ServiceID] = struct{}{}
		var found bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services s JOIN environments e ON e.id=$3 AND e.project_id=s.project_id AND e.deleted_at IS NULL WHERE s.id=$1 AND s.project_id=$2 AND s.deleted_at IS NULL)`, i.ServiceID, projectID, environmentID).Scan(&found); err != nil {
			return err
		}
		if !found {
			return identity.ErrAccessNotFound
		}
	}
	return nil
}

func authorizeEnvironmentChangesetInputServicesInTx(ctx context.Context, tx *pg.Tx, projectID, environmentID string, inputs []EnvironmentChangesetInput) error {
	items := make([]EnvironmentChangesetItem, 0, len(inputs))
	for _, input := range inputs {
		if strings.TrimSpace(input.ServiceID) == "" {
			continue
		}
		items = append(items, EnvironmentChangesetItem{Kind: strings.TrimSpace(input.Kind), Operation: strings.TrimSpace(input.Operation), ServiceID: strings.TrimSpace(input.ServiceID)})
	}
	return authorizeEnvironmentChangesetViewServicesInTx(ctx, tx, projectID, environmentID, items)
}

// preflightEnvironmentChangesetPut proves the current session, exact tenant
// lineage, and every structurally valid service target before any envelope/KMS
// work. It also returns an already-committed exact replay without resealing.
// The serializable mutation transaction repeats all authorization after KMS so
// revocation or membership changes during sealing still fail closed.
func (c *PostgresTenantMutationCoordinator) preflightEnvironmentChangesetPut(ctx context.Context, request EnvironmentChangesetMutationRequest) (TenantMutationResult, bool, error) {
	var result TenantMutationResult
	var replay bool
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		mutationCtx, authorization, err := c.authorizeEnvironmentChangesetInTx(ctx, tx, request.EnvironmentChangesetReadRequest, identity.CapabilityProjectRead, c.now().Truncate(time.Microsecond))
		if err != nil {
			return err
		}
		inputViews := make([]EnvironmentChangesetItem, 0, len(request.Changes))
		for _, input := range request.Changes {
			inputViews = append(inputViews, EnvironmentChangesetItem{Kind: strings.TrimSpace(input.Kind), Operation: strings.TrimSpace(input.Operation), ServiceID: strings.TrimSpace(input.ServiceID)})
		}
		if err := authorizeEnvironmentChangesetKinds(authorization, inputViews, true); err != nil {
			return err
		}
		if err := authorizeEnvironmentChangesetInputServicesInTx(mutationCtx, tx, authorization.ProjectID, request.EnvironmentID, request.Changes); err != nil {
			return err
		}
		reservation, err := c.Idempotency.LoadInTx(mutationCtx, tx, request.Key, request.Fingerprint)
		if errors.Is(err, idempotency.ErrReservationNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if terminal, ok, err := environmentChangesetReplay(reservation); ok || err != nil {
			result, replay = terminal, ok
			return err
		}
		return idempotency.ErrRequestInProgress
	})
	return cloneTenantMutationResult(result), replay, err
}

func (c *PostgresTenantMutationCoordinator) reserveEnvironmentChangesetInTx(ctx context.Context, tx *pg.Tx, request EnvironmentChangesetMutationRequest) (idempotency.Reservation, error) {
	return c.Idempotency.ReserveInTx(ctx, tx, request.Key, request.Fingerprint, 24*time.Hour)
}

func (c *PostgresTenantMutationCoordinator) bindEnvironmentChangesetFingerprint(request *EnvironmentChangesetMutationRequest) error {
	mac, err := variableintent.BodyMAC(c.VariableIntentMACKey, request.RawBody)
	if err != nil {
		return err
	}
	request.Fingerprint = idempotency.RequestFingerprint{Method: request.Method, Path: request.Path, BodySHA256: mac}
	clear(request.RawBody)
	request.RawBody = nil
	return nil
}

func clearEnvironmentChangesetInputValues(inputs []EnvironmentChangesetInput) {
	for i := range inputs {
		inputs[i].Value = nil
	}
}

func clearPreparedEnvironmentChangesetItems(items []preparedEnvironmentChangesetItem) {
	for i := range items {
		clear(items[i].Envelope.Ciphertext)
		clear(items[i].Envelope.WrappedDEK)
		items[i].Envelope = secrets.Envelope{}
		items[i].References = nil
		items[i].KEKFingerprint = ""
	}
}
func environmentChangesetReplay(r idempotency.Reservation) (TenantMutationResult, bool, error) {
	if !r.IsTerminal() {
		return TenantMutationResult{}, false, nil
	}
	if r.Response == nil {
		return TenantMutationResult{}, true, errors.New("environment changeset replay has no stored response")
	}
	return TenantMutationResult{Response: r.Response.Clone(), Replayed: true}, true, nil
}

func (c *PostgresTenantMutationCoordinator) auditEnvironmentChangesetInTx(ctx context.Context, tx *pg.Tx, request EnvironmentChangesetMutationRequest, authorization tenantDeployAuthorization, changeset EnvironmentChangeset, action string, now time.Time) error {
	_, err := c.Audit.AppendInTx(ctx, tx, audit.Row{RequestID: request.RequestID, OccurredAt: now, RecordedAt: now, ActorID: request.Principal.UserID, ActorType: "user", AuthMethod: request.Principal.AuthMethod, SourceIP: normalizedMutationSourceIP(request.SourceIP), UserAgent: request.UserAgent, WorkspaceID: authorization.WorkspaceID, ProjectID: authorization.ProjectID, EnvironmentID: request.EnvironmentID, ResourceKind: "environment_changeset", ResourceID: changeset.ID, Action: action, PolicyDecision: "allowed", After: map[string]any{"state": changeset.State, "revision": changeset.Revision, "item_count": len(changeset.Items)}, Attrs: map[string]any{"capabilities": environmentChangesetCapabilityNames(changeset.Items, true), "workspace_role": string(authorization.WorkspaceRole), "project_role": string(authorization.ProjectRole), "session_id": request.Principal.SessionID}})
	return err
}

func environmentChangesetProblemResponse(status int, code failurecodes.Code, message, requestID string, details map[string]any) (idempotency.StoredResponse, error) {
	body, err := marshalErrorResponse(status, code, message, requestID, details)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/problem+json")
	return idempotency.StoredResponse{StatusCode: status, Header: headers, Body: body}, nil
}

func (c *PostgresTenantMutationCoordinator) completeEnvironmentChangesetFailureInTx(ctx context.Context, tx *pg.Tx, reservation idempotency.Reservation, request EnvironmentChangesetMutationRequest, authorization tenantDeployAuthorization, changeset EnvironmentChangeset, response idempotency.StoredResponse, action string, reason failurecodes.Code, now time.Time) error {
	if _, err := c.Idempotency.CompleteFailureInTx(ctx, tx, reservation.Reference(), response); err != nil {
		return err
	}
	_, err := c.Audit.AppendInTx(ctx, tx, audit.Row{
		RequestID: request.RequestID, OccurredAt: now, RecordedAt: now,
		ActorID: request.Principal.UserID, ActorType: "user", AuthMethod: request.Principal.AuthMethod,
		SourceIP: normalizedMutationSourceIP(request.SourceIP), UserAgent: request.UserAgent,
		WorkspaceID: authorization.WorkspaceID, ProjectID: authorization.ProjectID, EnvironmentID: request.EnvironmentID,
		ResourceKind: "environment_changeset", ResourceID: changeset.ID, Action: action,
		ReasonCode: string(reason), PolicyDecision: "denied",
		Attrs: map[string]any{"capabilities": environmentChangesetCapabilityNames(changeset.Items, true), "session_id": request.Principal.SessionID},
	})
	return err
}

func environmentChangesetCapabilityNames(items []EnvironmentChangesetItem, write bool) []string {
	set := map[string]struct{}{}
	for _, item := range items {
		var capability identity.Capability
		switch item.Kind {
		case "variable":
			capability = identity.CapabilityVariablesRead
			if write {
				capability = identity.CapabilityVariablesWrite
			}
		case "runtime":
			capability = identity.CapabilityServiceRead
			if write {
				capability = identity.CapabilityServiceWrite
			}
		case "networking":
			capability = identity.CapabilityDomainsRead
			if write {
				capability = identity.CapabilityDomainsWrite
			}
		case "storage":
			capability = identity.CapabilityServiceRead
			if write {
				capability = identity.CapabilityVolumeWrite
				if item.Operation == "remove" {
					capability = identity.CapabilityVolumeDelete
				}
			}
		}
		if capability != "" {
			set[string(capability)] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for capability := range set {
		out = append(out, capability)
	}
	sort.Strings(out)
	return out
}

func (c *PostgresTenantMutationCoordinator) validateEnvironmentChangesetRead(request EnvironmentChangesetReadRequest) error {
	if c == nil || c.Pool == nil || request.Principal.Kind != "user" || request.Principal.UserID == "" || request.Session.ID != request.Principal.SessionID || request.Session.UserID != request.Principal.UserID || strings.TrimSpace(request.EnvironmentID) == "" {
		return identity.ErrSessionInvalid
	}
	return nil
}
func (c *PostgresTenantMutationCoordinator) validateEnvironmentChangesetMutation(request EnvironmentChangesetMutationRequest, put bool) error {
	if err := c.validateEnvironmentChangesetRead(request.EnvironmentChangesetReadRequest); err != nil {
		return err
	}
	if c.Idempotency == nil || c.Idempotency.Pool != c.Pool || c.Audit == nil || c.Audit.Pool != c.Pool || c.VariableKeyProvider == nil || len(c.VariableIntentMACKey) < 32 || c.IDFactory == nil {
		return errors.New("environment changesets require shared persistence, idempotency, audit, encryption, and intent authority")
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if len(request.RawBody) == 0 {
		return idempotency.ErrInvalidFingerprint
	}
	if put && (utf8.RuneCountInString(strings.TrimSpace(request.Note)) > 500 || len(request.Changes) == 0) {
		return errEnvironmentChangesetInvalid
	}
	return nil
}
func emptyEnvironmentChangesetView() EnvironmentChangesetView {
	return EnvironmentChangesetView{Validation: EnvironmentChangesetValidation{Valid: true, AffectedServices: []string{}, AffectedResources: []EnvironmentAffectedResource{}, Errors: []string{}, Warnings: []string{}}, Capabilities: environmentChangesetCapabilities(), ConfigurationState: EnvironmentConfigurationState{Services: []EnvironmentServiceConfigurationRevision{}}}
}
func environmentChangesetView(changeset EnvironmentChangeset, validation EnvironmentChangesetValidation) EnvironmentChangesetView {
	return EnvironmentChangesetView{Changeset: &changeset, Validation: validation, Capabilities: environmentChangesetCapabilities()}
}
