package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/metering"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/runtime/docker"
	templatepkg "github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestParseWorkerConfigDefaultsAndFlags(t *testing.T) {
	clearWorkerEnv(t)

	cfg, err := parseWorkerConfig(nil)
	if err != nil {
		t.Fatalf("parseWorkerConfig() error = %v", err)
	}
	if cfg.WorkerID != defaultWorkerID {
		t.Fatalf("WorkerID = %q, want %q", cfg.WorkerID, defaultWorkerID)
	}
	if cfg.ReclaimInterval != defaultWorkerReclaimInterval {
		t.Fatalf("ReclaimInterval = %s, want %s", cfg.ReclaimInterval, defaultWorkerReclaimInterval)
	}
	if cfg.PollInterval != defaultWorkerPollInterval {
		t.Fatalf("PollInterval = %s, want %s", cfg.PollInterval, defaultWorkerPollInterval)
	}
	if cfg.LeaseTTL != defaultWorkerLeaseTTL {
		t.Fatalf("LeaseTTL = %s, want %s", cfg.LeaseTTL, defaultWorkerLeaseTTL)
	}
	if cfg.UsageReconcile.Interval != defaultWorkerUsageReconcileInterval || cfg.UsageReconcile.Lookback != defaultWorkerUsageReconcileLookback || cfg.UsageReconcile.Timeout != defaultWorkerUsageReconcileTimeout || cfg.UsageReconcile.StageTimeout != defaultWorkerUsageReconcileStageTimeout || cfg.UsageReconcile.WorkspaceBatch != defaultWorkerUsageReconcileWorkspaceBatch {
		t.Fatalf("usage reconciliation defaults = %#v", cfg)
	}
	if cfg.RuntimeBackend != defaultWorkerRuntimeBackend {
		t.Fatalf("RuntimeBackend = %q, want %q", cfg.RuntimeBackend, defaultWorkerRuntimeBackend)
	}
	if cfg.RouteGroup != defaultWorkerRouteGroup {
		t.Fatalf("RouteGroup = %q, want %q", cfg.RouteGroup, defaultWorkerRouteGroup)
	}
	if cfg.RoutePublicPath != defaultWorkerRoutePublicPath {
		t.Fatalf("RoutePublicPath = %q, want %q", cfg.RoutePublicPath, defaultWorkerRoutePublicPath)
	}
	if cfg.RouteTargetPort != defaultWorkerRouteTargetPort {
		t.Fatalf("RouteTargetPort = %d, want %d", cfg.RouteTargetPort, defaultWorkerRouteTargetPort)
	}
	if cfg.ACMEEnabled || cfg.ACMEEnvironment != defaultWorkerACMEEnvironment || cfg.ACMEStateDir != defaultWorkerACMEStateDir {
		t.Fatalf("ACME defaults = enabled:%t environment:%q state:%q", cfg.ACMEEnabled, cfg.ACMEEnvironment, cfg.ACMEStateDir)
	}
	if cfg.DNSEnabled || cfg.DNSApply || cfg.DNSTTL != defaultWorkerDNSTTL {
		t.Fatalf("DNS defaults = enabled:%t apply:%t ttl:%d", cfg.DNSEnabled, cfg.DNSApply, cfg.DNSTTL)
	}
	if cfg.AgentID != cfg.WorkerID || cfg.HostID != cfg.AgentID {
		t.Fatalf("AgentID/HostID = %q/%q, want derived from worker id %q", cfg.AgentID, cfg.HostID, cfg.WorkerID)
	}

	cfg, err = parseWorkerConfig([]string{"--worker-id", "worker-a", "--runtime-backend", "docker", "--docker-host", "tcp://127.0.0.1:2375", "--agent-id", "agent-a", "--host-id", "host-a", "--trusted-image", "registry.example.com/api:v1,registry.example.com/worker:v2", "--trusted-image", "registry.example.com/api:v1", "--cosign-binary", "/usr/local/bin/cosign", "--cosign-public-key", "/etc/midnight/cosign.pub", "--cosign-certificate-identity-regexp", "^https://github.com/midnight/", "--cosign-certificate-oidc-issuer", "https://token.actions.githubusercontent.com", "--cosign-fulcio-root", "/etc/midnight/fulcio.pem", "--cosign-timeout", "7s", "--route-config-dir", t.TempDir(), "--route-group", "apps", "--route-public-host", "app.example.test", "--route-public-path", "/app", "--route-public-tls", "--route-cert-resolver", "le", "--route-target-port", "9090", "--acme-enabled", "--acme-email", "ops@example.test", "--acme-state-dir", filepath.Join(t.TempDir(), "acme"), "--acme-dns-provider", "manual", "--acme-dns-resolver", "1.1.1.1:53", "--reclaim-interval", "5s", "--poll-interval", "1s", "--lease-ttl", "30s"})
	if err != nil {
		t.Fatalf("parseWorkerConfig(flags) error = %v", err)
	}
	if cfg.WorkerID != "worker-a" || cfg.ReclaimInterval != 5*time.Second || cfg.PollInterval != time.Second || cfg.LeaseTTL != 30*time.Second {
		t.Fatalf("config = %#v, want flag overrides", cfg)
	}
	if cfg.RuntimeBackend != "docker" || cfg.DockerHost != "tcp://127.0.0.1:2375" || cfg.AgentID != "agent-a" || cfg.HostID != "host-a" {
		t.Fatalf("runtime config = %#v, want docker flag overrides", cfg)
	}
	if !reflect.DeepEqual(cfg.TrustedImages, []string{"registry.example.com/api:v1", "registry.example.com/worker:v2"}) {
		t.Fatalf("TrustedImages = %#v", cfg.TrustedImages)
	}
	if cfg.CosignBinaryPath != "/usr/local/bin/cosign" || cfg.CosignPublicKey != "/etc/midnight/cosign.pub" || cfg.CosignIdentity == "" || cfg.CosignOIDCIssuer == "" || cfg.CosignFulcioRoot == "" || cfg.CosignTimeout != 7*time.Second {
		t.Fatalf("cosign config = %#v", cfg)
	}
	if cfg.RouteGroup != "apps" || cfg.RoutePublicHost != "app.example.test" || cfg.RoutePublicPath != "/app" || !cfg.RoutePublicTLS || cfg.RouteCertResolver != "le" || cfg.RouteTargetPort != 9090 {
		t.Fatalf("route config = %#v, want route flag overrides", cfg)
	}
	if !cfg.ACMEEnabled || cfg.ACMEEmail != "ops@example.test" || cfg.ACMEDNSProvider != "manual" || !reflect.DeepEqual(cfg.ACMEResolvers, []string{"1.1.1.1:53"}) {
		t.Fatalf("ACME config = %#v, want explicit flag overrides", cfg)
	}
	if cfg.RouteCertResolver != "le" {
		t.Fatalf("RouteCertResolver = %q, want explicit route resolver", cfg.RouteCertResolver)
	}
}

func TestParseWorkerConfigUsesEnvironment(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("MIDNIGHT_WORKER_ID", "worker-env")
	t.Setenv("MIDNIGHT_WORKER_RECLAIM_INTERVAL", "11s")
	t.Setenv("MIDNIGHT_WORKER_POLL_INTERVAL", "2s")
	t.Setenv("MIDNIGHT_WORKER_LEASE_TTL", "45s")
	t.Setenv("MIDNIGHT_DB_DSN_FILE", filepath.Join(t.TempDir(), "db.dsn"))
	t.Setenv("MIDNIGHT_WORKER_RUNTIME_BACKEND", "docker")
	t.Setenv("MIDNIGHT_WORKER_DOCKER_HOST", "unix:///run/docker.sock")
	t.Setenv("MIDNIGHT_AGENT_ID", "agent-env")
	t.Setenv("MIDNIGHT_WORKER_HOST_ID", "host-env")
	t.Setenv("MIDNIGHT_WORKER_ROUTE_CONFIG_DIR", t.TempDir())
	t.Setenv("MIDNIGHT_WORKER_ROUTE_GROUP", "env-group")
	t.Setenv("MIDNIGHT_WORKER_ROUTE_PUBLIC_HOST", "env.example.test")
	t.Setenv("MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH", "/env")
	t.Setenv("MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS", "true")
	t.Setenv("MIDNIGHT_WORKER_ROUTE_CERT_RESOLVER", "env-le")
	t.Setenv("MIDNIGHT_WORKER_ROUTE_TARGET_PORT", "9091")
	t.Setenv("MIDNIGHT_WORKER_TRUSTED_IMAGES", "registry.example.com/api:v1, registry.example.com/worker:v2")
	t.Setenv("MIDNIGHT_WORKER_COSIGN_BINARY_PATH", "/opt/cosign")
	t.Setenv("MIDNIGHT_WORKER_COSIGN_PUBLIC_KEY", "/etc/midnight/cosign.pub")
	t.Setenv("MIDNIGHT_WORKER_COSIGN_CERTIFICATE_IDENTITY_REGEXP", "^https://github.com/midnight/")
	t.Setenv("MIDNIGHT_WORKER_COSIGN_CERTIFICATE_OIDC_ISSUER", "https://token.actions.githubusercontent.com")
	t.Setenv("MIDNIGHT_WORKER_COSIGN_FULCIO_ROOT", "/etc/midnight/fulcio.pem")
	t.Setenv("MIDNIGHT_WORKER_COSIGN_VERIFY_TIMEOUT", "9s")

	cfg, err := parseWorkerConfig(nil)
	if err != nil {
		t.Fatalf("parseWorkerConfig() error = %v", err)
	}
	if cfg.WorkerID != "worker-env" {
		t.Fatalf("WorkerID = %q", cfg.WorkerID)
	}
	if cfg.ReclaimInterval != 11*time.Second {
		t.Fatalf("ReclaimInterval = %s", cfg.ReclaimInterval)
	}
	if cfg.PollInterval != 2*time.Second {
		t.Fatalf("PollInterval = %s", cfg.PollInterval)
	}
	if cfg.LeaseTTL != 45*time.Second {
		t.Fatalf("LeaseTTL = %s", cfg.LeaseTTL)
	}
	if cfg.PostgresDSNFile == "" {
		t.Fatal("PostgresDSNFile is empty")
	}
	if cfg.RuntimeBackend != "docker" || cfg.DockerHost != "unix:///run/docker.sock" || cfg.AgentID != "agent-env" || cfg.HostID != "host-env" {
		t.Fatalf("runtime env config = %#v", cfg)
	}
	if cfg.RouteGroup != "env-group" || cfg.RoutePublicHost != "env.example.test" || cfg.RoutePublicPath != "/env" || !cfg.RoutePublicTLS || cfg.RouteCertResolver != "env-le" || cfg.RouteTargetPort != 9091 {
		t.Fatalf("route env config = %#v", cfg)
	}
	if !reflect.DeepEqual(cfg.TrustedImages, []string{"registry.example.com/api:v1", "registry.example.com/worker:v2"}) || cfg.CosignBinaryPath != "/opt/cosign" || cfg.CosignTimeout != 9*time.Second {
		t.Fatalf("trust env config = %#v", cfg)
	}
}

type fakeWorkerUsageReconciler struct {
	requests []metering.ScheduledReconcileRequest
}

func (f *fakeWorkerUsageReconciler) ReconcileScheduled(_ context.Context, request metering.ScheduledReconcileRequest) (metering.ScheduledReconcileResult, error) {
	f.requests = append(f.requests, request)
	return metering.ScheduledReconcileResult{WorkspacesExamined: 1}, nil
}

func TestRunWorkerUsageReconcilerRunsImmediatelyAndRepeats(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake := &fakeWorkerUsageReconciler{}
	reports := 0
	runWorkerUsageReconciler(ctx, fake, workerUsageReconcileConfig{
		Interval:       5 * time.Millisecond,
		Lookback:       time.Hour,
		Timeout:        5 * time.Millisecond,
		StageTimeout:   time.Millisecond,
		WorkspaceBatch: 7,
	}, func(_ metering.ScheduledReconcileResult, err error) {
		if err != nil {
			t.Fatalf("reconciliation report error = %v", err)
		}
		reports++
		if reports == 2 {
			cancel()
		}
	})
	if len(fake.requests) != 2 || reports != 2 {
		t.Fatalf("requests=%d reports=%d, want two immediate/periodic passes", len(fake.requests), reports)
	}
	for _, request := range fake.requests {
		if request.MaxWorkspaces != 7 || request.StageTimeout != time.Millisecond || request.Until.Sub(request.Since) != time.Hour {
			t.Fatalf("request=%+v", request)
		}
	}
}

func TestParseWorkerConfigRejectsInvalidValues(t *testing.T) {
	clearWorkerEnv(t)
	if _, err := parseWorkerConfig([]string{"--worker-id", ""}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want worker id error")
	}
	if _, err := parseWorkerConfig([]string{"--reclaim-interval", "0"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want reclaim interval error")
	}
	if _, err := parseWorkerConfig([]string{"--poll-interval", "0"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want poll interval error")
	}
	if _, err := parseWorkerConfig([]string{"--lease-ttl", "0"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want lease ttl error")
	}
	if _, err := parseWorkerConfig([]string{"--usage-reconcile-interval", "0"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want usage reconciliation interval error")
	}
	if _, err := parseWorkerConfig([]string{"--usage-reconcile-timeout", "2m"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want usage reconciliation timeout error")
	}
	if _, err := parseWorkerConfig([]string{"--usage-reconcile-workspace-batch", "1001"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want usage reconciliation batch error")
	}
	if _, err := parseWorkerConfig([]string{"--runtime-backend", "bogus"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want runtime backend error")
	}
	if _, err := parseWorkerConfig([]string{"--route-config-dir", t.TempDir()}); err != nil {
		t.Fatalf("parseWorkerConfig() generated-domain route config error = %v", err)
	}
	if _, err := parseWorkerConfig([]string{"--route-config-dir", t.TempDir(), "--route-public-host", "app.example.test", "--route-public-path", "bad"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want route public path error")
	}
	if _, err := parseWorkerConfig([]string{"--route-config-dir", t.TempDir(), "--route-public-host", "app.example.test", "--route-target-port", "0"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want route target port error")
	}
	if _, err := parseWorkerConfig([]string{"--route-config-dir", t.TempDir(), "--route-public-host", "app.example.test", "--route-cert-resolver", "le"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want cert resolver tls error")
	}
	if _, err := parseWorkerConfig([]string{"--trusted-image", "registry.example.com/api:v1"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want trust requires docker error")
	}
	if _, err := parseWorkerConfig([]string{"--runtime-backend", "docker", "--cosign-timeout", "0"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want cosign timeout error")
	}
	if _, err := parseWorkerConfig([]string{"--acme-enabled"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want missing ACME email error")
	}
	if _, err := parseWorkerConfig([]string{"--acme-enabled", "--acme-email", "ops@example.test", "--acme-dns-provider", "manual", "--acme-state-dir", "relative"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want absolute ACME state directory error")
	}
	if _, err := parseWorkerConfig([]string{"--acme-enabled", "--acme-email", "ops@example.test", "--acme-dns-provider", "manual", "--acme-environment", "production"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want production acknowledgement error")
	}
	if _, err := parseWorkerConfig([]string{"--acme-enabled", "--acme-email", "ops@example.test", "--acme-dns-provider", "manual", "--acme-environment", "preview"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want unsupported ACME environment error")
	}
	if _, err := parseWorkerConfig([]string{"--dns-enabled", "--dns-zone-id", "zone", "--dns-zone-name", "example.com", "--dns-route-target", "apps.example.com"}); err == nil {
		t.Fatal("parseWorkerConfig() error = nil, want missing Cloudflare token error")
	}
}

func TestParseWorkerConfigDNSIsExplicitAndDryRunByDefault(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("CLOUDFLARE_DNS_API_TOKEN", "test-token")
	cfg, err := parseWorkerConfig([]string{
		"--dns-enabled", "--dns-zone-id", "zone-1", "--dns-zone-name", "Example.COM.",
		"--dns-route-target", "Apps.Example.com.", "--dns-ipv4", "192.0.2.10",
		"--dns-ipv6", "2001:db8::10", "--dns-ttl", "600",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DNSEnabled || cfg.DNSApply || cfg.DNSProvider != "cloudflare" || cfg.DNSZoneID != "zone-1" ||
		cfg.DNSZoneName != "example.com" || cfg.DNSRouteTarget != "apps.example.com" ||
		cfg.DNSIPv4 != "192.0.2.10" || cfg.DNSIPv6 != "2001:db8::10" || cfg.DNSTTL != 600 {
		t.Fatalf("DNS config=%#v", cfg)
	}
	if _, err := parseWorkerConfig([]string{
		"--dns-enabled", "--dns-zone-id", "zone", "--dns-zone-name", "example.com",
		"--dns-route-target", "apps.example.com", "--dns-ipv4", "2001:db8::1",
	}); err == nil {
		t.Fatal("expected IPv4 family validation")
	}
}

func TestNewWorkerDomainLifecycleControllerIsOptIn(t *testing.T) {
	controller, err := newWorkerDomainLifecycleController(workerConfig{}, nil)
	if err != nil {
		t.Fatalf("disabled lifecycle construction: %v", err)
	}
	if controller != nil {
		t.Fatal("disabled lifecycle returned a controller")
	}
}

func TestParseWorkerConfigDerivesACMECertResolver(t *testing.T) {
	clearWorkerEnv(t)
	cfg, err := parseWorkerConfig([]string{
		"--acme-enabled", "--acme-email", "ops@example.test",
		"--acme-state-dir", filepath.Join(t.TempDir(), "acme"),
		"--acme-dns-provider", "manual",
	})
	if err != nil {
		t.Fatalf("parseWorkerConfig: %v", err)
	}
	if cfg.RouteCertResolver != "letsencrypt-staging" {
		t.Fatalf("RouteCertResolver = %q, want staging resolver default", cfg.RouteCertResolver)
	}
}

func TestBuildDeployExecutorDefaultsToLocalAndNoopPromoter(t *testing.T) {
	calls := []string{}
	runtimeAdapter := &workerTestActivationRuntime{calls: &calls}
	deps := executorWiringDeps{
		newLocalRuntime: func() deploy.ActivationRuntime { return runtimeAdapter },
		newDockerEngine: func(string) (docker.EngineClient, error) {
			t.Fatal("newDockerEngine called for local backend")
			return nil, nil
		},
		newDockerRuntime: func(context.Context, docker.Config) (deploy.ActivationRuntime, error) {
			t.Fatal("newDockerRuntime called for local backend")
			return nil, nil
		},
		newRoutePromoter: func(workerConfig) (deploy.RoutePromoter, error) {
			t.Fatal("newRoutePromoter called without route config dir")
			return nil, nil
		},
	}

	executor, cleanup, err := buildDeployExecutor(context.Background(), workerConfig{RuntimeBackend: "local"}, fakeOperationUpdater{}, deps)
	if err != nil {
		t.Fatalf("buildDeployExecutor() error = %v", err)
	}
	defer cleanup()
	if executor.Runtime != runtimeAdapter {
		t.Fatalf("Runtime = %#v, want fake local runtime", executor.Runtime)
	}
	if _, ok := executor.Promoter.(deploy.NoopRoutePromoter); !ok {
		t.Fatalf("Promoter = %T, want deploy.NoopRoutePromoter", executor.Promoter)
	}
}

func TestBuildDeployExecutorWiresRoutePromoterWhenConfigured(t *testing.T) {
	cfg := workerConfig{
		RuntimeBackend:  "local",
		RouteConfigDir:  t.TempDir(),
		RoutePublicHost: "app.example.test",
		RoutePublicPath: "/",
		RouteGroup:      "apps",
		RouteTargetPort: defaultWorkerRouteTargetPort,
	}
	calls := []string{}
	runtimeAdapter := &workerTestActivationRuntime{calls: &calls}
	promoter := &workerTestRoutePromoter{calls: &calls}
	routePromoterCalled := false
	deps := executorWiringDeps{
		newLocalRuntime:  func() deploy.ActivationRuntime { return runtimeAdapter },
		newDockerEngine:  func(string) (docker.EngineClient, error) { return nil, nil },
		newDockerRuntime: func(context.Context, docker.Config) (deploy.ActivationRuntime, error) { return nil, nil },
		newRoutePromoter: func(got workerConfig) (deploy.RoutePromoter, error) {
			routePromoterCalled = true
			if got.RouteConfigDir != cfg.RouteConfigDir || got.RoutePublicHost != cfg.RoutePublicHost {
				t.Fatalf("route promoter cfg = %#v, want %#v", got, cfg)
			}
			return promoter, nil
		},
	}

	executor, cleanup, err := buildDeployExecutor(context.Background(), cfg, fakeOperationUpdater{}, deps)
	if err != nil {
		t.Fatalf("buildDeployExecutor() error = %v", err)
	}
	defer cleanup()
	if executor.Promoter != promoter || !routePromoterCalled {
		t.Fatalf("Promoter = %T called=%t, want fake promoter", executor.Promoter, routePromoterCalled)
	}
}

func TestBuildDeployExecutorWiresDockerRuntimeAndCleanup(t *testing.T) {
	cfg := workerConfig{
		RuntimeBackend: "docker",
		DockerHost:     "tcp://127.0.0.1:2375",
		AgentID:        "agent-a",
		HostID:         "host-a",
	}
	engine := &closableEngine{EngineClient: docker.NewFakeEngineForTests()}
	calls := []string{}
	runtimeAdapter := &workerTestActivationRuntime{calls: &calls}
	var gotDockerHost string
	var gotDockerConfig docker.Config
	deps := executorWiringDeps{
		newLocalRuntime: func() deploy.ActivationRuntime {
			t.Fatal("newLocalRuntime called for docker backend")
			return nil
		},
		newDockerEngine: func(host string) (docker.EngineClient, error) {
			gotDockerHost = host
			return engine, nil
		},
		newDockerRuntime: func(_ context.Context, cfg docker.Config) (deploy.ActivationRuntime, error) {
			gotDockerConfig = cfg
			return runtimeAdapter, nil
		},
		newRoutePromoter: func(workerConfig) (deploy.RoutePromoter, error) {
			t.Fatal("newRoutePromoter called without route config dir")
			return nil, nil
		},
	}

	executor, cleanup, err := buildDeployExecutor(context.Background(), cfg, fakeOperationUpdater{}, deps)
	if err != nil {
		t.Fatalf("buildDeployExecutor() error = %v", err)
	}
	if executor.Runtime != runtimeAdapter {
		t.Fatalf("Runtime = %#v, want fake docker runtime", executor.Runtime)
	}
	if gotDockerHost != cfg.DockerHost {
		t.Fatalf("docker host = %q, want %q", gotDockerHost, cfg.DockerHost)
	}
	if gotDockerConfig.AgentID != cfg.AgentID || gotDockerConfig.HostID != cfg.HostID || gotDockerConfig.Engine != engine {
		t.Fatalf("docker config = %#v, want ids and engine", gotDockerConfig)
	}
	cleanup()
	if !engine.closed {
		t.Fatal("docker engine was not closed by cleanup")
	}
}

func TestBuildDeployExecutorRecoversDockerRuntimeState(t *testing.T) {
	cfg := workerConfig{
		RuntimeBackend: "docker",
		DockerHost:     "tcp://127.0.0.1:2375",
		AgentID:        "agent-a",
		HostID:         "host-a",
	}
	engine := &closableEngine{EngineClient: docker.NewFakeEngineForTests()}
	calls := []string{}
	runtimeAdapter := &workerRecoverableActivationRuntime{workerTestActivationRuntime: workerTestActivationRuntime{calls: &calls}}
	deps := executorWiringDeps{
		newLocalRuntime: func() deploy.ActivationRuntime {
			t.Fatal("newLocalRuntime called for docker backend")
			return nil
		},
		newDockerEngine: func(host string) (docker.EngineClient, error) {
			return engine, nil
		},
		newDockerRuntime: func(context.Context, docker.Config) (deploy.ActivationRuntime, error) {
			return runtimeAdapter, nil
		},
		newRoutePromoter: func(workerConfig) (deploy.RoutePromoter, error) {
			return deploy.NoopRoutePromoter{}, nil
		},
	}

	executor, cleanup, err := buildDeployExecutor(context.Background(), cfg, fakeOperationUpdater{}, deps)
	if err != nil {
		t.Fatalf("buildDeployExecutor() error = %v", err)
	}
	defer cleanup()
	if executor.Runtime != runtimeAdapter {
		t.Fatalf("Runtime = %#v, want recoverable runtime", executor.Runtime)
	}
	if runtimeAdapter.reconcileCalls != 1 {
		t.Fatalf("reconcile calls = %d, want startup recovery reconcile", runtimeAdapter.reconcileCalls)
	}
}

func TestBuildDeployExecutorInjectsStaticTrustedImages(t *testing.T) {
	image := workerTestImageRef()
	cfg := workerConfig{
		RuntimeBackend: "docker",
		DockerHost:     "tcp://127.0.0.1:2375",
		AgentID:        "agent-a",
		HostID:         "host-a",
		TrustedImages:  []string{image},
	}
	engine := &closableEngine{EngineClient: docker.NewFakeEngineForTests()}
	calls := []string{}
	runtimeAdapter := &workerTestActivationRuntime{calls: &calls}
	deps := executorWiringDeps{
		newLocalRuntime: func() deploy.ActivationRuntime {
			t.Fatal("newLocalRuntime called for docker backend")
			return nil
		},
		newDockerEngine: func(host string) (docker.EngineClient, error) {
			return engine, nil
		},
		newDockerRuntime: func(_ context.Context, cfg docker.Config) (deploy.ActivationRuntime, error) {
			return runtimeAdapter, nil
		},
		newRoutePromoter: func(workerConfig) (deploy.RoutePromoter, error) {
			return deploy.NoopRoutePromoter{}, nil
		},
	}

	executor, cleanup, err := buildDeployExecutor(context.Background(), cfg, fakeOperationUpdater{}, deps)
	if err != nil {
		t.Fatalf("buildDeployExecutor() error = %v", err)
	}
	defer cleanup()
	if executor.Runtime != runtimeAdapter {
		t.Fatalf("Runtime = %#v, want fake docker runtime", executor.Runtime)
	}
	if runtimeAdapter.verifier == nil {
		t.Fatal("signature verifier was not injected")
	}
	if err := runtimeAdapter.verifier.Verify(context.Background(), image); err != nil {
		t.Fatalf("Verify(trusted image) error = %v", err)
	}
	if err := runtimeAdapter.verifier.Verify(context.Background(), "registry.example.com/other:v1"); err == nil {
		t.Fatal("Verify(untrusted image) error = nil, want rejection")
	}
}

func TestSignatureVerifierFromConfigComposesLocalRegistryAndOfficialDatabases(t *testing.T) {
	official := make([]string, 0, len(templatepkg.Builtins()))
	for _, tmpl := range templatepkg.Builtins() {
		if len(tmpl.Services) != 1 {
			t.Fatalf("%s services=%d, want 1", tmpl.Slug, len(tmpl.Services))
		}
		official = append(official, tmpl.Services[0].Image)
	}
	verifier := signatureVerifierFromConfig(workerConfig{
		LocalRegistry:      "registry:5000",
		TrustLocalRegistry: true,
		TrustedImages:      official,
	})
	if _, ok := verifier.(docker.AnySignatureVerifier); !ok {
		t.Fatalf("verifier=%T, want docker.AnySignatureVerifier", verifier)
	}
	for _, image := range append(append([]string(nil), official...),
		"registry:5000/project/api@sha256:"+strings.Repeat("a", 64)) {
		if err := verifier.Verify(context.Background(), image); err != nil {
			t.Fatalf("Verify(%q): %v", image, err)
		}
	}
	for _, image := range []string{
		"postgres:16.14",
		"redis:7@sha256:" + strings.Repeat("b", 64),
		"registry:5000/project/api:latest",
	} {
		if err := verifier.Verify(context.Background(), image); err == nil {
			t.Fatalf("Verify(%q) unexpectedly succeeded", image)
		}
	}
}

func TestStockComposeTrustsCurrentOfficialDatabaseDigests(t *testing.T) {
	compose, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.local.yml"))
	if err != nil {
		t.Fatal(err)
	}
	images := make([]string, 0, len(templatepkg.Builtins()))
	for _, tmpl := range templatepkg.Builtins() {
		if len(tmpl.Services) != 1 {
			t.Fatalf("%s services=%d, want 1", tmpl.Slug, len(tmpl.Services))
		}
		images = append(images, tmpl.Services[0].Image)
	}
	want := "MIDNIGHT_WORKER_TRUSTED_IMAGES: ${MIDNIGHT_WORKER_TRUSTED_IMAGES:-" + strings.Join(images, ",") + "}"
	if !strings.Contains(string(compose), want) {
		t.Fatalf("stock Compose trust policy missing exact official image set %q", images)
	}
}

func TestBuildDeployExecutorInjectsCosignVerifier(t *testing.T) {
	cfg := workerConfig{
		RuntimeBackend:   "docker",
		DockerHost:       "tcp://127.0.0.1:2375",
		AgentID:          "agent-a",
		HostID:           "host-a",
		VerifySignatures: true,
		CosignBinaryPath: "/usr/local/bin/cosign",
		CosignPublicKey:  "/etc/midnight/cosign.pub",
		CosignTimeout:    5 * time.Second,
	}
	engine := &closableEngine{EngineClient: docker.NewFakeEngineForTests()}
	calls := []string{}
	runtimeAdapter := &workerTestActivationRuntime{calls: &calls}
	deps := executorWiringDeps{
		newLocalRuntime: func() deploy.ActivationRuntime {
			t.Fatal("newLocalRuntime called for docker backend")
			return nil
		},
		newDockerEngine: func(host string) (docker.EngineClient, error) {
			return engine, nil
		},
		newDockerRuntime: func(_ context.Context, cfg docker.Config) (deploy.ActivationRuntime, error) {
			return runtimeAdapter, nil
		},
		newRoutePromoter: func(workerConfig) (deploy.RoutePromoter, error) {
			return deploy.NoopRoutePromoter{}, nil
		},
	}

	_, cleanup, err := buildDeployExecutor(context.Background(), cfg, fakeOperationUpdater{}, deps)
	if err != nil {
		t.Fatalf("buildDeployExecutor() error = %v", err)
	}
	defer cleanup()
	if _, ok := runtimeAdapter.verifier.(docker.CosignExecVerifier); !ok {
		t.Fatalf("verifier = %T, want docker.CosignExecVerifier", runtimeAdapter.verifier)
	}
}

func TestWorkerRoutePromoterPublishesTraefikRoute(t *testing.T) {
	cfg := workerConfig{
		RouteConfigDir:  t.TempDir(),
		RouteGroup:      "apps",
		RoutePublicHost: "app.example.test",
		RoutePublicPath: "/",
		RouteTargetPort: 8080,
	}
	promoter, err := newWorkerRoutePromoter(cfg)
	if err != nil {
		t.Fatalf("newWorkerRoutePromoter() error = %v", err)
	}
	if err := promoter.Promote(context.Background(), runtime.RoutingTarget{
		DeploymentID: "dep-a",
		InstanceID:   "dep-a-r0",
		ContainerIP:  "10.0.0.8",
		Ready:        true,
	}, deploy.RoutePromotionMetadata{Public: true}); err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	// Routes publish as one flat artifact per route directly in the watched
	// root, prefixed by the route group so a second service cannot clobber the
	// first. They must not land in a subdirectory: Traefik's file provider
	// reads only the files directly inside providers.file.directory.
	matches, err := filepath.Glob(filepath.Join(cfg.RouteConfigDir, "apps.*.yaml"))
	if err != nil {
		t.Fatalf("glob published routes: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one published route file in group dir, got %d: %v", len(matches), matches)
	}
	got, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read published route: %v", err)
	}
	rendered := string(got)
	for _, want := range []string{"Host(`app.example.test`)", "PathPrefix(`/`)", "http://10.0.0.8:8080"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("published route missing %q:\n%s", want, rendered)
		}
	}
}

var errDeployExecution = errors.New("deploy execution failed")

func TestProcessLeasedDeployJobCompletesOnSuccess(t *testing.T) {
	store := &workerQueueStore{}
	job := queue.Job{ID: "job-1", Queue: queue.Deploy, LeaseID: "worker-1"}

	outcome, err := processLeasedDeployJob(context.Background(), store, job, time.Minute, fakeDeployExecutor{outcome: deploy.ExecutionOutcome{Succeeded: true, OperationID: "op-1"}}, nil)
	if err != nil {
		t.Fatalf("processLeasedDeployJob() error = %v", err)
	}
	if !outcome.Succeeded || store.completedJobID != job.ID || store.completedLeaseID != job.LeaseID {
		t.Fatalf("outcome=%#v store=%#v, want completed job", outcome, store)
	}
	if store.failedJobID != "" {
		t.Fatalf("failed job id = %q, want empty", store.failedJobID)
	}
}

func TestProcessLeasedDeployJobFailsLeaseOnExecutorError(t *testing.T) {
	store := &workerQueueStore{}
	job := queue.Job{ID: "job-1", Queue: queue.Deploy, LeaseID: "worker-1"}
	executor := fakeDeployExecutor{
		outcome: deploy.ExecutionOutcome{OperationID: "op-1", FailureCode: failurecodes.ResourceValidationFailed},
		err:     errDeployExecution,
	}

	outcome, err := processLeasedDeployJob(context.Background(), store, job, time.Minute, executor, nil)
	if !errors.Is(err, errDeployExecution) {
		t.Fatalf("processLeasedDeployJob() error = %v, want executor error", err)
	}
	if outcome.FailureCode != failurecodes.ResourceValidationFailed {
		t.Fatalf("outcome = %#v, want validation failure", outcome)
	}
	if store.failedJobID != job.ID || store.failedLeaseID != job.LeaseID || store.failReason != string(failurecodes.ResourceValidationFailed) {
		t.Fatalf("store=%#v, want failed lease with reason", store)
	}
	if store.completedJobID != "" {
		t.Fatalf("completed job id = %q, want empty", store.completedJobID)
	}
}

func TestProcessLeasedDeployJobHeartbeatsDuringLongExecutor(t *testing.T) {
	store := &workerQueueStore{heartbeatCh: make(chan time.Time, 4)}
	job := queue.Job{ID: "job-1", Queue: queue.Deploy, LeaseID: "worker-1"}
	executor := blockingDeployExecutor{
		execute: func(ctx context.Context, job queue.Job) (deploy.ExecutionOutcome, error) {
			select {
			case <-store.heartbeatCh:
				return deploy.ExecutionOutcome{Succeeded: true, OperationID: "op-1"}, nil
			case <-ctx.Done():
				return deploy.ExecutionOutcome{OperationID: "op-1"}, ctx.Err()
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for lease heartbeat")
				return deploy.ExecutionOutcome{}, nil
			}
		},
	}

	outcome, err := processLeasedDeployJob(context.Background(), store, job, 30*time.Millisecond, executor, nil)
	if err != nil {
		t.Fatalf("processLeasedDeployJob() error = %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("outcome = %#v, want success", outcome)
	}
	if count := store.heartbeatCount(); count == 0 {
		t.Fatal("heartbeat count = 0, want at least one heartbeat during execution")
	}
	if store.completedJobID != job.ID {
		t.Fatalf("completed job id = %q, want %q", store.completedJobID, job.ID)
	}
}

func TestProcessLeasedDeployJobStopsHeartbeatAfterCompletion(t *testing.T) {
	store := &workerQueueStore{heartbeatCh: make(chan time.Time, 8)}
	job := queue.Job{ID: "job-1", Queue: queue.Deploy, LeaseID: "worker-1"}
	executor := blockingDeployExecutor{
		execute: func(ctx context.Context, job queue.Job) (deploy.ExecutionOutcome, error) {
			select {
			case <-store.heartbeatCh:
				return deploy.ExecutionOutcome{Succeeded: true, OperationID: "op-1"}, nil
			case <-ctx.Done():
				return deploy.ExecutionOutcome{OperationID: "op-1"}, ctx.Err()
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for lease heartbeat")
				return deploy.ExecutionOutcome{}, nil
			}
		},
	}

	if _, err := processLeasedDeployJob(context.Background(), store, job, 30*time.Millisecond, executor, nil); err != nil {
		t.Fatalf("processLeasedDeployJob() error = %v", err)
	}
	countAfterCompletion := store.heartbeatCount()
	time.Sleep(50 * time.Millisecond)
	if got := store.heartbeatCount(); got != countAfterCompletion {
		t.Fatalf("heartbeat count after completion = %d, want %d", got, countAfterCompletion)
	}
}

func TestWorkerDeployExecutorAppendsStepOperationEvents(t *testing.T) {
	ctx := context.Background()
	operationStore := api.NewMemoryOperationStore()
	if _, err := operationStore.Create(ctx, orailtypes.Operation{ID: "op-1", Kind: "deploy", State: orailtypes.OperationQueued}); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	calls := []string{}
	base := deploy.Executor{
		Runtime: &workerTestActivationRuntime{
			calls:  &calls,
			pull:   runtime.PullVerifyResult{Image: workerTestImageRef(), Verified: true, Trusted: true},
			target: runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", Ready: true},
		},
		Promoter:   &workerTestRoutePromoter{calls: &calls},
		Operations: workerOperationUpdater{store: operationStore},
		Observer:   workerOperationEventObserver{store: operationStore},
		DrainGrace: time.Millisecond,
	}

	if _, err := processLeasedDeployJob(ctx, &workerQueueStore{}, workerTestDeployJob(t), time.Minute, base, nil); err != nil {
		t.Fatalf("processLeasedDeployJob() error = %v", err)
	}
	events, err := operationStore.ListEvents(ctx, "op-1")
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	messages := make([]string, 0, len(events))
	for _, event := range events {
		messages = append(messages, event.Message)
	}
	for _, want := range []string{
		"deploy worker step pull-verify started",
		"deploy worker step pull-verify succeeded",
		"deploy worker step schedule started",
		"deploy worker step readiness-routing-check succeeded",
		"deploy worker step promote succeeded",
		"deploy worker step drain-previous succeeded",
	} {
		if !containsMessageWithPrefix(messages, want) {
			t.Fatalf("operation event messages = %#v, want %q", messages, want)
		}
	}
	if !containsMessageWithPrefix(messages, "deploy worker step promote succeeded job_id=job-1 deployment_id=dep-2 instance_id=dep-2-r0") {
		t.Fatalf("operation event messages = %#v, want durable deploy identity on promote", messages)
	}
}

func TestPrivateDeployCommitsCanonicalRecordBeforePreviousDrain(t *testing.T) {
	ctx := context.Background()
	canonical := api.NewMemoryDeploymentStore(nil)
	if _, err := canonical.CreateDeployment(ctx, api.DeploymentRecord{
		ID: "dep-2", ProjectID: "prj-1", ServiceID: "api", EnvironmentID: "prod",
		Action: deploy.ActionDeployImage, OperationID: "op-1", ImageRef: workerTestImageRef(),
	}); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	executor := deploy.Executor{
		Runtime: &workerTestActivationRuntime{
			calls: &calls, pull: runtime.PullVerifyResult{Image: workerTestImageRef(), Verified: true, Trusted: true},
			target: runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", Ready: true},
		},
		Promoter:           &workerTestRoutePromoter{calls: &calls},
		Operations:         &workerTestOperationUpdater{},
		CanonicalCommitter: workerCanonicalCommitter{store: canonical},
		DrainGrace:         time.Millisecond,
	}
	if _, err := processLeasedDeployJob(ctx, &workerQueueStore{}, workerTestDeployJob(t), time.Minute, executor, canonical); err != nil {
		t.Fatalf("processLeasedDeployJob() error = %v", err)
	}
	record, err := canonical.GetDeploymentByOperation(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if record.State != deploy.DeploymentActive || !record.Active {
		t.Fatalf("canonical deployment = %#v, want active", record)
	}
	if len(calls) == 0 || calls[len(calls)-1] != "drain" {
		t.Fatalf("calls = %#v, want canonical activation completed before prior drain", calls)
	}
}

func TestWorkerCanonicalCommitterRecoversOnlyIndeterminateExactPromotion(t *testing.T) {
	for _, test := range []struct {
		name       string
		promoteErr error
		proved     bool
		wantErr    bool
		wantProofs int
	}{
		{
			name:       "commit applied acknowledgement lost",
			promoteErr: fmt.Errorf("%w: connection lost", pg.ErrTxCommitIndeterminate),
			proved:     true,
			wantProofs: 1,
		},
		{
			name:       "ambiguous but canonical proof absent",
			promoteErr: fmt.Errorf("%w: connection lost", pg.ErrTxCommitIndeterminate),
			wantErr:    true,
			wantProofs: 1,
		},
		{
			name:       "definite failure is never recovered",
			promoteErr: errors.New("promotion CAS rejected"),
			proved:     true,
			wantErr:    true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := deploymentPromotionRecoveryFixture(t)
			store.promoteErr = test.promoteErr
			store.deploymentProved = test.proved
			err := (workerCanonicalCommitter{store: store}).
				CommitCanonicalDeployment(context.Background(), "op-recovery")
			if test.wantErr != (err != nil) {
				t.Fatalf("CommitCanonicalDeployment() error=%v, wantErr=%v", err, test.wantErr)
			}
			if store.deploymentProofCalls != test.wantProofs {
				t.Fatalf(
					"deployment proof calls=%d, want %d",
					store.deploymentProofCalls,
					test.wantProofs,
				)
			}
		})
	}
}

func TestWorkerCanonicalTemplateCommitterRecoversOnlyExactAtomicProof(t *testing.T) {
	for _, test := range []struct {
		name       string
		promoteErr error
		proved     bool
		wantErr    bool
		wantProofs int
	}{
		{
			name:       "template commit applied acknowledgement lost",
			promoteErr: fmt.Errorf("%w: connection lost", pg.ErrTxCommitIndeterminate),
			proved:     true,
			wantProofs: 1,
		},
		{
			name:       "template proof absent",
			promoteErr: fmt.Errorf("%w: connection lost", pg.ErrTxCommitIndeterminate),
			wantErr:    true,
			wantProofs: 1,
		},
		{
			name:       "template definite failure",
			promoteErr: errors.New("template evidence transaction rejected"),
			proved:     true,
			wantErr:    true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := deploymentPromotionRecoveryFixture(t)
			store.templatePromoteErr = test.promoteErr
			store.templateProved = test.proved
			err := (workerCanonicalCommitter{store: store}).
				CommitCanonicalTemplateDeployment(
					context.Background(),
					"op-recovery",
					"dep-recovery",
				)
			if test.wantErr != (err != nil) {
				t.Fatalf(
					"CommitCanonicalTemplateDeployment() error=%v, wantErr=%v",
					err,
					test.wantErr,
				)
			}
			if store.templateProofCalls != test.wantProofs {
				t.Fatalf(
					"template proof calls=%d, want %d",
					store.templateProofCalls,
					test.wantProofs,
				)
			}
		})
	}
}

func TestWorkerCanonicalCommitterAcceptsOnlyProvedActiveRetry(t *testing.T) {
	store := deploymentPromotionRecoveryFixture(t)
	if err := store.DeploymentStore.PromoteDeployment(
		context.Background(),
		"dep-recovery",
		"",
	); err != nil {
		t.Fatal(err)
	}
	store.deploymentProved = true
	if err := (workerCanonicalCommitter{store: store}).
		CommitCanonicalDeployment(context.Background(), "op-recovery"); err != nil {
		t.Fatalf("proved active retry error=%v", err)
	}
	if store.deploymentProofCalls != 1 {
		t.Fatalf("active retry proof calls=%d, want 1", store.deploymentProofCalls)
	}
}

func TestWorkerDeployExecutorInjectsVariablesWithoutPersistingPlaintext(t *testing.T) {
	ctx := context.Background()
	operationStore := api.NewMemoryOperationStore()
	if _, err := operationStore.Create(ctx, orailtypes.Operation{ID: "op-1", Kind: "deploy", State: orailtypes.OperationQueued}); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	const secretValue = "postgres://worker:runtime-only-secret@db/app"
	calls := []string{}
	runtimeAdapter := &workerTestActivationRuntime{
		calls:  &calls,
		pull:   runtime.PullVerifyResult{Image: workerTestImageRef(), Verified: true, Trusted: true},
		target: runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", Ready: true},
	}
	executor := deploy.Executor{
		Runtime:    runtimeAdapter,
		Promoter:   &workerTestRoutePromoter{calls: &calls},
		Operations: workerOperationUpdater{store: operationStore},
		Observer:   workerOperationEventObserver{store: operationStore},
		Variables: workerTestVariableResolver{values: map[string]string{
			"DATABASE_URL": secretValue,
		}},
		DrainGrace: time.Millisecond,
	}
	job := workerTestDeployJob(t)
	if strings.Contains(string(job.Payload), secretValue) {
		t.Fatal("deploy queue payload contains variable plaintext before execution")
	}

	if _, err := processLeasedDeployJob(ctx, &workerQueueStore{}, job, time.Minute, executor, nil); err != nil {
		t.Fatalf("processLeasedDeployJob() error = %v", err)
	}
	if !containsString(runtimeAdapter.scheduled.Env, "DATABASE_URL="+secretValue) {
		t.Fatalf("scheduled env = %#v, want resolved DATABASE_URL", runtimeAdapter.scheduled.Env)
	}
	events, err := operationStore.ListEvents(ctx, "op-1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, event := range events {
		if strings.Contains(event.Message, secretValue) || strings.Contains(event.Message, "runtime-only-secret") {
			t.Fatalf("operation event leaked variable plaintext: %#v", event)
		}
	}
}

func TestWorkerDeployExecutorStopsCanceledDeployBeforePromotion(t *testing.T) {
	ctx := context.Background()
	operationStore := api.NewMemoryOperationStore()
	if _, err := operationStore.Create(ctx, orailtypes.Operation{ID: "op-1", Kind: "deploy", State: orailtypes.OperationQueued}); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	if _, err := operationStore.AppendEvent(ctx, api.OperationEvent{
		OperationID: "op-1",
		State:       orailtypes.OperationQueued,
		Message:     "cancel requested: deploy job already leased; worker will stop only at safe point before traffic promotion job_id=job-1",
	}); err != nil {
		t.Fatalf("append cancel event: %v", err)
	}
	calls := []string{}
	updater := workerOperationUpdater{store: operationStore}
	executor := deploy.Executor{
		Runtime: &workerTestActivationRuntime{
			calls:  &calls,
			pull:   runtime.PullVerifyResult{Image: workerTestImageRef(), Verified: true, Trusted: true},
			target: runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", Ready: true},
		},
		Promoter:   &workerTestRoutePromoter{calls: &calls},
		Operations: updater,
		Cancels:    updater,
		Observer:   workerOperationEventObserver{store: operationStore},
		DrainGrace: time.Millisecond,
	}
	store := &workerQueueStore{}

	outcome, err := processLeasedDeployJob(ctx, store, workerTestDeployJob(t), time.Minute, executor, nil)
	if err != nil {
		t.Fatalf("processLeasedDeployJob() error = %v", err)
	}
	if outcome.Succeeded || outcome.FailureCode != failurecodes.DeployCanceledBeforePromotion {
		t.Fatalf("outcome = %#v, want canceled before promotion", outcome)
	}
	if store.completedJobID != "job-1" || store.failedJobID != "" {
		t.Fatalf("queue store = %#v, want completed canceled deploy job without failure", store)
	}
	wantCalls := []string{"pull-verify", "schedule", "start", "resolve-routing", "drain"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want candidate cleanup without promote", calls)
	}
	operation, err := operationStore.Get(ctx, "op-1")
	if err != nil {
		t.Fatalf("get operation: %v", err)
	}
	if operation.State != orailtypes.OperationCanceled {
		t.Fatalf("operation state = %q, want CANCELED", operation.State)
	}
}

type workerCanonicalCancelStore struct {
	api.DeploymentStore
	operations api.OperationStore
	operation  orailtypes.Operation
	called     bool
}

func (s *workerCanonicalCancelStore) CancelDeploymentOperation(
	ctx context.Context,
	operationID orailtypes.ID,
) (orailtypes.Operation, error) {
	s.called = true
	operation, err := s.operations.Transition(
		ctx,
		operationID,
		orailtypes.OperationCanceled,
	)
	if err != nil {
		return orailtypes.Operation{}, err
	}
	s.operation = operation
	return operation, nil
}

func TestWorkerOperationUpdaterUsesCanonicalCancelCommit(t *testing.T) {
	ctx := context.Background()
	operations := api.NewMemoryOperationStore()
	if _, err := operations.Create(ctx, orailtypes.Operation{
		ID:    "op-canonical-cancel",
		Kind:  "deploy",
		State: orailtypes.OperationRunning,
	}); err != nil {
		t.Fatal(err)
	}
	deployments := &workerCanonicalCancelStore{
		DeploymentStore: api.NewMemoryDeploymentStore(nil),
		operations:      operations,
		operation: orailtypes.Operation{
			Kind: "deploy",
		},
	}
	updater := workerOperationUpdater{
		store:       operations,
		deployments: deployments,
	}

	operation, err := updater.Transition(
		ctx,
		"op-canonical-cancel",
		orailtypes.OperationCanceled,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !deployments.called ||
		operation.ID != "op-canonical-cancel" ||
		operation.State != orailtypes.OperationCanceled {
		t.Fatalf(
			"coordinated=%t operation=%#v",
			deployments.called,
			operation,
		)
	}
	events, err := operations.ListEvents(ctx, "op-canonical-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 ||
		!strings.Contains(
			events[0].Message,
			"deploy worker transitioned operation to CANCELED",
		) {
		t.Fatalf("events = %#v", events)
	}
}

func TestProcessLeasedDeployJobFailsQueueWithRegisteredReasonOnPromoteFailure(t *testing.T) {
	calls := []string{}
	promoteErr := errors.New("route promoter rejected candidate")
	store := &workerQueueStore{}
	ops := &workerTestOperationUpdater{}
	executor := deploy.Executor{
		Runtime: &workerTestActivationRuntime{
			calls:  &calls,
			pull:   runtime.PullVerifyResult{Image: workerTestImageRef(), Verified: true, Trusted: true},
			target: runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", ContainerIP: "127.0.0.1", Ready: true},
		},
		Promoter:   &workerTestRoutePromoter{calls: &calls, err: promoteErr},
		Operations: ops,
		DrainGrace: 10 * time.Second,
	}
	job := workerTestPublicDeployJob(t)

	outcome, err := processLeasedDeployJob(context.Background(), store, job, time.Minute, executor, nil)
	if !errors.Is(err, promoteErr) {
		t.Fatalf("processLeasedDeployJob() error = %v, want promote error", err)
	}
	wantCalls := []string{"pull-verify", "schedule", "start", "resolve-routing", "promote", "drain"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	if store.failedJobID != job.ID || store.failedLeaseID != job.LeaseID {
		t.Fatalf("store=%#v, want failed leased job", store)
	}
	if store.failReason != string(failurecodes.ReconcileStepFailed) {
		t.Fatalf("queue fail reason = %q, want %q", store.failReason, failurecodes.ReconcileStepFailed)
	}
	if !failurecodes.IsRegistered(failurecodes.Code(store.failReason)) {
		t.Fatalf("queue fail reason = %q, want registered failure code", store.failReason)
	}
	if store.completedJobID != "" {
		t.Fatalf("completed job id = %q, want empty", store.completedJobID)
	}
	if !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.ReconcileStepFailed) {
		t.Fatalf("ops=%#v, want failed reconcile operation", ops)
	}
	if !strings.Contains(ops.failReason, "promote failed: route promoter rejected candidate") || outcome.FailureReason != ops.failReason {
		t.Fatalf("failure reason outcome=%q ops=%q, want promote failure", outcome.FailureReason, ops.failReason)
	}
}

func TestProcessOneDeployJobLeasesAndProcessesDeployQueue(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	store := &workerQueueStore{leasedJob: queue.Job{ID: "job-1", Queue: queue.Deploy, LeaseID: "worker-1"}}

	processed, err := processOneDeployJob(context.Background(), store, "worker-1", time.Minute, now, fakeDeployExecutor{
		outcome: deploy.ExecutionOutcome{Succeeded: true, OperationID: "op-1"},
	}, nil)
	if err != nil {
		t.Fatalf("processOneDeployJob() error = %v", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if store.leaseRequest.Queue != queue.Deploy || store.leaseRequest.WorkerID != "worker-1" || store.leaseRequest.LeaseTime != time.Minute || !store.leaseRequest.Now.Equal(now) {
		t.Fatalf("lease request = %#v", store.leaseRequest)
	}
	if store.completedJobID != "job-1" {
		t.Fatalf("completed job = %q, want job-1", store.completedJobID)
	}
}

func TestProcessOneDeployJobRunsDeployExecutorAndCompletesOperation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	store := &workerQueueStore{leasedJob: workerTestDeployJob(t)}
	operationStore := api.NewMemoryOperationStore()
	if _, err := operationStore.Create(ctx, orailtypes.Operation{ID: "op-1", Kind: "deploy", State: orailtypes.OperationQueued}); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	calls := []string{}
	executor := deploy.Executor{
		Runtime: &workerTestActivationRuntime{
			calls:  &calls,
			pull:   runtime.PullVerifyResult{Image: workerTestImageRef(), Verified: true, Trusted: true},
			target: runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", Ready: true},
		},
		Promoter:   &workerTestRoutePromoter{calls: &calls},
		Operations: workerOperationUpdater{store: operationStore},
		Observer:   workerOperationEventObserver{store: operationStore},
		DrainGrace: time.Millisecond,
	}

	processed, err := processOneDeployJob(ctx, store, "worker-1", time.Minute, now, executor, nil)
	if err != nil {
		t.Fatalf("processOneDeployJob() error = %v", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if store.completedJobID != "job-1" || store.completedLeaseID != "worker-1" {
		t.Fatalf("store=%#v, want completed leased deploy job", store)
	}
	if store.failedJobID != "" {
		t.Fatalf("failed job id = %q, want empty", store.failedJobID)
	}
	operation, err := operationStore.Get(ctx, "op-1")
	if err != nil {
		t.Fatalf("Get operation: %v", err)
	}
	if operation.State != orailtypes.OperationSucceeded {
		t.Fatalf("operation state = %s, want %s", operation.State, orailtypes.OperationSucceeded)
	}
	messages := workerOperationMessages(t, operationStore, "op-1")
	for _, want := range []string{
		"deploy worker transitioned operation to RUNNING",
		"deploy worker transitioned operation to SUCCEEDED",
	} {
		if !containsString(messages, want) {
			t.Fatalf("operation event messages = %#v, want %q", messages, want)
		}
	}
}

func TestDashboardTemplateDeployFailsClosedWithoutQueueingSyntheticWork(t *testing.T) {
	t.Setenv("MIDNIGHT_EXPERIMENTAL", "1")
	queueStore := &workerAcceptanceQueueStore{jobID: "job_dashboard_template_1"}
	router := api.NewRouterWithDataStack(api.DataStack{QueueStore: queueStore})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/dashboard/template-deploy", strings.NewReader(`{"template_id":"postgres-16","service_name":"Postgres 16"}`)))
	if rr.Code != http.StatusNotImplemented || !strings.Contains(rr.Body.String(), `"state":"unavailable"`) {
		t.Fatalf("template deploy status = %d body=%s, want typed unavailable", rr.Code, rr.Body.String())
	}
	if len(queueStore.enqueued) != 0 {
		t.Fatalf("template deploy queued synthetic work: %#v", queueStore.enqueued)
	}
}

func TestProcessOneDeployJobFailsLeaseAndOperationOnDeployError(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	store := &workerQueueStore{leasedJob: workerTestPublicDeployJob(t)}
	operationStore := api.NewMemoryOperationStore()
	if _, err := operationStore.Create(ctx, orailtypes.Operation{ID: "op-1", Kind: "deploy", State: orailtypes.OperationQueued}); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	calls := []string{}
	promoteErr := errors.New("route promoter rejected candidate")
	executor := deploy.Executor{
		Runtime: &workerTestActivationRuntime{
			calls:  &calls,
			pull:   runtime.PullVerifyResult{Image: workerTestImageRef(), Verified: true, Trusted: true},
			target: runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", ContainerIP: "127.0.0.1", Ready: true},
		},
		Promoter:   &workerTestRoutePromoter{calls: &calls, err: promoteErr},
		Operations: workerOperationUpdater{store: operationStore},
		Observer:   workerOperationEventObserver{store: operationStore},
		DrainGrace: time.Millisecond,
	}

	processed, err := processOneDeployJob(ctx, store, "worker-1", time.Minute, now, executor, nil)
	if !errors.Is(err, promoteErr) {
		t.Fatalf("processOneDeployJob() error = %v, want promote error", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if store.failedJobID != "job-1" || store.failedLeaseID != "worker-1" {
		t.Fatalf("store=%#v, want failed leased deploy job", store)
	}
	if store.failReason != string(failurecodes.ReconcileStepFailed) {
		t.Fatalf("fail reason = %q, want %q", store.failReason, failurecodes.ReconcileStepFailed)
	}
	if store.completedJobID != "" {
		t.Fatalf("completed job id = %q, want empty", store.completedJobID)
	}
	operation, err := operationStore.Get(ctx, "op-1")
	if err != nil {
		t.Fatalf("Get operation: %v", err)
	}
	if operation.State != orailtypes.OperationFailed || operation.Code != orailtypes.FailureCode(failurecodes.ReconcileStepFailed) {
		t.Fatalf("operation = %#v, want failed reconcile operation", operation)
	}
	messages := workerOperationMessages(t, operationStore, "op-1")
	if !containsString(messages, "deploy worker transitioned operation to RUNNING") {
		t.Fatalf("operation event messages = %#v, want running transition", messages)
	}
	if !containsMessageWithPrefix(messages, "deploy worker failed operation: promote failed: route promoter rejected candidate") {
		t.Fatalf("operation event messages = %#v, want terminal failure reason", messages)
	}
}

func TestProcessOneDeployJobIgnoresNoWork(t *testing.T) {
	store := &workerQueueStore{leaseErr: queue.ErrNoWork}

	processed, err := processOneDeployJob(context.Background(), store, "worker-1", time.Minute, time.Now().UTC(), fakeDeployExecutor{}, nil)
	if err != nil {
		t.Fatalf("processOneDeployJob() error = %v", err)
	}
	if processed {
		t.Fatal("processed = true, want false")
	}
}

type fakeDeployExecutor struct {
	outcome deploy.ExecutionOutcome
	err     error
}

func (e fakeDeployExecutor) Execute(context.Context, queue.Job) (deploy.ExecutionOutcome, error) {
	return e.outcome, e.err
}

type blockingDeployExecutor struct {
	execute func(context.Context, queue.Job) (deploy.ExecutionOutcome, error)
}

func (e blockingDeployExecutor) Execute(ctx context.Context, job queue.Job) (deploy.ExecutionOutcome, error) {
	return e.execute(ctx, job)
}

type fakeOperationUpdater struct{}

func (fakeOperationUpdater) Transition(_ context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	return orailtypes.Operation{ID: id, Kind: "deploy", State: state}, nil
}

func (fakeOperationUpdater) Fail(_ context.Context, id orailtypes.ID, code orailtypes.FailureCode, reason string) (orailtypes.Operation, error) {
	return orailtypes.Operation{ID: id, Kind: "deploy", State: orailtypes.OperationFailed, Code: code, Reason: reason}, nil
}

type closableEngine struct {
	docker.EngineClient
	closed bool
}

func (e *closableEngine) Close() error {
	e.closed = true
	return nil
}

type workerTestActivationRuntime struct {
	calls     *[]string
	pull      runtime.PullVerifyResult
	target    runtime.RoutingTarget
	verifier  docker.SignatureVerifier
	scheduled runtime.ScheduleRequest
}

type workerRecoverableActivationRuntime struct {
	workerTestActivationRuntime
	reconcileCalls int
}

func (r *workerRecoverableActivationRuntime) Reconcile(context.Context) error {
	r.reconcileCalls++
	return nil
}

func (r *workerTestActivationRuntime) SetSignatureVerifier(verifier docker.SignatureVerifier) {
	r.verifier = verifier
}

func (r *workerTestActivationRuntime) PullVerify(context.Context, string) (runtime.PullVerifyResult, error) {
	*r.calls = append(*r.calls, "pull-verify")
	return r.pull, nil
}

func (r *workerTestActivationRuntime) Schedule(_ context.Context, request runtime.ScheduleRequest) (runtime.InstanceStatus, error) {
	*r.calls = append(*r.calls, "schedule")
	r.scheduled = request
	return runtime.InstanceStatus{State: runtime.InstanceStateAllocated}, nil
}

type workerTestVariableResolver struct {
	values map[string]string
	err    error
}

func (r workerTestVariableResolver) Resolve(context.Context, string, string, string) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make(map[string]string, len(r.values))
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *workerTestActivationRuntime) Start(context.Context, string, string) error {
	*r.calls = append(*r.calls, "start")
	return nil
}

func (r *workerTestActivationRuntime) ResolveRoutingTarget(context.Context, string, string) (runtime.RoutingTarget, error) {
	*r.calls = append(*r.calls, "resolve-routing")
	return r.target, nil
}

func (r *workerTestActivationRuntime) Drain(context.Context, string, string, time.Duration) error {
	*r.calls = append(*r.calls, "drain")
	return nil
}

type workerTestRoutePromoter struct {
	calls    *[]string
	err      error
	metadata []deploy.RoutePromotionMetadata
}

func (p *workerTestRoutePromoter) Promote(_ context.Context, _ runtime.RoutingTarget, metadata deploy.RoutePromotionMetadata) error {
	*p.calls = append(*p.calls, "promote")
	p.metadata = append(p.metadata, metadata)
	return p.err
}

type workerTestOperationUpdater struct {
	transitions []orailtypes.OperationState
	failed      bool
	failCode    orailtypes.FailureCode
	failReason  string
}

func (u *workerTestOperationUpdater) Transition(_ context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	u.transitions = append(u.transitions, state)
	return orailtypes.Operation{ID: id, Kind: "deploy", State: state}, nil
}

func (u *workerTestOperationUpdater) Fail(_ context.Context, id orailtypes.ID, code orailtypes.FailureCode, reason string) (orailtypes.Operation, error) {
	u.failed = true
	u.failCode = code
	u.failReason = reason
	return orailtypes.Operation{ID: id, Kind: "deploy", State: orailtypes.OperationFailed, Code: code, Reason: reason}, nil
}

type workerAcceptanceQueueStore struct {
	jobID            string
	state            queue.JobState
	enqueued         []queue.EnqueueRequest
	leased           bool
	completedJobID   string
	completedLeaseID string
	failedJobID      string
	failedLeaseID    string
	failReason       string
}

func (s *workerAcceptanceQueueStore) Enqueue(_ context.Context, request queue.EnqueueRequest) (queue.Job, error) {
	id := s.jobID
	if id == "" {
		id = "job-dashboard-template"
	}
	s.jobID = id
	s.state = queue.JobQueued
	s.enqueued = append(s.enqueued, request)
	return s.jobFromRequest(request), nil
}

func (s *workerAcceptanceQueueStore) Lease(_ context.Context, request queue.LeaseRequest) (queue.Job, error) {
	if s.state != queue.JobQueued || s.leased || len(s.enqueued) == 0 || request.Queue != queue.Deploy {
		return queue.Job{}, queue.ErrNoWork
	}
	s.leased = true
	s.state = queue.JobLeased
	job := s.jobFromRequest(s.enqueued[0])
	job.State = queue.JobLeased
	job.LeaseID = request.WorkerID
	job.LeasedUntil = request.Now.Add(request.LeaseTime)
	return job, nil
}

func (s *workerAcceptanceQueueStore) Heartbeat(context.Context, string, string, time.Time) error {
	return nil
}

func (s *workerAcceptanceQueueStore) Complete(_ context.Context, jobID string, leaseID string) error {
	s.completedJobID = jobID
	s.completedLeaseID = leaseID
	s.state = queue.JobSucceeded
	return nil
}

func (s *workerAcceptanceQueueStore) Fail(_ context.Context, jobID string, leaseID string, reason string) error {
	s.failedJobID = jobID
	s.failedLeaseID = leaseID
	s.failReason = reason
	s.state = queue.JobFailed
	return nil
}

func (s *workerAcceptanceQueueStore) jobFromRequest(request queue.EnqueueRequest) queue.Job {
	return queue.Job{
		ID:                s.jobID,
		Queue:             request.Queue,
		State:             s.state,
		Priority:          request.Priority,
		PartitionKey:      request.PartitionKey,
		ParentOperationID: request.ParentOperationID,
		AvailableAt:       request.AvailableAt,
		Payload:           append([]byte(nil), request.Payload...),
	}
}

var _ queue.Store = (*workerAcceptanceQueueStore)(nil)

type workerQueueStore struct {
	completedJobID    string
	completedLeaseID  string
	failedJobID       string
	failedLeaseID     string
	failReason        string
	leaseRequest      queue.LeaseRequest
	leasedJob         queue.Job
	leaseErr          error
	heartbeatMu       sync.Mutex
	heartbeatJobIDs   []string
	heartbeatLeaseIDs []string
	heartbeatUntils   []time.Time
	heartbeatCh       chan time.Time
}

func (s *workerQueueStore) Enqueue(context.Context, queue.EnqueueRequest) (queue.Job, error) {
	return queue.Job{}, nil
}

func (s *workerQueueStore) Lease(_ context.Context, request queue.LeaseRequest) (queue.Job, error) {
	s.leaseRequest = request
	if s.leaseErr != nil {
		return queue.Job{}, s.leaseErr
	}
	return s.leasedJob, nil
}

func (s *workerQueueStore) Heartbeat(_ context.Context, jobID string, leaseID string, until time.Time) error {
	s.heartbeatMu.Lock()
	s.heartbeatJobIDs = append(s.heartbeatJobIDs, jobID)
	s.heartbeatLeaseIDs = append(s.heartbeatLeaseIDs, leaseID)
	s.heartbeatUntils = append(s.heartbeatUntils, until)
	s.heartbeatMu.Unlock()
	if s.heartbeatCh != nil {
		select {
		case s.heartbeatCh <- until:
		default:
		}
	}
	return nil
}

func (s *workerQueueStore) Complete(_ context.Context, jobID string, leaseID string) error {
	s.completedJobID = jobID
	s.completedLeaseID = leaseID
	return nil
}

func (s *workerQueueStore) Fail(_ context.Context, jobID string, leaseID string, reason string) error {
	s.failedJobID = jobID
	s.failedLeaseID = leaseID
	s.failReason = reason
	return nil
}

func (s *workerQueueStore) heartbeatCount() int {
	s.heartbeatMu.Lock()
	defer s.heartbeatMu.Unlock()
	return len(s.heartbeatUntils)
}

var _ queue.Store = (*workerQueueStore)(nil)

func workerTestDeployJob(t *testing.T) queue.Job {
	t.Helper()
	payload, err := json.Marshal(deploy.DeployJobPayload{
		SchemaVersion: deploy.DeployJobPayloadSchema,
		OperationID:   "op-1",
		ServiceID:     "api",
		ProjectID:     "prj-1",
		EnvironmentID: "prod",
		Request:       deploy.DeployJobRequest{Image: workerTestImageRef()},
		Candidate: deploy.DeployJobCandidate{
			DeploymentID: "dep-2",
			InstanceID:   "dep-2-r0",
		},
		PreviousActive: deploy.ActiveInstanceRef{DeploymentID: "dep-1", InstanceID: "dep-1-r0"},
	})
	if err != nil {
		t.Fatalf("marshal deploy job: %v", err)
	}
	return queue.Job{ID: "job-1", Queue: queue.Deploy, LeaseID: "worker-1", Payload: payload}
}

func workerTestPublicDeployJob(t *testing.T) queue.Job {
	t.Helper()
	job := workerTestDeployJob(t)
	var payload deploy.DeployJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatalf("unmarshal deploy job: %v", err)
	}
	payload.Route = &deploy.DeployJobRoute{
		Name:   "api",
		Group:  "apps",
		Host:   "api.example.test",
		Path:   "/",
		Public: true,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal public deploy job: %v", err)
	}
	job.Payload = encoded
	return job
}

func workerTestImageRef() string {
	return "registry.example.com/api@sha256:0000000000000000000000000000000000000000000000000000000000000000"
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsMessageWithPrefix(values []string, want string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, want) {
			return true
		}
	}
	return false
}

func workerOperationMessages(t *testing.T, store api.OperationStore, operationID orailtypes.ID) []string {
	t.Helper()
	events, err := store.ListEvents(context.Background(), operationID)
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	messages := make([]string, 0, len(events))
	for _, event := range events {
		messages = append(messages, event.Message)
	}
	return messages
}

type deploymentPromotionRecoveryStore struct {
	api.DeploymentStore
	promoteErr           error
	templatePromoteErr   error
	deploymentProved     bool
	templateProved       bool
	deploymentProofCalls int
	templateProofCalls   int
}

func deploymentPromotionRecoveryFixture(t *testing.T) *deploymentPromotionRecoveryStore {
	t.Helper()
	store := api.NewMemoryDeploymentStore(nil)
	record, err := store.CreateDeployment(context.Background(), api.DeploymentRecord{
		ID:            "dep-recovery",
		ProjectID:     "prj-recovery",
		ServiceID:     "svc-recovery",
		EnvironmentID: "env-recovery",
		Action:        deploy.ActionDeployImage,
		OperationID:   "op-recovery",
		ImageRef:      workerTestImageRef(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeploymentDeploying(
		context.Background(),
		record.ID,
		deploy.DeploymentQueued,
	); err != nil {
		t.Fatal(err)
	}
	return &deploymentPromotionRecoveryStore{DeploymentStore: store}
}

func (s *deploymentPromotionRecoveryStore) PromoteDeployment(
	context.Context,
	string,
	string,
) error {
	return s.promoteErr
}

func (s *deploymentPromotionRecoveryStore) PromoteTemplateDeployment(
	context.Context,
	string,
	string,
) error {
	return s.templatePromoteErr
}

func (s *deploymentPromotionRecoveryStore) DeploymentPromotionCommitted(
	context.Context,
	string,
	string,
	string,
) (bool, error) {
	s.deploymentProofCalls++
	return s.deploymentProved, nil
}

func (s *deploymentPromotionRecoveryStore) TemplateDeploymentPromotionCommitted(
	context.Context,
	string,
	string,
	string,
) (bool, error) {
	s.templateProofCalls++
	return s.templateProved, nil
}

func clearWorkerEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"MIDNIGHT_WORKER_ID",
		"MIDNIGHT_WORKER_RECLAIM_INTERVAL",
		"MIDNIGHT_WORKER_POLL_INTERVAL",
		"MIDNIGHT_WORKER_LEASE_TTL",
		"MIDNIGHT_WORKER_USAGE_RECONCILE_INTERVAL",
		"MIDNIGHT_WORKER_USAGE_RECONCILE_LOOKBACK",
		"MIDNIGHT_WORKER_USAGE_RECONCILE_TIMEOUT",
		"MIDNIGHT_WORKER_USAGE_RECONCILE_STAGE_TIMEOUT",
		"MIDNIGHT_WORKER_USAGE_RECONCILE_WORKSPACE_BATCH",
		"MIDNIGHT_WORKER_RUNTIME_BACKEND",
		"MIDNIGHT_WORKER_DOCKER_HOST",
		"MIDNIGHT_WORKER_DOCKER_BASELINE",    // not-a-failure-code
		"MIDNIGHT_WORKER_DEPLOY_CONCURRENCY", // not-a-failure-code
		"MIDNIGHT_WORKER_PROBE_CONTAINER",    // not-a-failure-code
		"MIDNIGHT_WORKER_AGENT_ID",
		"MIDNIGHT_AGENT_ID",
		"MIDNIGHT_WORKER_HOST_ID",
		"MIDNIGHT_HOST_ID",
		"MIDNIGHT_WORKER_VERIFY_SIGNATURES",
		"MIDNIGHT_WORKER_TRUSTED_IMAGES",
		"MIDNIGHT_WORKER_COSIGN_BINARY_PATH",
		"COSIGN_BINARY_PATH",
		"MIDNIGHT_WORKER_COSIGN_PUBLIC_KEY",
		"COSIGN_PUBLIC_KEY",
		"MIDNIGHT_WORKER_COSIGN_CERTIFICATE_IDENTITY_REGEXP",
		"MIDNIGHT_WORKER_COSIGN_CERTIFICATE_OIDC_ISSUER",
		"MIDNIGHT_WORKER_COSIGN_FULCIO_ROOT",
		"MIDNIGHT_WORKER_COSIGN_VERIFY_TIMEOUT",
		"MIDNIGHT_WORKER_ROUTE_CONFIG_DIR",
		"MIDNIGHT_WORKER_ROUTE_GROUP",
		"MIDNIGHT_WORKER_ROUTE_PUBLIC_HOST",
		"MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH",
		"MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS",
		"MIDNIGHT_WORKER_ROUTE_CERT_RESOLVER",
		"MIDNIGHT_WORKER_ROUTE_TARGET_PORT",
		"MIDNIGHT_APPS_DOMAIN",                   // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_ENABLED",            // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_PROVIDER",           // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_ZONE_ID",            // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_ZONE_NAME",          // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_ROUTE_TARGET",       // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_IPV4",               // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_IPV6",               // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_TTL",                // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_APPLY",              // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_RECONCILE_INTERVAL", // not-a-failure-code
		"MIDNIGHT_WORKER_DNS_HTTP_TIMEOUT",       // not-a-failure-code
		"CLOUDFLARE_DNS_API_TOKEN",
		"DOCKER_HOST",
		"MIDNIGHT_DB_DSN",
		"MIDNIGHT_DATABASE_URL",
		"MIDNIGHT_DB_DSN_FILE",
	} {
		t.Setenv(name, "")
	}
}

// An ingress that is not a container has nothing to enroll into the
// per-environment bridge. Wiring the enroller anyway made every route publish
// fail with "No such container" on hosts that run Traefik as a service.
func TestIngressModeLabelDistinguishesHostFromContainer(t *testing.T) {
	if got := ingressModeLabel(workerConfig{}); got != "host" {
		t.Fatalf("empty ingress container = %q, want host", got)
	}
	if got := ingressModeLabel(workerConfig{IngressContainer: "  "}); got != "host" {
		t.Fatalf("blank ingress container = %q, want host", got)
	}
	if got := ingressModeLabel(workerConfig{IngressContainer: "midnight-traefik"}); got != "container:midnight-traefik" {
		t.Fatalf("configured ingress container = %q", got)
	}
}
