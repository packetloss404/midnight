// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/diagnosis"
	agentopenai "github.com/midnight/midnight/internal/agent/openai"
	"github.com/midnight/midnight/internal/agent/tools"
)

func TestWorkerDiagnosisSelectsProviderCredentialProtocolAndModel(t *testing.T) {
	for _, tc := range []struct {
		provider, override, model, key, path string
	}{
		{"", "", diagnosis.DefaultModel, "qa-anthropic", "/v1/messages"},
		{"claude", "", diagnosis.DefaultModel, "qa-anthropic", "/v1/messages"},
		{"anthropic", "qa-diagnosis", "qa-diagnosis", "qa-anthropic", "/v1/messages"},
		{"openai", "", agentopenai.DefaultChatModel, "qa-openai", "/v1/responses"},
		{"openai", "qa-diagnosis", "qa-diagnosis", "qa-openai", "/v1/responses"},
		{"minimax", "", "MiniMax-M2.7", "qa-minimax", "/anthropic/v1/messages"},
		{"minimax", "qa-diagnosis", "qa-diagnosis", "qa-minimax", "/anthropic/v1/messages"},
	} {
		t.Run(tc.provider+"/"+tc.override, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				key := r.Header.Get("X-Api-Key")
				if tc.provider == "openai" {
					key = r.Header.Get("Authorization")
					if key == "Bearer "+tc.key {
						key = tc.key
					}
				} else if r.Header.Get("Authorization") != "" {
					t.Error("compatible provider inherited an unrelated authorization credential")
					http.Error(w, "unexpected authorization", http.StatusBadRequest)
					return
				}
				if key != tc.key || r.URL.Path != tc.path || request.Model != tc.model {
					t.Errorf("wrong selected credential/protocol/model: path=%q model=%q", r.URL.Path, request.Model)
					http.Error(w, "unexpected provider request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.provider == "openai" {
					_, _ = fmt.Fprintf(w, `{"id":"qa-response","status":"completed","model":%q,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"diagnosis proof"}]}],"usage":{"input_tokens":2,"output_tokens":2}}`, tc.model)
				} else {
					_, _ = fmt.Fprintf(w, `{"id":"qa-message","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"diagnosis proof"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":2}}`, tc.model)
				}
			}))
			defer server.Close()
			t.Setenv("MIDNIGHT_AGENT_PROVIDER", tc.provider)
			t.Setenv("MIDNIGHT_AGENT_DIAGNOSIS_MODEL", tc.override)
			t.Setenv("ANTHROPIC_API_KEY", "qa-anthropic")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "unrelated-anthropic-auth-token") // not-a-failure-code
			if tc.provider == "minimax" {
				t.Setenv("ANTHROPIC_API_KEY", "")
			}
			t.Setenv("ANTHROPIC_BASE_URL", server.URL) // not-a-failure-code
			t.Setenv("OPENAI_API_KEY", "qa-openai")
			t.Setenv("MIDNIGHT_OPENAI_BASE_URL", server.URL+"/v1")
			t.Setenv("MIDNIGHT_OPENAI_CHAT_MODEL", "")
			t.Setenv("MINIMAX_API_KEY", "qa-minimax")
			t.Setenv("MIDNIGHT_MINIMAX_BASE_URL", server.URL+"/anthropic")
			t.Setenv("MIDNIGHT_MINIMAX_CHAT_MODEL", "")
			caller, model, err := newWorkerDiagnosisCaller(tools.New())
			if err != nil || model != tc.model {
				t.Fatalf("caller model=%q err=%v", model, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := caller.Send(ctx, agent.CallRequest{Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "diagnose")}, Model: model, MaxTokens: 32, MaxIterations: 1, Thinking: agent.ThinkingOff})
			if err != nil || result.FinalText != "diagnosis proof" || result.Usage.Model != model || calls.Load() != 1 {
				t.Fatalf("provider did not complete its selected request: result=%+v calls=%d err=%v", result, calls.Load(), err)
			}
		})
	}
}

func TestWorkerDiagnosisNeverFallsBackToAnotherProvidersKey(t *testing.T) {
	for _, provider := range []string{"openai", "minimax", "unknown"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("MIDNIGHT_AGENT_PROVIDER", provider)
			t.Setenv("ANTHROPIC_API_KEY", "must-not-be-forwarded")
			t.Setenv("OPENAI_API_KEY", "")
			t.Setenv("MINIMAX_API_KEY", "")
			if caller, _, err := newWorkerDiagnosisCaller(tools.New()); err == nil || caller != nil {
				t.Fatal("missing or unknown provider used an unrelated credential")
			}
		})
	}
}
