// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/tools"
)

func TestCallerResolvesCredentialsWithoutSDKEnvironmentAuthorization(t *testing.T) {
	for _, explicitBase := range []bool{false, true} {
		name := "environment-base"
		if explicitBase {
			name = "explicit-compatible-base"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Api-Key") != "selected-key" || r.Header.Get("Authorization") != "" {
					t.Error("request did not contain only the selected API credential")
					http.Error(w, "wrong credential", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"qa","type":"message","role":"assistant","model":"qa-model","content":[{"type":"text","text":"isolated"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`))
			}))
			defer server.Close()
			t.Setenv("ANTHROPIC_API_KEY", "")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "unrelated-bearer")           // not-a-failure-code
			t.Setenv("ANTHROPIC_PROFILE", "missing-profile-must-not-load") // not-a-failure-code
			t.Setenv("ANTHROPIC_BASE_URL", server.URL)                     // not-a-failure-code
			opts := Options{APIKey: "selected-key", Registry: tools.New(), DefaultModel: "qa-model"}
			if explicitBase {
				opts.BaseURL = server.URL
			}
			caller, err := NewCaller(opts)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := caller.Send(ctx, agent.CallRequest{Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "test")}, Thinking: agent.ThinkingOff, MaxTokens: 16})
			if err != nil || result.FinalText != "isolated" {
				t.Fatalf("isolated call failed: %v", err)
			}
		})
	}
}
