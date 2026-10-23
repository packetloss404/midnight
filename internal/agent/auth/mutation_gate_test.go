// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth_test

import (
	"context"
	"testing"

	auth "github.com/midnight/midnight/internal/agent/auth"
)

func TestEnvMutationGate_Defaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		env  string
		want bool
	}{
		{"empty defaults to true", "", true},
		{"true", "true", true},
		{"yes", "yes", true},
		{"on", "on", true},
		{"false", "false", false},
		{"no", "no", false},
		{"off", "off", false},
		{"disabled", "disabled", false},
		{"unrecognized falls back to true", "??", true},
		{"0", "0", false},
		{"1", "1", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := auth.NewEnvMutationGateFromValue(tc.env)
			got := g.AllowMutations(context.Background(), auth.SessionContext{})
			if got != tc.want {
				t.Fatalf("env %q: AllowMutations = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestSessionMutationGate(t *testing.T) {
	t.Parallel()
	g := auth.NewSessionMutationGate()
	if g.AllowMutations(context.Background(), auth.SessionContext{AllowMutations: false}) {
		t.Fatalf("session gate must respect AllowMutations=false")
	}
	if !g.AllowMutations(context.Background(), auth.SessionContext{AllowMutations: true}) {
		t.Fatalf("session gate must respect AllowMutations=true")
	}
}

func TestCompositeGate_RequiresBoth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		operator bool
		session  bool
		want     bool
	}{
		{"both true", true, true, true},
		{"operator off", false, true, false},
		{"session off", true, false, false},
		{"both off", false, false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			operatorVal := "false"
			if tc.operator {
				operatorVal = "true"
			}
			gate := auth.CompositeGate{
				Operator: auth.NewEnvMutationGateFromValue(operatorVal),
				Session:  auth.NewSessionMutationGate(),
			}
			got := gate.AllowMutations(context.Background(), auth.SessionContext{AllowMutations: tc.session})
			if got != tc.want {
				t.Fatalf("composite (op=%v sess=%v) = %v, want %v", tc.operator, tc.session, got, tc.want)
			}
		})
	}
}

func TestSessionContext_SessionKey(t *testing.T) {
	t.Parallel()
	if got := (auth.SessionContext{ConversationID: "conv_1"}).SessionKey(); got != "conv_1" {
		t.Fatalf("conv id wins, got %q", got)
	}
	if got := (auth.SessionContext{MCPSessionID: "mcp_1"}).SessionKey(); got != "mcp_1" {
		t.Fatalf("mcp id falls through, got %q", got)
	}
	if got := (auth.SessionContext{}).SessionKey(); got != "" {
		t.Fatalf("empty session, got %q", got)
	}
}
