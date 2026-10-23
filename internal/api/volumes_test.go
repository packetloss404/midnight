// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/volume"
)

func newVolumeTestRouter(t *testing.T) (http.Handler, volume.Store) {
	t.Helper()
	store := volume.NewMemoryStore()
	router := NewRouterWithDataStack(DataStack{
		VolumeStore:      store,
		VolumeMountStore: NewMemoryVolumeMountStore(),
	})
	return router, store
}

func volumeReq(t *testing.T, router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != "" {
		buf = bytes.NewBufferString(body)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if _, ok := headers["Idempotency-Key"]; !ok && (method == http.MethodPost || method == http.MethodPatch || method == http.MethodDelete) {
		req.Header.Set("Idempotency-Key", strings.ReplaceAll(method+"-"+path, "/", "_"))
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func decodeVolume(t *testing.T, body []byte) VolumeView {
	t.Helper()
	var v VolumeView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decodeVolume: %v body=%s", err, string(body))
	}
	return v
}

func TestVolumeCreateAndGet(t *testing.T) {
	t.Parallel()
	router, _ := newVolumeTestRouter(t)
	body := `{
		"project_id":"prj_one",
		"environment_id":"env_prod",
		"service_id":"svc_app",
		"name":"data",
		"size_bytes":1073741824,
		"mount_path":"/data"
	}`
	rr := volumeReq(t, router, http.MethodPost, "/api/v1alpha/volumes", body, map[string]string{"Idempotency-Key": "vol-create-1"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	got := decodeVolume(t, rr.Body.Bytes())
	if !strings.HasPrefix(got.ID, "vol_") {
		t.Fatalf("ID = %s, want vol_*", got.ID)
	}
	if got.MountPath != "/data" {
		t.Fatalf("MountPath = %s, want /data", got.MountPath)
	}
	if loc := rr.Header().Get("Location"); !strings.HasSuffix(loc, got.ID) {
		t.Fatalf("Location = %s, want suffix %s", loc, got.ID)
	}

	// GET roundtrip
	rr2 := volumeReq(t, router, http.MethodGet, "/api/v1alpha/volumes/"+got.ID, "", nil)
	if rr2.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", rr2.Code, rr2.Body.String())
	}
	gotGet := decodeVolume(t, rr2.Body.Bytes())
	if gotGet.ID != got.ID || gotGet.MountPath != "/data" {
		t.Fatalf("GET = %+v, want %+v", gotGet, got)
	}
}

func TestVolumeListByService(t *testing.T) {
	t.Parallel()
	router, _ := newVolumeTestRouter(t)
	createOne := func(idemKey, svc, name string) {
		body := `{"project_id":"prj_one","environment_id":"env_prod","service_id":"` + svc + `","name":"` + name + `","size_bytes":1024,"mount_path":"/data"}`
		rr := volumeReq(t, router, http.MethodPost, "/api/v1alpha/volumes", body, map[string]string{"Idempotency-Key": idemKey})
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, rr.Code, rr.Body.String())
		}
	}
	createOne("k1", "svc_a", "data1")
	createOne("k2", "svc_a", "data2")
	createOne("k3", "svc_b", "data3")

	rr := volumeReq(t, router, http.MethodGet, "/api/v1alpha/volumes?service_id=svc_a", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var out []VolumeView
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	for _, v := range out {
		if v.ServiceID != "svc_a" {
			t.Fatalf("ServiceID = %s, want svc_a", v.ServiceID)
		}
	}
}

func TestVolumeResizeUpAndRejectsDown(t *testing.T) {
	t.Parallel()
	router, _ := newVolumeTestRouter(t)
	createBody := `{"project_id":"prj_one","environment_id":"env_prod","service_id":"svc_app","name":"data","size_bytes":1024,"mount_path":"/data"}`
	rr := volumeReq(t, router, http.MethodPost, "/api/v1alpha/volumes", createBody, map[string]string{"Idempotency-Key": "create-1"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	v := decodeVolume(t, rr.Body.Bytes())

	// Resize UP
	rr = volumeReq(t, router, http.MethodPatch, "/api/v1alpha/volumes/"+v.ID, `{"size_bytes":2048}`, map[string]string{"Idempotency-Key": "resize-up"})
	if rr.Code != http.StatusOK {
		t.Fatalf("resize up status = %d, body = %s", rr.Code, rr.Body.String())
	}
	up := decodeVolume(t, rr.Body.Bytes())
	if up.SizeBytes != 2048 {
		t.Fatalf("SizeBytes after up = %d, want 2048", up.SizeBytes)
	}

	// Resize DOWN — rejected
	rr = volumeReq(t, router, http.MethodPatch, "/api/v1alpha/volumes/"+v.ID, `{"size_bytes":1024}`, map[string]string{"Idempotency-Key": "resize-down"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("resize down status = %d, want 409; body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), string(failurecodes.VolumeResizeDownUnsupported)) {
		t.Fatalf("body missing VOLUME_RESIZE_DOWN_UNSUPPORTED: %s", rr.Body.String())
	}
}

func TestVolumeDeleteRejectedWhileAttached(t *testing.T) {
	t.Parallel()
	router, store := newVolumeTestRouter(t)
	body := `{"project_id":"prj_one","environment_id":"env_prod","service_id":"svc_app","name":"data","size_bytes":1024,"mount_path":"/data"}`
	rr := volumeReq(t, router, http.MethodPost, "/api/v1alpha/volumes", body, map[string]string{"Idempotency-Key": "create-2"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	v := decodeVolume(t, rr.Body.Bytes())

	// Simulate an active attachment.
	_, err := store.RecordAttachment(t.Context(), volume.Attachment{
		VolumeID:      v.ID,
		DeploymentID:  "dep_1",
		InstanceID:    "inst_1",
		ServiceID:     "svc_app",
		EnvironmentID: "env_prod",
		MountPath:     "/data",
		Status:        volume.AttachmentActive,
		Sequence:      1,
	})
	if err != nil {
		t.Fatalf("RecordAttachment: %v", err)
	}

	rr = volumeReq(t, router, http.MethodDelete, "/api/v1alpha/volumes/"+v.ID, "", map[string]string{"Idempotency-Key": "delete-1"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("delete while attached: status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), string(failurecodes.VolumeAttached)) {
		t.Fatalf("body missing VOLUME_ATTACHED: %s", rr.Body.String())
	}
}

func TestVolumeDeleteIdempotentReplayPreservesNoContent(t *testing.T) {
	t.Parallel()
	router, _ := newVolumeTestRouter(t)
	body := `{"project_id":"prj_one","environment_id":"env_prod","service_id":"svc_app","name":"scratch","size_bytes":1024,"mount_path":"/scratch"}`
	create := volumeReq(t, router, http.MethodPost, "/api/v1alpha/volumes", body, map[string]string{"Idempotency-Key": "delete-replay-create"})
	if create.Code != http.StatusCreated {
		t.Fatalf("create: status = %d body=%s", create.Code, create.Body.String())
	}
	v := decodeVolume(t, create.Body.Bytes())
	deleteOnce := func() *httptest.ResponseRecorder {
		return volumeReq(t, router, http.MethodDelete, "/api/v1alpha/volumes/"+v.ID, "", map[string]string{"Idempotency-Key": "delete-replay"})
	}

	first := deleteOnce()
	if first.Code != http.StatusNoContent || first.Body.Len() != 0 {
		t.Fatalf("first delete = %d body=%q, want 204 with empty body", first.Code, first.Body.String())
	}
	replay := deleteOnce()
	if replay.Code != http.StatusNoContent || replay.Body.Len() != 0 {
		t.Fatalf("replayed delete = %d body=%q, want 204 with empty body", replay.Code, replay.Body.String())
	}
	if replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("Idempotent-Replayed = %q, want true", replay.Header().Get("Idempotent-Replayed"))
	}
}

func TestVolumeCreateValidates(t *testing.T) {
	t.Parallel()
	router, _ := newVolumeTestRouter(t)
	cases := []struct {
		name string
		body string
	}{
		{"missing service_id", `{"project_id":"p","environment_id":"e","name":"d","size_bytes":1024,"mount_path":"/data"}`},
		{"missing mount_path", `{"project_id":"p","environment_id":"e","service_id":"s","name":"d","size_bytes":1024}`},
		{"relative mount_path", `{"project_id":"p","environment_id":"e","service_id":"s","name":"d","size_bytes":1024,"mount_path":"data"}`},
		{"negative size", `{"project_id":"p","environment_id":"e","service_id":"s","name":"d","size_bytes":-1,"mount_path":"/data"}`},
	}
	for i, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			key := "validate-" + c.name
			_ = i
			rr := volumeReq(t, router, http.MethodPost, "/api/v1alpha/volumes", c.body, map[string]string{"Idempotency-Key": key})
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestVolumeCreateRejectsDuplicate(t *testing.T) {
	t.Parallel()
	router, _ := newVolumeTestRouter(t)
	body := `{"project_id":"p","environment_id":"e","service_id":"s","name":"d","size_bytes":1024,"mount_path":"/data"}`
	rr := volumeReq(t, router, http.MethodPost, "/api/v1alpha/volumes", body, map[string]string{"Idempotency-Key": "dup-1"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rr.Code, rr.Body.String())
	}
	rr = volumeReq(t, router, http.MethodPost, "/api/v1alpha/volumes", body, map[string]string{"Idempotency-Key": "dup-2"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("dup status = %d, want 409 body=%s", rr.Code, rr.Body.String())
	}
}

func TestVolumeGetNotFound(t *testing.T) {
	t.Parallel()
	router, _ := newVolumeTestRouter(t)
	rr := volumeReq(t, router, http.MethodGet, "/api/v1alpha/volumes/vol_missing", "", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
