package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

// TestStaticProblemCallsitesUseRegisteredCodeStatusPairs prevents newErrorResponse's
// fail-closed fallback from silently changing a statically declared API problem
// into INTERNAL_ERROR/500. Dynamic pairs are covered by their mapper tests and
// the registry's runtime emission test.
func TestStaticProblemCallsitesUseRegisteredCodeStatusPairs(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	parsed := make([]*ast.File, 0, len(files))
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		parsed = append(parsed, file)
	}
	for _, file := range parsed {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calledFunctionName(call.Fun)
			statusIndex, codeIndex, supported := problemCallArgumentIndexes(name)
			if !supported || len(call.Args) <= codeIndex {
				return true
			}
			status, statusOK := staticHTTPStatus(call.Args[statusIndex])
			code, codeOK := staticFailureCode(call.Args[codeIndex])
			if !statusOK || !codeOK {
				return true
			}
			if !failurecodes.IsPermittedProblemStatus(code, status) {
				position := fileSet.Position(call.Pos())
				t.Errorf("%s:%d emits unregistered problem pair %s/%d", filepath.Base(position.Filename), position.Line, code, status)
			}
			return true
		})
	}
}

func staticHTTPStatus(expression ast.Expr) (int, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || packageName.Name != "http" {
		return 0, false
	}
	status, ok := httpStatusConstants[selector.Sel.Name]
	return status, ok
}

func staticFailureCode(expression ast.Expr) (failurecodes.Code, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || packageName.Name != "failurecodes" {
		return "", false
	}
	code, ok := failureCodeConstants[selector.Sel.Name]
	return code, ok
}

var httpStatusConstants = map[string]int{
	"StatusBadGateway": http.StatusBadGateway, "StatusBadRequest": http.StatusBadRequest,
	"StatusConflict": http.StatusConflict, "StatusForbidden": http.StatusForbidden,
	"StatusInternalServerError": http.StatusInternalServerError, "StatusLengthRequired": http.StatusLengthRequired,
	"StatusMethodNotAllowed": http.StatusMethodNotAllowed, "StatusNotFound": http.StatusNotFound,
	"StatusNotImplemented": http.StatusNotImplemented, "StatusPreconditionFailed": http.StatusPreconditionFailed,
	"StatusPreconditionRequired": http.StatusPreconditionRequired, "StatusRequestEntityTooLarge": http.StatusRequestEntityTooLarge,
	"StatusServiceUnavailable": http.StatusServiceUnavailable, "StatusTooManyRequests": http.StatusTooManyRequests,
	"StatusUnauthorized": http.StatusUnauthorized, "StatusUnprocessableEntity": http.StatusUnprocessableEntity,
	"StatusUnsupportedMediaType": http.StatusUnsupportedMediaType, "StatusUpgradeRequired": http.StatusUpgradeRequired,
}

var failureCodeConstants = func() map[string]failurecodes.Code {
	return map[string]failurecodes.Code{
		"InternalError": failurecodes.InternalError, "NotImplemented": failurecodes.NotImplemented,
		"CapabilityUnavailable": failurecodes.CapabilityUnavailable, "MethodNotAllowed": failurecodes.MethodNotAllowed,
		"OperationNotFound": failurecodes.OperationNotFound, "OperationInvalidTransition": failurecodes.OperationInvalidTransition,
		"CursorInvalid": failurecodes.CursorInvalid, "PaginationLimitExceeded": failurecodes.PaginationLimitExceeded,
		"AuthTokenHeaderInvalid": failurecodes.AuthTokenHeaderInvalid, "AuthRequiredMissing": failurecodes.AuthRequiredMissing,
		"AuthCredentialsInvalid": failurecodes.AuthCredentialsInvalid, "AuthSessionInvalid": failurecodes.AuthSessionInvalid,
		"AuthorizationDenied": failurecodes.AuthorizationDenied, "AuthRateLimited": failurecodes.AuthRateLimited,
		"RequestRateLimited": failurecodes.RequestRateLimited, "IdempotencyKeyInvalid": failurecodes.IdempotencyKeyInvalid,
		"IdempotencyKeyRequired": failurecodes.IdempotencyKeyRequired, "IdempotencyKeyReused": failurecodes.IdempotencyKeyReused,
		"IdempotencyKeyReusedDifferentBody": failurecodes.IdempotencyKeyReusedDifferentBody,
		"IdempotencyRequestInProgress":      failurecodes.IdempotencyRequestInProgress,
		"ResourceNotFound":                  failurecodes.ResourceNotFound, "ResourceValidationFailed": failurecodes.ResourceValidationFailed,
		"ResourceConflict": failurecodes.ResourceConflict, "PreconditionRequired": failurecodes.PreconditionRequired,
		"PreconditionFailed": failurecodes.PreconditionFailed, "SourceArtifactQuotaExceeded": failurecodes.SourceArtifactQuotaExceeded,
		"SourceArtifactUploadBackpressure": failurecodes.SourceArtifactUploadBackpressure,
		"BackupOffhostMissing":             failurecodes.BackupOffhostMissing, "BackupDestinationWriteFailed": failurecodes.BackupDestinationWriteFailed,
		"RestorePlanSerializationFailed": failurecodes.RestorePlanSerializationFailed,
		"DeployJobEnqueueFailed":         failurecodes.DeployJobEnqueueFailed, "DeploySourceRefUnsupported": failurecodes.DeploySourceRefUnsupported,
		"DeployCancelQueueUnavailable": failurecodes.DeployCancelQueueUnavailable, "DeployCancelUnsafe": failurecodes.DeployCancelUnsafe,
		"LogQueryInvalid": failurecodes.LogQueryInvalid, "LogStoreUnavailable": failurecodes.LogStoreUnavailable,
		"MFARequired": failurecodes.MFARequired, "MFAInvalidCode": failurecodes.MFAInvalidCode,
		"VolumeAttached": failurecodes.VolumeAttached, "VolumeResizeDownUnsupported": failurecodes.VolumeResizeDownUnsupported,
		"ServiceDeleteRunning": failurecodes.ServiceDeleteRunning, "ServiceDeleteBlocked": failurecodes.ServiceDeleteBlocked,
		"VersionSkewRejected": failurecodes.VersionSkewRejected, "AgentClockSkew": failurecodes.AgentClockSkew,
		"AgentHeartbeatReplay": failurecodes.AgentHeartbeatReplay, "AgentHeartbeatExpired": failurecodes.AgentHeartbeatExpired,
		"AgentHeartbeatTampered": failurecodes.AgentHeartbeatTampered, "AgentStatusReportReplay": failurecodes.AgentStatusReportReplay,
		"AgentStatusReportExpired": failurecodes.AgentStatusReportExpired, "AgentStatusReportTampered": failurecodes.AgentStatusReportTampered,
	}
}()

func calledFunctionName(expression ast.Expr) string {
	switch function := expression.(type) {
	case *ast.Ident:
		return function.Name
	case *ast.SelectorExpr:
		return function.Sel.Name
	default:
		return ""
	}
}

func problemCallArgumentIndexes(name string) (status, code int, ok bool) {
	switch name {
	case "writeRequestError":
		return 2, 3, true
	case "WriteError":
		return 1, 2, true
	case "marshalErrorResponse", "newErrorResponse":
		return 0, 1, true
	case "writeDeployIntakeTerminalError", "writeUpTerminalError", "agentTerminalError":
		return 4, 5, true
	default:
		return 0, 0, false
	}
}

func TestProblemStatusConstantsUsedByASTGateRemainHTTPValues(t *testing.T) {
	// Keep imports and constant-evaluation assumptions honest if the AST gate is
	// refactored. These are compile-time sentinels, not behavior assertions.
	if strconv.Itoa(http.StatusInternalServerError) != "500" {
		t.Fatal("unexpected HTTP status constant")
	}
}
