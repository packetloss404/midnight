// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/varrefs"
)

func TestResolveBuildSecretsFiltersBuiltinsAndKeepsValuesOutOfMetadata(t *testing.T) {
	lookup := varrefs.NewMemoryLookup()
	lookup.SetService("prj", "env", "svc", "DATABASE_URL", "postgres://${{PASSWORD}}@db/app")
	lookup.SetService("prj", "env", "svc", "PASSWORD", "private-password")
	lookup.SetBuiltin("prj", "env", "svc", varrefs.BuiltinServiceName, "svc")
	resolver := varrefs.NewResolver(lookup)

	ids, err := listBuildSecretIDs(context.Background(), resolver, "prj", "env", "svc")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids, ","); got != "DATABASE_URL,PASSWORD" {
		t.Fatalf("build secret ids = %q", got)
	}
	if strings.Contains(strings.Join(ids, ","), "private-password") {
		t.Fatalf("metadata leaked plaintext: %#v", ids)
	}

	mounts, err := resolveBuildSecrets(context.Background(), resolver, "prj", "env", "svc", ids)
	if err != nil {
		t.Fatal(err)
	}
	defer build.ZeroBuildSecrets(mounts)
	if len(mounts) != 2 {
		t.Fatalf("mounts = %#v", mounts)
	}
	values := map[string]string{}
	for _, mount := range mounts {
		values[mount.ID] = string(mount.Value)
	}
	if values["DATABASE_URL"] != "postgres://private-password@db/app" || values["PASSWORD"] != "private-password" {
		t.Fatalf("resolved build secrets = %#v", values)
	}
	if _, exists := values[varrefs.BuiltinServiceName]; exists {
		t.Fatalf("runtime builtin was mounted as build secret: %#v", values)
	}
}

func TestResolveBuildSecretsFailsClosedOnUnavailableOrChangedResolver(t *testing.T) {
	_, err := resolveBuildSecrets(context.Background(), nil, "prj", "env", "svc", []string{"TOKEN"})
	if err == nil {
		t.Fatal("resolveBuildSecrets() succeeded without resolver")
	}

	lookup := varrefs.NewMemoryLookup()
	lookup.SetService("prj", "env", "svc", "OTHER", "value")
	resolver := varrefs.NewResolver(lookup)
	mounts, err := resolveBuildSecrets(context.Background(), resolver, "prj", "env", "svc", []string{"TOKEN"})
	if err == nil || mounts != nil {
		t.Fatalf("resolveBuildSecrets() = %#v, %v; want fail closed", mounts, err)
	}
}

func TestListBuildSecretIDsRejectsNonPortableVariableName(t *testing.T) {
	lookup := varrefs.NewMemoryLookup()
	lookup.SetService("prj", "env", "svc", "NOT-PORTABLE", "value")
	_, err := listBuildSecretIDs(context.Background(), varrefs.NewResolver(lookup), "prj", "env", "svc")
	if err == nil {
		t.Fatal("listBuildSecretIDs() accepted a non-portable environment name")
	}
}

func TestBuildSecretsUseImmutableDeploymentVersionsInsteadOfActiveValues(t *testing.T) {
	active := varrefs.NewMemoryLookup()
	active.SetService("prj", "env", "svc", "TOKEN", "active-new-value")
	pinned := varrefs.NewMemoryLookup()
	pinned.SetService("prj", "env", "svc", "TOKEN", "snapshot-old-value")
	var requested []varrefs.VariableVersionRef
	resolver := varrefs.NewResolver(&versionedBuildLookup{
		active: active, pinned: pinned, requested: &requested,
	})
	refs := []varrefs.VariableVersionRef{{
		ID: "var_token", ServiceID: "svc", Name: "TOKEN", Version: 3,
	}}

	ids, err := listBuildSecretIDsAtVersions(
		context.Background(), resolver, "prj", "env", "svc", &refs,
	)
	if err != nil {
		t.Fatal(err)
	}
	mounts, err := resolveBuildSecretsAtVersions(
		context.Background(), resolver, "prj", "env", "svc", ids, &refs,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer build.ZeroBuildSecrets(mounts)
	if len(mounts) != 1 || mounts[0].ID != "TOKEN" ||
		string(mounts[0].Value) != "snapshot-old-value" {
		t.Fatalf("pinned mounts=%#v", mounts)
	}
	if len(requested) != 1 || requested[0] != refs[0] {
		t.Fatalf("requested refs=%#v want=%#v", requested, refs)
	}

	empty := []varrefs.VariableVersionRef{}
	ids, err = listBuildSecretIDsAtVersions(
		context.Background(), resolver, "prj", "env", "svc", &empty,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("empty immutable snapshot fell back to active IDs: %#v", ids)
	}
}

type versionedBuildLookup struct {
	active    *varrefs.MemoryLookup
	pinned    *varrefs.MemoryLookup
	requested *[]varrefs.VariableVersionRef
}

func (l *versionedBuildLookup) LookupService(ctx context.Context, projectID, environmentID, serviceID, name string) (string, bool, error) {
	return l.active.LookupService(ctx, projectID, environmentID, serviceID, name)
}

func (l *versionedBuildLookup) LookupShared(ctx context.Context, projectID, environmentID, name string) (string, bool, error) {
	return l.active.LookupShared(ctx, projectID, environmentID, name)
}

func (l *versionedBuildLookup) LookupServiceByName(ctx context.Context, projectID, environmentID, name string) (string, bool, error) {
	return l.active.LookupServiceByName(ctx, projectID, environmentID, name)
}

func (l *versionedBuildLookup) LookupBuiltin(ctx context.Context, projectID, environmentID, serviceID, name string) (string, bool, error) {
	return l.active.LookupBuiltin(ctx, projectID, environmentID, serviceID, name)
}

func (l *versionedBuildLookup) ListServiceVariables(ctx context.Context, projectID, environmentID, serviceID string) ([]string, error) {
	return l.active.ListServiceVariables(ctx, projectID, environmentID, serviceID)
}

func (l *versionedBuildLookup) WithVariableVersions(refs []varrefs.VariableVersionRef) varrefs.VariableLookup {
	*l.requested = append([]varrefs.VariableVersionRef(nil), refs...)
	if len(refs) == 0 {
		return varrefs.NewMemoryLookup()
	}
	return l.pinned
}

var _ varrefs.VersionedVariableLookup = (*versionedBuildLookup)(nil)
