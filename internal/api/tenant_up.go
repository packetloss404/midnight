// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const tenantUpEnvironmentMaxBytes = 255

// TenantUpArtifactStore is the exact content-addressed staging seam consumed
// by the human upload handler. FSStore implements it directly; tests can
// supply a deterministic implementation without filesystem side effects.
type TenantUpArtifactStore interface {
	Stage(context.Context, io.Reader, sourceartifact.Limits) (sourceartifact.Staged, error)
	Commit(context.Context, sourceartifact.Staged) (sourceartifact.Artifact, error)
	Abort(sourceartifact.Staged) error
}

func createTenantServiceUp(
	w http.ResponseWriter,
	r *http.Request,
	serviceID orailtypes.ID,
	coordinator TenantUpIntakeCoordinator,
	artifacts TenantUpArtifactStore,
) {
	if coordinator == nil || artifacts == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant source upload is unavailable", nil)
		return
	}
	productPrincipal, classified := productPrincipalFromContext(r.Context())
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !classified ||
		productPrincipal.Kind != productPrincipalHuman ||
		!principalOK ||
		!sessionOK ||
		principal.UserID == "" ||
		session.UserID != principal.UserID ||
		!strictHumanAuthorizationHeader(r.Header) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
		return
	}

	key, ok := tenantUpIdempotencyKey(w, r)
	if !ok {
		return
	}
	environmentID, ok := tenantUpEnvironmentHeader(w, r)
	if !ok {
		return
	}
	commitMessage, ok := tenantUpCommitMessageHeader(w, r)
	if !ok {
		return
	}
	if !tenantUpContentTypeAccepted(r.Header) {
		writeRequestError(w, r, http.StatusUnsupportedMediaType, failurecodes.ResourceValidationFailed, "up upload requires one application/gzip body", nil)
		return
	}

	limits := sourceartifact.DefaultLimits
	if configured := upBodyLimitBytes(); configured > 0 && configured < limits.MaxCompressedBytes {
		limits.MaxCompressedBytes = configured
	}
	expectedDigest, ok := tenantUpArchiveDigestHeader(w, r)
	if !ok {
		return
	}
	if len(r.TransferEncoding) != 0 || r.ContentLength <= 0 {
		writeRequestError(w, r, http.StatusLengthRequired, failurecodes.ResourceValidationFailed, "up upload requires an exact positive Content-Length", map[string]any{"field": "Content-Length"})
		return
	}
	if r.ContentLength > limits.MaxCompressedBytes {
		writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "up upload exceeds the configured compressed archive limit", map[string]any{
			"compressed_limit": limits.MaxCompressedBytes,
		})
		return
	}
	preflight := TenantSourceUpPreflightRequest{
		Principal:       principal,
		Session:         session,
		ServiceID:       serviceID,
		EnvironmentID:   environmentID,
		CommitMessage:   commitMessage,
		Key:             key,
		ExpectedDigest:  expectedDigest,
		CompressedBytes: r.ContentLength,
	}
	admission, err := coordinator.PreflightSourceUp(r.Context(), preflight)
	if err != nil {
		writeTenantUpCoordinatorError(w, r, err, true)
		return
	}
	replayFollower := admission.Replay
	admissionFinished := replayFollower
	physicalBytesMayRemain := false
	defer func() {
		if admissionFinished {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = coordinator.FinishSourceUpAdmission(cleanupCtx, admission, physicalBytesMayRemain)
	}()
	limited := http.MaxBytesReader(w, r.Body, limits.MaxCompressedBytes)
	defer limited.Close()
	staged, err := artifacts.Stage(r.Context(), limited, limits)
	if err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr), errors.Is(err, sourceartifact.ErrLimitExceeded):
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "up upload exceeds the configured archive limits", map[string]any{
				"compressed_limit":   limits.MaxCompressedBytes,
				"uncompressed_limit": limits.MaxUncompressedBytes,
				"entry_limit":        limits.MaxEntries,
			})
		case errors.Is(err, sourceartifact.ErrInvalidArchive):
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "up upload is not a valid gzipped tar archive", nil)
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, context.DeadlineExceeded):
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant source upload dependency is unavailable", nil)
		default:
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "source archive staging failed", nil)
		}
		return
	}
	if staged.Digest != expectedDigest || staged.CompressedBytes != r.ContentLength {
		if err := artifacts.Abort(staged); err != nil {
			physicalBytesMayRemain = true
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "source archive identity mismatch cleanup failed", nil)
			return
		}
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "up upload bytes do not match the advertised digest and length", map[string]any{"field": "X-Midnight-Archive-Digest"})
		return
	}
	if replayFollower {
		if err := artifacts.Abort(staged); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "source replay staging cleanup failed", nil)
			return
		}
		replay, err := coordinator.FinalizeSourceUpReplay(r.Context(), preflight)
		if err != nil {
			writeTenantUpCoordinatorError(w, r, err, false)
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		writeStoredResponse(w, replay)
		return
	}
	stagedOwned := true
	defer func() {
		if stagedOwned {
			_ = artifacts.Abort(staged)
		}
	}()
	physicalBytesMayRemain = true
	prepared, err := coordinator.PublishSourceUp(
		r.Context(),
		TenantSourceUpPublishRequest{
			Preflight: preflight,
			Admission: admission,
			Staged:    staged,
		},
	)
	if err != nil {
		abortErr := artifacts.Abort(staged)
		stagedOwned = false
		if abortErr != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "source archive commit and staged ownership cleanup failed", nil)
			return
		}
		writeTenantUpCoordinatorError(w, r, err, false)
		return
	}
	stagedOwned = false
	fingerprintDigest, err := tenantUpFingerprintDigest(serviceID, environmentID, commitMessage, prepared)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "tenant source upload fingerprint failed", nil)
		return
	}
	result, err := coordinator.DeploySourceArtifact(r.Context(), TenantSourceUpRequest{
		Principal:     principal,
		Session:       session,
		ServiceID:     serviceID,
		EnvironmentID: environmentID,
		CommitMessage: commitMessage,
		Artifact:      prepared,
		Admission:     admission,
		Key:           key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodPost,
			Path:       tenantUpIdempotencyPath(serviceID),
			BodySHA256: fingerprintDigest,
		},
		RequestID: middleware.FromContext(r.Context()),
		SourceIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		writeTenantUpCoordinatorError(w, r, err, false)
		return
	}
	admissionFinished = true
	if result.Response.StatusCode < 100 {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "tenant source upload failed", nil)
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

func tenantUpArchiveDigestHeader(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("X-Midnight-Archive-Digest")
	if len(values) != 1 ||
		strings.Contains(values[0], ",") ||
		values[0] != strings.TrimSpace(values[0]) ||
		!canonicalSHA256Digest(values[0]) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "exactly one canonical X-Midnight-Archive-Digest header is required", map[string]any{"field": "X-Midnight-Archive-Digest"})
		return "", false
	}
	return values[0], true
}

func strictHumanAuthorizationHeader(header http.Header) bool {
	values := header.Values("Authorization")
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return false
	}
	_, ok := singleBearerToken(header)
	return ok
}

func tenantUpIdempotencyKey(w http.ResponseWriter, r *http.Request) (idempotency.Key, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyRequired, "exactly one Idempotency-Key header is required", nil)
		return "", false
	}
	key := idempotency.Key(values[0])
	if strings.Contains(string(key), ",") || idempotency.ValidateKey(key) != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return "", false
	}
	return key, true
}

func tenantUpEnvironmentHeader(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("X-Midnight-Environment")
	if len(values) != 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "exactly one X-Midnight-Environment header is required", map[string]any{"field": "X-Midnight-Environment"})
		return "", false
	}
	value := strings.TrimSpace(values[0])
	if value == "" ||
		len(value) > tenantUpEnvironmentMaxBytes ||
		!utf8.ValidString(value) ||
		strings.Contains(value, ",") ||
		containsHeaderControl(value) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid X-Midnight-Environment header", map[string]any{"field": "X-Midnight-Environment"})
		return "", false
	}
	return value, true
}

func tenantUpCommitMessageHeader(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("X-Midnight-Commit-Message")
	if len(values) > 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "at most one X-Midnight-Commit-Message header is accepted", map[string]any{"field": "X-Midnight-Commit-Message"})
		return "", false
	}
	if len(values) == 0 {
		return "", true
	}
	value := strings.TrimSpace(values[0])
	if err := validateTenantUpMessage(value); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid X-Midnight-Commit-Message header", map[string]any{"field": "X-Midnight-Commit-Message"})
		return "", false
	}
	return value, true
}

func containsHeaderControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func tenantUpContentTypeAccepted(header http.Header) bool {
	if len(header.Values("Content-Encoding")) != 0 {
		// The representation itself is one gzip-compressed tar archive.
		// A second HTTP content-coding layer makes proxy behavior and the
		// idempotency fingerprint ambiguous, so the tenant boundary rejects it.
		return false
	}
	values := header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(values[0])
	return err == nil && isAcceptableUpContentType(strings.ToLower(mediaType))
}

func writeTenantUpCoordinatorError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
	preflight bool,
) {
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "source upload target not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to upload source to this target", nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different source upload", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
	case errors.Is(err, sourceartifact.ErrReservationInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "source upload reservation is in progress", nil)
	case errors.Is(err, sourceartifact.ErrReservationConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with a different source upload intent", nil)
	case errors.Is(err, sourceartifact.ErrQuotaExceeded):
		writeSourceArtifactLimitError(w, r, err, http.StatusRequestEntityTooLarge, failurecodes.SourceArtifactQuotaExceeded, "workspace source artifact quota is exceeded")
	case errors.Is(err, sourceartifact.ErrUploadBackpressure):
		w.Header().Set("Retry-After", "1")
		writeSourceArtifactLimitError(w, r, err, http.StatusTooManyRequests, failurecodes.SourceArtifactUploadBackpressure, "source upload concurrency is temporarily exhausted")
	case errors.Is(err, sourceartifact.ErrReservationInvalid):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "source upload reservation is invalid or expired", nil)
	case errors.Is(err, ErrResourceInvalid),
		errors.Is(err, idempotency.ErrInvalidKey),
		errors.Is(err, idempotency.ErrInvalidFingerprint):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid tenant source upload request", nil)
	case errors.Is(err, ErrTenantDeployIntakeUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant source upload dependency is unavailable", nil)
	case err != nil && preflight:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "tenant source upload preflight failed", nil)
	case err != nil:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "tenant source upload failed", nil)
	}
}

func writeSourceArtifactLimitError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
	status int,
	code failurecodes.Code,
	message string,
) {
	details := map[string]any{}
	var limitErr *sourceartifact.LimitError
	if errors.As(err, &limitErr) {
		details["quota_bytes"] = limitErr.Snapshot.QuotaBytes
		details["used_bytes"] = limitErr.Snapshot.UsedBytes
		details["reserved_bytes"] = limitErr.Snapshot.ReservedBytes
		details["requested_bytes"] = limitErr.Snapshot.RequestedBytes
	}
	writeRequestError(w, r, status, code, message, details)
}
