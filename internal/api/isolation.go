package api

import (
	"net/http"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/isolation"
)

func buildIsolationEvidencePlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	var request isolation.BuildEvidenceRequest
	if !decodeStrictJSONRequest(w, r, &request, "invalid build isolation evidence planning request") {
		return
	}
	plan, err := isolation.PlanBuildEvidence(request)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	WriteJSON(w, http.StatusOK, plan)
}
