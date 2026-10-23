// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVolumeBackupCreateCLIQueuesDurableWork(t *testing.T) {
	t.Setenv(mutationJournalDirEnv, t.TempDir())
	var retention float64
	var key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1alpha/volumes/vol_1/backups" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		key = request.Header.Get("Idempotency-Key")
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		retention, _ = body["retention_days"].(float64)
		writeJSONForCLITest(w, http.StatusAccepted, map[string]any{
			"run":    map[string]any{"id": "vbk_1", "volume_id": "vol_1", "state": "pending"},
			"job_id": "job_1",
		})
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := runVolume([]string{
		"backup", "create", "--api-base", server.URL, "--token", "token",
		"--retention-days", "14", "vol_1",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if key == "" || retention != 14 || !bytes.Contains(stdout.Bytes(), []byte("vbk_1")) {
		t.Fatalf("key=%q retention=%v output=%q", key, retention, stdout.String())
	}
}

func TestVolumeRestoreApplyCLIUsesAtomicEndpoint(t *testing.T) {
	var key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1alpha/volume-restores/vrs_1/apply" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		key = request.Header.Get("Idempotency-Key")
		writeJSONForCLITest(w, http.StatusAccepted, map[string]any{
			"restore":           map[string]any{"id": "vrs_1", "state": "applied"},
			"deployment_action": map[string]any{"outbox_id": "wox_1", "created": true},
			"created":           true,
		})
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := runVolume([]string{
		"restore", "apply", "--api-base", server.URL, "--token", "token", "vrs_1",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if key == "" || !bytes.Contains(stdout.Bytes(), []byte("immutable redeploy queued")) {
		t.Fatalf("key=%q output=%q", key, stdout.String())
	}
}

func TestVolumeBackupCreateReusesPendingKeyAfterDroppedResponse(t *testing.T) {
	t.Setenv(mutationJournalDirEnv, t.TempDir())
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		keys = append(keys, request.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test server does not support hijacking")
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("Hijack: %v", err)
				return
			}
			_ = connection.Close()
			return
		}
		writeJSONForCLITest(w, http.StatusAccepted, map[string]any{
			"run":    map[string]any{"id": "vbk_replayed", "volume_id": "vol_1", "state": "pending"},
			"job_id": "job_replayed",
		})
	}))
	defer server.Close()

	args := []string{
		"backup", "create", "--api-base", server.URL, "--token", "token",
		"--retention-days", "14", "vol_1",
	}
	var firstOut, firstErr bytes.Buffer
	if code := runVolume(args, &firstOut, &firstErr); code != 1 {
		t.Fatalf("first exit=%d stdout=%q stderr=%q", code, firstOut.String(), firstErr.String())
	}
	if !bytes.Contains(firstErr.Bytes(), []byte("outcome is unknown")) ||
		!bytes.Contains(firstErr.Bytes(), []byte("--idempotency-key")) {
		t.Fatalf("first stderr=%q, want retry recovery", firstErr.String())
	}

	var retryOut, retryErr bytes.Buffer
	if code := runVolume(args, &retryOut, &retryErr); code != 0 {
		t.Fatalf("retry exit=%d stdout=%q stderr=%q", code, retryOut.String(), retryErr.String())
	}
	if len(keys) != 2 || keys[0] == "" || keys[1] != keys[0] {
		t.Fatalf("keys=%q, want dropped-response retry to reuse one key", keys)
	}
}
