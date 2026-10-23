// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// DefaultUpBodyLimitBytes is the 50 MiB compatibility default. Human-session
// uploads have a hard 50 MiB ceiling and use this setting only when it lowers
// that ceiling; the retained automation/operator route may still tune its
// separate legacy extraction cap in either direction.
const DefaultUpBodyLimitBytes int64 = 50 << 20

// upBodyLimitBytes returns the configured tarball body cap, falling back to
// DefaultUpBodyLimitBytes when the env var is empty or malformed.
func upBodyLimitBytes() int64 {
	raw := strings.TrimSpace(os.Getenv("MIDNIGHT_UP_BODY_LIMIT_BYTES"))
	if raw == "" {
		return DefaultUpBodyLimitBytes
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return DefaultUpBodyLimitBytes
	}
	return n
}

// UpExtractionRoot is the parent directory where extracted tarballs live.
// Override via `MIDNIGHT_UP_EXTRACTION_ROOT`; empty falls back to the
// operating-system temp directory.
func upExtractionRoot() string {
	root := strings.TrimSpace(os.Getenv("MIDNIGHT_UP_EXTRACTION_ROOT"))
	if root == "" {
		root = os.TempDir()
	}
	return root
}

// createServiceUp handles `POST /api/v1alpha/services/{id}/up`. The body is
// the gzipped tar of the operator's working directory; on accept we extract
// it under a temp path, enqueue a build job whose source intent is
// `local://<path>` under the internal-only legacy-local provider, and return
// the operation envelope.
func createServiceUp(w http.ResponseWriter, r *http.Request, serviceID orailtypes.ID, serviceStore ServiceStore, operationStore OperationStore, queueStore queue.Store, deploymentStore DeploymentStore, durableIntake DurableDeployIntake, idem idempotency.Store, emitter *auditEmitter, clock func() time.Time, scopeResolvers ...MutationScopeResolver) {
	if queueStore == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "up intake queue is not wired", map[string]any{"service_id": serviceID})
		return
	}
	if operationStore == nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation store is not wired", map[string]any{"service_id": serviceID})
		return
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}

	limit := upBodyLimitBytes()
	contentType := strings.TrimSpace(strings.ToLower(r.Header.Get("Content-Type")))
	if contentType == "" {
		contentType = "application/gzip"
	}
	if !isAcceptableUpContentType(contentType) {
		writeRequestError(w, r, http.StatusUnsupportedMediaType, failurecodes.ResourceValidationFailed, "up upload requires application/gzip body", map[string]any{
			"service_id":   serviceID,
			"content_type": contentType,
		})
		return
	}

	key := idempotency.Key(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyRequired, "Idempotency-Key header is required", map[string]any{"service_id": serviceID})
		return
	}

	// Cap the body before we touch the disk.
	limited := http.MaxBytesReader(w, r.Body, limit)
	defer limited.Close()

	service, err := serviceStore.GetService(r.Context(), serviceID)
	if errors.Is(err, ErrResourceNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", map[string]any{"service_id": serviceID})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service lookup failed", map[string]any{"service_id": serviceID})
		return
	}
	environmentID := strings.TrimSpace(r.Header.Get("X-Midnight-Environment"))
	if environmentID == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "up request requires X-Midnight-Environment", map[string]any{
			"service_id": serviceID,
			"field":      "X-Midnight-Environment",
		})
		return
	}
	if scopeResolver := firstMutationScopeResolver(scopeResolvers); scopeResolver != nil {
		_, err := scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
			ProjectID:          string(service.ProjectID),
			ServiceID:          string(service.ID),
			EnvironmentID:      environmentID,
			RequireProject:     true,
			RequireService:     true,
			RequireEnvironment: true,
		})
		if err != nil {
			writeMutationScopeError(w, r, err)
			return
		}
	}

	// Stage the tarball to a temp file so we can hash it for idempotency
	// AND keep a stable on-disk artefact to extract from.
	tmpRoot := upExtractionRoot()
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "up extraction root create failed", map[string]any{"service_id": serviceID})
		return
	}
	tarball, err := os.CreateTemp(tmpRoot, "midnight-up-*.tar.gz")
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "up tarball staging failed", map[string]any{"service_id": serviceID})
		return
	}
	tarballPath := tarball.Name()
	cleanupTarball := func() { _ = os.Remove(tarballPath) }
	defer func() {
		_ = tarball.Close()
	}()

	bodyBytes, err := io.Copy(tarball, limited)
	if err != nil {
		cleanupTarball()
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "up upload exceeds the configured body limit", map[string]any{
				"service_id": serviceID,
				"limit":      limit,
			})
			return
		}
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "up upload body read failed", map[string]any{
			"service_id": serviceID,
			"reason":     err.Error(),
		})
		return
	}
	if bodyBytes == 0 {
		cleanupTarball()
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "up upload body is empty", map[string]any{"service_id": serviceID})
		return
	}

	// Reserve idempotency keyed on a hash of the tarball plus the service.
	if _, err := tarball.Seek(0, io.SeekStart); err != nil {
		cleanupTarball()
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "up tarball seek failed", map[string]any{"service_id": serviceID})
		return
	}
	bodyHash, err := idempotency.BodyHashSHA256Reader(tarball)
	if err != nil {
		cleanupTarball()
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "up tarball hash failed", map[string]any{"service_id": serviceID})
		return
	}
	bodyHash = legacyUpIdempotencyBodyHash(r.Header, bodyHash)
	fingerprint := idempotencyRequestFingerprintForRoute(
		r,
		v1alphaPrefix+"/services/{service_id}/up",
		bodyHash,
	)
	reservation, err := idem.Reserve(r.Context(), key, fingerprint, 24*time.Hour)
	if errors.Is(err, idempotency.ErrInvalidKey) {
		cleanupTarball()
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", map[string]any{"service_id": serviceID})
		return
	}
	if errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
		cleanupTarball()
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", map[string]any{"service_id": serviceID})
		return
	}
	if errors.Is(err, idempotency.ErrKeyReused) {
		cleanupTarball()
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", map[string]any{"service_id": serviceID})
		return
	}
	if errors.Is(err, idempotency.ErrRequestInProgress) {
		cleanupTarball()
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", map[string]any{"service_id": serviceID, "operation_id": reservation.OperationID})
		return
	}
	if err != nil {
		cleanupTarball()
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency reservation failed", map[string]any{"service_id": serviceID})
		return
	}
	ref := reservation.Reference()
	if reservationHasExactStoredResponse(reservation) {
		cleanupTarball()
		stored := reservation.Response.Clone()
		w.Header().Set("Idempotent-Replayed", "true")
		writeStoredResponse(w, stored)
		return
	}
	if reservation.OperationID != "" {
		cleanupTarball()
		operation, err := operationStore.Get(r.Context(), orailtypes.ID(reservation.OperationID))
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation replay failed", map[string]any{"operation_id": reservation.OperationID})
			return
		}
		deploymentID := ""
		if deploymentStore != nil {
			if deployment, lookupErr := deploymentStore.GetDeploymentByOperation(r.Context(), string(operation.ID)); lookupErr == nil {
				deploymentID = deployment.ID
			}
		}
		writeUpOperationAccepted(w, operation, deploymentID, true)
		return
	}

	// Extract the tarball to a sibling directory.
	if _, err := tarball.Seek(0, io.SeekStart); err != nil {
		cleanupTarball()
		writeUpTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "up tarball rewind failed", map[string]any{"service_id": serviceID})
		return
	}
	checkoutDir, err := os.MkdirTemp(tmpRoot, "midnight-up-src-*")
	if err != nil {
		cleanupTarball()
		writeUpTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "up checkout dir create failed", map[string]any{"service_id": serviceID})
		return
	}
	checkoutOwned := true
	defer func() {
		if checkoutOwned {
			_ = os.RemoveAll(checkoutDir)
		}
	}()
	archiveLimits := sourceartifact.DefaultLimits
	if err := extractGzippedTarball(tarball, checkoutDir, archiveLimits); err != nil {
		cleanupTarball()
		_ = os.RemoveAll(checkoutDir)
		status := http.StatusBadRequest
		message := "up tarball is not a valid gzipped tar"
		if errors.Is(err, errUpArchiveLimitExceeded) {
			status = http.StatusRequestEntityTooLarge
			message = "up tarball exceeds archive extraction limits"
		}
		writeUpTerminalError(w, r, idem, ref, status, failurecodes.ResourceValidationFailed, message, map[string]any{
			"service_id":                  serviceID,
			"reason":                      err.Error(),
			"entry_limit":                 archiveLimits.MaxEntries,
			"expanded_stream_limit_bytes": archiveLimits.MaxUncompressedBytes,
		})
		return
	}
	if err := tarball.Close(); err != nil {
		cleanupTarball()
		writeUpTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "up tarball close failed", map[string]any{"service_id": serviceID})
		return
	}
	// The extracted checkout is the queue handoff artifact; keeping a second
	// tarball copy only leaks disk and is not used for replay.
	cleanupTarball()

	commitMessage := strings.TrimSpace(r.Header.Get("X-Midnight-Commit-Message"))
	// Postgres timestamps have microsecond precision. Use that exact instant for
	// the stored response as well as the durable rows so a replay and later GET
	// cannot differ only because the response retained nanoseconds.
	enqueuedAt := clock().UTC().Truncate(time.Microsecond)
	if durableIntake != nil {
		operation := orailtypes.Operation{
			ID:        defaultOperationID(),
			Kind:      "build",
			State:     orailtypes.OperationPending,
			CreatedAt: enqueuedAt,
			UpdatedAt: enqueuedAt,
		}
		intakeRequest := DeployIntakeRequest{
			EnvironmentID: environmentID,
			Source: &DeploySourceIntent{
				Provider: DeploySourceProviderLegacyLocal,
				Repo:     "local://" + checkoutDir,
				Ref:      firstNonEmpty(commitMessage, "local-tarball"),
			},
		}
		canonical, err := canonicalDeploymentRecord(
			service,
			intakeRequest,
			operation.ID,
			acceptedDeploymentIntentKey(operation.ID),
		)
		if err != nil {
			writeUpTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "canonical deployment prepare failed", map[string]any{"operation_id": operation.ID})
			return
		}
		canonical.ID = mintID(orailtypes.IDPrefixDeployment)
		intakeRequest.DeploymentID = canonical.ID
		intakeRequest.InstanceID = canonical.ID + "-r0"
		intakeRequest.PreviousActive = resolvePreviousActiveDeployment(r.Context(), deploymentStore, service.ID, environmentID, canonical.ID)
		canonical.ExpectedActiveID = previousActiveDeploymentID(intakeRequest.PreviousActive)
		payload, err := buildDeployJobPayload(operation.ID, service, intakeRequest, enqueuedAt)
		if err != nil {
			writeUpTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "up payload encode failed", map[string]any{"operation_id": operation.ID})
			return
		}
		enqueueRequest := buildDeployEnqueueRequest(operation.ID, service, enqueuedAt, payload)
		enqueueRequest.Queue = queue.Build
		response, err := tenantUpAcceptedStoredResponse(operation, canonical.ID)
		if err != nil {
			writeUpTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "up response encode failed", map[string]any{"operation_id": operation.ID})
			return
		}
		result, err := durableIntake.CommitDeployIntake(r.Context(), DeployIntakeCommit{
			Operation:      operation,
			Deployment:     canonical,
			IdempotencyKey: key,
			IdempotencyRef: ref,
			Response:       response,
			Enqueue:        enqueueRequest,
			InitialMessage: durableDeployIntakeMessage(service, intakeRequest, queue.Build) + " source=local",
			Now:            enqueuedAt,
		})
		if err != nil {
			writeUpTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "durable up intake commit failed", map[string]any{"operation_id": operation.ID})
			return
		}
		// The durable outbox now owns the checkout path carried in its payload.
		checkoutOwned = false
		deliveryState := "pending"
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "operation",
			ResourceID:   string(result.Operation.ID),
			ProjectID:    string(service.ProjectID),
			Action:       "service.up.accept",
			After: map[string]any{
				"service_id":          string(service.ID),
				"project_id":          string(service.ProjectID),
				"queue":               string(queue.Build),
				"outbox_id":           result.OutboxID,
				"delivery_state":      deliveryState,
				"deployment_id":       result.Deployment.ID,
				"operation_id":        string(result.Operation.ID),
				"parent_operation_id": string(result.Operation.ID),
				"operation_kind":      result.Operation.Kind,
				"source_kind":         "local",
				"body_bytes":          bodyBytes,
				"environment":         environmentID,
			},
		})
		writeStoredResponse(w, response)
		return
	}

	capture := newIdempotencyResponseCapture(w)
	w = capture
	defer capture.Complete(r, idem, ref)

	operation, err := CreateQueuedOperation(r.Context(), operationStore, "build")
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation create failed", map[string]any{"service_id": serviceID})
		return
	}
	if _, err := idem.AttachOperation(r.Context(), ref, string(operation.ID)); err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency operation attach failed", map[string]any{"operation_id": operation.ID})
		return
	}

	intakeRequest := DeployIntakeRequest{
		EnvironmentID: environmentID,
		Source: &DeploySourceIntent{
			Provider: DeploySourceProviderLegacyLocal,
			Repo:     "local://" + checkoutDir,
			Ref:      firstNonEmpty(commitMessage, "local-tarball"),
			Subpath:  "",
		},
	}
	canonical, err := createCanonicalDeployment(
		r.Context(),
		deploymentStore,
		service,
		intakeRequest,
		operation.ID,
		acceptedDeploymentIntentKey(operation.ID),
	)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "canonical deployment create failed", map[string]any{"operation_id": operation.ID})
		return
	}
	intakeRequest.DeploymentID = canonical.ID
	intakeRequest.InstanceID = canonical.ID + "-r0"
	intakeRequest.PreviousActive = resolvePreviousActiveDeployment(r.Context(), deploymentStore, service.ID, environmentID, canonical.ID)
	payload, err := buildDeployJobPayload(operation.ID, service, intakeRequest, enqueuedAt)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "up payload encode failed", map[string]any{"operation_id": operation.ID})
		return
	}
	enqueueRequest := buildDeployEnqueueRequest(operation.ID, service, enqueuedAt, payload)
	enqueueRequest.Queue = queue.Build
	job, err := queueStore.Enqueue(r.Context(), enqueueRequest)
	if err != nil {
		failDeployIntakeOperation(r, operationStore, operation.ID, failurecodes.DeployJobEnqueueFailed, "up build job enqueue failed")
		_ = deploymentStore.FailDeployment(r.Context(), canonical.ID, deploy.DeploymentQueued, string(failurecodes.DeployJobEnqueueFailed), "up build job enqueue failed")
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.DeployJobEnqueueFailed, "up build job enqueue failed", map[string]any{"operation_id": operation.ID})
		return
	}
	// The durable queue now owns the checkout. The build worker removes it
	// only after a successful build-to-deploy handoff so failed jobs can retry.
	checkoutOwned = false
	if _, err := operationStore.AppendEvent(r.Context(), OperationEvent{OperationID: operation.ID, State: operation.State, Message: "build job enqueued: job_id=" + job.ID + " operation_id=" + string(operation.ID) + " source=local"}); err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation event append failed", map[string]any{"operation_id": operation.ID, "job_id": job.ID})
		return
	}
	if _, err := operationStore.AppendEvent(r.Context(), OperationEvent{
		OperationID: operation.ID,
		State:       operation.State,
		Message:     deployIntakeQueuedEventMessage(service, intakeRequest, job) + " up_kind=local checkout=" + checkoutDir,
		CreatedAt:   enqueuedAt,
	}); err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "up intake metadata event append failed", map[string]any{"operation_id": operation.ID})
		return
	}
	emitter.emit(r.Context(), audit.Row{
		ResourceKind: "operation",
		ResourceID:   string(operation.ID),
		ProjectID:    string(service.ProjectID),
		Action:       "service.up.accept",
		After: map[string]any{
			"service_id":          string(service.ID),
			"project_id":          string(service.ProjectID),
			"queue":               string(queue.Build),
			"job_id":              job.ID,
			"operation_id":        string(operation.ID),
			"parent_operation_id": string(operation.ID),
			"operation_kind":      operation.Kind,
			"source_kind":         "local",
			"checkout":            checkoutDir,
			"tarball":             tarballPath,
			"body_bytes":          bodyBytes,
			"environment":         environmentID,
		},
	})

	writeUpOperationAccepted(w, operation, canonical.ID, false)
}

// upOperationResponse is the JSON envelope `midnight up` consumes. It is a
// superset of the bare orailtypes.Operation so the CLI can pluck the
// operation + deployment + route fields without a second round-trip.
type upOperationResponse struct {
	OperationID  string                `json:"operation_id"`
	DeploymentID string                `json:"deployment_id,omitempty"`
	Operation    orailtypes.Operation  `json:"operation"`
	Route        *upOperationRouteInfo `json:"route,omitempty"`
	Replayed     bool                  `json:"replayed,omitempty"`
}

type upOperationRouteInfo struct {
	URL string `json:"url,omitempty"`
}

func writeUpOperationAccepted(w http.ResponseWriter, operation orailtypes.Operation, deploymentID string, replayed bool) {
	w.Header().Set("Location", v1alphaPrefix+"/operations/"+string(operation.ID))
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	response := upOperationResponse{
		OperationID:  string(operation.ID),
		DeploymentID: deploymentID,
		Operation:    operation,
		Replayed:     replayed,
	}
	WriteJSON(w, http.StatusAccepted, response)
}

func writeUpTerminalError(w http.ResponseWriter, r *http.Request, idem idempotency.Store, ref idempotency.Reference, status int, code failurecodes.Code, message string, details map[string]any) {
	body, err := marshalErrorResponse(status, code, message, middleware.FromContext(r.Context()), details)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "error response encode failed", details)
		return
	}
	stored := idempotency.StoredResponse{
		StatusCode: responseStatus(body, status),
		Header:     http.Header{"Content-Type": []string{"application/problem+json"}},
		Body:       body,
	}
	if ref.Key != "" {
		if _, err := idem.CompleteFailure(r.Context(), ref, stored); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency failure completion failed", details)
			return
		}
	}
	writeStoredResponse(w, stored)
}

func isAcceptableUpContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return false
	}
	switch strings.ToLower(mediaType) {
	case "application/gzip",
		"application/x-gzip",
		"application/x-tar+gzip",
		"application/x-compressed-tar":
		return true
	default:
		return false
	}
}

func legacyUpIdempotencyBodyHash(header http.Header, archiveHash string) string {
	fingerprintBody, _ := json.Marshal(struct {
		ArchiveSHA256      string   `json:"archive_sha256"`
		EnvironmentHeaders []string `json:"environment_headers"`
		CommitHeaders      []string `json:"commit_message_headers"`
	}{
		ArchiveSHA256:      archiveHash,
		EnvironmentHeaders: append([]string(nil), header.Values("X-Midnight-Environment")...),
		CommitHeaders:      append([]string(nil), header.Values("X-Midnight-Commit-Message")...),
	})
	return idempotency.BodyHashSHA256(fingerprintBody)
}

var errUpArchiveLimitExceeded = errors.New("up archive limit exceeded")

type upExpandedLimitReader struct {
	reader   io.Reader
	limit    int64
	consumed int64
}

func (reader *upExpandedLimitReader) Read(buffer []byte) (int, error) {
	remaining := reader.limit - reader.consumed
	if remaining <= 0 {
		var probe [1]byte
		n, err := reader.reader.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("%w: expanded tar stream exceeds %d bytes", errUpArchiveLimitExceeded, reader.limit)
		}
		return 0, err
	}
	if int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	n, err := reader.reader.Read(buffer)
	reader.consumed += int64(n)
	return n, err
}

// extractGzippedTarball decompresses the supplied gzip stream and extracts
// its tar entries under destDir. It rejects entries that resolve outside
// destDir, hard- or symlinks (defense-in-depth: the build pipeline is
// chrooted but we still reject). Entry count and the complete expanded tar
// stream—including headers, metadata, padding, empty entries, and trailing
// bytes—are bounded by the same defaults as content-addressed uploads.
func extractGzippedTarball(r io.Reader, destDir string, limits sourceartifact.Limits) error {
	if limits.MaxEntries <= 0 || limits.MaxUncompressedBytes <= 0 {
		return fmt.Errorf("%w: archive limits must be positive", errUpArchiveLimitExceeded)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip header: %w", err)
	}
	defer gz.Close()
	expanded := &upExpandedLimitReader{
		reader: gz,
		limit:  limits.MaxUncompressedBytes,
	}
	tr := tar.NewReader(expanded)
	var (
		entryCount int
		fileBytes  int64
	)
	cleanDest, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("abs dest: %w", err)
	}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, errUpArchiveLimitExceeded) {
			return err
		}
		if err != nil {
			return fmt.Errorf("tar header: %w", err)
		}
		entryCount++
		if entryCount > limits.MaxEntries {
			return fmt.Errorf("%w: entry count exceeds %d", errUpArchiveLimitExceeded, limits.MaxEntries)
		}
		// Reject absolute paths and traversals.
		name := strings.TrimLeft(filepath.ToSlash(header.Name), "/")
		if name == "" {
			continue
		}
		if strings.Contains(name, "..") {
			return fmt.Errorf("tarball entry %q escapes destination", header.Name)
		}
		target := filepath.Join(cleanDest, filepath.FromSlash(name))
		rel, err := filepath.Rel(cleanDest, target)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("tarball entry %q escapes destination", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > limits.MaxUncompressedBytes-fileBytes {
				return fmt.Errorf("%w: regular-file bytes exceed %d", errUpArchiveLimitExceeded, limits.MaxUncompressedBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", target, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return fmt.Errorf("open %s: %w", target, err)
			}
			n, err := io.CopyN(f, tr, header.Size)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if errors.Is(err, errUpArchiveLimitExceeded) {
				return err
			}
			if err != nil {
				return fmt.Errorf("write %s: %w", target, err)
			}
			if n != header.Size {
				return fmt.Errorf("write %s: truncated archive entry", target)
			}
			fileBytes += n
		case tar.TypeSymlink, tar.TypeLink:
			// Quietly skip links; the build pipeline does not need them.
			continue
		default:
			// Unknown type: skip rather than fail so legitimate metadata
			// entries (PAX headers, etc.) survive a round-trip.
			continue
		}
	}
	if entryCount == 0 {
		return errors.New("tar stream has no entries")
	}
	trailingBytes, err := io.Copy(io.Discard, expanded)
	if errors.Is(err, errUpArchiveLimitExceeded) {
		return err
	}
	if err != nil {
		return fmt.Errorf("gzip trailer: %w", err)
	}
	if trailingBytes != 0 {
		return fmt.Errorf("tar stream has %d trailing decompressed bytes", trailingBytes)
	}
	return nil
}
