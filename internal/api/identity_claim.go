package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

const identityClaimPath = v1alphaPrefix + "/auth/claim"

var identityWorkspaceSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

type identityClaimRequest struct {
	Email                string `json:"email"`
	DisplayName          string `json:"display_name"`
	Password             string `json:"password"`
	TOTPSecret           string `json:"totp_secret"`
	TOTPCode             string `json:"totp_code"`
	WorkspaceSlug        string `json:"workspace_slug"`
	WorkspaceDisplayName string `json:"workspace_display_name"`
}

func identityClaimHandler(
	store identity.AccessStore,
	manager *identity.Manager,
	idem idempotency.Store,
	coordinator IdentityClaimCoordinator,
	clock func() time.Time,
	emitter *auditEmitter,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if store == nil || manager == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "tenant identity storage is not configured", nil)
			return
		}
		var request identityClaimRequest
		if _, ok := decodeStrictJSONRequestBodyWithLimit(w, r, &request, "invalid workspace claim request", sensitiveJSONBodyLimitBytes); !ok {
			return
		}
		request.Email = strings.ToLower(strings.TrimSpace(request.Email))
		request.DisplayName = strings.TrimSpace(request.DisplayName)
		request.WorkspaceSlug = strings.ToLower(strings.TrimSpace(request.WorkspaceSlug))
		request.WorkspaceDisplayName = strings.TrimSpace(request.WorkspaceDisplayName)
		if !validIdentityEmail(request.Email) || request.DisplayName == "" ||
			!identityWorkspaceSlugPattern.MatchString(request.WorkspaceSlug) ||
			request.WorkspaceDisplayName == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "workspace claim fields are invalid", nil)
			return
		}
		now := clock().UTC()
		totpSecret, err := bootstrap.DecodeTOTPSecret(request.TOTPSecret)
		if err != nil || bootstrap.VerifyTOTP(totpSecret, request.TOTPCode, now) != nil {
			clearSensitiveBytes(totpSecret)
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.MFAInvalidCode, "TOTP enrollment secret and code did not verify", nil)
			return
		}
		defer clearSensitiveBytes(totpSecret)
		passwordHash, err := bootstrap.HashPassword(request.Password)
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "password does not meet the minimum requirements", nil)
			return
		}
		userID, err := newIdentityID("usr", 18)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "identity provisioning failed", nil)
			return
		}
		totpEnvelope, err := manager.SealTOTP(r.Context(), userID, totpSecret)
		if err != nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "TOTP enrollment could not be secured", nil)
			return
		}
		redactedFingerprintBody, err := json.Marshal(struct {
			Email                string `json:"email"`
			DisplayName          string `json:"display_name"`
			WorkspaceSlug        string `json:"workspace_slug"`
			WorkspaceDisplayName string `json:"workspace_display_name"`
		}{
			Email:                request.Email,
			DisplayName:          request.DisplayName,
			WorkspaceSlug:        request.WorkspaceSlug,
			WorkspaceDisplayName: request.WorkspaceDisplayName,
		})
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "workspace claim fingerprint failed", nil)
			return
		}
		rawIdempotencyKey := idempotency.Key(r.Header.Get("Idempotency-Key"))
		if err := idempotency.ValidateKey(rawIdempotencyKey); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
			return
		}
		r.Header.Set("Idempotency-Key", identityClaimIdempotencyKey(rawIdempotencyKey))
		r = r.WithContext(idempotency.WithTenant(r.Context(), idempotency.Tenant(identity.DefaultWorkspaceID)))
		claimRequest := identity.ClaimDefaultWorkspaceRequest{
			UserID:               userID,
			Email:                request.Email,
			DisplayName:          request.DisplayName,
			PasswordHash:         passwordHash,
			TOTPSecretEnvelope:   totpEnvelope,
			TOTPEnrolledAt:       now,
			WorkspaceSlug:        request.WorkspaceSlug,
			WorkspaceDisplayName: request.WorkspaceDisplayName,
			Now:                  now,
		}
		if coordinator != nil {
			result, err := coordinator.ClaimDefaultWorkspace(r.Context(), IdentityClaimCommitRequest{
				Claim: claimRequest,
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
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "workspace claim failed", nil)
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
		result, err := store.ClaimDefaultWorkspace(r.Context(), claimRequest)
		switch {
		case errors.Is(err, identity.ErrWorkspaceClaimed):
			emitter.emit(r.Context(), audit.Row{
				ActorType: "system", AuthMethod: "operator-bearer",
				WorkspaceID: identity.DefaultWorkspaceID, ResourceKind: "workspace",
				ResourceID: identity.DefaultWorkspaceID, Action: "identity.default-workspace.claim.denied",
				ReasonCode: string(failurecodes.ResourceConflict), PolicyDecision: "denied",
				Attrs: map[string]any{"actor_class": "operator-recovery"},
			})
			writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "default workspace is already claimed", nil)
			return
		case errors.Is(err, identity.ErrIdentityConflict):
			emitter.emit(r.Context(), audit.Row{
				ActorType: "system", AuthMethod: "operator-bearer",
				WorkspaceID: identity.DefaultWorkspaceID, ResourceKind: "workspace",
				ResourceID: identity.DefaultWorkspaceID, Action: "identity.default-workspace.claim.denied",
				ReasonCode: string(failurecodes.ResourceConflict), PolicyDecision: "denied",
				Attrs: map[string]any{"actor_class": "operator-recovery"},
			})
			writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "identity or workspace already exists", nil)
			return
		case err != nil:
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "workspace claim failed", nil)
			return
		}
		emitter.emit(r.Context(), audit.Row{
			ActorID: result.User.ID, ActorType: "user", AuthMethod: "password+totp",
			WorkspaceID: result.Workspace.ID, ResourceKind: "workspace",
			ResourceID: result.Workspace.ID, Action: "identity.default-workspace.claim",
			PolicyDecision: "allowed",
			After: map[string]any{
				"user_id":        result.User.ID,
				"workspace_slug": result.Workspace.Slug,
				"role":           string(identity.WorkspaceRoleAdmin),
				"mfa_enabled":    result.User.MFAEnabled,
			},
		})
		responseBody, err := json.Marshal(result)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "workspace claim response failed", nil)
			return
		}
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		headers.Set("Cache-Control", "no-store")
		_, _ = idem.CompleteSuccess(r.Context(), reservation.Reference(), idempotency.StoredResponse{
			StatusCode: http.StatusCreated,
			Header:     headers,
			Body:       append(responseBody, '\n'),
		})
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusCreated, result)
	}
}

func validIdentityEmail(email string) bool {
	at := strings.LastIndexByte(email, '@')
	return at > 0 && at < len(email)-1 && len(email) <= 254 && !strings.ContainsAny(email, " \t\r\n")
}

func newIdentityID(prefix string, size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func identityClaimIdempotencyKey(raw idempotency.Key) string {
	sum := sha256.Sum256([]byte("midnight/identity-claim/v1\x00" + string(raw)))
	return "identity-claim-" + hex.EncodeToString(sum[:])
}

func clearSensitiveBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
