package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/midnight/midnight/internal/version"
)

func TestCompatHandler(t *testing.T) {
	rr := httptest.NewRecorder()
	CompatHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/compat", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var got version.Compatibility
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.APIVersion != version.APIVersion {
		t.Fatalf("api version = %q, want %q", got.APIVersion, version.APIVersion)
	}
}
