package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildAgentEvidenceIndexRendersDeterministicJSON(t *testing.T) {
	index := BuildAgentEvidenceIndex(
		AgentEvidenceRef{Path: "evidence/release.json", Status: AgentReleaseStatusNeedsReview},
		AgentEvidenceRef{Path: "evidence/loop.json", Status: LoopExecutionStatusPartial},
		AgentEvidenceRef{Path: "evidence/decision.json", Status: AgentDecisionStatusPending},
		[]AgentEvidenceRef{
			{Path: "evidence/runtime/routes.json", Status: "observed"},
			{Path: "evidence/runtime/containers.json", Status: "observed"},
		},
	)

	encoded, err := MarshalAgentEvidenceIndex(index)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("index json is invalid: %s", encoded)
	}
	want := `{"evidence_refs":[{"kind":"agent_release_evidence","path":"evidence/release.json","status":"needs-review"},{"kind":"loop_execution_artifact","path":"evidence/loop.json","status":"partial"},{"kind":"decision_artifact","path":"evidence/decision.json","status":"pending"},{"kind":"runtime_observation_evidence","path":"evidence/runtime/containers.json","status":"observed"},{"kind":"runtime_observation_evidence","path":"evidence/runtime/routes.json","status":"observed"}],"status_rollup":[{"status":"needs-review","count":1},{"status":"observed","count":2},{"status":"partial","count":1},{"status":"pending","count":1}]}`
	if string(encoded) != want {
		t.Fatalf("json = %s, want %s", encoded, want)
	}
}

func TestValidateAgentEvidenceIndexRejectsUnsafeRelativePaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "absolute", path: "C:/tmp/release.json", want: "relative"},
		{name: "parent traversal", path: "evidence/../release.json", want: "clean"},
		{name: "unclean", path: "evidence//release.json", want: "clean"},
		{name: "empty", path: " ", want: "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := BuildAgentEvidenceIndex(
				AgentEvidenceRef{Path: tt.path, Status: AgentReleaseStatusReady},
				AgentEvidenceRef{Path: "evidence/loop.json", Status: LoopExecutionStatusCompleted},
				AgentEvidenceRef{Path: "evidence/decision.json", Status: AgentDecisionStatusIdle},
				nil,
			)
			if err := ValidateAgentEvidenceIndex(index); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateAgentEvidenceIndexRejectsInvalidStatusRollup(t *testing.T) {
	index := BuildAgentEvidenceIndex(
		AgentEvidenceRef{Path: "evidence/release.json", Status: AgentReleaseStatusReady},
		AgentEvidenceRef{Path: "evidence/loop.json", Status: LoopExecutionStatusCompleted},
		AgentEvidenceRef{Path: "evidence/decision.json", Status: AgentDecisionStatusIdle},
		nil,
	)
	index.StatusRollup = []AgentEvidenceStatusRollup{{Status: AgentReleaseStatusReady, Count: 99}}

	if err := ValidateAgentEvidenceIndex(index); err == nil || !strings.Contains(err.Error(), "rollup") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestAgentEvidenceIndexJSONRoundTripUsesStrictDecode(t *testing.T) {
	index := BuildAgentEvidenceIndex(
		AgentEvidenceRef{Path: "evidence/release.json", Status: AgentReleaseStatusReady},
		AgentEvidenceRef{Path: "evidence/loop.json", Status: LoopExecutionStatusCompleted},
		AgentEvidenceRef{Path: "evidence/decision.json", Status: AgentDecisionStatusIdle},
		[]AgentEvidenceRef{{Path: "evidence/runtime/routes.json", Status: "observed"}},
	)
	path := filepath.Join(t.TempDir(), "agent-evidence-index.json")

	if err := WriteAgentEvidenceIndexJSON(path, index); err != nil {
		t.Fatalf("write index json: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written json: %v", err)
	}
	want, err := MarshalAgentEvidenceIndex(index)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	if string(written) != string(want) {
		t.Fatalf("written json = %s, want %s", written, want)
	}

	decoded, err := ReadAgentEvidenceIndexJSON(path)
	if err != nil {
		t.Fatalf("read index json: %v", err)
	}
	if !reflect.DeepEqual(decoded, index) {
		t.Fatalf("decoded index = %+v, want %+v", decoded, index)
	}
}

func TestReadAgentEvidenceIndexJSONRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "unknown field",
			content: `{"evidence_refs":[{"kind":"agent_release_evidence","path":"evidence/release.json","status":"ready"},{"kind":"loop_execution_artifact","path":"evidence/loop.json","status":"completed"},{"kind":"decision_artifact","path":"evidence/decision.json","status":"idle"}],"status_rollup":[{"status":"completed","count":1},{"status":"idle","count":1},{"status":"ready","count":1}],"extra":true}`,
			want:    "unknown field",
		},
		{
			name:    "trailing value",
			content: `{"evidence_refs":[{"kind":"agent_release_evidence","path":"evidence/release.json","status":"ready"},{"kind":"loop_execution_artifact","path":"evidence/loop.json","status":"completed"},{"kind":"decision_artifact","path":"evidence/decision.json","status":"idle"}],"status_rollup":[{"status":"completed","count":1},{"status":"idle","count":1},{"status":"ready","count":1}]} {}`,
			want:    "exactly one value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent-evidence-index.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write test json: %v", err)
			}
			if _, err := ReadAgentEvidenceIndexJSON(path); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("read validation error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestAgentEvidenceIndexJSONRejectsInvalidPaths(t *testing.T) {
	index := BuildAgentEvidenceIndex(
		AgentEvidenceRef{Path: "evidence/release.json", Status: AgentReleaseStatusReady},
		AgentEvidenceRef{Path: "evidence/loop.json", Status: LoopExecutionStatusCompleted},
		AgentEvidenceRef{Path: "evidence/decision.json", Status: AgentDecisionStatusIdle},
		nil,
	)
	tempDir := t.TempDir()

	if err := WriteAgentEvidenceIndexJSON(filepath.Join(tempDir, "agent-evidence-index.txt"), index); err == nil || !strings.Contains(err.Error(), ".json") {
		t.Fatalf("write extension validation error = %v", err)
	}
	if _, err := ReadAgentEvidenceIndexJSON(filepath.Join(tempDir, "agent-evidence-index.txt")); err == nil || !strings.Contains(err.Error(), ".json") {
		t.Fatalf("read extension validation error = %v", err)
	}
	if err := WriteAgentEvidenceIndexJSON(filepath.Join(tempDir, "missing", "agent-evidence-index.json"), index); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("write parent validation error = %v", err)
	}
}
