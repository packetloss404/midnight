package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/pkg/configcode"
)

func TestConfigDiffPlansMissingResources(t *testing.T) {
	router := NewRouter()
	body := `{"api_version":"midnight/v1alpha","projects":[{"slug":"alpha"}],"services":[{"project_slug":"alpha","slug":"web"}]}`
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/config/diff", strings.NewReader(body)))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var plan configcode.Plan
	if err := json.Unmarshal(rr.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Summary.Create != 2 || plan.Summary.Total != 2 {
		t.Fatalf("summary = %#v", plan.Summary)
	}
}

func TestConfigApplyCreatesResourcesAndSecondDiffIsNoop(t *testing.T) {
	router := NewRouter()
	body := `{"api_version":"midnight/v1alpha","projects":[{"slug":"alpha"}],"services":[{"project_slug":"alpha","slug":"web"}]}`
	applyReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/config/apply", strings.NewReader(body))
	applyReq.Header.Set("Idempotency-Key", "apply-alpha")
	applyRR := httptest.NewRecorder()

	router.ServeHTTP(applyRR, applyReq)

	if applyRR.Code != http.StatusOK {
		t.Fatalf("apply status = %d, body = %s", applyRR.Code, applyRR.Body.String())
	}
	var apply ConfigApplyResponse
	if err := json.Unmarshal(applyRR.Body.Bytes(), &apply); err != nil {
		t.Fatal(err)
	}
	if apply.Applied != 2 {
		t.Fatalf("applied = %d, want 2", apply.Applied)
	}

	diffRR := httptest.NewRecorder()
	router.ServeHTTP(diffRR, httptest.NewRequest(http.MethodPost, "/api/v1alpha/config/diff", strings.NewReader(body)))
	if diffRR.Code != http.StatusOK {
		t.Fatalf("diff status = %d, body = %s", diffRR.Code, diffRR.Body.String())
	}
	var plan configcode.Plan
	if err := json.Unmarshal(diffRR.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Summary.Noop != 2 || plan.Summary.Create != 0 {
		t.Fatalf("summary = %#v", plan.Summary)
	}
}

func TestConfigSyncFailsClosedWithoutHumanChangesetAuthority(t *testing.T) {
	router := NewRouter()
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/config/sync", strings.NewReader(`{"api_version":"midnight/v1alpha"}`)))
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("sync status=%d body=%s", rr.Code, rr.Body.String())
	}
}
