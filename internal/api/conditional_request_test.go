package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestStrongVersionETagAndRequiredIfMatch(t *testing.T) {
	if got := strongVersionETag(42); got != `"42"` {
		t.Fatalf("ETag = %q", got)
	}
	request := httptest.NewRequest(http.MethodPatch, "/resource", nil)
	request.Header.Set("If-Match", `"42"`)
	recorder := httptest.NewRecorder()
	if version, ok := requireIfMatchVersion(recorder, request); !ok || version != 42 {
		t.Fatalf("version = %d, ok = %v, body=%s", version, ok, recorder.Body.String())
	}
}

func TestEnvironmentConditionalLifecycleUsesAtomicVersionCAS(t *testing.T) {
	store := NewMemoryEnvironmentStore(nil)
	environment, err := store.CreateEnvironment(context.Background(), Environment{
		ID: "env_conditional", ProjectID: "prj_a", Slug: "dev", Name: "Development", Tier: "development",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := environmentsHandler(store)

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, v1alphaPrefix+"/environments/"+string(environment.ID), nil))
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"1"` {
		t.Fatalf("GET = %d ETag=%q body=%s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodPatch, v1alphaPrefix+"/environments/"+string(environment.ID), strings.NewReader(`{"name":"Next"}`)))
	assertConditionalProblem(t, missing, http.StatusPreconditionRequired, failurecodes.PreconditionRequired)

	staleRequest := httptest.NewRequest(http.MethodPatch, v1alphaPrefix+"/environments/"+string(environment.ID), strings.NewReader(`{"name":"Next"}`))
	staleRequest.Header.Set("If-Match", `"2"`)
	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, staleRequest)
	assertConditionalProblem(t, stale, http.StatusPreconditionFailed, failurecodes.PreconditionFailed)

	updateRequest := httptest.NewRequest(http.MethodPatch, v1alphaPrefix+"/environments/"+string(environment.ID), strings.NewReader(`{"name":"Next"}`))
	updateRequest.Header.Set("If-Match", `"1"`)
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, updateRequest)
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"2"` {
		t.Fatalf("PATCH = %d ETag=%q body=%s", updated.Code, updated.Header().Get("ETag"), updated.Body.String())
	}

	staleDeleteRequest := httptest.NewRequest(http.MethodDelete, v1alphaPrefix+"/environments/"+string(environment.ID), nil)
	staleDeleteRequest.Header.Set("If-Match", `"1"`)
	staleDelete := httptest.NewRecorder()
	handler.ServeHTTP(staleDelete, staleDeleteRequest)
	assertConditionalProblem(t, staleDelete, http.StatusPreconditionFailed, failurecodes.PreconditionFailed)

	deleteRequest := httptest.NewRequest(http.MethodDelete, v1alphaPrefix+"/environments/"+string(environment.ID), nil)
	deleteRequest.Header.Set("If-Match", `"2"`)
	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, deleteRequest)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestMemoryEnvironmentConditionalUpdateHasOneWinner(t *testing.T) {
	store := NewMemoryEnvironmentStore(nil)
	environment, err := store.CreateEnvironment(context.Background(), Environment{
		ID: "env_race", ProjectID: "prj_a", Slug: "dev", Name: "Development", Tier: "development",
	})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct{ err error }
	outcomes := make(chan outcome, 2)
	for _, name := range []string{"A", "B"} {
		go func(name string) {
			candidate := environment
			candidate.Name = name
			_, err := store.UpdateEnvironmentCAS(context.Background(), candidate, 1)
			outcomes <- outcome{err: err}
		}(name)
	}
	var success, stale int
	for range 2 {
		result := <-outcomes
		switch {
		case result.err == nil:
			success++
		case errors.Is(result.err, ErrResourcePreconditionFailed):
			stale++
		default:
			t.Fatalf("unexpected update error: %v", result.err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success/stale = %d/%d", success, stale)
	}
	got, err := store.GetEnvironment(context.Background(), orailtypes.ID("env_race"))
	if err != nil || got.ResourceVersion != 2 {
		t.Fatalf("environment = %#v, error=%v", got, err)
	}
}

func TestMemoryEnvironmentConditionalDeleteChecksVersionBeforeProtection(t *testing.T) {
	store := NewMemoryEnvironmentStore(nil)
	environment, err := store.CreateEnvironment(context.Background(), Environment{
		ID: "env_protected", ProjectID: "prj_a", Slug: "production", Name: "Production", Tier: "production", IsProtected: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteEnvironmentCAS(context.Background(), environment.ID, environment.ResourceVersion+1); !errors.Is(err, ErrResourcePreconditionFailed) {
		t.Fatalf("stale protected delete error = %v, want precondition failure", err)
	}
	if err := store.DeleteEnvironmentCAS(context.Background(), environment.ID, environment.ResourceVersion); !errors.Is(err, ErrResourceConflict) {
		t.Fatalf("current protected delete error = %v, want protected-resource conflict", err)
	}
}

func assertConditionalProblem(t *testing.T, recorder *httptest.ResponseRecorder, status int, code failurecodes.Code) {
	t.Helper()
	var problem ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != status || problem.Code != code {
		t.Fatalf("status/code = %d/%s, want %d/%s; body=%s", recorder.Code, problem.Code, status, code, recorder.Body.String())
	}
}

func TestRequiredIfMatchFailures(t *testing.T) {
	for name, values := range map[string][]string{
		"missing":       nil,
		"weak":          {`W/"2"`},
		"wildcard":      {`*`},
		"list":          {`"1", "2"`},
		"invalid":       {`"version"`},
		"leading-zero":  {`"01"`},
		"plus-sign":     {`"+1"`},
		"negative-zero": {`"-0"`},
		"repeated":      {`"1"`, `"1"`},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodDelete, "/resource", nil)
			for _, value := range values {
				request.Header.Add("If-Match", value)
			}
			recorder := httptest.NewRecorder()
			if _, ok := requireIfMatchVersion(recorder, request); ok {
				t.Fatal("precondition unexpectedly accepted")
			}
			wantStatus := http.StatusPreconditionFailed
			wantCode := failurecodes.PreconditionFailed
			if name == "missing" {
				wantStatus = http.StatusPreconditionRequired
				wantCode = failurecodes.PreconditionRequired
			}
			var problem ErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != wantStatus || problem.Code != wantCode {
				t.Fatalf("status/code = %d/%s, want %d/%s", recorder.Code, problem.Code, wantStatus, wantCode)
			}
		})
	}
}

func setTestIfMatchFromExpectedVersion(t *testing.T, request *http.Request, body string) {
	t.Helper()
	var payload struct {
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if json.Unmarshal([]byte(body), &payload) == nil && payload.ExpectedVersion != nil && *payload.ExpectedVersion >= 0 {
		request.Header.Set("If-Match", strongVersionETag(*payload.ExpectedVersion))
	}
}
