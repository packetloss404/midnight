package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestDeploymentResourceFromRecordExposesOnlyDurableEvidence(t *testing.T) {
	created := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	started := created.Add(time.Minute)
	completed := started.Add(2 * time.Minute)
	sha := strings.Repeat("A", 40)
	record := DeploymentRecord{
		ID:            "dpl_evidence",
		ServiceID:     "svc_evidence",
		ProjectID:     "prj_evidence",
		EnvironmentID: "env_evidence",
		Action:        deploy.ActionDeploySource,
		SourceType:    "github",
		Repo:          "https://github.com/acme/widget.git",
		Ref:           strings.ToLower(sha),
		SHA:           sha,
		OperationID:   "op_evidence",
		State:         deploy.DeploymentActive,
		Active:        true,
		CreatedAt:     created,
		UpdatedAt:     completed,
		StartedAt:     &started,
		CompletedAt:   &completed,
	}

	got := deploymentResourceFromRecord(Service{
		ID:        orailtypes.ID("svc_fallback"),
		ProjectID: orailtypes.ID("prj_fallback"),
	}, record)
	if got.Action != "deploy_source" || got.SourceType != "github" || got.SourceRepository != record.Repo || got.SourceRef != record.Ref {
		t.Fatalf("source evidence = %#v", got)
	}
	if got.CommitSHA != strings.ToLower(sha) {
		t.Fatalf("commit_sha = %q", got.CommitSHA)
	}
	if got.TriggerActorType != "provider_automation" || got.TriggerActorLabel != "GitHub App" {
		t.Fatalf("actor evidence = %q/%q", got.TriggerActorType, got.TriggerActorLabel)
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(created) || got.StartedAt == nil || !got.StartedAt.Equal(started) || got.CompletedAt == nil || !got.CompletedAt.Equal(completed) {
		t.Fatalf("lifecycle evidence = created %v started %v completed %v", got.CreatedAt, got.StartedAt, got.CompletedAt)
	}
}

func TestDeploymentResourceDoesNotInferUnprovenActorOrCommit(t *testing.T) {
	got := deploymentResourceFromRecord(Service{}, DeploymentRecord{
		SourceType: "git",
		Ref:        strings.Repeat("b", 40),
		SHA:        "branch-name-not-a-commit",
	})
	if got.CommitSHA != "" || got.TriggerActorType != "" || got.TriggerActorLabel != "" {
		t.Fatalf("unproven evidence leaked as fact: %#v", got)
	}

	github := deploymentResourceFromRecord(Service{}, DeploymentRecord{
		Action:     deploy.ActionDeploySource,
		SourceType: "github",
		Repo:       "https://github.com/acme/widget.git",
		Ref:        strings.Repeat("c", 40),
	})
	if github.CommitSHA != strings.Repeat("c", 40) || github.TriggerActorType != "provider_automation" {
		t.Fatalf("signed-webhook evidence = %#v", github)
	}

	redeploy := deploymentResourceFromRecord(Service{}, DeploymentRecord{
		Action:     deploy.ActionRedeployImage,
		SourceType: "github",
		Repo:       "https://github.com/acme/widget.git",
		SHA:        strings.Repeat("d", 40),
	})
	if redeploy.TriggerActorType != "" || redeploy.TriggerActorLabel != "" {
		t.Fatalf("user-triggered replay inherited provider actor: %#v", redeploy)
	}

	unprovenGitHub := deploymentResourceFromRecord(Service{}, DeploymentRecord{
		Action:     deploy.ActionDeploySource,
		SourceType: "github",
		Repo:       "https://github.com/acme/widget.git",
		Ref:        "refs/heads/main",
		SHA:        strings.Repeat("e", 40),
	})
	if unprovenGitHub.TriggerActorType != "" || unprovenGitHub.TriggerActorLabel != "" {
		t.Fatalf("mutable GitHub ref inferred provider actor: %#v", unprovenGitHub)
	}
}

func TestDeploymentResourceRedactsUnsafeLegacySourceEvidenceAndZeroTime(t *testing.T) {
	zero := time.Time{}
	for name, record := range map[string]DeploymentRecord{
		"credentials": {
			SourceType:  "github",
			Repo:        "https://user:secret@github.com/acme/private.git",
			Ref:         strings.Repeat("a", 40),
			StartedAt:   &zero,
			CompletedAt: &zero,
		},
		"query secret": {
			SourceType: "git",
			Repo:       "https://git.example/acme/private.git?token=secret",
			Ref:        "main",
		},
		"opaque url": {
			SourceType: "git",
			Repo:       "https:credential-like@github.com/acme/private.git",
			Ref:        "main",
		},
		"internal artifact": {
			SourceType: "artifact",
			Repo:       "sourceartifact://sha256:" + strings.Repeat("b", 64),
			Ref:        "sha256:" + strings.Repeat("b", 64),
		},
		"control character": {
			SourceType: "git",
			Repo:       "https://git.example/acme/private.git\nleak",
			Ref:        "main",
		},
		"format character": {
			SourceType: "git",
			Repo:       "https://git.example/acme/\u202eprivate.git",
			Ref:        "refs/heads/main",
		},
	} {
		t.Run(name, func(t *testing.T) {
			resource := deploymentResourceFromRecord(Service{}, record)
			if resource.SourceRepository != "" || resource.SourceRef != "" ||
				resource.TriggerActorType != "" || resource.TriggerActorLabel != "" {
				t.Fatalf("unsafe source evidence projected: %#v", resource)
			}
			body, err := json.Marshal(resource)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{`"created_at"`, `"started_at"`, `"completed_at"`} {
				if strings.Contains(string(body), field) {
					t.Fatalf("zero lifecycle field %s serialized: %s", field, body)
				}
			}
		})
	}

	unsafeRef := deploymentResourceFromRecord(Service{}, DeploymentRecord{
		SourceType: "git",
		Repo:       "https://git.example/acme/public.git",
		Ref:        "refs/heads/main\u202eleak",
	})
	if unsafeRef.SourceRepository == "" || unsafeRef.SourceRef != "" {
		t.Fatalf("unsafe ref projection = %#v", unsafeRef)
	}
}
