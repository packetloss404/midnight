//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package tcpproxy_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/api"
	dbpg "github.com/midnight/midnight/internal/db/pg"
	midnightnetwork "github.com/midnight/midnight/internal/network"
	"github.com/midnight/midnight/internal/tcpproxy"
	migrations "github.com/midnight/midnight/migrations"
)

func TestDockerPublishedPortEnrollsRoutesRedeploysWithdrawsAndDetaches(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	controlName := "midnight-tcp-control-" + suffix
	control, err := tc.GenericNetwork(ctx, tc.GenericNetworkRequest{NetworkRequest: tc.NetworkRequest{
		Name: controlName, Driver: "bridge", CheckDuplicate: true,
	}})
	if err != nil {
		skipDockerUnavailable(t, err)
		t.Fatalf("create control network: %v", err)
	}
	t.Cleanup(func() { _ = control.Remove(context.Background()) })

	postgres, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
		ContainerRequest: tc.ContainerRequest{
			Image: "postgres:16-alpine",
			Env: map[string]string{
				"POSTGRES_DB": "midnight", "POSTGRES_USER": "midnight", "POSTGRES_PASSWORD": "midnight",
			},
			ExposedPorts:   []string{"5432/tcp"},
			Networks:       []string{controlName},
			NetworkAliases: map[string][]string{controlName: {"postgres"}},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		skipDockerUnavailable(t, err)
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = postgres.Terminate(context.Background()) })
	host, err := postgres.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mappedPostgresPort, err := postgres.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	hostDSN := fmt.Sprintf("postgresql://midnight:midnight@%s:%s/midnight?sslmode=disable", host, mappedPostgresPort.Port())
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: hostDSN, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := (&dbpg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "tcp-docker-proof"})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := resources.CreateEnvironment(ctx, api.Environment{
		ProjectID: project.ID, Slug: "production", Tier: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, api.Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	scope := tcpproxy.Scope{
		ProjectID: string(project.ID), EnvironmentID: string(environment.ID), ServiceID: string(service.ID),
	}
	environmentNetwork := midnightnetwork.ProjectNetwork{
		ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID,
	}
	managed, err := tc.GenericNetwork(ctx, tc.GenericNetworkRequest{NetworkRequest: tc.NetworkRequest{
		Name: environmentNetwork.Name(), Driver: "bridge", CheckDuplicate: true,
		Labels: environmentNetwork.Labels(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Remove(context.Background()) })

	backendOne := startHTTPBackend(t, ctx, environmentNetwork.Name(), "backend-one")
	backendOneIP := containerNetworkIP(t, ctx, backendOne, environmentNetwork.Name())
	seedActiveDeployment(t, ctx, pool, scope, "dep_tcp_docker_one", "inst_tcp_docker_one", backendOneIP)

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	proxyName := "midnight-tcp-proxy-" + suffix
	proxy, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
		ContainerRequest: tc.ContainerRequest{
			FromDockerfile: tc.FromDockerfile{
				Context: repoRoot, Dockerfile: "internal/tcpproxy/testdata/integration.Dockerfile",
			},
			Name:         proxyName,
			ExposedPorts: []string{"20000/tcp"},
			Networks:     []string{controlName},
			Env: map[string]string{
				"MIDNIGHT_DB_DSN":              "postgresql://midnight:midnight@postgres:5432/midnight?sslmode=disable",
				"MIDNIGHT_TCP_PROXY_CONTAINER": proxyName, // not-a-failure-code
				"DOCKER_HOST":                  "unix:///var/run/docker.sock",
			},
			Binds: []string{"/var/run/docker.sock:/var/run/docker.sock"},
			WaitingFor: wait.ForLog("tcp proxy integration helper started").
				WithStartupTimeout(5 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		skipDockerUnavailable(t, err)
		t.Fatalf("start TCP proxy helper: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Terminate(context.Background()) })
	proxyInspect, err := proxy.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	controlEndpoint := proxyInspect.NetworkSettings.Networks[controlName]
	if controlEndpoint == nil || !controlEndpoint.Gateway.IsValid() {
		t.Fatalf("proxy control-network gateway=%#v", controlEndpoint)
	}

	store := tcpproxy.NewPostgresStore(pool)
	store.BaseDomain = "example.test"
	store.Ports = tcpproxy.PortRange{Start: 20000, End: 20000}
	endpoint, err := store.CreateEndpoint(ctx, tcpproxy.CreateEndpointInput{
		Scope: scope, RequestedExternalPort: 20000, BackendPort: 80,
	})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	eventually(t, 20*time.Second, func() bool {
		value, lookupErr := store.GetEndpoint(ctx, endpoint.ID)
		return lookupErr == nil && value.ApplyState == tcpproxy.ApplyActive &&
			value.ListenerBound && value.BackendReachable
	}, "endpoint did not become active")
	eventually(t, 10*time.Second, func() bool {
		return containerAttached(t, ctx, proxy, environmentNetwork.Name())
	}, "proxy did not join the scoped environment network")

	proxyHost, err := proxy.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proxyPort, err := proxy.MappedPort(ctx, "20000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	publicURL := "http://" + net.JoinHostPort(proxyHost, proxyPort.Port())
	eventually(t, 10*time.Second, func() bool {
		return fetchBody(publicURL) == "backend-one"
	}, "published host port did not reach first scoped backend")
	var observedPeer string
	eventually(t, 10*time.Second, func() bool {
		observedPeer = observedPeerFromLogs(ctx, proxy)
		return observedPeer != ""
	}, "proxy did not record the Docker-observed peer")
	observedIP := net.ParseIP(observedPeer)
	if observedIP == nil || observedIP.IsLoopback() {
		t.Fatalf("Docker-observed peer=%q, want a non-loopback NAT address", observedPeer)
	}
	t.Logf(
		"Docker published host traffic arrived as peer %s (control bridge gateway %s), not 127.0.0.1",
		observedPeer,
		controlEndpoint.Gateway,
	)

	backendTwo := startHTTPBackend(t, ctx, environmentNetwork.Name(), "backend-two")
	backendTwoIP := containerNetworkIP(t, ctx, backendTwo, environmentNetwork.Name())
	redeploy(t, ctx, pool, scope, "dep_tcp_docker_one", "dep_tcp_docker_two", "inst_tcp_docker_two", backendTwoIP)
	eventually(t, 20*time.Second, func() bool {
		return fetchBody(publicURL) == "backend-two"
	}, "controller retained the stale backend after redeploy")

	if _, err := store.DisableEndpoint(ctx, endpoint.ID); err != nil {
		t.Fatalf("withdraw endpoint: %v", err)
	}
	eventually(t, 20*time.Second, func() bool {
		value, lookupErr := store.GetEndpoint(ctx, endpoint.ID)
		return lookupErr == nil && value.ApplyState == tcpproxy.ApplyWithdrawn && !value.ListenerBound
	}, "endpoint did not become withdrawn")
	eventually(t, 10*time.Second, func() bool {
		return !containerAttached(t, ctx, proxy, environmentNetwork.Name())
	}, "proxy remained attached after the final endpoint withdrawal")
	eventually(t, 10*time.Second, func() bool {
		client := http.Client{Timeout: 250 * time.Millisecond}
		response, requestErr := client.Get(publicURL)
		if requestErr != nil {
			return true
		}
		_ = response.Body.Close()
		return false
	}, "withdrawn published port remained reachable")
}

func startHTTPBackend(t *testing.T, ctx context.Context, networkName, body string) tc.Container {
	t.Helper()
	container, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
		ContainerRequest: tc.ContainerRequest{
			Image:        "nginx:1.27-alpine",
			Networks:     []string{networkName},
			ExposedPorts: []string{"80/tcp"},
			Files: []tc.ContainerFile{{
				Reader: bytes.NewBufferString(body), ContainerFilePath: "/usr/share/nginx/html/index.html", FileMode: 0o644,
			}},
			WaitingFor: wait.ForListeningPort("80/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start backend: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	return container
}

func containerNetworkIP(t *testing.T, ctx context.Context, value tc.Container, networkName string) string {
	t.Helper()
	inspect, err := value.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := inspect.NetworkSettings.Networks[networkName]
	if endpoint == nil || !endpoint.IPAddress.IsValid() {
		t.Fatalf("container network %s endpoint=%#v", networkName, endpoint)
	}
	return endpoint.IPAddress.String()
}

func containerAttached(t *testing.T, ctx context.Context, value tc.Container, networkName string) bool {
	t.Helper()
	inspect, err := value.Inspect(ctx)
	if err != nil {
		return false
	}
	return inspect.NetworkSettings != nil && inspect.NetworkSettings.Networks[networkName] != nil
}

func seedActiveDeployment(
	t *testing.T,
	ctx context.Context,
	pool *dbpg.Pool,
	scope tcpproxy.Scope,
	deploymentID string,
	instanceID string,
	ip string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action
		) VALUES ($1,$2,$3,$4,'deploy_image')`,
		deploymentID, scope.ProjectID, scope.ServiceID, scope.EnvironmentID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='deploying' WHERE id=$1`, deploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='active', active=TRUE WHERE id=$1`, deploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_environment_state (
			service_id, environment_id, active_deployment_id
		) VALUES ($1,$2,$3)`, scope.ServiceID, scope.EnvironmentID, deploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_instances (
			deployment_id, instance_id, replica_ordinal, desired_state,
			observed_state, container_id, container_ip, ready
		) VALUES ($1,$2,0,'running','running',$2,$3,TRUE)`,
		deploymentID, instanceID, ip,
	); err != nil {
		t.Fatal(err)
	}
}

func redeploy(
	t *testing.T,
	ctx context.Context,
	pool *dbpg.Pool,
	scope tcpproxy.Scope,
	oldDeployment string,
	newDeployment string,
	instanceID string,
	ip string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='superseded', active=FALSE WHERE id=$1`, oldDeployment); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action
		) VALUES ($1,$2,$3,$4,'deploy_image')`,
		newDeployment, scope.ProjectID, scope.ServiceID, scope.EnvironmentID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='deploying' WHERE id=$1`, newDeployment); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='active', active=TRUE WHERE id=$1`, newDeployment); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE service_environment_state
		   SET active_deployment_id=$1, version=version+1
		 WHERE service_id=$2 AND environment_id=$3`,
		newDeployment, scope.ServiceID, scope.EnvironmentID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_instances (
			deployment_id, instance_id, replica_ordinal, desired_state,
			observed_state, container_id, container_ip, ready
		) VALUES ($1,$2,0,'running','running',$2,$3,TRUE)`,
		newDeployment, instanceID, ip,
	); err != nil {
		t.Fatal(err)
	}
}

func fetchBody(rawURL string) string {
	client := http.Client{
		Timeout:   500 * time.Millisecond,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	response, err := client.Get(rawURL)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 128))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func observedPeerFromLogs(ctx context.Context, value tc.Container) string {
	logs, err := value.Logs(ctx)
	if err != nil {
		return ""
	}
	defer logs.Close()
	body, err := io.ReadAll(io.LimitReader(logs, 1<<20))
	if err != nil {
		return ""
	}
	const marker = "tcp proxy observed peer="
	for _, line := range strings.Split(string(body), "\n") {
		index := strings.Index(line, marker)
		if index < 0 {
			continue
		}
		address := strings.TrimSpace(line[index+len(marker):])
		host, _, err := net.SplitHostPort(address)
		if err == nil {
			return strings.Trim(host, "[]")
		}
	}
	return ""
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(message)
}

func skipDockerUnavailable(t *testing.T, err error) {
	t.Helper()
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "docker") &&
		(strings.Contains(message, "cannot connect") || strings.Contains(message, "not running") ||
			strings.Contains(message, "no such file") || strings.Contains(message, "permission denied")) {
		t.Skipf("Docker unavailable: %v", err)
	}
}
