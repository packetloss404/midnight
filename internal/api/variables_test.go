// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/secrets"
)

func TestVariablesHandlerCreatesMetadataOnlySealedVariable(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/variables", strings.NewReader(`{
		"name":"DATABASE_URL",
		"workspace_id":"wsp_1",
		"project_id":"prj_1",
		"environment_id":"env_prod",
		"service_id":"svc_api",
		"scope":"sealed",
		"value":"postgres://user:super-secret@host/db"
	}`))
	req.Header.Set("Idempotency-Key", "var-create-db")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "super-secret") || strings.Contains(rr.Body.String(), "postgres://") {
		t.Fatalf("response leaked plaintext: %s", rr.Body.String())
	}
	if location := rr.Header().Get("Location"); !strings.HasPrefix(location, "/api/v1alpha/variables/sealed_") {
		t.Fatalf("Location = %q, want sealed variable route", location)
	}
	var meta secrets.VariableMeta
	if err := json.Unmarshal(rr.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Name != "DATABASE_URL" || meta.Scope != secrets.ScopeSealed || meta.LengthBucket != "17-64" {
		t.Fatalf("meta = %#v, want sealed DATABASE_URL metadata", meta)
	}

	getRR := httptest.NewRecorder()
	router.ServeHTTP(getRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/variables/"+string(meta.ID), nil))
	if getRR.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRR.Code, getRR.Body.String())
	}
	if strings.Contains(getRR.Body.String(), "super-secret") || strings.Contains(getRR.Body.String(), "postgres://") {
		t.Fatalf("get response leaked plaintext: %s", getRR.Body.String())
	}
}

func TestVariablesHandlerReplaysCreatedMetadata(t *testing.T) {
	router := NewRouter()
	body := `{"name":"EMPTY_VALUE","scope":"secret","value":""}`
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/variables", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "var-create-empty")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	first := post()
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d body=%s, want 201", first.Code, first.Body.String())
	}
	second := post()
	if second.Code != http.StatusCreated || second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status/header = %d/%q body=%s, want created replay", second.Code, second.Header().Get("Idempotent-Replayed"), second.Body.String())
	}
	if second.Body.String() != first.Body.String() || second.Header().Get("Location") != first.Header().Get("Location") {
		t.Fatalf("replay body/location = %q/%q, want %q/%q", second.Body.String(), second.Header().Get("Location"), first.Body.String(), first.Header().Get("Location"))
	}
}

func TestVariablesHandlerReplaysValidationFailure(t *testing.T) {
	router := NewRouter()
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/variables", strings.NewReader(`{"name":"","scope":"secret","value":"x"}`))
		req.Header.Set("Idempotency-Key", "var-invalid-name")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	first := post()
	if first.Code != http.StatusBadRequest {
		t.Fatalf("first status = %d body=%s, want 400", first.Code, first.Body.String())
	}
	second := post()
	if second.Code != http.StatusBadRequest || second.Header().Get("Idempotent-Replayed") != "true" || second.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d/%q/%s, want verbatim 400 replay %s", second.Code, second.Header().Get("Idempotent-Replayed"), second.Body.String(), first.Body.String())
	}
}

func TestVariablesHandlerListsMetadataOnly(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/variables", strings.NewReader(`{"name":"PUBLIC_URL","scope":"plain","value":"https://example.test"}`))
	req.Header.Set("Idempotency-Key", "var-create-public")
	router.ServeHTTP(httptest.NewRecorder(), req)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/variables", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "https://example.test") {
		t.Fatalf("list response leaked variable value: %s", rr.Body.String())
	}
	var metas []secrets.VariableMeta
	if err := json.Unmarshal(rr.Body.Bytes(), &metas); err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Name != "PUBLIC_URL" || metas[0].Scope != secrets.ScopePlain { // not-a-failure-code
		t.Fatalf("metas = %#v, want PUBLIC_URL metadata", metas)
	}
}

func TestVariablesHandlerReturnsReferenceMetadata(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/variables", strings.NewReader(`{"name":"REDIS_URL","scope":"secret","value":"redis://${{cache.HOST}}:${{cache.PORT}}/0"}`))
	req.Header.Set("Idempotency-Key", "var-ref-shape")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201", rr.Code, rr.Body.String())
	}
	var meta secrets.VariableMeta
	if err := json.Unmarshal(rr.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ValueKind != "reference" {
		t.Fatalf("value_kind = %q, want reference", meta.ValueKind)
	}
	if len(meta.References) != 2 {
		t.Fatalf("references = %#v, want 2", meta.References)
	}
	if meta.References[0].Kind != "service_var" || meta.References[0].Service != "cache" || meta.References[0].Name != "HOST" {
		t.Fatalf("references[0] = %#v", meta.References[0])
	}
}

func TestVariablesHandlerRejectsInvalidReferenceSyntax(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/variables", strings.NewReader(`{"name":"BROKEN","scope":"secret","value":"prefix ${{missing-close"}`))
	req.Header.Set("Idempotency-Key", "var-bad-ref")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.ResourceValidationFailed {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.ResourceValidationFailed)
	}
	if reason, _ := got.Error.Details["reason"].(string); reason != "variable.references.invalid" {
		t.Fatalf("details.reason = %q, want variable.references.invalid", reason)
	}
}

func TestVariablesHandlerRejectsInvalidScope(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/variables", strings.NewReader(`{"name":"TOKEN","scope":"fake","value":"x"}`))
	req.Header.Set("Idempotency-Key", "var-bad-scope")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.ResourceValidationFailed {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.ResourceValidationFailed)
	}
}

func TestVariablesHandlerUpdateDeleteLifecycleIsWriteOnlyAndIdempotent(t *testing.T) {
	router := NewRouter()
	create := httptest.NewRequest(http.MethodPost, "/api/v1alpha/variables", strings.NewReader(`{
		"name":"API_TOKEN",
		"scope":"sealed",
		"value":"first-plaintext"
	}`))
	create.Header.Set("Idempotency-Key", "variable-lifecycle-create")
	createdRR := httptest.NewRecorder()
	router.ServeHTTP(createdRR, create)
	if createdRR.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createdRR.Code, createdRR.Body.String())
	}
	var created secrets.VariableMeta
	if err := json.Unmarshal(createdRR.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	updateBody := `{"scope":"sealed","value":"rotated-plaintext","expected_version":1}`
	update := httptest.NewRequest(http.MethodPatch, "/api/v1alpha/variables/"+string(created.ID), strings.NewReader(updateBody))
	setTestIfMatchFromExpectedVersion(t, update, updateBody)
	update.Header.Set("Idempotency-Key", "variable-lifecycle-update")
	updatedRR := httptest.NewRecorder()
	router.ServeHTTP(updatedRR, update)
	if updatedRR.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", updatedRR.Code, updatedRR.Body.String())
	}
	if strings.Contains(updatedRR.Body.String(), "rotated-plaintext") || strings.Contains(updatedRR.Body.String(), "first-plaintext") {
		t.Fatalf("update response leaked plaintext: %s", updatedRR.Body.String())
	}
	var updated secrets.VariableMeta
	if err := json.Unmarshal(updatedRR.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updatedRR.Header().Get("ETag") != `"2"` {
		t.Fatalf("updated version/etag = %d/%q, want 2", updated.Version, updatedRR.Header().Get("ETag"))
	}

	staleBody := `{"scope":"sealed","value":"must-not-win","expected_version":1}`
	stale := httptest.NewRequest(http.MethodPatch, "/api/v1alpha/variables/"+string(created.ID), strings.NewReader(staleBody))
	setTestIfMatchFromExpectedVersion(t, stale, staleBody)
	stale.Header.Set("Idempotency-Key", "variable-lifecycle-stale")
	staleRR := httptest.NewRecorder()
	router.ServeHTTP(staleRR, stale)
	if staleRR.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale status = %d body=%s, want 412", staleRR.Code, staleRR.Body.String())
	}
	if strings.Contains(staleRR.Body.String(), "must-not-win") {
		t.Fatalf("conflict response leaked plaintext: %s", staleRR.Body.String())
	}
	var conflict ErrorResponse
	if err := json.Unmarshal(staleRR.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Error.Code != failurecodes.PreconditionFailed ||
		conflict.Error.Details["expected_version"] != float64(1) ||
		conflict.Error.Details["actual_version"] != float64(2) {
		t.Fatalf("conflict = %#v", conflict.Error)
	}
	staleReplay := httptest.NewRequest(http.MethodPatch, "/api/v1alpha/variables/"+string(created.ID), strings.NewReader(staleBody))
	setTestIfMatchFromExpectedVersion(t, staleReplay, staleBody)
	staleReplay.Header.Set("Idempotency-Key", "variable-lifecycle-stale")
	staleReplayRR := httptest.NewRecorder()
	router.ServeHTTP(staleReplayRR, staleReplay)
	if staleReplayRR.Code != http.StatusPreconditionFailed ||
		staleReplayRR.Header().Get("Idempotent-Replayed") != "true" ||
		staleReplayRR.Body.String() != staleRR.Body.String() {
		t.Fatalf("stale replay = %d/%q/%s, want verbatim 412 replay", staleReplayRR.Code, staleReplayRR.Header().Get("Idempotent-Replayed"), staleReplayRR.Body.String())
	}
	reusedBody := `{"scope":"sealed","value":"different-body","expected_version":1}`
	reused := httptest.NewRequest(http.MethodPatch, "/api/v1alpha/variables/"+string(created.ID), strings.NewReader(reusedBody))
	setTestIfMatchFromExpectedVersion(t, reused, reusedBody)
	reused.Header.Set("Idempotency-Key", "variable-lifecycle-stale")
	reusedRR := httptest.NewRecorder()
	router.ServeHTTP(reusedRR, reused)
	if reusedRR.Code != http.StatusConflict {
		t.Fatalf("reused-key status = %d body=%s, want 409", reusedRR.Code, reusedRR.Body.String())
	}
	var reusedConflict ErrorResponse
	if err := json.Unmarshal(reusedRR.Body.Bytes(), &reusedConflict); err != nil {
		t.Fatal(err)
	}
	if reusedConflict.Error.Code != failurecodes.IdempotencyKeyReusedDifferentBody {
		t.Fatalf("reused-key code = %q, want %q", reusedConflict.Error.Code, failurecodes.IdempotencyKeyReusedDifferentBody)
	}

	deleteBody := `{"expected_version":2}`
	remove := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1alpha/variables/"+string(created.ID), strings.NewReader(deleteBody))
		setTestIfMatchFromExpectedVersion(t, req, deleteBody)
		req.Header.Set("Idempotency-Key", "variable-lifecycle-delete")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	firstDelete := remove()
	if firstDelete.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s, want 204", firstDelete.Code, firstDelete.Body.String())
	}
	replayDelete := remove()
	if replayDelete.Code != http.StatusNoContent || replayDelete.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("delete replay = %d/%q", replayDelete.Code, replayDelete.Header().Get("Idempotent-Replayed"))
	}
	getRR := httptest.NewRecorder()
	router.ServeHTTP(getRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/variables/"+string(created.ID), nil))
	if getRR.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d body=%s, want 404", getRR.Code, getRR.Body.String())
	}
}

func TestVariablesHandlerCreateRequiresVersionForExistingName(t *testing.T) {
	router := NewRouter()
	post := func(key, value string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/variables", strings.NewReader(`{"name":"TOKEN","scope":"secret","value":"`+value+`"}`))
		req.Header.Set("Idempotency-Key", key)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	if first := post("variable-create-first", "alpha"); first.Code != http.StatusCreated {
		t.Fatalf("first = %d body=%s", first.Code, first.Body.String())
	}
	conflict := post("variable-create-second", "beta")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("second = %d body=%s, want 409", conflict.Code, conflict.Body.String())
	}
	if strings.Contains(conflict.Body.String(), "beta") {
		t.Fatalf("conflict leaked proposed value: %s", conflict.Body.String())
	}
}

func TestVariablesHandlerRejectsOwnershipDriftOnUpdate(t *testing.T) {
	router := NewRouter()
	create := httptest.NewRequest(http.MethodPost, "/api/v1alpha/variables", strings.NewReader(`{
		"name":"SCOPED_TOKEN",
		"workspace_id":"workspace-1",
		"project_id":"project-1",
		"environment_id":"environment-1",
		"service_id":"service-1",
		"scope":"secret",
		"value":"original-value"
	}`))
	create.Header.Set("Idempotency-Key", "scoped-variable-create")
	createdRR := httptest.NewRecorder()
	router.ServeHTTP(createdRR, create)
	if createdRR.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createdRR.Code, createdRR.Body.String())
	}
	var created secrets.VariableMeta
	if err := json.Unmarshal(createdRR.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	update := httptest.NewRequest(http.MethodPatch, "/api/v1alpha/variables/"+string(created.ID), strings.NewReader(`{
		"workspace_id":"workspace-1",
		"project_id":"project-other",
		"environment_id":"environment-1",
		"service_id":"service-1",
		"scope":"secret",
		"value":"must-not-move",
		"expected_version":1
	}`))
	update.Header.Set("If-Match", `"1"`)
	update.Header.Set("Idempotency-Key", "scoped-variable-drift")
	updateRR := httptest.NewRecorder()
	router.ServeHTTP(updateRR, update)
	if updateRR.Code != http.StatusBadRequest {
		t.Fatalf("update status = %d body=%s, want 400", updateRR.Code, updateRR.Body.String())
	}
	if strings.Contains(updateRR.Body.String(), "must-not-move") {
		t.Fatalf("scope error leaked proposed value: %s", updateRR.Body.String())
	}

	getRR := httptest.NewRecorder()
	router.ServeHTTP(getRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/variables/"+string(created.ID), nil))
	if getRR.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", getRR.Code, getRR.Body.String())
	}
	var got secrets.VariableMeta
	if err := json.Unmarshal(getRR.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Project != "project-1" {
		t.Fatalf("metadata changed after rejected scope drift: %#v", got)
	}
}

func TestFilterVariableMetasRequiresEnvironmentBeforeIncludingShared(t *testing.T) {
	metas := []secrets.VariableMeta{
		{ID: "service-token", Project: "project-1", Environment: "environment-1", Service: "service-1", Name: "TOKEN"},
		{ID: "shared-same-env", Project: "project-1", Environment: "environment-1", Name: "SHARED"},
		{ID: "shared-other-env", Project: "project-2", Environment: "environment-2", Name: "OTHER_SHARED"},
	}

	serviceOnly := filterVariableMetas(metas, "", "", "service-1")
	if len(serviceOnly) != 1 || serviceOnly[0].ID != "service-token" {
		t.Fatalf("service-only filter = %#v, want exact service rows only", serviceOnly)
	}
	scoped := filterVariableMetas(metas, "project-1", "environment-1", "service-1")
	if len(scoped) != 2 || scoped[0].ID != "service-token" || scoped[1].ID != "shared-same-env" {
		t.Fatalf("scoped service filter = %#v, want service plus same-environment shared rows", scoped)
	}
}
