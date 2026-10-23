//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	templatepkg "github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/migrations"
)

func TestCommunityTemplateForkLifecycleIsDurableAndConditional(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	stack, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool: pool, MigrationsFS: migrations.FS, ManifestPath: "manifest.yaml",
		CursorSecret:          []byte("community-template-cursor-secret"),
		AutomationTokenMACKey: []byte("community-template-automation-key"),
		InvitationMACKey:      []byte("community-template-invitation-key"),
		VariableIntentMACKey:  []byte("community-template-variable-intent-key"),
		VariableKeyProvider:   testVariableKeyProvider(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	discovery := validAPIDiscovery()
	stack.CommunityTemplateDiscoverer = fakeCommunityDiscoverer(func(context.Context, string, string) (templatepkg.CommunityDiscovery, error) {
		return discovery, nil
	})
	stack.ProductionAuthRequired = false
	resources := stack.ProjectStore.(*PostgresResourceStore)
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "community-template-proof", DisplayName: "Community template proof"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRouterWithDataStack(stack)
	createBody := `{"project_id":"` + string(project.ID) + `","source":"` + discovery.Provenance.Source + `","ref":"` + discovery.Provenance.Ref + `","expected_manifest_sha256":"` + discovery.Provenance.ManifestSHA256 + `"}`
	created := doRouterRequest(handler, http.MethodPost, "/api/v1alpha/templates/community/forks", createBody, map[string]string{"Idempotency-Key": "community-template-proof"})
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"1"` {
		t.Fatalf("create status=%d headers=%v body=%s", created.Code, created.Header(), created.Body.String())
	}
	var fork templatepkg.CommunityFork
	if err := json.Unmarshal(created.Body.Bytes(), &fork); err != nil {
		t.Fatal(err)
	}
	if fork.TrustClass != "local" || !fork.Valid || fork.Provenance != discovery.Provenance {
		t.Fatalf("fork=%+v", fork)
	}
	assertTableCount(t, ctx, pool, "community_template_forks", 1)

	draft := fork.Draft
	draft.Template.Services[0].Image = "acme/worker:latest"
	draftJSON, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	update := doRouterRequest(handler, http.MethodPatch, "/api/v1alpha/templates/community/forks/"+fork.ID, `{"project_id":"`+string(project.ID)+`","draft":`+string(draftJSON)+`}`, map[string]string{"If-Match": `"1"`})
	if update.Code != http.StatusOK || update.Header().Get("ETag") != `"2"` || !strings.Contains(update.Body.String(), `"image_digest"`) || !strings.Contains(update.Body.String(), `"valid":false`) {
		t.Fatalf("update status=%d headers=%v body=%s", update.Code, update.Header(), update.Body.String())
	}
	stale := doRouterRequest(handler, http.MethodPatch, "/api/v1alpha/templates/community/forks/"+fork.ID, `{"project_id":"`+string(project.ID)+`","draft":`+string(draftJSON)+`}`, map[string]string{"If-Match": `"1"`})
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}

	var upstreamSlug, source, ref string
	var version int64
	if err := pool.QueryRow(ctx, `
		SELECT upstream_manifest->'template'->>'slug',provenance->>'source',provenance->>'ref',resource_version
		  FROM community_template_forks WHERE id=$1`, fork.ID,
	).Scan(&upstreamSlug, &source, &ref, &version); err != nil {
		t.Fatal(err)
	}
	if upstreamSlug != discovery.Manifest.Template.Slug || source != discovery.Provenance.Source || ref != discovery.Provenance.Ref || version != 2 {
		t.Fatalf("durable evidence slug=%q source=%q ref=%q version=%d", upstreamSlug, source, ref, version)
	}
}
