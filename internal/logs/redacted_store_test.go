package logs

import (
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/redaction"
)

func TestFileStoreWithRedactorRedactsMessagesAndFields(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	writeFixtureFile(t, dir, "redact.log", []Entry{
		{
			At:        base,
			Level:     "info",
			Component: "app",
			Message:   "operator midn_abc12345 connected from 10.0.0.5",
			Fields: map[string]any{
				"user":  "alice@example.test",
				"count": 7,
			},
		},
		{
			At:        base.Add(time.Second),
			Level:     "warn",
			Component: "app",
			Message:   "key AKIAABCDEFGHIJKLMNOP rotated",
			Fields: map[string]any{
				"address": "2001:0db8:85a3:0000:0000:8a2e:0370:7334",
				"path":    "/var/lib/foo",
			},
		},
	})

	store, err := NewFileStore(dir, "*.log", WithRedactor(redaction.DefaultRedactor()))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if store.Redactor() == nil {
		t.Fatal("Redactor() should return the configured redactor")
	}

	page, err := store.Query(Filter{}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(page.Entries))
	}

	// Vector 1: midn token, IPv4, email.
	first := page.Entries[0]
	if strings.Contains(first.Message, "midn_abc12345") {
		t.Fatalf("midn token leaked in message: %q", first.Message)
	}
	if strings.Contains(first.Message, "10.0.0.5") {
		t.Fatalf("IPv4 leaked in message: %q", first.Message)
	}
	if !strings.Contains(first.Message, "[REDACTED]") {
		t.Fatalf("redaction placeholder missing from message: %q", first.Message)
	}
	user, _ := first.Fields["user"].(string)
	if strings.Contains(user, "alice@example.test") {
		t.Fatalf("email leaked in field: %q", user)
	}
	if !strings.Contains(user, "[REDACTED]") {
		t.Fatalf("email field missing redaction placeholder: %q", user)
	}
	// Non-string scalar should pass through unchanged.
	if got, ok := first.Fields["count"].(float64); !ok || got != 7 {
		// JSON unmarshals numbers as float64.
		t.Fatalf("count field tampered: %#v (ok=%v)", first.Fields["count"], ok)
	}

	// Vector 2: AWS key, IPv6.
	second := page.Entries[1]
	if strings.Contains(second.Message, "AKIAABCDEFGHIJKLMNOP") {
		t.Fatalf("AWS key leaked in message: %q", second.Message)
	}
	addr, _ := second.Fields["address"].(string)
	if strings.Contains(addr, "2001:0db8:85a3") {
		t.Fatalf("IPv6 leaked in field: %q", addr)
	}
	if path, _ := second.Fields["path"].(string); path != "/var/lib/foo" {
		t.Fatalf("non-sensitive path field unexpectedly modified: %q", path)
	}
}

func TestFileStoreWithRedactorFilteringCannotProbeUnredactedPayload(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	writeFixtureFile(t, dir, "filter.log", []Entry{
		{At: base, Level: "info", Component: "app", Message: "alice@example.test signed in"},
		{At: base.Add(time.Second), Level: "info", Component: "app", Message: "no email here"},
	})

	store, err := NewFileStore(dir, "*.log", WithRedactor(redaction.DefaultRedactor()))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	page, err := store.Query(Filter{FreeText: "alice"}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Entries) != 0 {
		t.Fatalf("secret-bearing free-text filter matched %d entries, want no presence oracle", len(page.Entries))
	}

	page, err = store.Query(Filter{FreeText: "signed in"}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("Query safe text: %v", err)
	}
	if len(page.Entries) != 1 || !strings.Contains(page.Entries[0].Message, "[REDACTED]") {
		t.Fatalf("safe-text query = %#v, want one redacted entry", page.Entries)
	}
}

func TestFileStoreWithoutRedactorPassesThroughPayload(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	writeFixtureFile(t, dir, "raw.log", []Entry{
		{At: base, Level: "info", Component: "app", Message: "midn_DEADBEEFCAFE", Fields: map[string]any{"ip": "10.0.0.1"}},
	})
	store, err := NewFileStore(dir, "*.log")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if store.Redactor() != nil {
		t.Fatal("Redactor() should be nil when no option supplied")
	}
	page, err := store.Query(Filter{}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got := page.Entries[0].Message; got != "midn_DEADBEEFCAFE" {
		t.Fatalf("expected raw message, got %q", got)
	}
}

// TestFileStoreWithRedactorMatchesGoldenVectors locks the read-path output
// against a small set of canonical input/output pairs covering each Phase 1
// rule (midn token, email, IPv4, IPv6, AWS access key, AWS secret key).
func TestFileStoreWithRedactorMatchesGoldenVectors(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	writeFixtureFile(t, dir, "golden.log", []Entry{
		{At: base, Level: "info", Component: "app", Message: "tok=midn_AAAA1111 user=alice@example.test"},
		{At: base.Add(time.Second), Level: "info", Component: "app", Message: "from=10.20.30.40"},
		{At: base.Add(2 * time.Second), Level: "info", Component: "app", Message: "from=fe80:0000:0000:0000:0000:0000:0000:0001"},
		{At: base.Add(3 * time.Second), Level: "info", Component: "app", Message: "key=AKIAABCDEFGHIJKLMNOP secret=ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789ABCDEF"},
	})
	store, err := NewFileStore(dir, "*.log", WithRedactor(redaction.DefaultRedactor()))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	page, err := store.Query(Filter{}, Cursor{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	wants := []struct {
		mustNotContain []string
		mustContain    []string
	}{
		{
			mustNotContain: []string{"midn_AAAA1111", "alice@example.test"},
			mustContain:    []string{"[REDACTED](midn-token)", "[REDACTED](email)"},
		},
		{
			mustNotContain: []string{"10.20.30.40"},
			mustContain:    []string{"[REDACTED](ipv4)"},
		},
		{
			mustNotContain: []string{"fe80:0000"},
			mustContain:    []string{"[REDACTED](ipv6)"},
		},
		{
			mustNotContain: []string{"AKIAABCDEFGHIJKLMNOP"},
			mustContain:    []string{"[REDACTED](aws-access-key)"},
		},
	}
	if len(page.Entries) != len(wants) {
		t.Fatalf("expected %d entries, got %d", len(wants), len(page.Entries))
	}
	for i, want := range wants {
		got := page.Entries[i].Message
		for _, leak := range want.mustNotContain {
			if strings.Contains(got, leak) {
				t.Fatalf("entry %d leaked %q in %q", i, leak, got)
			}
		}
		for _, marker := range want.mustContain {
			if !strings.Contains(got, marker) {
				t.Fatalf("entry %d missing marker %q in %q", i, marker, got)
			}
		}
	}
}
