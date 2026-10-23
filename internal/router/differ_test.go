package router

import "testing"

func TestPlanRouteDiffCreatesUpdatesSwapsRemoves(t *testing.T) {
	plan := PlanRouteDiff(RouteDifferInput{
		RouteGroup: "group-a",
		Desired: []RouteInput{
			{Name: "alpha", Host: "alpha.example.test", ServiceURL: "http://10.0.0.1:8080"},
			{Name: "bravo", Host: "bravo-new.example.test", ServiceURL: "http://10.0.0.2:8080"},
			{Name: "charlie", Host: "charlie.example.test", ServiceURL: "http://10.0.0.3:9090"},
			{Name: "delta", Host: "delta.example.test", ServiceURL: "http://10.0.0.4:8080"},
		},
		Observed: []ObservedRouteState{
			// alpha is identical -> noop
			{Name: "alpha", Host: "alpha.example.test", ServiceURL: "http://10.0.0.1:8080"},
			// bravo's host changed -> swap
			{Name: "bravo", Host: "bravo-old.example.test", ServiceURL: "http://10.0.0.2:8080"},
			// charlie's service URL changed -> update
			{Name: "charlie", Host: "charlie.example.test", ServiceURL: "http://10.0.0.3:8080"},
			// echo no longer desired -> remove
			{Name: "echo", Host: "echo.example.test", ServiceURL: "http://10.0.0.5:8080"},
		},
	})

	if plan.Creates != 1 || plan.Updates != 1 || plan.Swaps != 1 || plan.Removes != 1 || plan.Noops != 1 {
		t.Fatalf("unexpected counts: %#v", plan)
	}

	gotByName := make(map[string]RouteDiffEntry, len(plan.Entries))
	for _, entry := range plan.Entries {
		if entry.IdempotencyKey == "" {
			t.Fatalf("entry %q is missing idempotency key", entry.RouteName)
		}
		gotByName[entry.RouteName] = entry
	}
	if got := gotByName["alpha"].Step; got != RouteDiffStepNoop {
		t.Fatalf("alpha step = %q, want %q", got, RouteDiffStepNoop)
	}
	if got := gotByName["bravo"].Step; got != RouteDiffStepSwap {
		t.Fatalf("bravo step = %q, want %q", got, RouteDiffStepSwap)
	}
	if got := gotByName["charlie"].Step; got != RouteDiffStepUpdate {
		t.Fatalf("charlie step = %q, want %q", got, RouteDiffStepUpdate)
	}
	if got := gotByName["delta"].Step; got != RouteDiffStepCreate {
		t.Fatalf("delta step = %q, want %q", got, RouteDiffStepCreate)
	}
	if got := gotByName["echo"].Step; got != RouteDiffStepRemove {
		t.Fatalf("echo step = %q, want %q", got, RouteDiffStepRemove)
	}

	if reasons := gotByName["bravo"].Reasons; len(reasons) == 0 || reasons[0] != "host-changed" {
		t.Fatalf("bravo reasons = %v, want host-changed first", reasons)
	}
	if reasons := gotByName["charlie"].Reasons; len(reasons) != 1 || reasons[0] != "service-url-changed" {
		t.Fatalf("charlie reasons = %v, want service-url-changed", reasons)
	}
	if reasons := gotByName["alpha"].Reasons; len(reasons) != 0 {
		t.Fatalf("alpha noop should have no reasons, got %v", reasons)
	}
}

func TestPlanRouteDiffIsDeterministic(t *testing.T) {
	input := RouteDifferInput{
		RouteGroup: "group-z",
		Desired: []RouteInput{
			{Name: "z", Host: "z.example.test", ServiceURL: "http://10.0.0.1:8080"},
			{Name: "a", Host: "a.example.test", ServiceURL: "http://10.0.0.2:8080"},
		},
		Observed: []ObservedRouteState{
			{Name: "a", Host: "a.example.test", ServiceURL: "http://10.0.0.2:8080"},
		},
	}
	first := PlanRouteDiff(input)
	second := PlanRouteDiff(input)

	if len(first.Entries) != len(second.Entries) {
		t.Fatalf("entry length mismatch: %d vs %d", len(first.Entries), len(second.Entries))
	}
	for i := range first.Entries {
		if first.Entries[i].IdempotencyKey != second.Entries[i].IdempotencyKey {
			t.Fatalf("idempotency key drift at %d: %q vs %q", i, first.Entries[i].IdempotencyKey, second.Entries[i].IdempotencyKey)
		}
		if first.Entries[i].RouteName != second.Entries[i].RouteName || first.Entries[i].Step != second.Entries[i].Step {
			t.Fatalf("entry drift at %d: %+v vs %+v", i, first.Entries[i], second.Entries[i])
		}
	}
	// Entries must be sorted by route name.
	for i := 1; i < len(first.Entries); i++ {
		if first.Entries[i-1].RouteName > first.Entries[i].RouteName {
			t.Fatalf("entries are not sorted by name: %v", first.Entries)
		}
	}
}

func TestPlanRouteDiffIdempotencyKeyChangesWhenContentChanges(t *testing.T) {
	base := RouteDifferInput{
		RouteGroup: "group-a",
		Desired: []RouteInput{
			{Name: "app", Host: "app.example.test", ServiceURL: "http://10.0.0.1:8080"},
		},
	}
	changed := base
	changed.Desired = []RouteInput{
		{Name: "app", Host: "app.example.test", ServiceURL: "http://10.0.0.2:8080"},
	}

	first := PlanRouteDiff(base).Entries[0]
	second := PlanRouteDiff(changed).Entries[0]
	if first.IdempotencyKey == second.IdempotencyKey {
		t.Fatal("idempotency key should differ when service URL changes")
	}
}

func TestPlanRouteDiffMiddlewaresAreOrderInvariant(t *testing.T) {
	base := PlanRouteDiff(RouteDifferInput{
		Desired: []RouteInput{{
			Name:        "app",
			Host:        "app.example.test",
			ServiceURL:  "http://10.0.0.1:8080",
			Middlewares: []string{"a", "b"},
		}},
		Observed: []ObservedRouteState{{
			Name:        "app",
			Host:        "app.example.test",
			ServiceURL:  "http://10.0.0.1:8080",
			Middlewares: []string{"b", "a"},
		}},
	})
	if len(base.Entries) != 1 || base.Entries[0].Step != RouteDiffStepNoop {
		t.Fatalf("expected noop for middleware order swap, got %#v", base)
	}
}
