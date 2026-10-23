// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

const identityRecoveryPath = v1alphaPrefix + "/auth/password-reset"

// identityRecoveryRequest resets one installation administrator's local
// credentials. It is the supported answer to a lost or mistyped first-run
// password: /auth/claim is one-shot, so without this route the only way back
// into a claimed installation is dropping and re-migrating the database.
//
// TOTP is never silently dropped. Either the caller enrolls a replacement
// second factor (totp_secret + totp_code, verified exactly the way the claim
// verifies a first enrollment), or it sets preserve_totp and the account keeps
// the authenticator it already has.
type identityRecoveryRequest struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	TOTPSecret   string `json:"totp_secret"`
	TOTPCode     string `json:"totp_code"`
	PreserveTOTP bool   `json:"preserve_totp"`
}

func identityRecoveryHandler(
	recovery identity.AdministratorRecoveryStore,
	manager *identity.Manager,
	idem idempotency.Store,
	coordinator IdentityRecoveryCoordinator,
	clock func() time.Time,
	emitter *auditEmitter,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		// Fail closed unless credential mutation and session verification are
		// the same authority. Resetting a password in one store while sessions
		// are checked against another would report success and change nothing.
		if recovery == nil || manager == nil || !manager.SharesRecoveryAuthority(recovery) {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable administrator recovery is not configured", nil)
			return
		}
		if !operatorRecoveryActor(r.Context()) {
			writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthOperatorRecoveryRequired,
				"administrator credential recovery requires the installation operator recovery credential", map[string]any{
					"env":           "MIDNIGHT_API_BEARER_TOKEN",
					"recovery_hint": failurecodes.RecoveryHint(failurecodes.AuthOperatorRecoveryRequired),
				})
			return
		}
		var request identityRecoveryRequest
		if _, ok := decodeStrictJSONRequestBodyWithLimit(w, r, &request, "invalid administrator recovery request", sensitiveJSONBodyLimitBytes); !ok {
			return
		}
		request.Email = strings.ToLower(strings.TrimSpace(request.Email))
		request.TOTPSecret = strings.TrimSpace(request.TOTPSecret)
		request.TOTPCode = strings.TrimSpace(request.TOTPCode)
		if !validIdentityEmail(request.Email) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "administrator recovery fields are invalid", nil)
			return
		}
		// The two TOTP dispositions are mutually exclusive. Accepting enrollment
		// material alongside preserve_totp would leave which factor survives up
		// to server precedence rather than the operator's stated intent.
		if request.PreserveTOTP && (request.TOTPSecret != "" || request.TOTPCode != "") {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed,
				"preserve_totp cannot be combined with a replacement TOTP enrollment", nil)
			return
		}
		if !request.PreserveTOTP && (request.TOTPSecret == "" || request.TOTPCode == "") {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed,
				"a replacement TOTP secret and code are required unless preserve_totp is set", nil)
			return
		}

		now := clock().UTC()
		passwordHash, err := bootstrap.HashPassword(request.Password)
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "password does not meet the minimum requirements", nil)
			return
		}
		target, err := recovery.RecoverableAdministrator(r.Context(), request.Email)
		if err != nil {
			if errors.Is(err, identity.ErrAdministratorNotRecoverable) {
				emitter.emit(r.Context(), identityRecoveryDeniedAuditRow(
					audit.Row{}, "", string(failurecodes.ResourceNotFound), now,
				))
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound,
					"no enabled administrator account matches that address", nil)
				return
			}
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "administrator recovery lookup failed", nil)
			return
		}

		var totpEnvelope []byte
		if !request.PreserveTOTP {
			totpSecret, decodeErr := bootstrap.DecodeTOTPSecret(request.TOTPSecret)
			if decodeErr != nil || bootstrap.VerifyTOTP(totpSecret, request.TOTPCode, now) != nil {
				clearSensitiveBytes(totpSecret)
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.MFAInvalidCode, "TOTP enrollment secret and code did not verify", nil)
				return
			}
			defer clearSensitiveBytes(totpSecret)
			// The envelope is bound to the server-resolved user ID, never to
			// anything the request body chose.
			totpEnvelope, err = manager.SealTOTP(r.Context(), target.UserID, totpSecret)
			if err != nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "TOTP enrollment could not be secured", nil)
				return
			}
		} else if !target.TOTPEnrolled {
			emitter.emit(r.Context(), identityRecoveryDeniedAuditRow(
				audit.Row{}, target.UserID, string(failurecodes.ResourceValidationFailed), now,
			))
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed,
				"this account has no TOTP enrollment to preserve; supply a replacement enrollment", nil)
			return
		}

		redactedFingerprintBody, err := json.Marshal(struct {
			Email        string `json:"email"`
			UserID       string `json:"user_id"`
			PreserveTOTP bool   `json:"preserve_totp"`
		}{
			Email:        request.Email,
			UserID:       target.UserID,
			PreserveTOTP: request.PreserveTOTP,
		})
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "administrator recovery fingerprint failed", nil)
			return
		}
		rawIdempotencyKey := idempotency.Key(r.Header.Get("Idempotency-Key"))
		if err := idempotency.ValidateKey(rawIdempotencyKey); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
			return
		}
		r.Header.Set("Idempotency-Key", identityRecoveryIdempotencyKey(rawIdempotencyKey))
		r = r.WithContext(idempotency.WithTenant(r.Context(), idempotency.Tenant(identity.DefaultWorkspaceID)))

		resetRequest := identity.ResetAdministratorCredentialsRequest{
			UserID:             target.UserID,
			Email:              request.Email,
			PasswordHash:       passwordHash,
			PreserveTOTP:       request.PreserveTOTP,
			TOTPSecretEnvelope: totpEnvelope,
			TOTPEnrolledAt:     now,
			RevokedReason:      "administrator-password-reset",
			Now:                now,
		}
		if coordinator != nil {
			result, err := coordinator.ResetAdministratorCredentials(r.Context(), IdentityRecoveryCommitRequest{
				Reset: resetRequest,
				Key:   idempotency.Key(r.Header.Get("Idempotency-Key")),
				Fingerprint: idempotency.RequestFingerprint{
					Method:     r.Method,
					Path:       idempotencyRouteTemplate(r),
					BodySHA256: idempotency.BodyHashSHA256(redactedFingerprintBody),
				},
				RequestID: middleware.FromContext(r.Context()),
				SourceIP:  r.RemoteAddr,
				UserAgent: r.UserAgent(),
			})
			switch {
			case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
				writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
				return
			case errors.Is(err, idempotency.ErrKeyReused):
				writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
				return
			case errors.Is(err, idempotency.ErrRequestInProgress):
				w.Header().Set("Retry-After", "1")
				writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
				return
			case err != nil:
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "administrator recovery failed", nil)
				return
			}
			if result.Replayed {
				w.Header().Set("Idempotent-Replayed", "true")
			}
			writeStoredResponse(w, result.Response)
			return
		}

		reservation, ok := reserveIdempotencyOrAbort(w, r, idem, redactedFingerprintBody)
		if !ok {
			return
		}
		idempotentResponse := newIdempotencyResponseCapture(w)
		w = idempotentResponse
		defer idempotentResponse.Complete(r, idem, reservation.Reference())
		result, err := recovery.ResetAdministratorCredentials(r.Context(), resetRequest)
		switch {
		case errors.Is(err, identity.ErrAdministratorNotRecoverable):
			emitter.emit(r.Context(), identityRecoveryDeniedAuditRow(
				audit.Row{}, target.UserID, string(failurecodes.ResourceNotFound), now,
			))
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound,
				"no enabled administrator account matches that address", nil)
			return
		case errors.Is(err, identity.ErrTOTPEnrollmentMissing):
			emitter.emit(r.Context(), identityRecoveryDeniedAuditRow(
				audit.Row{}, target.UserID, string(failurecodes.ResourceValidationFailed), now,
			))
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed,
				"this account has no TOTP enrollment to preserve; supply a replacement enrollment", nil)
			return
		case err != nil:
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "administrator recovery failed", nil)
			return
		}
		emitter.emit(r.Context(), identityRecoveryAllowedAuditRow(audit.Row{}, result, now))
		responseBody, err := json.Marshal(result)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "administrator recovery response failed", nil)
			return
		}
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		headers.Set("Cache-Control", "no-store")
		_, _ = idem.CompleteSuccess(r.Context(), reservation.Reference(), idempotency.StoredResponse{
			StatusCode: http.StatusOK,
			Header:     headers,
			Body:       append(responseBody, '\n'),
		})
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusOK, result)
	}
}

// operatorRecoveryActor reports whether the request carries the installation
// recovery credential (or is running on an unauthenticated local development
// stack). A human session — even an installation administrator's — is refused:
// this route exists precisely for the case where no human credential works,
// and letting one signed-in account rewrite another's password and second
// factor would be a lateral takeover no other route offers.
func operatorRecoveryActor(ctx context.Context) bool {
	switch idempotency.ActorFromContext(ctx) {
	case idempotency.ActorRecoveryOperator, idempotency.ActorLocalDeveloper:
		return true
	default:
		return false
	}
}

func identityRecoveryIdempotencyKey(raw idempotency.Key) string {
	sum := sha256.Sum256([]byte("midnight/identity-recovery/v1\x00" + string(raw)))
	return "identity-recovery-" + hex.EncodeToString(sum[:])
}

func identityRecoveryAllowedAuditRow(
	row audit.Row,
	result identity.ResetAdministratorCredentialsResult,
	now time.Time,
) audit.Row {
	row.OccurredAt = now
	row.ActorType = "system"
	row.AuthMethod = "operator-bearer"
	row.ResourceKind = "user"
	row.ResourceID = result.User.ID
	row.Action = "identity.administrator.credentials.reset"
	row.PolicyDecision = "allowed"
	row.Attrs = map[string]any{"actor_class": "operator-recovery"}
	row.After = map[string]any{
		"user_id":          result.User.ID,
		"totp_reenrolled":  result.TOTPReenrolled,
		"sessions_revoked": result.SessionsRevoked,
		"mfa_enabled":      result.User.MFAEnabled,
	}
	return row
}

func identityRecoveryDeniedAuditRow(row audit.Row, userID, reasonCode string, now time.Time) audit.Row {
	row.OccurredAt = now
	row.ActorType = "system"
	row.AuthMethod = "operator-bearer"
	row.ResourceKind = "user"
	row.ResourceID = userID
	row.Action = "identity.administrator.credentials.reset.denied"
	row.ReasonCode = reasonCode
	row.PolicyDecision = "denied"
	row.Attrs = map[string]any{"actor_class": "operator-recovery"}
	return row
}
