package previews

import (
	"strings"
	"testing"
)

func TestCurrentLifecycleCapabilityIsExplicitlyClosed(t *testing.T) {
	capability := CurrentLifecycleCapability()
	if capability.State != "setup_required" || capability.AutomaticLifecycle ||
		capability.ReceivedEvent != "pull_request" ||
		capability.CurrentDisposition != "accepted_but_not_routed" {
		t.Fatalf("capability = %#v", capability)
	}
	if got, want := strings.Join(capability.RequestedActions, ","),
		"opened:create,reopened:update,synchronize:update,closed:delete"; got != want {
		t.Fatalf("requested actions = %q, want %q", got, want)
	}
	want := map[string]bool{
		"pull_request_router_missing":     false,
		"durable_preview_mapping_missing": false,
		"preview_isolation_missing":       false,
		"preview_cleanup_missing":         false,
	}
	if len(capability.Blockers) != len(want) {
		t.Fatalf("blockers = %#v, want exactly %d closed blockers", capability.Blockers, len(want))
	}
	for _, blocker := range capability.Blockers {
		if _, ok := want[blocker.Code]; !ok || want[blocker.Code] || strings.TrimSpace(blocker.Detail) == "" {
			t.Fatalf("unexpected, duplicate, or empty blocker %#v", blocker)
		}
		want[blocker.Code] = true
	}
	for code, present := range want {
		if !present {
			t.Fatalf("missing blocker %q in %#v", code, capability.Blockers)
		}
	}
}
