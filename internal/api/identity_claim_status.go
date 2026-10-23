package api

import (
	"net/http"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
)

const identityClaimStatusPath = identityClaimPath + "/status"

// identityClaimStatusResponse is deliberately a single boolean.
//
// The console has to decide whether to render first-run setup *before* any
// credential exists, so this route cannot sit behind the operator gate. To make
// that safe the response carries no administrator email, no user count, no
// workspace slug, and no timestamps: an anonymous caller learns only whether
// this installation still needs its first administrator, which is already
// observable from the outside (an unclaimed installation accepts the one-shot
// claim, a claimed one rejects it with 409). Anything richer would turn a
// setup probe into an unauthenticated tenant-enumeration surface.
type identityClaimStatusResponse struct {
	Claimed bool `json:"claimed"`
}

func identityClaimStatusHandler(store identity.AccessStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		reader, ok := store.(identity.ClaimStatusReader)
		if store == nil || !ok {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "tenant identity storage is not configured", nil)
			return
		}
		claimed, err := reader.DefaultWorkspaceClaimed(r.Context())
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "workspace claim status failed", nil)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusOK, identityClaimStatusResponse{Claimed: claimed})
	}
}
