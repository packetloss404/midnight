package failurecodes

import "net/http"

// ProblemType is the runtime side of the public OpenAPI x-problem-types
// registry. Status is the RFC 9457 recommended status. Statuses is the closed,
// sorted set of status codes that legacy v1alpha handlers are permitted to
// emit for the same established failure code.
type ProblemType struct {
	Code     Code
	Type     string
	Title    string
	Status   int
	Statuses []int
}

// apiProblemTypes contains codes that may appear in an HTTP ErrorResponse.
// Operation/job failure codes that are never emitted as problem details remain
// in All(), but intentionally do not widen the stable HTTP error taxonomy.
var apiProblemTypes = []ProblemType{
	problem(InternalError, "Internal error", http.StatusInternalServerError, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable),
	problem(NotImplemented, "Not implemented", http.StatusNotImplemented, http.StatusNotImplemented),
	problem(CapabilityUnavailable, "Capability unavailable", http.StatusServiceUnavailable, http.StatusServiceUnavailable),
	problem(AgentNotConfigured, "Agent not configured", http.StatusServiceUnavailable, http.StatusServiceUnavailable),
	problem(MethodNotAllowed, "Method not allowed", http.StatusMethodNotAllowed, http.StatusMethodNotAllowed),
	problem(OperationNotFound, "Operation not found", http.StatusNotFound, http.StatusNotFound),
	problem(OperationInvalidTransition, "Operation invalid transition", http.StatusConflict, http.StatusConflict),
	problem(CursorInvalid, "Cursor invalid", http.StatusBadRequest, http.StatusBadRequest),
	problem(PaginationLimitExceeded, "Pagination limit exceeded", http.StatusBadRequest, http.StatusBadRequest),
	problem(AuthTokenHeaderInvalid, "Authorization token header invalid", http.StatusUnauthorized, http.StatusUnauthorized),
	problem(AuthRequiredMissing, "Authentication required", http.StatusUnauthorized, http.StatusUnauthorized, http.StatusForbidden),
	problem(AuthCredentialsInvalid, "Authentication credentials invalid", http.StatusUnauthorized, http.StatusUnauthorized),
	problem(AuthSessionInvalid, "Authentication session invalid", http.StatusUnauthorized, http.StatusUnauthorized),
	problem(AuthorizationDenied, "Authorization denied", http.StatusForbidden, http.StatusForbidden),
	problem(AuthRateLimited, "Authentication rate limited", http.StatusTooManyRequests, http.StatusTooManyRequests),
	problem(InvitationAccountExists, "Invited account already exists", http.StatusConflict, http.StatusConflict),
	problem(AuthOperatorRecoveryRequired, "Operator recovery credential required", http.StatusForbidden, http.StatusForbidden),
	problem(RequestRateLimited, "Request rate limited", http.StatusTooManyRequests, http.StatusTooManyRequests),
	problem(IdempotencyKeyInvalid, "Idempotency key invalid", http.StatusBadRequest, http.StatusBadRequest),
	problem(IdempotencyKeyRequired, "Idempotency key required", http.StatusBadRequest, http.StatusBadRequest),
	problem(IdempotencyKeyReused, "Idempotency key reused", http.StatusConflict, http.StatusConflict),
	problem(IdempotencyKeyReusedDifferentBody, "Idempotency key reused with different body", http.StatusConflict, http.StatusConflict),
	problem(IdempotencyRequestInProgress, "Idempotency request in progress", http.StatusConflict, http.StatusConflict),
	problem(ResourceNotFound, "Resource not found", http.StatusNotFound, http.StatusNotFound),
	problem(ResourceValidationFailed, "Resource validation failed", http.StatusBadRequest, http.StatusBadRequest, http.StatusLengthRequired, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity),
	problem(ResourceConflict, "Resource conflict", http.StatusConflict, http.StatusConflict),
	problem(PreconditionRequired, "Precondition required", http.StatusPreconditionRequired, http.StatusPreconditionRequired),
	problem(PreconditionFailed, "Precondition failed", http.StatusPreconditionFailed, http.StatusPreconditionFailed),
	problem(SourceArtifactQuotaExceeded, "Source artifact quota exceeded", http.StatusRequestEntityTooLarge, http.StatusRequestEntityTooLarge),
	problem(SourceArtifactUploadBackpressure, "Source artifact upload backpressure", http.StatusTooManyRequests, http.StatusTooManyRequests),
	problem(BackupOffhostMissing, "Off-host backup unavailable", http.StatusServiceUnavailable, http.StatusBadRequest, http.StatusServiceUnavailable),
	problem(BackupDestinationWriteFailed, "Backup destination write failed", http.StatusInternalServerError, http.StatusInternalServerError),
	problem(RestorePlanSerializationFailed, "Restore plan serialization failed", http.StatusInternalServerError, http.StatusInternalServerError),
	problem(DeployJobEnqueueFailed, "Deploy job enqueue failed", http.StatusInternalServerError, http.StatusInternalServerError),
	problem(DeploySourceRefUnsupported, "Deploy source reference unsupported", http.StatusBadRequest, http.StatusBadRequest),
	problem(DeployCancelQueueUnavailable, "Deploy cancel queue unavailable", http.StatusConflict, http.StatusConflict),
	problem(DeployCancelUnsafe, "Deploy cancel unsafe", http.StatusConflict, http.StatusConflict, http.StatusServiceUnavailable),
	problem(LogQueryInvalid, "Log query invalid", http.StatusBadRequest, http.StatusBadRequest),
	problem(LogStoreUnavailable, "Log store unavailable", http.StatusServiceUnavailable, http.StatusServiceUnavailable),
	problem(MFARequired, "Multi-factor authentication required", http.StatusUnauthorized, http.StatusUnauthorized),
	problem(MFAInvalidCode, "Multi-factor authentication code invalid", http.StatusUnauthorized, http.StatusBadRequest, http.StatusUnauthorized),
	problem(VolumeAttached, "Volume attached", http.StatusConflict, http.StatusConflict),
	problem(ServiceDeleteRunning, "Service delete blocked by running deployment", http.StatusConflict, http.StatusConflict),
	problem(ServiceDeleteBlocked, "Service delete blocked by a dependent record", http.StatusConflict, http.StatusConflict),
	problem(VolumeResizeDownUnsupported, "Volume resize down unsupported", http.StatusConflict, http.StatusConflict),
	problem(VersionSkewRejected, "Version skew rejected", http.StatusUpgradeRequired, http.StatusUpgradeRequired),
	problem(AgentClockSkew, "Agent clock skew", http.StatusBadRequest, http.StatusBadRequest),
	problem(AgentHeartbeatReplay, "Agent heartbeat replay", http.StatusConflict, http.StatusConflict),
	problem(AgentHeartbeatExpired, "Agent heartbeat expired", http.StatusBadRequest, http.StatusBadRequest),
	problem(AgentHeartbeatTampered, "Agent heartbeat tampered", http.StatusUnauthorized, http.StatusUnauthorized),
	problem(AgentStatusReportReplay, "Agent status report replay", http.StatusConflict, http.StatusConflict),
	problem(AgentStatusReportExpired, "Agent status report expired", http.StatusBadRequest, http.StatusBadRequest),
	problem(AgentStatusReportTampered, "Agent status report tampered", http.StatusUnauthorized, http.StatusUnauthorized),
}

var apiProblemTypesByCode = func() map[Code]ProblemType {
	out := make(map[Code]ProblemType, len(apiProblemTypes))
	for _, definition := range apiProblemTypes {
		out[definition.Code] = definition
	}
	return out
}()

func problem(code Code, title string, status int, statuses ...int) ProblemType {
	return ProblemType{
		Code: code, Type: DocsURL(code), Title: title, Status: status,
		Statuses: statuses,
	}
}

// APIProblemTypes returns a defensive copy in stable declaration order.
func APIProblemTypes() []ProblemType {
	out := make([]ProblemType, len(apiProblemTypes))
	for index, definition := range apiProblemTypes {
		out[index] = definition
		out[index].Statuses = append([]int(nil), definition.Statuses...)
	}
	return out
}

// LookupProblemType resolves a code in the stable HTTP problem registry.
func LookupProblemType(code Code) (ProblemType, bool) {
	definition, ok := apiProblemTypesByCode[code]
	definition.Statuses = append([]int(nil), definition.Statuses...)
	return definition, ok
}

// IsPermittedProblemStatus reports whether code may be emitted with status.
func IsPermittedProblemStatus(code Code, status int) bool {
	definition, ok := apiProblemTypesByCode[code]
	if !ok {
		return false
	}
	for _, allowed := range definition.Statuses {
		if allowed == status {
			return true
		}
	}
	return false
}
