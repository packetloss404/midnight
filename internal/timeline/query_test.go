package timeline

import (
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

func seedTimeline(t *testing.T) *MemoryStore {
	t.Helper()
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	mustAppend := func(e Event) {
		t.Helper()
		if err := store.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	mustAppend(Event{DeploymentID: "dep-a", EventSeq: 1, Type: EventIngressRouteRendered, At: base, Message: "render dep-a"})
	mustAppend(Event{DeploymentID: "dep-a", EventSeq: 2, Type: EventIngressRouteArtifactWritten, At: base.Add(time.Second), Message: "artifact dep-a"})
	mustAppend(Event{DeploymentID: "dep-a", EventSeq: 3, Type: EventIngressRouteActive, At: base.Add(2 * time.Second), Message: "active dep-a"})
	mustAppend(Event{DeploymentID: "dep-b", EventSeq: 1, Type: EventIngressRouteRendered, At: base.Add(3 * time.Second), Message: "render dep-b"})
	mustAppend(Event{DeploymentID: "dep-b", EventSeq: 2, Type: EventACMEDNSPending, At: base.Add(4 * time.Second), Message: "dns dep-b"})
	mustAppend(Event{DeploymentID: "dep-c", EventSeq: 1, Type: EventACMECertificateReady, At: base.Add(5 * time.Second), Message: "cert dep-c"})
	return store
}

func TestQueryPaginatesAcrossEventTypesAndDeployments(t *testing.T) {
	store := seedTimeline(t)

	first, err := store.Query(Filter{}, QueryCursor{}, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Events) != 2 || !first.Truncated {
		t.Fatalf("first page = %#v", first)
	}
	if first.Events[0].DeploymentID != "dep-a" || first.Events[0].EventSeq != 1 {
		t.Fatalf("first event = %#v", first.Events[0])
	}
	if first.NextCursor.DeploymentID != "dep-a" || first.NextCursor.AfterSeq != 2 {
		t.Fatalf("first cursor = %#v", first.NextCursor)
	}

	second, err := store.Query(Filter{}, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Events) != 2 || !second.Truncated {
		t.Fatalf("second page = %#v", second)
	}
	if second.Events[0].DeploymentID != "dep-a" || second.Events[0].EventSeq != 3 {
		t.Fatalf("second.0 = %#v", second.Events[0])
	}
	if second.Events[1].DeploymentID != "dep-b" || second.Events[1].EventSeq != 1 {
		t.Fatalf("second.1 = %#v", second.Events[1])
	}

	third, err := store.Query(Filter{}, second.NextCursor, 10)
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if third.Truncated {
		t.Fatal("third page should not be truncated")
	}
	if len(third.Events) != 2 {
		t.Fatalf("third page len = %d, want 2", len(third.Events))
	}
	if third.Events[1].DeploymentID != "dep-c" || third.Events[1].EventSeq != 1 {
		t.Fatalf("third final event = %#v", third.Events[1])
	}
}

func TestQueryFilterByDeploymentIDsUnion(t *testing.T) {
	store := seedTimeline(t)
	page, err := store.Query(Filter{DeploymentIDs: []string{"dep-a", "dep-c"}}, QueryCursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 4 {
		t.Fatalf("len = %d, want 4 (3 from dep-a + 1 from dep-c)", len(page.Events))
	}
	for _, ev := range page.Events {
		if ev.DeploymentID != "dep-a" && ev.DeploymentID != "dep-c" {
			t.Fatalf("unexpected deployment in result: %q", ev.DeploymentID)
		}
	}
}

func TestQueryFilterByKindsAlias(t *testing.T) {
	store := seedTimeline(t)
	page, err := store.Query(Filter{Kinds: []EventType{EventIngressRouteRendered}}, QueryCursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("len = %d, want 2 rendered events", len(page.Events))
	}
	for _, ev := range page.Events {
		if ev.Type != EventIngressRouteRendered {
			t.Fatalf("unexpected type %q", ev.Type)
		}
	}
}

func TestQueryFilterTypesAndKindsUnion(t *testing.T) {
	store := seedTimeline(t)
	page, err := store.Query(Filter{
		Types: []EventType{EventIngressRouteActive},
		Kinds: []EventType{EventACMECertificateReady},
	}, QueryCursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("len = %d, want 2", len(page.Events))
	}
}

func TestQueryFilterSinceUntil(t *testing.T) {
	store := seedTimeline(t)
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	page, err := store.Query(Filter{
		Since: base.Add(2 * time.Second),
		Until: base.Add(4 * time.Second),
	}, QueryCursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("len = %d, want 3", len(page.Events))
	}
	if page.Events[0].EventSeq != 3 || page.Events[2].EventSeq != 2 {
		t.Fatalf("range filter results = %#v", page.Events)
	}
}

func TestQueryFilterFreeText(t *testing.T) {
	store := seedTimeline(t)
	page, err := store.Query(Filter{FreeText: "DEP-B"}, QueryCursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("len = %d, want 2 dep-b events", len(page.Events))
	}
}

func TestQueryFilterRejectsEmptyDeploymentIDsEntry(t *testing.T) {
	store := seedTimeline(t)
	_, err := store.Query(Filter{DeploymentIDs: []string{"dep-a", ""}}, QueryCursor{}, 10)
	if err == nil {
		t.Fatal("expected validation error for empty deployment id entry")
	}
	code, _, ok := failurecodes.ReasonOf(err)
	if !ok || code != failurecodes.TimelineQueryInvalid {
		t.Fatalf("reason = (%q, %v); want TIMELINE_QUERY_INVALID", code, ok)
	}
}

func TestQueryFilterRejectsEmptyKindEntry(t *testing.T) {
	store := seedTimeline(t)
	_, err := store.Query(Filter{Kinds: []EventType{""}}, QueryCursor{}, 10)
	if err == nil {
		t.Fatal("expected validation error for empty kind entry")
	}
}

func TestQueryFilterDeploymentIDLegacyFieldStillWorks(t *testing.T) {
	store := seedTimeline(t)
	page, err := store.Query(Filter{DeploymentID: "dep-b"}, QueryCursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("len = %d, want 2 dep-b events", len(page.Events))
	}
}

func TestQueryFilterEffectiveDeploymentsCombinesLegacyAndPlural(t *testing.T) {
	filter := Filter{DeploymentID: "dep-a", DeploymentIDs: []string{"dep-b", "dep-a"}}
	got := filter.effectiveDeployments()
	want := []string{"dep-a", "dep-b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
