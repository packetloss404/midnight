//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresEnvironmentResourceEvidenceStoreReadsDurableTruth(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	statements := []string{
		`INSERT INTO workspaces (id,slug,display_name,created_at,updated_at)
		 VALUES ('wrk_resource_evidence','resource-evidence','Resource Evidence',$1,$1)`,
		`INSERT INTO projects (id,workspace_id,slug,display_name,created_at,updated_at)
		 VALUES ('prj_resource_evidence','wrk_resource_evidence','app','App',$1,$1)`,
		`INSERT INTO environments (
			id,project_id,slug,name,tier,is_protected,access_restricted,created_at,updated_at
		 ) VALUES ('env_resource_evidence','prj_resource_evidence','production','Production','production',FALSE,FALSE,$1,$1)`,
		`INSERT INTO services (id,project_id,slug,display_name,created_at,updated_at)
		 VALUES ('svc_resource_evidence','prj_resource_evidence','api','API',$1,$1)`,
		`INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,source_type,repo,ref,sha,
			root_directory,dockerfile_path,builder,image_ref,image_digest,state,active,
			trigger_key,runtime_desired_state,runtime_observed_state,runtime_generation,
			created_at,updated_at
		 ) VALUES (
			'dep_resource_evidence','prj_resource_evidence','svc_resource_evidence',
			'env_resource_evidence','deploy_image','github','acme/api','main',repeat('b',40),
			'services/api','deploy/api.Dockerfile','dockerfile',
			'registry.example/api@sha256:` + hash + `','sha256:` + hash + `',
			'queued',FALSE,'trigger:resource-evidence','running','unknown',1,$1,$1
		 )`,
		`UPDATE deployments SET state='deploying',runtime_observed_state='running',updated_at=$1
		 WHERE id='dep_resource_evidence'`,
		`UPDATE deployments SET state='active',active=TRUE,updated_at=$1
		 WHERE id='dep_resource_evidence'`,
		`INSERT INTO deployment_instances (
			deployment_id,instance_id,replica_ordinal,desired_state,observed_state,
			container_id,container_ip,ready,created_at,updated_at
		 ) VALUES
			('dep_resource_evidence','dep_resource_evidence-r0',0,'running','running','ctr-0','10.44.0.10',TRUE,$1,$1),
			('dep_resource_evidence','dep_resource_evidence-r1',1,'running','running','ctr-1','10.44.0.11',FALSE,$1,$1)`,
		`INSERT INTO route_controller_groups (
			project_id,environment_id,route_group,desired_revision,observed_revision,
			applied_revision,apply_state,artifact_hash,created_at,updated_at
		 ) VALUES (
			'prj_resource_evidence','env_resource_evidence','resource-evidence',1,1,1,
			'ready','` + hash + `',$1,$1
		 )`,
		`INSERT INTO domains (
			id,project_id,environment_id,service_id,kind,hostname,normalized_hostname,
			tls_desired,tls_state,desired_revision,created_at,updated_at
		 ) VALUES (
			'dom_resource_evidence','prj_resource_evidence','env_resource_evidence',
			'svc_resource_evidence','custom','api.example.test','api.example.test',
			TRUE,'ready',1,$1,$1
		 )`,
		`INSERT INTO http_routes (
			id,domain_id,project_id,environment_id,service_id,name,path_prefix,
			target_port,public_enabled,desired_revision,created_at,updated_at
		 ) VALUES (
			'rte_resource_evidence','dom_resource_evidence','prj_resource_evidence',
			'env_resource_evidence','svc_resource_evidence','api','/v1',8080,TRUE,1,$1,$1
		 )`,
		`INSERT INTO route_backends (
			id,route_id,project_id,environment_id,service_id,deployment_id,instance_id,
			service_url,ready,desired_revision,observed_at
		 ) VALUES (
			'bkd_resource_evidence','rte_resource_evidence','prj_resource_evidence',
			'env_resource_evidence','svc_resource_evidence','dep_resource_evidence',
			'dep_resource_evidence-r0','http://10.44.0.10:8080',TRUE,1,$1
		 )`,
		`INSERT INTO route_publication_observations (
			project_id,environment_id,desired_revision,artifact_hash,
			traefik_acknowledged,ingress_probe_succeeded,probe_url,probe_status,state,observed_at
		 ) VALUES (
			'prj_resource_evidence','env_resource_evidence',1,'` + hash + `',
			TRUE,TRUE,'https://api.example.test/v1',200,'ready',$1
		 )`,
		`INSERT INTO github_workspace_installation_grants (
			workspace_id,installation_id,account_login,target_type,granted_by_actor,
			created_at,updated_at
		 ) VALUES ('wrk_resource_evidence',42,'acme','Organization','operator',$1,$1)`,
		`INSERT INTO github_workspace_repository_grants (
			workspace_id,installation_id,repository_id,repository_full_name,clone_url,
			default_branch,repository_private,repository_access_verified_at,
			granted_by_actor,created_at,updated_at
		 ) VALUES (
			'wrk_resource_evidence',42,84,'acme/api','https://github.com/acme/api.git',
			'main',TRUE,$1,'operator',$1,$1
		 )`,
		`INSERT INTO service_github_links (
			service_id,project_id,workspace_id,installation_id,repository_id,
			repository_full_name,branch,watch_paths,root_directory,config_path,
			wait_for_ci,required_checks,auto_deploy,repository_access_verified_at,
			created_at,updated_at
		 ) VALUES (
			'svc_resource_evidence','prj_resource_evidence','wrk_resource_evidence',42,84,
			'acme/api','main',ARRAY['services/api/**'],'services/api','midnight.toml',
			TRUE,ARRAY['test'],TRUE,$1,$1,$1
		 )`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed resource evidence: %v\n%s", err, statement)
		}
	}

	store := NewPostgresEnvironmentResourceEvidenceStore(pool)
	replicas, err := store.ListPersistedReplicasByEnvironment(
		ctx, "prj_resource_evidence", "env_resource_evidence",
	)
	if err != nil || len(replicas) != 1 || replicas[0].DesiredReplicas != 2 ||
		replicas[0].ReadyReplicas != 1 || replicas[0].RunningReplicas != 2 {
		t.Fatalf("replicas=%#v err=%v", replicas, err)
	}
	endpoints, err := store.ListPersistedEndpointsByEnvironment(
		ctx, "prj_resource_evidence", "env_resource_evidence",
	)
	if err != nil || len(endpoints) != 1 ||
		endpoints[0].DeploymentID != "dep_resource_evidence" ||
		endpoints[0].URL != "https://api.example.test/v1" || !endpoints[0].TLS {
		t.Fatalf("endpoints=%#v err=%v", endpoints, err)
	}
	sources, err := store.ListPersistedSourcesByProject(
		ctx, "wrk_resource_evidence", "prj_resource_evidence",
	)
	if err != nil || len(sources) != 1 || sources[0].Repository != "acme/api" ||
		len(sources[0].WatchPaths) != 1 || sources[0].ConfigPath != "midnight.toml" ||
		!sources[0].WaitForCI || len(sources[0].RequiredChecks) != 1 ||
		!sources[0].AutoDeploy {
		t.Fatalf("sources=%#v err=%v", sources, err)
	}
}
