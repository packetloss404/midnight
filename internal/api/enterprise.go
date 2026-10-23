package api

import (
	"errors"
	"net/http"

	"github.com/midnight/midnight/internal/enterprise"
	enterpriseaudit "github.com/midnight/midnight/internal/enterprise/audit"
	"github.com/midnight/midnight/internal/enterprise/identity"
	"github.com/midnight/midnight/internal/failurecodes"
)

func enterpriseIdentityPlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	var request identity.PlanRequest
	if !decodeStrictJSONRequest(w, r, &request, "invalid enterprise identity planning request") {
		return
	}
	plan, err := identity.BuildPlan(request)
	if errors.Is(err, identity.ErrInvalidConfig) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "enterprise identity planning failed", nil)
		return
	}
	WriteJSON(w, http.StatusOK, plan)
}

func enterpriseAuditExportPlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	var request enterpriseaudit.ExportRequest
	if !decodeStrictJSONRequest(w, r, &request, "invalid enterprise audit export planning request") {
		return
	}
	plan, err := enterpriseaudit.PlanExport(request)
	if errors.Is(err, enterpriseaudit.ErrInvalidExportPlan) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "enterprise audit export planning failed", nil)
		return
	}
	WriteJSON(w, http.StatusOK, plan)
}

func enterpriseControlsPlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	var request enterprise.ControlsPlan
	if !decodeStrictJSONRequest(w, r, &request, "invalid enterprise controls planning request") {
		return
	}
	WriteJSON(w, http.StatusOK, enterprise.NewControlsPlan(request))
}
