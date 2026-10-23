package api

import (
	"net/http"

	"github.com/midnight/midnight/internal/version"
)

func CompatHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		WriteJSON(w, http.StatusOK, version.Compat())
	})
}
