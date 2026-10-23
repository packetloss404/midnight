package logs

import "testing"

func TestInferLevelUsesNDJSONAttributesFirst(t *testing.T) {
	attrs := map[string]any{"severity_text": "WARNING"}
	if got := InferLevel(attrs, "request completed"); got != LevelWarn {
		t.Fatalf("InferLevel = %q, want %q", got, LevelWarn)
	}
}

func TestInferLevelFallsBackToMessage(t *testing.T) {
	if got := InferLevel(nil, "route reconciliation failed"); got != LevelError {
		t.Fatalf("InferLevel error message = %q, want %q", got, LevelError)
	}
	if got := InferLevel(map[string]any{"level": "unknown"}, "cache degraded"); got != LevelWarn {
		t.Fatalf("InferLevel warning message = %q, want %q", got, LevelWarn)
	}
	if got := InferLevel(nil, "route active"); got != LevelInfo {
		t.Fatalf("InferLevel default = %q, want %q", got, LevelInfo)
	}
}
