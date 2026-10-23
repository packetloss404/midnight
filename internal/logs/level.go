package logs

import "strings"

const (
	LevelTrace = "trace"
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// InferLevel returns a normalized log level from common NDJSON attributes, then
// falls back to conservative message keyword matching.
func InferLevel(attrs map[string]any, message string) string {
	for _, key := range []string{"level", "severity", "severity_text", "log.level"} {
		if level, ok := normalizeLevel(attrs[key]); ok {
			return level
		}
	}

	message = strings.ToLower(message)
	switch {
	case strings.Contains(message, "panic") || strings.Contains(message, "fatal") || strings.Contains(message, "error") || strings.Contains(message, "failed"):
		return LevelError
	case strings.Contains(message, "warn") || strings.Contains(message, "degraded"):
		return LevelWarn
	case strings.Contains(message, "debug"):
		return LevelDebug
	case strings.Contains(message, "trace"):
		return LevelTrace
	default:
		return LevelInfo
	}
}

func normalizeLevel(value any) (string, bool) {
	level, ok := value.(string)
	if !ok {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace":
		return LevelTrace, true
	case "debug":
		return LevelDebug, true
	case "info", "information", "notice":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error", "err", "fatal", "panic":
		return LevelError, true
	default:
		return "", false
	}
}
