package logs

import (
	"errors"
	"strings"
	"time"
)

// Cursor identifies a position in a log stream without prescribing storage or IO.
type Cursor struct {
	Stream string    `json:"stream,omitempty"`
	At     time.Time `json:"at,omitempty"`
	Token  string    `json:"token,omitempty"`
	// File names the source file the cursor was emitted from, when applicable.
	File string `json:"file,omitempty"`
	// Offset is the zero-based line offset inside File at which to resume.
	Offset int `json:"offset,omitempty"`
}

// Filter is the Phase 0 log query shape. It intentionally performs no IO.
type Filter struct {
	Since  time.Time `json:"since,omitempty"`
	Until  time.Time `json:"until,omitempty"`
	Tail   int       `json:"tail,omitempty"`
	Follow bool      `json:"follow,omitempty"`
	// Levels narrows results to the named levels (info, warn, error, ...).
	// An empty slice matches any level.
	Levels []string `json:"levels,omitempty"`
	// Components narrows results to the named source components/services.
	// Empty matches any component.
	Components []string `json:"components,omitempty"`
	// Deployments narrows results to the named deployment IDs by inspecting
	// Entry.Fields["deployment_id"].
	Deployments []string `json:"deployments,omitempty"`
	// Operations narrows results to the named operation IDs by inspecting
	// Entry.Fields["operation_id"].
	Operations []string `json:"operations,omitempty"`
	// FreeText, when non-empty, requires the substring (case-insensitive) to
	// appear in the entry's Message or any string-valued Fields entry.
	FreeText string `json:"freeText,omitempty"`
}

// QueryResult is one page of log query output. NextCursor is set when the
// caller should request another page.
type QueryResult struct {
	Entries    []Entry `json:"entries"`
	NextCursor Cursor  `json:"next_cursor,omitempty"`
	Truncated  bool    `json:"truncated,omitempty"`
}

// Validate enforces the Phase 1 log query invariants.
func (filter Filter) Validate() error {
	if filter.Tail < 0 {
		return errors.New("tail must be greater than or equal to zero")
	}
	if !filter.Until.IsZero() && !filter.Since.IsZero() && filter.Until.Before(filter.Since) {
		return errors.New("until must not precede since")
	}
	for _, level := range filter.Levels {
		if strings.TrimSpace(level) == "" {
			return errors.New("level filter must not be empty")
		}
	}
	for _, component := range filter.Components {
		if strings.TrimSpace(component) == "" {
			return errors.New("component filter must not be empty")
		}
	}
	for _, deployment := range filter.Deployments {
		if strings.TrimSpace(deployment) == "" {
			return errors.New("deployment filter must not be empty")
		}
	}
	for _, operation := range filter.Operations {
		if strings.TrimSpace(operation) == "" {
			return errors.New("operation filter must not be empty")
		}
	}
	return nil
}

// Match reports whether the entry satisfies the filter (other than tail/since,
// which are handled by the store implementations).
func (filter Filter) Match(entry Entry) bool {
	if !filter.Since.IsZero() && entry.At.Before(filter.Since) {
		return false
	}
	if !filter.Until.IsZero() && entry.At.After(filter.Until) {
		return false
	}
	if len(filter.Levels) > 0 && !containsFold(filter.Levels, entry.Level) {
		return false
	}
	if len(filter.Components) > 0 && !containsFold(filter.Components, entry.Component) {
		return false
	}
	if len(filter.Deployments) > 0 {
		raw, ok := entry.Fields["deployment_id"].(string)
		if !ok || !containsFold(filter.Deployments, raw) {
			return false
		}
	}
	if len(filter.Operations) > 0 {
		raw, ok := entry.Fields["operation_id"].(string)
		if !ok || !containsFold(filter.Operations, raw) {
			return false
		}
	}
	if filter.FreeText != "" {
		needle := strings.ToLower(filter.FreeText)
		if !strings.Contains(strings.ToLower(entry.Message), needle) {
			matched := false
			for _, value := range entry.Fields {
				if str, ok := value.(string); ok && strings.Contains(strings.ToLower(str), needle) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

func containsFold(haystack []string, needle string) bool {
	for _, item := range haystack {
		if strings.EqualFold(item, needle) {
			return true
		}
	}
	return false
}
