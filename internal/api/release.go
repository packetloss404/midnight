package api

import (
	"net/http"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/release"
)

func releaseDisconnectedBundlePlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	var request release.DisconnectedBundlePlan
	if !decodeStrictJSONRequest(w, r, &request, "invalid disconnected bundle planning request") {
		return
	}
	WriteJSON(w, http.StatusOK, release.NewDisconnectedBundlePlan(request))
}
