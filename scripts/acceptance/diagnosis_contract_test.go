package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDiagnosisRequiresRealIntakeEventLineageAndRemovalReadback(t *testing.T) {
	for _, mode := range []string{"success", "wrong-model", "wrong-lineage", "removal-still-running", "lost-intake-response"} {
		t.Run(mode, func(t *testing.T) {
			image := "registry.example.invalid/qa/failure@sha256:" + strings.Repeat("a", 64)
			r := newRunner(config{values: map[string]string{"QA_API_BASE": "https://qa.example.invalid", "QA_PROJECT_ID": "prj_qa", "QA_SERVICE_ID": "svc_qa", "QA_ENVIRONMENT_ID": "env_qa", "MODEL": "fixture-model", "DIAGNOSIS_IMAGE": image}}, &receipt{RunID: strings.Repeat("a", 24)})
			r.token = "midn_user_fixture"
			intakes := 0
			removed := false
			intakeKey := ""
			r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				path := req.URL.Path
				if req.Header.Get("Authorization") != "Bearer midn_user_fixture" {
					t.Error("not a product human-session call")
				}
				if strings.HasSuffix(path, "/deployments") && req.Method == "POST" {
					intakes++
					if intakes > 1 && req.Header.Get("Idempotency-Key") != intakeKey {
						t.Error("lost intake recovered with different key")
					}
					intakeKey = req.Header.Get("Idempotency-Key")
					if mode == "lost-intake-response" && intakes == 1 {
						return httpJSON(502, nil), nil
					}
					return httpJSON(202, map[string]string{"id": "op_failed", "state": "QUEUED"}), nil
				}
				if path == "/api/v1alpha/operations/op_failed" {
					return httpJSON(200, map[string]string{"id": "op_failed", "state": "FAILED"}), nil
				}
				if strings.HasSuffix(path, "/deployments") && req.Method == "GET" {
					return httpJSON(200, map[string]any{"items": []any{map[string]string{"id": "dep_failed", "operation_id": "op_failed", "service_id": "svc_qa", "environment_id": "env_qa", "status": "FAILED", "image": image}}}), nil
				}
				if strings.HasSuffix(path, "/operations/op_failed/events") {
					response := httpJSON(200, nil)
					response.Body = io.NopCloser(strings.NewReader("event: operation\ndata: {\"message\":\"deploy:diagnosis_attached diagnosis_id=conv_diagnosis\"}\n\n"))
					return response, nil
				}
				if path == "/api/v1alpha/agent/conversations/conv_diagnosis" {
					model := "fixture-model"
					if mode == "wrong-model" {
						model = "other-model"
					}
					operation := "op_failed"
					if mode == "wrong-lineage" {
						operation = "op_other"
					}
					return httpJSON(200, map[string]any{"service_id": "svc_qa", "model": model, "metadata": map[string]string{"midnight.operation_id": operation, "midnight.deployment_id": "dep_failed"}}), nil
				}
				if strings.HasSuffix(path, "/conv_diagnosis/messages") {
					return httpJSON(200, map[string]any{"data": []any{map[string]any{"content": []any{map[string]string{"kind": "text", "text": "Observed actual failure evidence."}}}}}), nil
				}
				if strings.HasSuffix(path, "/dep_failed/actions/remove") {
					removed = true
					return httpJSON(202, map[string]string{"id": "op_remove"}), nil
				}
				if path == "/api/v1alpha/operations/op_remove" {
					return httpJSON(200, map[string]string{"id": "op_remove", "state": "SUCCEEDED"}), nil
				}
				if strings.HasSuffix(path, "/dep_failed") {
					observed := "removed"
					if mode == "removal-still-running" {
						observed = "running"
					}
					return httpJSON(200, map[string]any{"id": "dep_failed", "service_id": "svc_qa", "environment_id": "env_qa", "runtime_observed_state": observed, "active": false}), nil
				}
				t.Error("unexpected diagnosis path " + path)
				return httpJSON(500, nil), nil
			})
			err := r.diagnosis(context.Background())
			if mode == "success" {
				if err != nil || len(r.receipt.Checks) != 2 || !r.receipt.Cleanup.Verified {
					t.Fatalf("diagnosis contract failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("unproven diagnosis or cleanup reported success")
			}
			if !removed {
				t.Fatal("owned failed deployment not removed")
			}
			if mode == "removal-still-running" && r.receipt.Cleanup.Verified {
				t.Fatal("action acknowledgement without removal readback accepted")
			}
		})
	}
}
