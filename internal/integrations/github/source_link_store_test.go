// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package github

import (
	"errors"
	"testing"
	"time"
)

func TestInstallationGrantRequiresExclusiveAssignmentMetadata(t *testing.T) {
	valid := InstallationGrant{
		WorkspaceID:    " wrk_acme ",
		InstallationID: 42,
		AccountLogin:   " acme ",
		TargetType:     "Organization",
		GrantedByActor: "operator:root",
	}.Normalize()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid installation grant: %v", err)
	}
	if valid.WorkspaceID != "wrk_acme" || valid.AccountLogin != "acme" {
		t.Fatalf("normalized installation grant = %#v", valid)
	}

	for name, mutate := range map[string]func(*InstallationGrant){
		"workspace":    func(value *InstallationGrant) { value.WorkspaceID = "" },
		"installation": func(value *InstallationGrant) { value.InstallationID = 0 },
		"account":      func(value *InstallationGrant) { value.AccountLogin = "" },
		"target":       func(value *InstallationGrant) { value.TargetType = "Enterprise" },
		"actor":        func(value *InstallationGrant) { value.GrantedByActor = "" },
	} {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			if err := value.Validate(); !errors.Is(err, ErrLinkInvalid) {
				t.Fatalf("Validate() error = %v, want ErrLinkInvalid", err)
			}
		})
	}
}

func TestRepositoryGrantRequiresImmutableVerifiedRepository(t *testing.T) {
	verifiedAt := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	valid := RepositoryGrant{
		WorkspaceID:    "wrk_acme",
		InstallationID: 42,
		RepositoryID:   4242,
		Repository:     "Acme/API",
		CloneURL:       "https://github.com/acme/api.git",
		DefaultBranch:  "refs/heads/main",
		VerifiedAt:     verifiedAt,
		GrantedByActor: "operator:root",
	}.Normalize()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid repository grant: %v", err)
	}
	if valid.Repository != "acme/api" || valid.DefaultBranch != "main" {
		t.Fatalf("normalized repository grant = %#v", valid)
	}

	for name, mutate := range map[string]func(*RepositoryGrant){
		"repository id": func(value *RepositoryGrant) { value.RepositoryID = 0 },
		"repository":    func(value *RepositoryGrant) { value.Repository = "invalid" },
		"clone url":     func(value *RepositoryGrant) { value.CloneURL = "" },
		"verification":  func(value *RepositoryGrant) { value.VerifiedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			if err := value.Validate(); !errors.Is(err, ErrLinkInvalid) {
				t.Fatalf("Validate() error = %v, want ErrLinkInvalid", err)
			}
		})
	}
}

func TestDurableServiceLinkRequiresExactScopeAndRepositoryIdentity(t *testing.T) {
	valid := DurableServiceLink{
		ServiceID:   "svc_api",
		ProjectID:   "prj_acme",
		WorkspaceID: "wrk_acme",
		Link: ServiceLink{
			InstallationID: 42,
			RepositoryID:   4242,
			Repo:           "acme/api",
			Branch:         "main",
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid durable service link: %v", err)
	}

	for name, mutate := range map[string]func(*DurableServiceLink){
		"service":       func(value *DurableServiceLink) { value.ServiceID = "" },
		"project":       func(value *DurableServiceLink) { value.ProjectID = "" },
		"workspace":     func(value *DurableServiceLink) { value.WorkspaceID = "" },
		"repository id": func(value *DurableServiceLink) { value.Link.RepositoryID = 0 },
		"wait for ci": func(value *DurableServiceLink) {
			value.Link.WaitForCI = true
		},
		"required checks": func(value *DurableServiceLink) {
			value.Link.WaitForCI = false
			value.Link.RequiredChecks = []string{"build"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			if err := value.Validate(); !errors.Is(err, ErrLinkInvalid) {
				t.Fatalf("Validate() error = %v, want ErrLinkInvalid", err)
			}
		})
	}
}

func TestInstallationAdvisoryLockKeyIsCanonical(t *testing.T) {
	if got := InstallationAdvisoryLockKey(42); got != "github-installation:42" {
		t.Fatalf("InstallationAdvisoryLockKey(42) = %q", got)
	}
}
