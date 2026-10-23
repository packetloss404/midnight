package registry

import (
	"sort"
	"strings"
	"time"
)

// CacheEntry is one locally cached registry object. The cache planner is
// intentionally storage-agnostic; callers provide observed entries and execute
// returned actions through their own backend.
type CacheEntry struct {
	Repository   string
	Digest       string
	SizeBytes    int64
	LastAccessed time.Time
	LastVerified time.Time
	Pinned       bool
}

type CachePolicy struct {
	MaxBytes   int64
	MaxAge     time.Duration
	StaleAfter time.Duration
	Now        time.Time
}

type CacheActionKind string

const (
	CacheKeep    CacheActionKind = "keep"
	CacheEvict   CacheActionKind = "evict"
	CacheRefresh CacheActionKind = "refresh"
)

type CacheAction struct {
	Repository string          `json:"repository"`
	Digest     string          `json:"digest"`
	Kind       CacheActionKind `json:"kind"`
	Reason     string          `json:"reason"`
	SizeBytes  int64           `json:"sizeBytes"`
}

type CachePlan struct {
	GeneratedAt time.Time     `json:"generatedAt"`
	MaxBytes    int64         `json:"maxBytes,omitempty"`
	Actions     []CacheAction `json:"actions"`
}

// PlanCache returns deterministic keep/evict actions for a local registry
// cache. Pinned entries are never evicted. Age eviction runs before size
// eviction; size eviction removes least-recently-accessed unpinned entries.
func PlanCache(entries []CacheEntry, policy CachePolicy) CachePlan {
	now := policy.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ordered := append([]CacheEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Repository != ordered[j].Repository {
			return ordered[i].Repository < ordered[j].Repository
		}
		return ordered[i].Digest < ordered[j].Digest
	})

	actions := make([]CacheAction, 0, len(ordered))
	keptSize := int64(0)
	for _, entry := range ordered {
		action := cacheAction(entry, CacheKeep, "within policy")
		if strings.TrimSpace(entry.Digest) == "" {
			action.Kind = CacheEvict
			action.Reason = "missing digest"
		} else if entry.Pinned {
			action = cacheRetentionAction(entry, policy, now, "pinned")
			keptSize += entry.SizeBytes
		} else if policy.MaxAge > 0 && !entry.LastAccessed.IsZero() && entry.LastAccessed.Add(policy.MaxAge).Before(now) {
			action.Kind = CacheEvict
			action.Reason = "older than max age"
		} else {
			action = cacheRetentionAction(entry, policy, now, "within policy")
			keptSize += entry.SizeBytes
		}
		actions = append(actions, action)
	}

	if policy.MaxBytes > 0 && keptSize > policy.MaxBytes {
		indices := make([]int, 0, len(actions))
		for i, action := range actions {
			if (action.Kind == CacheKeep || action.Kind == CacheRefresh) && action.Reason != "pinned" {
				indices = append(indices, i)
			}
		}
		sort.SliceStable(indices, func(i, j int) bool {
			left := ordered[indices[i]]
			right := ordered[indices[j]]
			if !left.LastAccessed.Equal(right.LastAccessed) {
				return left.LastAccessed.Before(right.LastAccessed)
			}
			if left.Repository != right.Repository {
				return left.Repository < right.Repository
			}
			return left.Digest < right.Digest
		})
		for _, index := range indices {
			if keptSize <= policy.MaxBytes {
				break
			}
			actions[index].Kind = CacheEvict
			actions[index].Reason = "over max bytes"
			keptSize -= actions[index].SizeBytes
		}
	}

	return CachePlan{GeneratedAt: now, MaxBytes: policy.MaxBytes, Actions: actions}
}

func cacheAction(entry CacheEntry, kind CacheActionKind, reason string) CacheAction {
	return CacheAction{Repository: entry.Repository, Digest: entry.Digest, Kind: kind, Reason: reason, SizeBytes: entry.SizeBytes}
}

func cacheRetentionAction(entry CacheEntry, policy CachePolicy, now time.Time, fallback string) CacheAction {
	if policy.StaleAfter > 0 && !entry.LastVerified.IsZero() && entry.LastVerified.Add(policy.StaleAfter).Before(now) {
		return cacheAction(entry, CacheRefresh, "metadata stale")
	}
	return cacheAction(entry, CacheKeep, fallback)
}
