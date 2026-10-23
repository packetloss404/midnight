package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanRouteArtifactPrewriteWritesSkipsAndSortsDeterministically(t *testing.T) {
	renderedB, fileB := renderedRouteGroupForTest(t, RouteInput{Name: "b", Host: "b.example.test", ServiceURL: "http://127.0.0.1:8081"})
	renderedA, fileA := renderedRouteGroupForTest(t, RouteInput{Name: "a", Host: "a.example.test", ServiceURL: "http://127.0.0.1:8080"})

	plan := PlanRouteArtifactPrewrite([]RouteArtifactPrewriteInput{
		{
			RouteGroup:   "group-b",
			File:         fileB,
			Rendered:     renderedB,
			ExpectedHash: RenderedConfigHash(renderedB),
		},
		{
			RouteGroup:   "group-a",
			File:         fileA,
			Rendered:     renderedA,
			ExpectedHash: RenderedConfigHash(renderedA),
			ExistingHash: RenderedConfigHash(renderedA),
		},
	})

	if len(plan.Items) != 2 {
		t.Fatalf("item count = %d, want 2", len(plan.Items))
	}
	if plan.Items[0].RouteGroup != "group-a" || plan.Items[1].RouteGroup != "group-b" {
		t.Fatalf("items not sorted deterministically: %#v", plan.Items)
	}
	if plan.Writes != 1 || plan.Skips != 1 || plan.Errors != 0 {
		t.Fatalf("unexpected counts: %#v", plan)
	}
	if plan.Items[0].Action != RouteArtifactPrewriteActionSkip || plan.Items[0].BytesToWrite != 0 {
		t.Fatalf("unexpected skip item: %#v", plan.Items[0])
	}
	if plan.Items[1].Action != RouteArtifactPrewriteActionWrite || plan.Items[1].BytesToWrite != len([]byte(renderedB)) {
		t.Fatalf("unexpected write item: %#v", plan.Items[1])
	}
	if plan.BytesToWrite != len([]byte(renderedB)) {
		t.Fatalf("BytesToWrite = %d, want %d", plan.BytesToWrite, len([]byte(renderedB)))
	}
	if plan.Items[1].Format != "traefik.dynamic.yaml" {
		t.Fatalf("default format = %q, want traefik.dynamic.yaml", plan.Items[1].Format)
	}
}

func TestPlanRouteArtifactPrewriteErrorsWithSupportSafeDiagnostics(t *testing.T) {
	rendered, file := renderedRouteGroupForTest(t, RouteInput{Name: "app", Host: "secret.example.test", ServiceURL: "http://127.0.0.1:8080"})

	plan := PlanRouteArtifactPrewrite([]RouteArtifactPrewriteInput{{
		RouteGroup:   "group-a",
		File:         file,
		Rendered:     rendered,
		ExpectedHash: RenderedConfigHash("older-rendered"),
	}})

	if plan.Writes != 0 || plan.Skips != 0 || plan.Errors != 1 || plan.BytesToWrite != 0 {
		t.Fatalf("unexpected counts: %#v", plan)
	}
	if len(plan.Items) != 1 || plan.Items[0].Action != RouteArtifactPrewriteActionError {
		t.Fatalf("unexpected item: %#v", plan.Items)
	}
	joined := strings.Join(plan.Items[0].Diagnostics, "\n")
	if !strings.Contains(joined, "code=route_artifact_stale_hash") {
		t.Fatalf("missing stale hash diagnostic: %s", joined)
	}
	for _, leaked := range []string{"secret.example.test", "older-rendered", rendered} {
		if strings.Contains(joined, leaked) {
			t.Fatalf("support-safe diagnostics leaked %q: %s", leaked, joined)
		}
	}
}

func TestSummarizeRoutePublicationFromPrewritePlan(t *testing.T) {
	renderedWrite, fileWrite := renderedRouteGroupForTest(t, RouteInput{Name: "write", Host: "write.example.test", ServiceURL: "http://127.0.0.1:8080"})
	renderedSkip, fileSkip := renderedRouteGroupForTest(t, RouteInput{Name: "skip", Host: "skip.example.test", ServiceURL: "http://127.0.0.1:8081"})
	renderedStale, fileStale := renderedRouteGroupForTest(t, RouteInput{Name: "stale", Host: "stale.example.test", ServiceURL: "http://127.0.0.1:8082"})

	plan := PlanRouteArtifactPrewrite([]RouteArtifactPrewriteInput{
		{
			RouteGroup:   "write-group",
			File:         fileWrite,
			Rendered:     renderedWrite,
			ExpectedHash: RenderedConfigHash(renderedWrite),
		},
		{
			RouteGroup:   "skip-group",
			File:         fileSkip,
			Rendered:     renderedSkip,
			ExpectedHash: RenderedConfigHash(renderedSkip),
			ExistingHash: RenderedConfigHash(renderedSkip),
		},
		{
			RouteGroup:   "stale-group",
			File:         fileStale,
			Rendered:     renderedStale,
			ExpectedHash: RenderedConfigHash("older-rendered"),
		},
	})

	summary := SummarizeRoutePublication(plan)
	if summary.Writes != 1 || summary.Skips != 1 || summary.Errors != 1 || summary.TotalBytes != len([]byte(renderedWrite)) || summary.StaleCount != 1 {
		t.Fatalf("unexpected summary counts: %#v", summary)
	}
	wantActions := []string{
		"regenerate stale route artifacts before publishing",
		"review support-safe route artifact diagnostics",
	}
	if strings.Join(summary.NextActions, "\n") != strings.Join(wantActions, "\n") {
		t.Fatalf("next actions = %#v, want %#v", summary.NextActions, wantActions)
	}
}

func TestRenderRoutePublicationSummaryJSONIsDeterministicAndSupportSafe(t *testing.T) {
	summary := RoutePublicationSummary{
		Writes:      2,
		Skips:       1,
		Errors:      0,
		TotalBytes:  123,
		StaleCount:  0,
		NextActions: []string{"publish planned route artifact writes"},
	}

	first, err := RenderRoutePublicationSummaryJSON(summary)
	if err != nil {
		t.Fatalf("RenderRoutePublicationSummaryJSON returned error: %v", err)
	}
	second, err := RenderRoutePublicationSummaryJSON(summary)
	if err != nil {
		t.Fatalf("RenderRoutePublicationSummaryJSON returned error on second render: %v", err)
	}

	want := `{"writes":2,"skips":1,"errors":0,"totalBytes":123,"staleCount":0,"nextActions":["publish planned route artifact writes"]}`
	if first != want || second != want {
		t.Fatalf("rendered JSON not deterministic:\nfirst:  %s\nsecond: %s\nwant:   %s", first, second, want)
	}
	for _, leaked := range []string{"example.test", "http://127.0.0.1", "secret", "token"} {
		if strings.Contains(first, leaked) {
			t.Fatalf("summary JSON leaked %q: %s", leaked, first)
		}
	}
}

func TestSummarizeRoutePublicationNextActionWhenCurrent(t *testing.T) {
	summary := SummarizeRoutePublication(RouteArtifactPrewritePlan{Skips: 2})

	if strings.Join(summary.NextActions, "\n") != "no publication needed; artifacts already current" {
		t.Fatalf("next actions = %#v", summary.NextActions)
	}
}

func TestApplyRouteArtifactPublicationWritesOnlyWriteItems(t *testing.T) {
	renderedWrite, fileWrite := renderedRouteGroupForTest(t, RouteInput{Name: "write", Host: "write.example.test", ServiceURL: "http://127.0.0.1:8080"})
	renderedSkip, fileSkip := renderedRouteGroupForTest(t, RouteInput{Name: "skip", Host: "skip.example.test", ServiceURL: "http://127.0.0.1:8081"})
	plan := PlanRouteArtifactPrewrite([]RouteArtifactPrewriteInput{
		{
			RouteGroup:   "write-group",
			Format:       "traefik.dynamic.yaml",
			File:         fileWrite,
			Rendered:     renderedWrite,
			ExpectedHash: RenderedConfigHash(renderedWrite),
		},
		{
			RouteGroup:   "skip-group",
			Format:       "traefik.dynamic.yaml",
			File:         fileSkip,
			Rendered:     renderedSkip,
			ExpectedHash: RenderedConfigHash(renderedSkip),
			ExistingHash: RenderedConfigHash(renderedSkip),
		},
	})
	writer := &fakeRouteArtifactPublicationWriter{}

	evidence, err := ApplyRouteArtifactPublication(plan, writer)
	if err != nil {
		t.Fatalf("ApplyRouteArtifactPublication returned error: %v", err)
	}

	if evidence.Status != RouteArtifactPublicationApplyStatusReady || evidence.Writes != 1 || evidence.Skips != 1 || evidence.Errors != 0 || evidence.BytesWritten != len([]byte(renderedWrite)) {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	if len(writer.writes) != 1 {
		t.Fatalf("writer calls = %d, want 1", len(writer.writes))
	}
	if writer.writes[0].RouteGroup != "write-group" || string(writer.writes[0].Content) != renderedWrite {
		t.Fatalf("unexpected writer call: %#v", writer.writes[0])
	}
}

func TestApplyRouteArtifactPublicationRefusesErrorItems(t *testing.T) {
	rendered, file := renderedRouteGroupForTest(t, RouteInput{Name: "app", Host: "secret.example.test", ServiceURL: "http://127.0.0.1:8080"})
	plan := PlanRouteArtifactPrewrite([]RouteArtifactPrewriteInput{{
		RouteGroup:   "secret-route-group",
		File:         file,
		Rendered:     rendered,
		ExpectedHash: RenderedConfigHash("older-rendered"),
	}})
	writer := &fakeRouteArtifactPublicationWriter{}

	evidence, err := ApplyRouteArtifactPublication(plan, writer)
	if err == nil {
		t.Fatal("ApplyRouteArtifactPublication returned nil error, want refusal")
	}

	if evidence.Status != RouteArtifactPublicationApplyStatusRefused || evidence.Writes != 0 || evidence.Skips != 0 || evidence.Errors != 1 || evidence.BytesWritten != 0 {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	if len(writer.writes) != 0 {
		t.Fatalf("writer calls = %d, want 0", len(writer.writes))
	}
	joined := strings.Join(evidence.Diagnostics, "\n")
	if !strings.Contains(joined, "code=route_artifact_stale_hash") {
		t.Fatalf("missing stale hash diagnostic: %s", joined)
	}
	for _, leaked := range []string{"secret-route-group", "secret.example.test", "older-rendered", rendered} {
		if strings.Contains(joined, leaked) {
			t.Fatalf("support-safe diagnostics leaked %q: %s", leaked, joined)
		}
	}
}

func TestRouteArtifactPublicationApplyEvidenceJSONRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apply-evidence.json")
	evidence := RouteArtifactPublicationApplyEvidence{
		Status:       RouteArtifactPublicationApplyStatusReady,
		Writes:       1,
		Skips:        2,
		BytesWritten: 123,
	}

	if err := WriteRouteArtifactPublicationApplyEvidenceJSON(path, evidence); err != nil {
		t.Fatalf("WriteRouteArtifactPublicationApplyEvidenceJSON returned error: %v", err)
	}
	got, err := ReadRouteArtifactPublicationApplyEvidenceJSON(path)
	if err != nil {
		t.Fatalf("ReadRouteArtifactPublicationApplyEvidenceJSON returned error: %v", err)
	}

	if got.Status != evidence.Status || got.Writes != evidence.Writes || got.Skips != evidence.Skips || got.Errors != evidence.Errors || got.BytesWritten != evidence.BytesWritten {
		t.Fatalf("round trip evidence = %#v, want %#v", got, evidence)
	}
}

func TestRouteArtifactPublicationApplyEvidenceJSONRequiresJSONPath(t *testing.T) {
	evidence := RouteArtifactPublicationApplyEvidence{Status: RouteArtifactPublicationApplyStatusReady}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "apply-evidence.txt")} {
		if err := WriteRouteArtifactPublicationApplyEvidenceJSON(path, evidence); err == nil {
			t.Fatalf("WriteRouteArtifactPublicationApplyEvidenceJSON(%q) returned nil error, want path validation error", path)
		}
		if _, err := ReadRouteArtifactPublicationApplyEvidenceJSON(path); err == nil {
			t.Fatalf("ReadRouteArtifactPublicationApplyEvidenceJSON(%q) returned nil error, want path validation error", path)
		}
	}
}

func TestReadRouteArtifactPublicationApplyEvidenceJSONIsStrict(t *testing.T) {
	dir := t.TempDir()
	unknownFieldPath := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknownFieldPath, []byte(`{"status":"ready","writes":0,"skips":0,"errors":0,"bytesWritten":0,"unexpected":true}`), 0o600); err != nil {
		t.Fatalf("write unknown-field evidence: %v", err)
	}
	if _, err := ReadRouteArtifactPublicationApplyEvidenceJSON(unknownFieldPath); err == nil {
		t.Fatal("ReadRouteArtifactPublicationApplyEvidenceJSON accepted unknown field")
	}

	trailingPath := filepath.Join(dir, "trailing.json")
	if err := os.WriteFile(trailingPath, []byte(`{"status":"ready","writes":0,"skips":0,"errors":0,"bytesWritten":0}{}`), 0o600); err != nil {
		t.Fatalf("write trailing evidence: %v", err)
	}
	if _, err := ReadRouteArtifactPublicationApplyEvidenceJSON(trailingPath); err == nil {
		t.Fatal("ReadRouteArtifactPublicationApplyEvidenceJSON accepted trailing JSON")
	}
}

func TestRouteArtifactPublicationApplyEvidenceCannotBeReadyWithErrors(t *testing.T) {
	evidence := RouteArtifactPublicationApplyEvidence{
		Status: RouteArtifactPublicationApplyStatusReady,
		Errors: 1,
	}
	path := filepath.Join(t.TempDir(), "invalid-ready.json")

	if err := WriteRouteArtifactPublicationApplyEvidenceJSON(path, evidence); err == nil {
		t.Fatal("WriteRouteArtifactPublicationApplyEvidenceJSON accepted ready evidence with errors")
	}
	if err := os.WriteFile(path, []byte(`{"status":"ready","writes":0,"skips":0,"errors":1,"bytesWritten":0}`), 0o600); err != nil {
		t.Fatalf("write invalid ready evidence: %v", err)
	}
	if _, err := ReadRouteArtifactPublicationApplyEvidenceJSON(path); err == nil {
		t.Fatal("ReadRouteArtifactPublicationApplyEvidenceJSON accepted ready evidence with errors")
	}
}

func TestRouteArtifactPublicationApplyEvidenceCannotBeReadyWithDiagnostics(t *testing.T) {
	evidence := RouteArtifactPublicationApplyEvidence{
		Status:      RouteArtifactPublicationApplyStatusReady,
		Diagnostics: []string{"code=route_publication_write_failed"},
	}
	path := filepath.Join(t.TempDir(), "invalid-ready-diagnostics.json")

	if err := WriteRouteArtifactPublicationApplyEvidenceJSON(path, evidence); err == nil {
		t.Fatal("WriteRouteArtifactPublicationApplyEvidenceJSON accepted ready evidence with diagnostics")
	}
	if err := os.WriteFile(path, []byte(`{"status":"ready","writes":0,"skips":0,"errors":0,"bytesWritten":0,"diagnostics":["code=route_publication_write_failed"]}`), 0o600); err != nil {
		t.Fatalf("write invalid ready diagnostics evidence: %v", err)
	}
	if _, err := ReadRouteArtifactPublicationApplyEvidenceJSON(path); err == nil {
		t.Fatal("ReadRouteArtifactPublicationApplyEvidenceJSON accepted ready evidence with diagnostics")
	}
}

type fakeRouteArtifactPublicationWriter struct {
	writes []RouteArtifactPublicationWrite
}

func (writer *fakeRouteArtifactPublicationWriter) WriteRouteArtifact(write RouteArtifactPublicationWrite) (int, error) {
	write.Content = append([]byte(nil), write.Content...)
	writer.writes = append(writer.writes, write)
	return len(write.Content), nil
}

func renderedRouteGroupForTest(t *testing.T, route RouteInput) (string, TraefikFile) {
	t.Helper()
	rendered, err := RenderTraefikDynamicYAML([]RouteInput{route})
	if err != nil {
		t.Fatalf("RenderTraefikDynamicYAML returned error: %v", err)
	}
	file, err := RenderTraefikFile([]RouteInput{route})
	if err != nil {
		t.Fatalf("RenderTraefikFile returned error: %v", err)
	}
	return rendered, file
}
