//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package pg_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/tcpproxy"
)

func TestTCPProxyDurabilityAllocationAndActiveReplicaResolution(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "tcp-proof"})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := resources.CreateEnvironment(ctx, api.Environment{
		ProjectID: project.ID, Slug: "production", Tier: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, api.Service{ProjectID: project.ID, Slug: "database"})
	if err != nil {
		t.Fatal(err)
	}
	secondService, err := resources.CreateService(ctx, api.Service{ProjectID: project.ID, Slug: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	thirdService, err := resources.CreateService(ctx, api.Service{ProjectID: project.ID, Slug: "queue"})
	if err != nil {
		t.Fatal(err)
	}
	scope := tcpproxy.Scope{
		ProjectID: string(project.ID), EnvironmentID: string(environment.ID), ServiceID: string(service.ID),
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action
		) VALUES ('dep_tcp_a', $1, $2, $3, 'deploy_image')`,
		project.ID, service.ID, environment.ID,
	); err != nil {
		t.Fatalf("seed active deployment: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='deploying' WHERE id='dep_tcp_a'`); err != nil {
		t.Fatalf("advance active deployment: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='active', active=TRUE WHERE id='dep_tcp_a'`); err != nil {
		t.Fatalf("activate deployment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_environment_state (
			service_id, environment_id, active_deployment_id
		) VALUES ($1, $2, 'dep_tcp_a')`,
		service.ID, environment.ID,
	); err != nil {
		t.Fatalf("seed active deployment pointer: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_instances (
			deployment_id, instance_id, replica_ordinal, desired_state,
			observed_state, container_id, container_ip, ready
		) VALUES
			('dep_tcp_a', 'dep_tcp_a-r0', 0, 'running', 'running', 'container-a0', '10.42.0.8', TRUE),
			('dep_tcp_a', 'dep_tcp_a-r1', 1, 'running', 'running', 'container-a1', '10.42.0.9', TRUE)`,
	); err != nil {
		t.Fatalf("seed active deployment instances: %v", err)
	}

	resolver := tcpproxy.PostgresBackendResolver{Pool: pool}
	backends, err := resolver.ResolveBackends(ctx, scope)
	if err != nil {
		t.Fatalf("ResolveBackends: %v", err)
	}
	if len(backends) != 2 || backends[0].Host != "10.42.0.8" || backends[1].Host != "10.42.0.9" {
		t.Fatalf("backends=%#v", backends)
	}

	newStore := func() *tcpproxy.PostgresStore {
		store := tcpproxy.NewPostgresStore(pool)
		store.BaseDomain = "example.test"
		store.Ports = tcpproxy.PortRange{Start: 26000, End: 26001}
		return store
	}
	inputs := []tcpproxy.CreateEndpointInput{
		{Scope: scope, BackendPort: 5432},
		{Scope: tcpproxy.Scope{
			ProjectID: string(project.ID), EnvironmentID: string(environment.ID), ServiceID: string(secondService.ID),
		}, BackendPort: 6379},
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var created [2]tcpproxy.Endpoint
	var createErr [2]error
	for index := range inputs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			created[index], createErr[index] = newStore().CreateEndpoint(ctx, inputs[index])
		}(index)
	}
	close(start)
	wg.Wait()
	for index, err := range createErr {
		if err != nil {
			t.Fatalf("CreateEndpoint %d: %v", index, err)
		}
	}
	ports := []int{created[0].ExternalPort, created[1].ExternalPort}
	sort.Ints(ports)
	if fmt.Sprint(ports) != "[26000 26001]" {
		t.Fatalf("collision-safe ports=%v", ports)
	}
	if created[0].AllowedCIDRs == nil || len(created[0].AllowedCIDRs) != 0 {
		t.Fatalf("public peer policy=%#v", created[0].AllowedCIDRs)
	}

	restartedPool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer restartedPool.Close()
	restarted := tcpproxy.NewPostgresStore(restartedPool)
	durable, err := restarted.GetEndpoint(ctx, created[0].ID)
	if err != nil {
		t.Fatalf("GetEndpoint after restart: %v", err)
	}
	if durable.ExternalPort != created[0].ExternalPort || durable.PublicAddress() != created[0].PublicAddress() {
		t.Fatalf("durable endpoint=%#v created=%#v", durable, created[0])
	}

	if _, err := newStore().DisableEndpoint(ctx, created[0].ID); err != nil {
		t.Fatalf("DisableEndpoint: %v", err)
	}
	reuseStore := newStore()
	reused, err := reuseStore.CreateEndpoint(ctx, tcpproxy.CreateEndpointInput{
		Scope: tcpproxy.Scope{
			ProjectID: string(project.ID), EnvironmentID: string(environment.ID), ServiceID: string(thirdService.ID),
		},
		RequestedExternalPort: created[0].ExternalPort,
		BackendPort:           5672,
	})
	if err != nil {
		t.Fatalf("reuse withdrawn port: %v", err)
	}
	if reused.ExternalPort != created[0].ExternalPort {
		t.Fatalf("reused port=%d want=%d", reused.ExternalPort, created[0].ExternalPort)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE deployments SET state='superseded', active=FALSE WHERE id='dep_tcp_a';
	`); err != nil {
		t.Fatalf("supersede old deployment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action
		) VALUES ('dep_tcp_b', $1, $2, $3, 'deploy_image')`,
		project.ID, service.ID, environment.ID,
	); err != nil {
		t.Fatalf("seed redeployment: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='deploying' WHERE id='dep_tcp_b'`); err != nil {
		t.Fatalf("advance redeployment: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='active', active=TRUE WHERE id='dep_tcp_b'`); err != nil {
		t.Fatalf("activate redeployment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE service_environment_state
		   SET active_deployment_id='dep_tcp_b', version=version+1
		 WHERE service_id=$1 AND environment_id=$2`,
		service.ID, environment.ID,
	); err != nil {
		t.Fatalf("promote redeployment pointer: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_instances (
			deployment_id, instance_id, replica_ordinal, desired_state,
			observed_state, container_id, container_ip, ready
		) VALUES ('dep_tcp_b', 'dep_tcp_b-r0', 0, 'running', 'running', 'container-b0', '10.42.1.20', TRUE)`,
	); err != nil {
		t.Fatalf("seed redeployment instance: %v", err)
	}
	backends, err = resolver.ResolveBackends(ctx, scope)
	if err != nil {
		t.Fatalf("ResolveBackends after redeploy: %v", err)
	}
	if len(backends) != 1 || backends[0].DeploymentID != "dep_tcp_b" || backends[0].Host != "10.42.1.20" {
		t.Fatalf("redeployed backends=%#v", backends)
	}
}
