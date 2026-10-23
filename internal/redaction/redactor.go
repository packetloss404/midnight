// Package redaction provides a pluggable secret redactor over structured log
// entries and free-form text. The redactor preserves schema (no fields are
// dropped) and replaces matched substrings with a stable placeholder so
// downstream consumers can still tell that something was redacted.
package redaction

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/midnight/midnight/internal/failurecodes"
)

// Placeholder is the constant string written in place of every matched secret.
// The pattern name is appended in parentheses so analysts know what was hidden.
const Placeholder = "[REDACTED]"

// Rule is one named pattern + replacement pair. Pattern is a Go regexp; when
// Pattern matches anywhere in a string, the matched substring is replaced
// with Replace. When Replace is empty, Placeholder is used.
type Rule struct {
	// Name uniquely identifies the rule; it is appended to the placeholder.
	Name string
	// Pattern is the compiled regexp.
	Pattern *regexp.Regexp
	// Replace is the constant replacement; defaults to Placeholder when empty.
	Replace string
}

// Redactor applies a registered rule set to free-form strings and to
// log-style maps. It is safe for concurrent use after Build returns.
type Redactor struct {
	rules []Rule
}

// Builder accumulates rules before they are compiled into a Redactor. Use
// NewBuilder to construct one.
type Builder struct {
	mu    sync.Mutex
	rules []Rule
	names map[string]struct{}
}

// NewBuilder returns an empty rule builder.
func NewBuilder() *Builder {
	return &Builder{names: make(map[string]struct{})}
}

// Add registers a rule by raw pattern. It returns a *failurecodes.FailureError
// with reason REDACTION_RULE_INVALID when name is empty, the regexp fails to
// compile, or the name is already registered.
func (b *Builder) Add(name, pattern, replace string) error {
	if b == nil {
		return errors.New("redaction builder is nil")
	}
	if strings.TrimSpace(name) == "" {
		return failurecodes.New(failurecodes.RedactionRuleInvalid, "rule name is required")
	}
	if strings.TrimSpace(pattern) == "" {
		return failurecodes.New(failurecodes.RedactionRuleInvalid, fmt.Sprintf("rule %q pattern is required", name))
	}
	rx, err := regexp.Compile(pattern)
	if err != nil {
		return failurecodes.Wrap(failurecodes.RedactionRuleInvalid, fmt.Sprintf("rule %q pattern invalid", name), err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.names[name]; exists {
		return failurecodes.New(failurecodes.RedactionRuleInvalid, fmt.Sprintf("rule %q is already registered", name))
	}
	b.names[name] = struct{}{}
	b.rules = append(b.rules, Rule{Name: name, Pattern: rx, Replace: replace})
	return nil
}

// addCompiled appends a rule whose pattern has already been validated. It is
// the no-error path used by static initializers like DefaultRedactor; the
// regexp is compiled with regexp.MustCompile at the call site so any
// mis-typed pattern fails inside the standard library, not here.
func (b *Builder) addCompiled(name string, rx *regexp.Regexp, replace string) {
	if b == nil || rx == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.names[name]; exists {
		return
	}
	b.names[name] = struct{}{}
	b.rules = append(b.rules, Rule{Name: name, Pattern: rx, Replace: replace})
}

// Build compiles the accumulated rules into a Redactor. The returned Redactor
// owns the rules; the builder is reset.
func (b *Builder) Build() *Redactor {
	if b == nil {
		return &Redactor{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	rules := make([]Rule, len(b.rules))
	copy(rules, b.rules)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	out := &Redactor{rules: rules}
	b.rules = nil
	b.names = make(map[string]struct{})
	return out
}

// DefaultRedactor returns a Redactor preloaded with the Phase 1 rule set:
// Midnight tokens (`midn_*`), email addresses, IPv4, IPv6, and AWS keys.
func DefaultRedactor() *Redactor {
	builder := NewBuilder()
	builder.addCompiled("midn-token", regexp.MustCompile(`midn_[A-Za-z0-9]{8,}`), "")
	builder.addCompiled("email", regexp.MustCompile(`(?i)[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,24}`), "")
	builder.addCompiled("ipv4", regexp.MustCompile(`\b((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\b`), "")
	builder.addCompiled("ipv6", regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}\b`), "")
	builder.addCompiled("aws-access-key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "")
	builder.addCompiled("aws-secret-key", regexp.MustCompile(`\b[A-Za-z0-9/+=]{40}\b`), "")
	return builder.Build()
}

// Rules returns the registered rule names in sorted order.
func (r *Redactor) Rules() []string {
	if r == nil {
		return nil
	}
	out := make([]string, len(r.rules))
	for i, rule := range r.rules {
		out[i] = rule.Name
	}
	return out
}

// RedactString replaces every match of every registered rule in input with a
// labeled placeholder. Rules apply in registration order; the first match
// wins for overlapping ranges.
func (r *Redactor) RedactString(input string) string {
	if r == nil || len(r.rules) == 0 {
		return input
	}
	out := input
	for _, rule := range r.rules {
		out = rule.Pattern.ReplaceAllStringFunc(out, func(_ string) string {
			if rule.Replace != "" {
				return rule.Replace
			}
			return fmt.Sprintf("%s(%s)", Placeholder, rule.Name)
		})
	}
	return out
}

// RedactMap walks fields recursively and redacts every string value while
// preserving the map structure (no keys are dropped). Non-string scalars are
// passed through untouched.
func (r *Redactor) RedactMap(fields map[string]any) map[string]any {
	if r == nil || fields == nil {
		return fields
	}
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		out[key] = r.redactValue(value)
	}
	return out
}

func (r *Redactor) redactValue(value any) any {
	switch typed := value.(type) {
	case string:
		return r.RedactString(typed)
	case map[string]any:
		return r.RedactMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = r.redactValue(item)
		}
		return out
	default:
		return value
	}
}
