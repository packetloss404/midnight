// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/agent"
	agentauth "github.com/midnight/midnight/internal/agent/auth"
)

type captureCaller struct{ last agent.CallRequest }

func (c *captureCaller) Send(_ context.Context, req agent.CallRequest) (agent.CallResult, error) {
	c.last = req
	return agent.CallResult{FinalText: "ok"}, nil
}

func (c *captureCaller) Stream(_ context.Context, req agent.CallRequest, out chan<- agent.StreamEvent) error {
	c.last = req
	out <- agent.StreamEvent{Kind: agent.EventTextDelta, Delta: "ok"}
	out <- agent.StreamEvent{Kind: agent.EventDone, StopReason: "end_turn"}
	close(out)
	return nil
}

type stubServices struct{ agent.ServicesAPI }

func TestSendPassesToolEnvironmentWithOperatorGate(t *testing.T) {
	caller := &captureCaller{}
	svc, err := NewService(Config{
		Store:  NewMemoryStore(nil),
		Caller: caller,
		Env:    agent.Env{Services: stubServices{}, Actor: "agent:chat", AllowMutations: true},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	conv, err := svc.Start(context.Background(), StartInput{ProjectID: "prj_1", ActorID: "user_1"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if conv.Model != DefaultModel {
		t.Fatalf("conversation model = %q, want %q", conv.Model, DefaultModel)
	}
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "hi", AllowMutations: false}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if caller.last.Env.Services == nil {
		t.Fatal("tool environment was not passed to the caller")
	}
	if caller.last.Env.AllowMutations {
		t.Fatal("mutations must stay off when the user toggle is off")
	}
	if caller.last.Env.ProjectScope != "prj_1" || !strings.HasSuffix(caller.last.Env.Actor, ":user_1") {
		t.Fatalf("env scope/actor = %q/%q", caller.last.Env.ProjectScope, caller.last.Env.Actor)
	}
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "again", AllowMutations: true}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !caller.last.Env.AllowMutations {
		t.Fatal("operator gate open + user toggle on must allow mutations")
	}
}

func TestSendOperatorGateClosedOverridesUserToggle(t *testing.T) {
	caller := &captureCaller{}
	svc, _ := NewService(Config{Store: NewMemoryStore(nil), Caller: caller, Env: agent.Env{AllowMutations: false}})
	conv, _ := svc.Start(context.Background(), StartInput{ActorID: "user_1"})
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "hi", AllowMutations: true}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if caller.last.Env.AllowMutations {
		t.Fatal("operator gate closed must win over the user toggle")
	}
}

func TestSendWithoutCallerReturnsTypedError(t *testing.T) {
	svc, _ := NewService(Config{Store: NewMemoryStore(nil)})
	conv, _ := svc.Start(context.Background(), StartInput{ActorID: "user_1"})
	_, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "hi"}, nil)
	if !errors.Is(err, ErrCallerNotConfigured) {
		t.Fatalf("Send error = %v, want ErrCallerNotConfigured", err)
	}
}

// ---- budget + kill switch --------------------------------------------------

// usageCaller reports a fixed usage per turn so budget accumulation is
// deterministic.
type usageCaller struct {
	usage agent.Usage
	calls int
}

func (c *usageCaller) Send(context.Context, agent.CallRequest) (agent.CallResult, error) {
	c.calls++
	return agent.CallResult{FinalText: "ok", Usage: c.usage}, nil
}

func (c *usageCaller) Stream(_ context.Context, _ agent.CallRequest, out chan<- agent.StreamEvent) error {
	c.calls++
	usage := c.usage
	out <- agent.StreamEvent{Kind: agent.EventTextDelta, Delta: "ok"}
	out <- agent.StreamEvent{Kind: agent.EventDone, StopReason: "end_turn", Usage: &usage}
	close(out)
	return nil
}

func newBudgetedService(t *testing.T, caller agent.Caller, budget agentauth.SessionBudget, revoker agentauth.SessionRevoker) *Service {
	t.Helper()
	svc, err := NewService(Config{
		Store:   NewMemoryStore(nil),
		Caller:  caller,
		Budget:  budget,
		Revoker: revoker,
		HostID:  "test-host",
		Env:     agent.Env{Services: stubServices{}, Actor: "agent:chat"},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestSendStopsWhenConversationTokenCapExhausted(t *testing.T) {
	caller := &usageCaller{usage: agent.Usage{InputTokens: 600, OutputTokens: 500}}
	budget := agentauth.NewMemoryBudget(agentauth.BudgetConfig{
		ConversationTokens:    1000,
		ConversationCostCents: 100000,
		HostDailyCostCents:    100000,
	})
	svc := newBudgetedService(t, caller, budget, nil)

	conv, err := svc.Start(context.Background(), StartInput{ProjectID: "prj_1", ActorID: "user_1"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// First turn is allowed and consumes 1100 tokens, past the cap.
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "one"}, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	// Second turn must be refused before the model is called.
	_, err = svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "two"}, nil)
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("second Send error = %v, want ErrBudgetExhausted", err)
	}
	var budgetErr *BudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("error does not carry BudgetError: %v", err)
	}
	if budgetErr.Used.ConversationTokens != 1100 || budgetErr.Limit.ConversationTokens != 1000 {
		t.Errorf("budget error used=%d limit=%d, want 1100/1000",
			budgetErr.Used.ConversationTokens, budgetErr.Limit.ConversationTokens)
	}
	if caller.calls != 1 {
		t.Errorf("model was called %d times, want 1: an exhausted budget must not reach the provider", caller.calls)
	}
	// The refused turn must not leave an orphan user message behind.
	page, err := svc.ListMessages(context.Background(), conv.ID, "user_1", MessageListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(page.Items) != 2 {
		t.Errorf("transcript has %d messages, want 2 (the one answered turn)", len(page.Items))
	}
}

func TestSendStopsWhenConversationCostCapExhausted(t *testing.T) {
	caller := &usageCaller{usage: agent.Usage{OutputTokens: 10, CostUSDEstimated: 0.40}}
	budget := agentauth.NewMemoryBudget(agentauth.BudgetConfig{
		ConversationTokens:    1_000_000,
		ConversationCostCents: 50,
		HostDailyCostCents:    1_000_000,
	})
	svc := newBudgetedService(t, caller, budget, nil)
	conv, _ := svc.Start(context.Background(), StartInput{ActorID: "user_1"})

	// 40 cents, then 80 cents cumulative — the second check trips.
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "one"}, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "two"}, nil); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "three"}, nil); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("third Send error = %v, want ErrBudgetExhausted", err)
	}
}

func TestSendStopsWhenHostDailyCapExhaustedAndReportsRetry(t *testing.T) {
	caller := &usageCaller{usage: agent.Usage{CostUSDEstimated: 1.00}}
	budget := agentauth.NewMemoryBudget(agentauth.BudgetConfig{
		ConversationTokens:    1_000_000,
		ConversationCostCents: 1_000_000,
		HostDailyCostCents:    50,
	})
	svc := newBudgetedService(t, caller, budget, nil)

	first, _ := svc.Start(context.Background(), StartInput{ActorID: "user_1"})
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: first.ID, ActorID: "user_1", Content: "one"}, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	// A different conversation on the same host is still blocked: the
	// host-daily cap is what stops a runaway from being worked around by
	// opening a new chat.
	second, _ := svc.Start(context.Background(), StartInput{ActorID: "user_1"})
	_, err := svc.Send(context.Background(), SendInput{ConversationID: second.ID, ActorID: "user_1", Content: "two"}, nil)
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("second conversation error = %v, want ErrBudgetExhausted", err)
	}
	var budgetErr *BudgetError
	if !errors.As(err, &budgetErr) || budgetErr.RetryAt.IsZero() {
		t.Fatalf("host-daily breach must carry a RetryAt, got %v", err)
	}
}

func TestSendRefusesRevokedConversation(t *testing.T) {
	caller := &usageCaller{}
	revoker := agentauth.NewMemorySessionRevoker()
	svc := newBudgetedService(t, caller, nil, revoker)

	conv, _ := svc.Start(context.Background(), StartInput{ActorID: "user_1"})
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "one"}, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if err := revoker.Revoke(context.Background(), conv.ID, "operator_9", "runaway spend"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "two"}, nil)
	if !errors.Is(err, ErrConversationRevoked) {
		t.Fatalf("Send after revoke = %v, want ErrConversationRevoked", err)
	}
	if !strings.Contains(err.Error(), "operator_9") || !strings.Contains(err.Error(), "runaway spend") {
		t.Errorf("revoke error should name who stopped it and why, got %q", err.Error())
	}
	if caller.calls != 1 {
		t.Errorf("model was called %d times, want 1: a revoked conversation must not reach the provider", caller.calls)
	}
}

// TestBudgetDefaultsOnWhenUnconfigured is the regression guard for the
// state this wiring replaced: the budget existed, was constructed at two
// bootstrap sites, and was consulted by nothing.
func TestBudgetDefaultsOnWhenUnconfigured(t *testing.T) {
	t.Setenv(agentauth.EnvConversationTokenCap, "100")
	svc, err := NewService(Config{
		Store:  NewMemoryStore(nil),
		Caller: &usageCaller{usage: agent.Usage{InputTokens: 200}},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	conv, _ := svc.Start(context.Background(), StartInput{ActorID: "user_1"})
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "one"}, nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{ConversationID: conv.ID, ActorID: "user_1", Content: "two"}, nil); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("Send with no explicit budget = %v, want the default cap to apply", err)
	}
}

func TestUSDToCentsRoundsUp(t *testing.T) {
	for _, tc := range []struct {
		usd  float64
		want int64
	}{{0, 0}, {0.0001, 1}, {0.01, 1}, {0.015, 2}, {1.00, 100}} {
		if got := usdToCents(tc.usd); got != tc.want {
			t.Errorf("usdToCents(%v) = %d, want %d", tc.usd, got, tc.want)
		}
	}
}
