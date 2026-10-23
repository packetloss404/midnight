// Package limits evaluates plan limits against metered usage snapshots. It is
// intentionally policy-only: callers decide whether violations block admission,
// trigger billing, or only annotate operator evidence.
package limits
