package registry

import (
	"sort"
	"time"
)

type RetentionPlan struct {
	GeneratedAt time.Time
	DryRun      bool
	Rules       []RetentionRule
	Actions     []RetentionAction
}

type RetentionRule struct {
	Repository  string
	KeepLast    int
	KeepSince   time.Duration
	KeepDigests []string
	KeepTags    []string
}

type ImageTag struct {
	Repository string
	Name       string
	Digest     string
	SizeBytes  int64
	PushedAt   time.Time
}

type ImagePin struct {
	Repository    string
	Digest        string
	Active        bool
	RollbackUntil time.Time
}

type RetentionAction struct {
	Repository string   `json:"repository"`
	Digest     string   `json:"digest"`
	Tags       []string `json:"tags,omitempty"`
	Reason     string   `json:"reason"`
	Delete     bool     `json:"delete"`
	SizeBytes  int64    `json:"sizeBytes"`
}

type GCPlanRequest struct {
	GeneratedAt time.Time
	Tags        []ImageTag
	Pins        []ImagePin
	Rules       []RetentionRule
}

type GCApplyResult struct {
	AppliedAt    time.Time
	DryRun       bool
	ConfirmApply bool
	Summary      GCSummary
	Actions      []RetentionAction
}

type GCApplyRequest struct {
	Plan         RetentionPlan
	ConfirmApply bool
}

type GCSummary struct {
	Repositories   int            `json:"repositories"`
	Candidates     int            `json:"candidates"`
	Deletions      int            `json:"deletions"`
	ProtectedBytes int64          `json:"protectedBytes"`
	DeletedBytes   int64          `json:"deletedBytes"`
	ReasonCounts   map[string]int `json:"reasonCounts,omitempty"`
}

func (p RetentionPlan) Deletions() []RetentionAction {
	deletions := make([]RetentionAction, 0, len(p.Actions))
	for _, action := range p.Actions {
		if action.Delete {
			deletions = append(deletions, action)
		}
	}

	return deletions
}

// PlanGCDryRun creates a non-mutating registry GC plan. Digests referenced by
// active pins, rollback-window pins, explicit keep rules, or the last N pushed
// tags for a repository are never marked for deletion.
func PlanGCDryRun(request GCPlanRequest) RetentionPlan {
	now := request.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}

	rulesByRepository := make(map[string]RetentionRule, len(request.Rules))
	protected := make(map[string]string)
	protectedTags := make(map[string]string)
	for _, rule := range request.Rules {
		rulesByRepository[rule.Repository] = rule
		for _, digest := range rule.KeepDigests {
			protect(protected, rule.Repository, digest, "explicit pin")
		}
		for _, tag := range rule.KeepTags {
			protectTag(protectedTags, rule.Repository, tag, "protected tag")
		}
	}

	for _, pin := range request.Pins {
		if pin.Active {
			protect(protected, pin.Repository, pin.Digest, "active pin")
			continue
		}
		if !pin.RollbackUntil.IsZero() && !pin.RollbackUntil.Before(now) {
			protect(protected, pin.Repository, pin.Digest, "rollback window")
		}
	}

	tagsByRepository := make(map[string][]ImageTag)
	for _, tag := range request.Tags {
		tagsByRepository[tag.Repository] = append(tagsByRepository[tag.Repository], tag)
		if reason, ok := protectedTags[tagKey(tag.Repository, tag.Name)]; ok {
			protect(protected, tag.Repository, tag.Digest, reason)
		}
	}

	for repository, tags := range tagsByRepository {
		sort.SliceStable(tags, func(i, j int) bool {
			return tags[i].PushedAt.After(tags[j].PushedAt)
		})

		keepLast := rulesByRepository[repository].KeepLast
		if keepLast > len(tags) {
			keepLast = len(tags)
		}
		for _, tag := range tags[:keepLast] {
			protect(protected, repository, tag.Digest, "last-N tag")
		}
	}

	seen := make(map[string]int)
	actions := make([]RetentionAction, 0, len(request.Tags))
	for _, tag := range request.Tags {
		key := digestKey(tag.Repository, tag.Digest)
		if index, ok := seen[key]; ok {
			actions[index].Tags = append(actions[index].Tags, tag.Name)
			continue
		}
		seen[key] = len(actions)

		reason, ok := protected[key]
		action := RetentionAction{Repository: tag.Repository, Digest: tag.Digest, Tags: []string{tag.Name}, Reason: reason, SizeBytes: tag.SizeBytes}
		if !ok {
			action.Reason = "unprotected"
			action.Delete = true
		}
		actions = append(actions, action)
	}

	return RetentionPlan{GeneratedAt: now, DryRun: true, Rules: request.Rules, Actions: actions}
}

func ApplyGCPlan(request GCApplyRequest) GCApplyResult {
	appliedAt := request.Plan.GeneratedAt
	if appliedAt.IsZero() {
		appliedAt = time.Now().UTC()
	}

	summary := summarizeGCActions(request.Plan.Actions)
	deletions := request.Plan.Deletions()
	if len(deletions) > 0 && !request.ConfirmApply {
		return GCApplyResult{AppliedAt: appliedAt, DryRun: true, ConfirmApply: false, Summary: summary}
	}

	return GCApplyResult{AppliedAt: appliedAt, DryRun: false, ConfirmApply: request.ConfirmApply, Summary: summary, Actions: deletions}
}

func summarizeGCActions(actions []RetentionAction) GCSummary {
	repositories := map[string]bool{}
	summary := GCSummary{Candidates: len(actions), ReasonCounts: map[string]int{}}
	for _, action := range actions {
		repositories[action.Repository] = true
		if action.Reason != "" {
			summary.ReasonCounts[action.Reason]++
		}
		if action.Delete {
			summary.Deletions++
			summary.DeletedBytes += action.SizeBytes
			continue
		}
		summary.ProtectedBytes += action.SizeBytes
	}
	summary.Repositories = len(repositories)
	if len(summary.ReasonCounts) == 0 {
		summary.ReasonCounts = nil
	}

	return summary
}

func protect(protected map[string]string, repository, digest, reason string) {
	if digest == "" {
		return
	}
	key := digestKey(repository, digest)
	if _, exists := protected[key]; !exists {
		protected[key] = reason
	}
}

func protectTag(protected map[string]string, repository, tag, reason string) {
	if tag == "" {
		return
	}
	key := tagKey(repository, tag)
	if _, exists := protected[key]; !exists {
		protected[key] = reason
	}
}

func digestKey(repository, digest string) string {
	return repository + "\x00" + digest
}

func tagKey(repository, tag string) string {
	return repository + "\x00" + tag
}
