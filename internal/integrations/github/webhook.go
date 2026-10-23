// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
)

// ErrWebhookSignatureInvalid is returned when the X-Hub-Signature-256
// header is malformed or does not match the body HMAC.
var ErrWebhookSignatureInvalid = errors.New("github: webhook signature invalid")

// WebhookVerifier validates `X-Hub-Signature-256: sha256=<hex>` headers
// against a shared secret. GitHub computes the HMAC over the raw body
// only; no timestamp is involved (replay protection is handled by the
// webhook receiver's dedupe store).
type WebhookVerifier struct {
	Secret []byte
}

// NewWebhookVerifier returns a verifier bound to secret.
func NewWebhookVerifier(secret string) WebhookVerifier {
	return WebhookVerifier{Secret: []byte(secret)}
}

// Verify returns nil when header is a valid sha256=<hex> signature over
// body using the configured secret.
func (v WebhookVerifier) Verify(header string, body []byte) error {
	if len(v.Secret) == 0 {
		return ErrWebhookSignatureInvalid
	}
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return ErrWebhookSignatureInvalid
	}
	candidate := strings.TrimPrefix(header, prefix)
	if len(candidate) != 64 {
		return ErrWebhookSignatureInvalid
	}
	mac := hmac.New(sha256.New, v.Secret)
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(candidate)) != 1 {
		return ErrWebhookSignatureInvalid
	}
	return nil
}

// PushEventCommit is one commit entry from a push event.
type PushEventCommit struct {
	ID       string   `json:"id"`
	Added    []string `json:"added,omitempty"`
	Removed  []string `json:"removed,omitempty"`
	Modified []string `json:"modified,omitempty"`
}

// ParsePushEventCommits decodes the commits[] array. Used by the
// watch_paths filter to decide whether any commit touched a path the
// service cares about.
func ParsePushEventCommits(body []byte) ([]PushEventCommit, error) {
	var raw struct {
		Commits []PushEventCommit `json:"commits"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return raw.Commits, nil
}

// PushEventInstallationID extracts the installation.id field from a
// push event body. Returns 0 when absent (older payloads / legacy
// webhooks without an App).
func PushEventInstallationID(body []byte) int64 {
	var raw struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0
	}
	return raw.Installation.ID
}

// PushEventCloneURL extracts the https clone URL from a push event so
// the build worker can clone via x-access-token://. Returns the public
// clone URL when present.
func PushEventCloneURL(body []byte) string {
	var raw struct {
		Repository struct {
			CloneURL string `json:"clone_url"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	return raw.Repository.CloneURL
}

// MaxPushWebhookCommits is GitHub's hard cap on the number of entries in a
// push webhook's `commits[]` array. When a push contains this many delivered
// commits the array MAY be truncated — the change we care about could live in
// a commit GitHub dropped from the payload.
//
// Note: GitHub's push webhook does NOT populate `size`/`distinct_size`, so the
// only reliable truncation signal available to a receiver is the length of the
// delivered commit list reaching the cap.
const MaxPushWebhookCommits = 20

// CommitsMayBeTruncated reports whether the delivered commit list may have been
// truncated by GitHub (i.e. hit the 20-commit cap). When true, absence of a
// matching path in the delivered commits does NOT prove the push left the watch
// paths untouched.
func CommitsMayBeTruncated(commits []PushEventCommit) bool {
	return len(commits) >= MaxPushWebhookCommits
}

// WatchPathReason is the audit reason a path-filter decision records.
type WatchPathReason string

const (
	// WatchReasonNoWatchPaths: no (effective) watch filter is configured, so
	// every push triggers.
	WatchReasonNoWatchPaths WatchPathReason = "no_watch_paths"
	// WatchReasonMatchedWatchPath: a delivered commit touched a watched path.
	WatchReasonMatchedWatchPath WatchPathReason = "matched_watch_path"
	// WatchReasonNoMatchingPaths: a full (non-truncated) commit list touched
	// no watched path — safe to skip.
	WatchReasonNoMatchingPaths WatchPathReason = "no_matching_paths"
	// WatchReasonTruncatedFailOpen: the commit list may be truncated and a
	// watch filter is set, so we FAIL OPEN and trigger rather than risk
	// silently skipping a deploy whose change sits in a dropped commit.
	WatchReasonTruncatedFailOpen WatchPathReason = "truncated_push_fail_open"
)

// WatchPathDecision is the structured outcome of evaluating a push against a
// service's watch_paths filter.
type WatchPathDecision struct {
	// Matched is true when the deploy should trigger.
	Matched bool
	// Root is the matched watch-path root (used as the deploy subpath). Empty
	// for no-filter and fail-open triggers.
	Root string
	// Reason is the audit reason for the decision.
	Reason WatchPathReason
	// Truncated records whether the delivered commit list may be truncated.
	Truncated bool
}

// EvaluateWatchPaths decides whether a push should trigger a deploy given the
// service's watch_paths filter and the delivered commits.
//
// It FAILS OPEN: when a watch filter is set and the delivered commit list may
// be truncated (>= MaxPushWebhookCommits), it triggers with
// WatchReasonTruncatedFailOpen instead of dropping the deploy — a missed
// auto-deploy is worse than an occasional redundant one.
//
// Match semantics: a watch path "services/api" matches the commit file
// "services/api/main.go" and "services/api". Glob is intentionally not
// supported in MVP — the prefix match is panel-locked.
func EvaluateWatchPaths(watchPaths []string, commits []PushEventCommit) WatchPathDecision {
	truncated := CommitsMayBeTruncated(commits)
	normalized := normalizeWatchPaths(watchPaths)
	if len(normalized) == 0 {
		// No filter (or an operator value like ["/"] that normalizes to
		// nothing): every push triggers.
		return WatchPathDecision{Matched: true, Reason: WatchReasonNoWatchPaths, Truncated: truncated}
	}
	// Prefer a precise match reason when a delivered commit already proves the
	// watch path was touched, even on a possibly-truncated push.
	for _, c := range commits {
		for _, f := range concat(c.Added, c.Modified, c.Removed) {
			for _, root := range normalized {
				if pathHasPrefix(f, root) {
					return WatchPathDecision{Matched: true, Root: root, Reason: WatchReasonMatchedWatchPath, Truncated: truncated}
				}
			}
		}
	}
	if truncated {
		// Fail open: we cannot prove the watch paths were untouched.
		return WatchPathDecision{Matched: true, Reason: WatchReasonTruncatedFailOpen, Truncated: true}
	}
	return WatchPathDecision{Matched: false, Reason: WatchReasonNoMatchingPaths}
}

// WatchPathsMatch returns true when any commit in commits touched a
// path under any entry in watchPaths. An empty watchPaths slice matches
// every commit (no filter).
//
// This is a thin wrapper over EvaluateWatchPaths preserved for existing
// callers; it inherits the fail-open behavior on truncated pushes.
func WatchPathsMatch(watchPaths []string, commits []PushEventCommit) (matched bool, matchedRoot string) {
	d := EvaluateWatchPaths(watchPaths, commits)
	return d.Matched, d.Root
}

// normalizeWatchPaths trims and de-slashes watch-path entries, dropping blanks
// and entries (like "/") that normalize to nothing.
func normalizeWatchPaths(watchPaths []string) []string {
	if len(watchPaths) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(watchPaths))
	for _, wp := range watchPaths {
		wp = strings.Trim(strings.TrimSpace(wp), "/")
		if wp == "" {
			continue
		}
		normalized = append(normalized, wp)
	}
	return normalized
}

func concat(a, b, c []string) []string {
	out := make([]string, 0, len(a)+len(b)+len(c))
	out = append(out, a...)
	out = append(out, b...)
	out = append(out, c...)
	return out
}

func pathHasPrefix(file, root string) bool {
	file = strings.TrimPrefix(path.Clean(file), "./")
	root = strings.TrimPrefix(path.Clean(root), "./")
	if file == root {
		return true
	}
	return strings.HasPrefix(file, root+"/")
}

// EventType identifies a known GitHub webhook event (the value of the
// `X-GitHub-Event` header).
type EventType string

const (
	EventPing                    EventType = "ping"
	EventPush                    EventType = "push"
	EventInstallation            EventType = "installation"
	EventInstallationRepos       EventType = "installation_repositories"
	EventPullRequest             EventType = "pull_request"
	EventCheckRun                EventType = "check_run"
	EventCheckSuite              EventType = "check_suite"
	EventWorkflowRun             EventType = "workflow_run"
	EventInstallationTargetEvent EventType = "installation_target"
)

// IsKnown returns true for the events Midnight currently understands.
func (e EventType) IsKnown() bool {
	switch e {
	case EventPing, EventPush, EventInstallation, EventInstallationRepos,
		EventPullRequest, EventCheckRun, EventCheckSuite, EventWorkflowRun,
		EventInstallationTargetEvent:
		return true
	default:
		return false
	}
}
