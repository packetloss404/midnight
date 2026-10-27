package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	dockervolume "github.com/moby/moby/api/types/volume"

	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/agent/diagnosis"
	"github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	midnightdns "github.com/midnight/midnight/internal/dns"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/metering"
	"github.com/midnight/midnight/internal/monitors"
	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/runtime/docker"
	"github.com/midnight/midnight/internal/runtime/local"
	"github.com/midnight/midnight/internal/scheduler"
	schedulerlocal "github.com/midnight/midnight/internal/scheduler/local"
	"github.com/midnight/midnight/internal/scheduler/multihost"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/tcpproxy"
	templatepkg "github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/internal/varrefs"
	"github.com/midnight/midnight/internal/version"
	midnightvolume "github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const defaultWorkerID = "midnight-worker"
const defaultWorkerRuntimeBackend = "local"
const defaultWorkerReclaimInterval = 30 * time.Second
const defaultWorkerPollInterval = 2 * time.Second
const defaultWorkerLeaseTTL = 5 * time.Minute
const defaultDeployDrainGrace = 30 * time.Second
const defaultWorkerRouteGroup = "default"
const defaultWorkerRoutePublicPath = "/"
const defaultWorkerRouteTargetPort = 8080
const defaultWorkerACMEStateDir = "/var/lib/midnight/acme"
const defaultWorkerACMEEnvironment = "staging"
const defaultWorkerACMEInterval = 30 * time.Second
const defaultWorkerACMEHTTPTimeout = 60 * time.Second
const defaultWorkerDNSInterval = 30 * time.Second
const defaultWorkerDNSHTTPTimeout = 10 * time.Second
const defaultWorkerDNSTTL = 300
const defaultWorkerUsageReconcileInterval = time.Minute
const defaultWorkerUsageReconcileLookback = 24 * time.Hour
const defaultWorkerUsageReconcileTimeout = 45 * time.Second
const defaultWorkerUsageReconcileStageTimeout = 5 * time.Second
const defaultWorkerUsageReconcileWorkspaceBatch = 100

func main() {
	if err := run("midnight-worker", os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(name string, args []string) error {
	if len(args) > 0 && args[0] == "version" {
		fmt.Println(version.Version)
		return nil
	}
	if len(args) > 0 && args[0] == "serve" {
		return runServe(args[1:])
	}

	fmt.Printf("%s %s\n", name, version.Version)
	return nil
}

type workerConfig struct {
	WorkerID           string
	PostgresDSN        string
	PostgresDSNFile    string
	RuntimeBackend     string
	SchedulerBackend   string
	DockerHost         string
	DockerBaseline     string
	DeployConcurrency  int
	ProbeContainer     string
	AgentID            string
	HostID             string
	VerifySignatures   bool
	TrustedImages      []string
	TrustLocalRegistry bool
	LocalRegistry      string
	CosignBinaryPath   string
	CosignPublicKey    string
	CosignIdentity     string
	CosignOIDCIssuer   string
	CosignFulcioRoot   string
	CosignTimeout      time.Duration
	RouteConfigDir     string
	RouteGroup         string
	RoutePublicHost    string
	RoutePublicPath    string
	RoutePublicTLS     bool
	RouteCertResolver  string
	RouteTargetPort    int
	AppsDomain         string
	TraefikAPIURL      string
	IngressProbeURL    string
	IngressContainer   string
	ACMEEnabled        bool
	ACMEEmail          string
	ACMEStateDir       string
	ACMEEnvironment    string
	ACMEDNSProvider    string
	ACMEResolvers      []string
	ACMEHTTPTimeout    time.Duration
	ACMEProductionAck  bool
	ACMEInterval       time.Duration
	DNSEnabled         bool
	DNSProvider        string
	DNSZoneID          string
	DNSZoneName        string
	DNSRouteTarget     string
	DNSIPv4            string
	DNSIPv6            string
	DNSTTL             int
	DNSApply           bool
	DNSInterval        time.Duration
	DNSHTTPTimeout     time.Duration
	DNSCloudflareToken string
	TCPProxyBindHost   string
	TCPProxyContainer  string
	TCPProxyController string
	TCPProxyInterval   time.Duration
	ReclaimInterval    time.Duration
	PollInterval       time.Duration
	LeaseTTL           time.Duration
	UsageReconcile     workerUsageReconcileConfig
	SkipMigrations     bool
	AuditCheckpoints   bool
	AuditKeyPath       string
}

type workerUsageReconcileConfig struct {
	Interval       time.Duration
	Lookback       time.Duration
	Timeout        time.Duration
	StageTimeout   time.Duration
	WorkspaceBatch int
}

func runServe(args []string) error {
	cfg, err := parseWorkerConfig(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveWorker(ctx, cfg)
}

func parseWorkerConfig(args []string) (workerConfig, error) {
	cfg := workerConfig{
		AuditCheckpoints:   true,
		AuditKeyPath:       envFirst(audit.DefaultSigningKeyFile, "MIDNIGHT_AUDIT_SIGNING_KEY_PATH"), // not-a-failure-code
		WorkerID:           envFirst(defaultWorkerID, "MIDNIGHT_WORKER_ID"),
		PostgresDSN:        envFirst("", pg.EnvDSN, "MIDNIGHT_DATABASE_URL"),
		PostgresDSNFile:    envFirst(pg.DefaultDSNFile, pg.EnvDSNFile),
		RuntimeBackend:     envFirst(defaultWorkerRuntimeBackend, "MIDNIGHT_WORKER_RUNTIME_BACKEND"),
		SchedulerBackend:   envFirst(string(scheduler.SelectorLocal), scheduler.SelectorEnv),
		DockerHost:         envFirst("", "MIDNIGHT_WORKER_DOCKER_HOST", "DOCKER_HOST"),
		DockerBaseline:     envFirst(string(docker.BaselineEnforce), "MIDNIGHT_WORKER_DOCKER_BASELINE"),  // not-a-failure-code
		DeployConcurrency:  envInt(defaultWorkerDeployConcurrency, "MIDNIGHT_WORKER_DEPLOY_CONCURRENCY"), // not-a-failure-code
		ProbeContainer:     envFirst("", "MIDNIGHT_WORKER_PROBE_CONTAINER"),                              // not-a-failure-code
		AgentID:            envFirst("", "MIDNIGHT_WORKER_AGENT_ID", "MIDNIGHT_AGENT_ID"),
		HostID:             envFirst("", "MIDNIGHT_WORKER_HOST_ID", "MIDNIGHT_HOST_ID"),
		VerifySignatures:   envBool(false, "MIDNIGHT_WORKER_VERIFY_SIGNATURES"),
		TrustedImages:      envList("MIDNIGHT_WORKER_TRUSTED_IMAGES"),
		TrustLocalRegistry: envBool(false, "MIDNIGHT_WORKER_TRUST_LOCAL_REGISTRY"),
		LocalRegistry:      envFirst("", "MIDNIGHT_REGISTRY_ENDPOINT", "MIDNIGHT_REGISTRY_ADDR"),
		CosignBinaryPath:   envFirst("", "MIDNIGHT_WORKER_COSIGN_BINARY_PATH", "COSIGN_BINARY_PATH"),
		CosignPublicKey:    envFirst("", "MIDNIGHT_WORKER_COSIGN_PUBLIC_KEY", "COSIGN_PUBLIC_KEY"),
		CosignIdentity:     envFirst("", "MIDNIGHT_WORKER_COSIGN_CERTIFICATE_IDENTITY_REGEXP"),
		CosignOIDCIssuer:   envFirst("", "MIDNIGHT_WORKER_COSIGN_CERTIFICATE_OIDC_ISSUER"),
		CosignFulcioRoot:   envFirst("", "MIDNIGHT_WORKER_COSIGN_FULCIO_ROOT"),
		CosignTimeout:      envDuration(docker.DefaultCosignVerifyTimeout, "MIDNIGHT_WORKER_COSIGN_VERIFY_TIMEOUT"),
		RouteConfigDir:     envFirst("", "MIDNIGHT_WORKER_ROUTE_CONFIG_DIR"),
		RouteGroup:         envFirst(defaultWorkerRouteGroup, "MIDNIGHT_WORKER_ROUTE_GROUP"),
		RoutePublicHost:    envFirst("", "MIDNIGHT_WORKER_ROUTE_PUBLIC_HOST"),
		RoutePublicPath:    envFirst(defaultWorkerRoutePublicPath, "MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH"),
		RoutePublicTLS:     envBool(false, "MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS"),
		RouteCertResolver:  envFirst("", "MIDNIGHT_WORKER_ROUTE_CERT_RESOLVER"),
		RouteTargetPort:    envInt(defaultWorkerRouteTargetPort, "MIDNIGHT_WORKER_ROUTE_TARGET_PORT"),
		AppsDomain:         envFirst("", "MIDNIGHT_APPS_DOMAIN"),              // not-a-failure-code
		TraefikAPIURL:      envFirst("", "MIDNIGHT_WORKER_TRAEFIK_API_URL"),   // not-a-failure-code
		IngressProbeURL:    envFirst("", "MIDNIGHT_WORKER_INGRESS_PROBE_URL"), // not-a-failure-code
		IngressContainer:   envFirst("", "MIDNIGHT_WORKER_INGRESS_CONTAINER"), // not-a-failure-code
		ACMEEnabled:        envBool(false, "MIDNIGHT_WORKER_ACME_ENABLED"),
		ACMEEmail:          envFirst("", "MIDNIGHT_WORKER_ACME_EMAIL"),                                        // not-a-failure-code
		ACMEStateDir:       envFirst(defaultWorkerACMEStateDir, "MIDNIGHT_WORKER_ACME_STATE_DIR"),             // not-a-failure-code
		ACMEEnvironment:    envFirst(defaultWorkerACMEEnvironment, "MIDNIGHT_WORKER_ACME_ENVIRONMENT"),        // not-a-failure-code
		ACMEDNSProvider:    envFirst("", "MIDNIGHT_WORKER_ACME_DNS_PROVIDER"),                                 // not-a-failure-code
		ACMEResolvers:      envList("MIDNIGHT_WORKER_ACME_DNS_RESOLVERS"),                                     // not-a-failure-code
		ACMEHTTPTimeout:    envDuration(defaultWorkerACMEHTTPTimeout, "MIDNIGHT_WORKER_ACME_HTTP_TIMEOUT"),    // not-a-failure-code
		ACMEProductionAck:  envBool(false, "MIDNIGHT_WORKER_ACME_PRODUCTION_ACK"),                             // not-a-failure-code
		ACMEInterval:       envDuration(defaultWorkerACMEInterval, "MIDNIGHT_WORKER_ACME_RECONCILE_INTERVAL"), // not-a-failure-code
		DNSEnabled:         envBool(false, "MIDNIGHT_WORKER_DNS_ENABLED"),
		DNSProvider:        envFirst("cloudflare", "MIDNIGHT_WORKER_DNS_PROVIDER"),                          // not-a-failure-code
		DNSZoneID:          envFirst("", "MIDNIGHT_WORKER_DNS_ZONE_ID"),                                     // not-a-failure-code
		DNSZoneName:        envFirst("", "MIDNIGHT_WORKER_DNS_ZONE_NAME"),                                   // not-a-failure-code
		DNSRouteTarget:     envFirst("", "MIDNIGHT_WORKER_DNS_ROUTE_TARGET"),                                // not-a-failure-code
		DNSIPv4:            envFirst("", "MIDNIGHT_WORKER_DNS_IPV4"),                                        // not-a-failure-code
		DNSIPv6:            envFirst("", "MIDNIGHT_WORKER_DNS_IPV6"),                                        // not-a-failure-code
		DNSTTL:             envInt(defaultWorkerDNSTTL, "MIDNIGHT_WORKER_DNS_TTL"),                          // not-a-failure-code
		DNSApply:           envBool(false, "MIDNIGHT_WORKER_DNS_APPLY"),                                     // not-a-failure-code
		DNSInterval:        envDuration(defaultWorkerDNSInterval, "MIDNIGHT_WORKER_DNS_RECONCILE_INTERVAL"), // not-a-failure-code
		DNSHTTPTimeout:     envDuration(defaultWorkerDNSHTTPTimeout, "MIDNIGHT_WORKER_DNS_HTTP_TIMEOUT"),    // not-a-failure-code
		DNSCloudflareToken: envFirst("", "CLOUDFLARE_DNS_API_TOKEN"),                                        // not-a-failure-code
		TCPProxyBindHost:   envFirst("", "MIDNIGHT_TCP_PROXY_BIND_HOST"),
		TCPProxyContainer:  envFirst("", "MIDNIGHT_TCP_PROXY_CONTAINER"), // not-a-failure-code
		TCPProxyController: envFirst(defaultWorkerID, "MIDNIGHT_TCP_PROXY_CONTROLLER_ID"),
		TCPProxyInterval:   envDuration(tcpproxy.DefaultReconcileInterval, "MIDNIGHT_TCP_PROXY_RECONCILE_INTERVAL"),
		ReclaimInterval:    envDuration(defaultWorkerReclaimInterval, "MIDNIGHT_WORKER_RECLAIM_INTERVAL"),
		PollInterval:       envDuration(defaultWorkerPollInterval, "MIDNIGHT_WORKER_POLL_INTERVAL"),
		LeaseTTL:           envDuration(defaultWorkerLeaseTTL, "MIDNIGHT_WORKER_LEASE_TTL"),
		UsageReconcile: workerUsageReconcileConfig{
			Interval:       envDuration(defaultWorkerUsageReconcileInterval, "MIDNIGHT_WORKER_USAGE_RECONCILE_INTERVAL"),
			Lookback:       envDuration(defaultWorkerUsageReconcileLookback, "MIDNIGHT_WORKER_USAGE_RECONCILE_LOOKBACK"),
			Timeout:        envDuration(defaultWorkerUsageReconcileTimeout, "MIDNIGHT_WORKER_USAGE_RECONCILE_TIMEOUT"),
			StageTimeout:   envDuration(defaultWorkerUsageReconcileStageTimeout, "MIDNIGHT_WORKER_USAGE_RECONCILE_STAGE_TIMEOUT"),
			WorkspaceBatch: envInt(defaultWorkerUsageReconcileWorkspaceBatch, "MIDNIGHT_WORKER_USAGE_RECONCILE_WORKSPACE_BATCH"),
		},
	}
	if raw := strings.TrimSpace(os.Getenv("MIDNIGHT_AUDIT_CHECKPOINT_ENABLED")); raw != "" { // not-a-failure-code
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return workerConfig{}, errors.New("MIDNIGHT_AUDIT_CHECKPOINT_ENABLED must be true or false")
		}
		cfg.AuditCheckpoints = value
	}
	fs := flag.NewFlagSet("midnight-worker serve", flag.ContinueOnError)
	fs.BoolVar(&cfg.AuditCheckpoints, "audit-checkpoints", cfg.AuditCheckpoints, "create signed audit checkpoints at UTC midnight and catch up on startup")
	fs.StringVar(&cfg.AuditKeyPath, "audit-signing-key-file", cfg.AuditKeyPath, "durable mode-0600 audit signing key file")
	fs.StringVar(&cfg.WorkerID, "worker-id", cfg.WorkerID, "stable worker process id")
	fs.StringVar(&cfg.PostgresDSN, "postgres-dsn", cfg.PostgresDSN, "Postgres DSN (default: $MIDNIGHT_DB_DSN)")
	fs.StringVar(&cfg.PostgresDSNFile, "postgres-dsn-file", cfg.PostgresDSNFile, "Postgres DSN file (default: $MIDNIGHT_DB_DSN_FILE or /etc/midnight/db.dsn)")
	fs.StringVar(&cfg.RuntimeBackend, "runtime-backend", cfg.RuntimeBackend, "runtime backend: local or docker")
	fs.StringVar(&cfg.DockerBaseline, "docker-baseline", cfg.DockerBaseline, "docker daemon.json baseline handling: enforce (default) or warn (developer engines only)")
	fs.IntVar(&cfg.DeployConcurrency, "deploy-concurrency", cfg.DeployConcurrency, "maximum deploy jobs executed at once; jobs for the same service always run one at a time")
	fs.StringVar(&cfg.ProbeContainer, "probe-container", cfg.ProbeContainer, "container this worker runs in (compose/dev stacks); it is attached to each environment network so readiness probes can reach workloads")
	fs.StringVar(&cfg.SchedulerBackend, "scheduler", cfg.SchedulerBackend, "scheduler backend: local (default) or multihost; multihost is stubbed per ADR-0018")
	fs.StringVar(&cfg.DockerHost, "docker-host", cfg.DockerHost, "Docker Engine host (default: $DOCKER_HOST)")
	fs.StringVar(&cfg.AgentID, "agent-id", cfg.AgentID, "agent id used for runtime resource labels")
	fs.StringVar(&cfg.HostID, "host-id", cfg.HostID, "host id used for runtime resource labels")
	fs.BoolVar(&cfg.VerifySignatures, "verify-signatures", cfg.VerifySignatures, "verify image signatures with cosign before pull")
	fs.Var((*stringListFlag)(&cfg.TrustedImages), "trusted-image", "image reference trusted by the static verifier; repeatable or comma-separated")
	fs.BoolVar(&cfg.TrustLocalRegistry, "trust-local-registry", cfg.TrustLocalRegistry, "trust immutable digests produced in the configured private Midnight registry")
	fs.StringVar(&cfg.LocalRegistry, "local-registry", cfg.LocalRegistry, "private Midnight registry endpoint eligible for digest trust")
	fs.StringVar(&cfg.CosignBinaryPath, "cosign-binary", cfg.CosignBinaryPath, "path to pinned cosign binary for signature verification")
	fs.StringVar(&cfg.CosignPublicKey, "cosign-public-key", cfg.CosignPublicKey, "cosign public key path for key-pair verification")
	fs.StringVar(&cfg.CosignIdentity, "cosign-certificate-identity-regexp", cfg.CosignIdentity, "cosign keyless certificate identity regexp")
	fs.StringVar(&cfg.CosignOIDCIssuer, "cosign-certificate-oidc-issuer", cfg.CosignOIDCIssuer, "cosign keyless certificate OIDC issuer")
	fs.StringVar(&cfg.CosignFulcioRoot, "cosign-fulcio-root", cfg.CosignFulcioRoot, "Fulcio root certificate chain path for keyless verification")
	fs.DurationVar(&cfg.CosignTimeout, "cosign-timeout", cfg.CosignTimeout, "cosign verify timeout")
	fs.StringVar(&cfg.RouteConfigDir, "route-config-dir", cfg.RouteConfigDir, "Traefik dynamic config directory; empty disables route publication")
	fs.StringVar(&cfg.RouteGroup, "route-group", cfg.RouteGroup, "route publication group name")
	fs.StringVar(&cfg.RoutePublicHost, "route-public-host", cfg.RoutePublicHost, "public host for promoted routes")
	fs.StringVar(&cfg.RoutePublicPath, "route-public-path", cfg.RoutePublicPath, "public path prefix for promoted routes")
	fs.BoolVar(&cfg.RoutePublicTLS, "route-public-tls", cfg.RoutePublicTLS, "enable TLS on promoted Traefik routes")
	fs.StringVar(&cfg.RouteCertResolver, "route-cert-resolver", cfg.RouteCertResolver, "Traefik cert resolver for promoted TLS routes")
	fs.IntVar(&cfg.RouteTargetPort, "route-target-port", cfg.RouteTargetPort, "container port Traefik should route to when runtime target omits a port")
	fs.StringVar(&cfg.AppsDomain, "apps-domain", cfg.AppsDomain, "base domain for generated service hostnames")
	fs.StringVar(&cfg.TraefikAPIURL, "traefik-api-url", cfg.TraefikAPIURL, "trusted Traefik API base URL used for active-config acknowledgement")
	fs.StringVar(&cfg.IngressProbeURL, "ingress-probe-url", cfg.IngressProbeURL, "trusted ingress address used with the desired Host header for route probes")
	fs.StringVar(&cfg.IngressContainer, "ingress-container", cfg.IngressContainer, "explicit Traefik container id/name enrolled into environment bridges")
	fs.BoolVar(&cfg.ACMEEnabled, "acme-enabled", cfg.ACMEEnabled, "enable the opt-in worker-owned ACME domain lifecycle")
	fs.StringVar(&cfg.ACMEEmail, "acme-email", cfg.ACMEEmail, "ACME account email (required when ACME is enabled)")
	fs.StringVar(&cfg.ACMEStateDir, "acme-state-dir", cfg.ACMEStateDir, "absolute ACME account and certificate state directory")
	fs.StringVar(&cfg.ACMEEnvironment, "acme-environment", cfg.ACMEEnvironment, "ACME directory: staging (default) or production")
	fs.StringVar(&cfg.ACMEDNSProvider, "acme-dns-provider", cfg.ACMEDNSProvider, "ACME DNS-01 provider: cloudflare, route53, or manual")
	fs.Var((*stringListFlag)(&cfg.ACMEResolvers), "acme-dns-resolver", "public DNS resolver address used for ACME propagation checks; repeatable")
	fs.DurationVar(&cfg.ACMEHTTPTimeout, "acme-http-timeout", cfg.ACMEHTTPTimeout, "timeout for ACME provider HTTP calls")
	fs.BoolVar(&cfg.ACMEProductionAck, "acme-production-ack", cfg.ACMEProductionAck, "explicitly acknowledge ACME production issuance")
	fs.DurationVar(&cfg.ACMEInterval, "acme-reconcile-interval", cfg.ACMEInterval, "domain lifecycle reconciliation interval")
	fs.BoolVar(&cfg.DNSEnabled, "dns-enabled", cfg.DNSEnabled, "enable provider-neutral DNS reconciliation")
	fs.StringVar(&cfg.DNSProvider, "dns-provider", cfg.DNSProvider, "DNS provider (cloudflare)")
	fs.StringVar(&cfg.DNSZoneID, "dns-zone-id", cfg.DNSZoneID, "opaque provider zone id")
	fs.StringVar(&cfg.DNSZoneName, "dns-zone-name", cfg.DNSZoneName, "authoritative DNS zone name")
	fs.StringVar(&cfg.DNSRouteTarget, "dns-route-target", cfg.DNSRouteTarget, "CNAME routing target for non-apex domains (defaults to apps-domain)")
	fs.StringVar(&cfg.DNSIPv4, "dns-ipv4", cfg.DNSIPv4, "IPv4 routing target for apex/address records")
	fs.StringVar(&cfg.DNSIPv6, "dns-ipv6", cfg.DNSIPv6, "IPv6 routing target for apex/address records")
	fs.IntVar(&cfg.DNSTTL, "dns-ttl", cfg.DNSTTL, "managed DNS record TTL; 1 selects provider automatic TTL")
	fs.BoolVar(&cfg.DNSApply, "dns-apply", cfg.DNSApply, "apply DNS plans; disabled by default for dry-run safety")
	fs.DurationVar(&cfg.DNSInterval, "dns-reconcile-interval", cfg.DNSInterval, "managed DNS reconciliation interval")
	fs.DurationVar(&cfg.DNSHTTPTimeout, "dns-http-timeout", cfg.DNSHTTPTimeout, "DNS provider HTTP timeout")
	fs.StringVar(&cfg.TCPProxyBindHost, "tcp-proxy-bind-host", cfg.TCPProxyBindHost, "host/interface for executable TCP listeners; empty disables the controller")
	fs.StringVar(&cfg.TCPProxyContainer, "tcp-proxy-container", cfg.TCPProxyContainer, "explicit Docker id/name of this trusted worker for scoped network enrollment")
	fs.StringVar(&cfg.TCPProxyController, "tcp-proxy-controller-id", cfg.TCPProxyController, "stable controller evidence identifier")
	fs.DurationVar(&cfg.TCPProxyInterval, "tcp-proxy-reconcile-interval", cfg.TCPProxyInterval, "durable TCP listener reconciliation interval")
	fs.DurationVar(&cfg.ReclaimInterval, "reclaim-interval", cfg.ReclaimInterval, "expired lease reclaim interval")
	fs.DurationVar(&cfg.PollInterval, "poll-interval", cfg.PollInterval, "deploy queue poll interval")
	fs.DurationVar(&cfg.LeaseTTL, "lease-ttl", cfg.LeaseTTL, "deploy job lease duration")
	fs.DurationVar(&cfg.UsageReconcile.Interval, "usage-reconcile-interval", cfg.UsageReconcile.Interval, "background usage reconciliation interval")
	fs.DurationVar(&cfg.UsageReconcile.Lookback, "usage-reconcile-lookback", cfg.UsageReconcile.Lookback, "overlapping evidence recovery window")
	fs.DurationVar(&cfg.UsageReconcile.Timeout, "usage-reconcile-timeout", cfg.UsageReconcile.Timeout, "whole usage reconciliation pass timeout")
	fs.DurationVar(&cfg.UsageReconcile.StageTimeout, "usage-reconcile-stage-timeout", cfg.UsageReconcile.StageTimeout, "per-producer usage reconciliation timeout")
	fs.IntVar(&cfg.UsageReconcile.WorkspaceBatch, "usage-reconcile-workspace-batch", cfg.UsageReconcile.WorkspaceBatch, "maximum workspaces per usage reconciliation pass")
	fs.BoolVar(&cfg.SkipMigrations, "skip-migrations", false, "skip applying pending schema migrations at startup")
	if err := fs.Parse(args); err != nil {
		return workerConfig{}, err
	}
	normalizeWorkerConfig(&cfg)
	if strings.TrimSpace(cfg.WorkerID) == "" {
		return workerConfig{}, errors.New("worker id is required")
	}
	switch cfg.RuntimeBackend {
	case "local", "docker":
	default:
		return workerConfig{}, errors.New("runtime backend must be local or docker")
	}
	if _, err := docker.ParseBaselineMode(cfg.DockerBaseline); err != nil {
		return workerConfig{}, err
	}
	if cfg.DeployConcurrency < 1 {
		return workerConfig{}, errors.New("deploy concurrency must be at least 1")
	}
	if cfg.AuditCheckpoints && !filepath.IsAbs(cfg.AuditKeyPath) {
		return workerConfig{}, errors.New("audit signing key path must be absolute")
	}
	if _, err := scheduler.ParseSelector(cfg.SchedulerBackend); err != nil {
		return workerConfig{}, fmt.Errorf("scheduler backend %q: %w (valid: local, multihost)", cfg.SchedulerBackend, err)
	}
	if strings.TrimSpace(cfg.RouteConfigDir) != "" {
		if !strings.HasPrefix(cfg.RoutePublicPath, "/") {
			return workerConfig{}, errors.New("route public path must start with /")
		}
		if cfg.RouteTargetPort <= 0 {
			return workerConfig{}, errors.New("route target port must be positive")
		}
		if strings.TrimSpace(cfg.RouteCertResolver) != "" && !cfg.RoutePublicTLS {
			return workerConfig{}, errors.New("route cert resolver requires route public tls")
		}
	}
	if cfg.ACMEEnabled {
		if cfg.ACMEEmail == "" {
			return workerConfig{}, errors.New("ACME email is required when the domain lifecycle is enabled")
		}
		if !filepath.IsAbs(cfg.ACMEStateDir) {
			return workerConfig{}, errors.New("ACME state directory must be absolute")
		}
		if cfg.ACMEDNSProvider == "" {
			return workerConfig{}, errors.New("ACME DNS provider is required when the domain lifecycle is enabled")
		}
		switch cfg.ACMEEnvironment {
		case "staging":
		case "production":
			if !cfg.ACMEProductionAck {
				return workerConfig{}, errors.New("ACME production requires explicit production acknowledgement")
			}
		default:
			return workerConfig{}, fmt.Errorf("ACME environment %q is unsupported (use staging or production)", cfg.ACMEEnvironment)
		}
		if cfg.ACMEHTTPTimeout <= 0 {
			return workerConfig{}, errors.New("ACME HTTP timeout must be positive")
		}
		if cfg.ACMEInterval <= 0 {
			return workerConfig{}, errors.New("ACME reconcile interval must be positive")
		}
	}
	if cfg.DNSEnabled {
		if cfg.DNSProvider != "cloudflare" {
			return workerConfig{}, errors.New("DNS provider must be cloudflare")
		}
		if cfg.DNSZoneID == "" || cfg.DNSZoneName == "" {
			return workerConfig{}, errors.New("DNS zone id and zone name are required when DNS reconciliation is enabled")
		}
		if cfg.DNSCloudflareToken == "" {
			return workerConfig{}, errors.New("CLOUDFLARE_DNS_API_TOKEN is required when Cloudflare DNS reconciliation is enabled")
		}
		if cfg.DNSRouteTarget == "" && cfg.DNSIPv4 == "" && cfg.DNSIPv6 == "" {
			return workerConfig{}, errors.New("DNS route target, IPv4, or IPv6 is required when DNS reconciliation is enabled")
		}
		if cfg.DNSIPv4 != "" {
			address, parseErr := netip.ParseAddr(cfg.DNSIPv4)
			if parseErr != nil || !address.Is4() {
				return workerConfig{}, errors.New("DNS IPv4 target must be an IPv4 address")
			}
		}
		if cfg.DNSIPv6 != "" {
			address, parseErr := netip.ParseAddr(cfg.DNSIPv6)
			if parseErr != nil || !address.Is6() || address.Is4In6() {
				return workerConfig{}, errors.New("DNS IPv6 target must be an IPv6 address")
			}
		}
		if cfg.DNSTTL != 1 && (cfg.DNSTTL < 60 || cfg.DNSTTL > 86400) {
			return workerConfig{}, errors.New("DNS TTL must be provider-auto (1) or 60..86400 seconds")
		}
		if cfg.DNSInterval <= 0 || cfg.DNSHTTPTimeout <= 0 {
			return workerConfig{}, errors.New("DNS reconciliation interval and HTTP timeout must be positive")
		}
	}
	if cfg.ReclaimInterval <= 0 {
		return workerConfig{}, errors.New("reclaim interval must be positive")
	}
	if cfg.PollInterval <= 0 {
		return workerConfig{}, errors.New("poll interval must be positive")
	}
	if cfg.LeaseTTL <= 0 {
		return workerConfig{}, errors.New("lease ttl must be positive")
	}
	if cfg.UsageReconcile.Interval <= 0 || cfg.UsageReconcile.Lookback <= 0 || cfg.UsageReconcile.Timeout <= 0 || cfg.UsageReconcile.StageTimeout <= 0 {
		return workerConfig{}, errors.New("usage reconciliation durations must be positive")
	}
	if cfg.UsageReconcile.Timeout > cfg.UsageReconcile.Interval {
		return workerConfig{}, errors.New("usage reconciliation timeout must not exceed its interval")
	}
	if cfg.UsageReconcile.StageTimeout > cfg.UsageReconcile.Timeout {
		return workerConfig{}, errors.New("usage reconciliation stage timeout must not exceed the pass timeout")
	}
	if cfg.UsageReconcile.WorkspaceBatch <= 0 || cfg.UsageReconcile.WorkspaceBatch > 1000 {
		return workerConfig{}, errors.New("usage reconciliation workspace batch must be 1..1000")
	}
	if cfg.CosignTimeout <= 0 {
		return workerConfig{}, errors.New("cosign timeout must be positive")
	}
	if cfg.TCPProxyBindHost != "" {
		if cfg.RuntimeBackend != "docker" {
			return workerConfig{}, errors.New("TCP proxy execution requires the Docker worker runtime")
		}
		if cfg.TCPProxyContainer == "" {
			return workerConfig{}, errors.New("TCP proxy execution requires an explicit trusted proxy container")
		}
		if cfg.TCPProxyInterval <= 0 {
			return workerConfig{}, errors.New("TCP proxy reconcile interval must be positive")
		}
	}
	if (cfg.VerifySignatures || len(cfg.TrustedImages) > 0) && cfg.RuntimeBackend != "docker" {
		return workerConfig{}, errors.New("image trust verification requires docker runtime backend")
	}
	return cfg, nil
}

func normalizeWorkerConfig(cfg *workerConfig) {
	cfg.WorkerID = strings.TrimSpace(cfg.WorkerID)
	cfg.RuntimeBackend = strings.ToLower(strings.TrimSpace(cfg.RuntimeBackend))
	cfg.SchedulerBackend = strings.ToLower(strings.TrimSpace(cfg.SchedulerBackend))
	if cfg.SchedulerBackend == "" {
		cfg.SchedulerBackend = string(scheduler.SelectorLocal)
	}
	cfg.DockerHost = strings.TrimSpace(cfg.DockerHost)
	cfg.AgentID = strings.TrimSpace(cfg.AgentID)
	if cfg.AgentID == "" {
		cfg.AgentID = cfg.WorkerID
	}
	cfg.HostID = strings.TrimSpace(cfg.HostID)
	if cfg.HostID == "" {
		cfg.HostID = cfg.AgentID
	}
	cfg.CosignBinaryPath = strings.TrimSpace(cfg.CosignBinaryPath)
	cfg.CosignPublicKey = strings.TrimSpace(cfg.CosignPublicKey)
	cfg.CosignIdentity = strings.TrimSpace(cfg.CosignIdentity)
	cfg.CosignOIDCIssuer = strings.TrimSpace(cfg.CosignOIDCIssuer)
	cfg.CosignFulcioRoot = strings.TrimSpace(cfg.CosignFulcioRoot)
	cfg.TrustedImages = normalizeStringList(cfg.TrustedImages)
	cfg.RouteConfigDir = strings.TrimSpace(cfg.RouteConfigDir)
	cfg.RouteGroup = strings.TrimSpace(cfg.RouteGroup)
	if cfg.RouteGroup == "" {
		cfg.RouteGroup = defaultWorkerRouteGroup
	}
	cfg.RoutePublicHost = strings.TrimSpace(cfg.RoutePublicHost)
	cfg.RoutePublicPath = strings.TrimSpace(cfg.RoutePublicPath)
	if cfg.RoutePublicPath == "" {
		cfg.RoutePublicPath = defaultWorkerRoutePublicPath
	}
	cfg.RouteCertResolver = strings.TrimSpace(cfg.RouteCertResolver)
	cfg.AppsDomain = strings.TrimSpace(cfg.AppsDomain)
	cfg.TraefikAPIURL = strings.TrimSpace(cfg.TraefikAPIURL)
	cfg.IngressProbeURL = strings.TrimSpace(cfg.IngressProbeURL)
	cfg.IngressContainer = strings.TrimSpace(cfg.IngressContainer)
	cfg.ACMEEmail = strings.TrimSpace(cfg.ACMEEmail)
	cfg.ACMEStateDir = strings.TrimSpace(cfg.ACMEStateDir)
	if cfg.ACMEStateDir == "" {
		cfg.ACMEStateDir = defaultWorkerACMEStateDir
	}
	cfg.ACMEEnvironment = strings.ToLower(strings.TrimSpace(cfg.ACMEEnvironment))
	if cfg.ACMEEnvironment == "" {
		cfg.ACMEEnvironment = defaultWorkerACMEEnvironment
	}
	if cfg.ACMEEnabled && cfg.RouteCertResolver == "" {
		cfg.RouteCertResolver = "letsencrypt-" + cfg.ACMEEnvironment
	}
	cfg.ACMEDNSProvider = strings.ToLower(strings.TrimSpace(cfg.ACMEDNSProvider))
	cfg.ACMEResolvers = normalizeStringList(cfg.ACMEResolvers)
	cfg.DNSProvider = strings.ToLower(strings.TrimSpace(cfg.DNSProvider))
	cfg.DNSZoneID = strings.TrimSpace(cfg.DNSZoneID)
	cfg.DNSZoneName = strings.Trim(strings.ToLower(strings.TrimSpace(cfg.DNSZoneName)), ".")
	cfg.DNSRouteTarget = strings.Trim(strings.ToLower(strings.TrimSpace(cfg.DNSRouteTarget)), ".")
	if cfg.DNSRouteTarget == "" {
		cfg.DNSRouteTarget = strings.Trim(strings.ToLower(cfg.AppsDomain), ".")
	}
	cfg.DNSIPv4 = strings.TrimSpace(cfg.DNSIPv4)
	cfg.DNSIPv6 = strings.TrimSpace(cfg.DNSIPv6)
	cfg.DNSCloudflareToken = strings.TrimSpace(cfg.DNSCloudflareToken)
	cfg.TCPProxyBindHost = strings.TrimSpace(cfg.TCPProxyBindHost)
	cfg.TCPProxyContainer = strings.TrimSpace(cfg.TCPProxyContainer)
	cfg.TCPProxyController = strings.TrimSpace(cfg.TCPProxyController)
}

func serveWorker(ctx context.Context, cfg workerConfig) error {
	loaded, err := loadWorkerDSN(cfg)
	if err != nil {
		return err
	}
	pool, err := pg.Open(ctx, pg.PoolConfig{
		DSN:            loaded.DSN,
		MaxConns:       4,
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres ping: %w", err)
	}
	if !cfg.SkipMigrations {
		if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
	}
	stopAuditCheckpoints, err := startWorkerAuditCheckpoints(ctx, pool, cfg)
	if err != nil {
		return err
	}
	defer stopAuditCheckpoints()
	usageReconciler := metering.NewPostgresStore(pool)
	go runWorkerUsageReconciler(ctx, usageReconciler, cfg.UsageReconcile, func(result metering.ScheduledReconcileResult, err error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "usage reconciliation failed workspaces=%d workspace_errors=%d: %v\n", result.WorkspacesExamined, result.WorkspaceErrors, err)
			return
		}
		if result.ProducerTimeouts > 0 || result.ProducerBounds > 0 {
			fmt.Fprintf(os.Stderr, "usage reconciliation completed workspaces=%d timeouts=%d bounded=%d\n", result.WorkspacesExamined, result.ProducerTimeouts, result.ProducerBounds)
		}
	})
	store := queue.NewPostgresStore(pool)
	// Wrap the Postgres operation store with the observer fan-out so any
	// FAILED transition emits OnFailed to registered observers. The
	// diagnosis runner (when wired) registers here without coupling the
	// FSM caller to the agent track. See ADR-0025.
	observedStore := api.NewObservedOperationStore(api.NewPostgresOperationStore(pool))
	var operationStore api.OperationStore = observedStore
	registerWorkerLifecycleObservers(observedStore, pool)
	// Canonical deployment mirror (stage 2): drives the deployments-table row
	// from the deploy-job outcome. Nil-safe downstream.
	var deploymentStore api.DeploymentStore = api.NewPostgresDeploymentStore(pool)
	telemetryStore := observability.NewPostgresStore(pool)
	variableKeyProvider, err := secrets.NewFileKeyProvider(secrets.FileKEKConfig{})
	if err != nil {
		return fmt.Errorf("load variable KEK: %w", err)
	}
	variableEnvelopeStore := secrets.NewPostgresEnvelopeStore(pool)
	variableVault := secrets.NewVaultWithStore(variableKeyProvider, secrets.DenyByDefault{}, variableEnvelopeStore)
	decryptAudit := secrets.NewPostgresDecryptAuditSink(
		audit.NewPostgresChain(pool),
		cfg.WorkerID,
	)
	baseExecutor, cleanup, err := buildDeployExecutor(
		ctx,
		cfg,
		workerOperationUpdater{
			store:       operationStore,
			deployments: deploymentStore,
		},
		defaultExecutorWiringDeps(),
	)
	if err != nil {
		return err
	}
	defer cleanup()
	baseExecutor.Observer = workerOperationEventObserver{
		store:       operationStore,
		deployments: deploymentStore,
		telemetry:   telemetryStore,
	}
	// Resolve encrypted variables immediately before Schedule. The queue job
	// remains value-free; only the in-memory ScheduleRequest receives the
	// rendered KEY=VALUE slice.
	baseExecutor.Variables = varrefs.NewResolver(
		secrets.NewAuditedRuntimeLookup(variableVault, variableEnvelopeStore, decryptAudit),
	)
	// Config is resolved from Postgres immediately before execution and bound
	// to the canonical deployment as an immutable, metadata-only snapshot.
	// Runtime policy is never accepted from the queue payload.
	baseExecutor.Configuration = serviceconfig.NewStore(pool)
	baseExecutor.Instances = deploy.NewInstanceStore(pool)
	baseExecutor.CanonicalCommitter = workerCanonicalCommitter{store: deploymentStore}
	var instanceResolver deploy.InstanceRuntimeResolver
	if resolver, ok := baseExecutor.Runtime.(deploy.InstanceRuntimeResolver); ok {
		instanceResolver = resolver
		if err := baseExecutor.Instances.ReconcileRuntime(ctx, instanceResolver); err != nil {
			return fmt.Errorf("reconcile durable deployment instances: %w", err)
		}
	}
	templateRuns := templatepkg.NewPostgresRunStore(pool)
	baseExecutor.TemplateRuns = templateRuns
	if templateRuntime, ok := baseExecutor.Runtime.(deploy.TemplateVerificationRuntime); ok {
		baseExecutor.TemplateVerifier = &deploy.OfficialDatabaseTemplateVerifier{Runtime: templateRuntime}
	} else if cfg.RuntimeBackend == "docker" {
		return errors.New("Docker runtime does not implement official template verification")
	}
	if predeployRuntime, ok := baseExecutor.Runtime.(deploy.PredeployRunner); ok {
		baseExecutor.Predeploy = &deploy.PredeployExecutor{
			Store:  deploy.NewPostgresPredeployStore(pool),
			Runner: predeployRuntime,
		}
		cronExecutor := &deploy.CronExecutor{
			Store:     deploy.NewPostgresCronStore(pool),
			Runner:    predeployRuntime,
			Variables: baseExecutor.Variables,
		}
		baseExecutor.Cron = cronExecutor.Store
		// Cron execution is intentionally independent of the deploy queue:
		// cursor/occurrence rows are the durable work source after activation.
		// A restart simply observes any retained deterministic one-off.
		if err := cronExecutor.RunDue(ctx); err != nil {
			return fmt.Errorf("initial cron execution: %w", err)
		}
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				if err := cronExecutor.RunDue(ctx); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "cron execution failed: %v\n", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	} else if cfg.RuntimeBackend == "docker" {
		return errors.New("Docker runtime does not implement crash-safe predeploy execution")
	}
	var telemetryDone <-chan struct{}
	if cfg.RuntimeBackend == "docker" {
		collector, err := observability.NewDockerCollector(cfg.DockerHost, telemetryStore, cfg.WorkerID)
		if err != nil {
			return fmt.Errorf("wire Docker observability collector: %w", err)
		}
		defer func() { _ = collector.Close() }()
		done := make(chan struct{})
		telemetryDone = done
		go func() {
			defer close(done)
			collector.Run(ctx, func(err error) {
				fmt.Fprintf(os.Stderr, "Docker observability collection failed: %v\n", err)
			})
		}()
	}
	monitorStore := monitors.NewPostgresStore(pool)
	monitorEvaluator := monitors.Evaluator{
		Store: monitorStore, Metrics: telemetryStore,
		WorkerID: cfg.WorkerID + "-monitors",
	}
	monitorDeliveries := monitors.DeliveryWorker{
		Store: monitorStore, Secrets: monitors.VaultSecretResolver{Vault: variableVault},
		WorkerID: cfg.WorkerID + "-monitor-delivery",
	}
	volumeStore := midnightvolume.NewPostgresStore(pool)
	volumeRecoveryWorker, err := buildVolumeBackupWorker(ctx, cfg, pool, volumeStore)
	if err != nil {
		return err
	}
	var volumeDriver *midnightvolume.DockerDriver
	if volumeRuntime, ok := baseExecutor.Runtime.(workerVolumeRuntime); ok {
		volumeDriver = midnightvolume.NewDockerDriver(workerVolumeClient{runtime: volumeRuntime})
	}
	// Always wire the durable store. A non-Docker runtime has no volume driver
	// and therefore fails closed only when a service actually declares a
	// volume; stateless local-mode deploys remain available.
	volumeManager := midnightvolume.NewManager(volumeStore, volumeDriver)
	if observer, ok := baseExecutor.Runtime.(workerVolumeObserver); ok {
		observed, err := observer.ObserveVolumeAttachments(ctx)
		if err != nil {
			return fmt.Errorf("observe Docker volume attachments: %w", err)
		}
		recovered := make([]midnightvolume.ObservedAttachment, 0, len(observed))
		for _, item := range observed {
			recovered = append(recovered, midnightvolume.ObservedAttachment{
				VolumeID:      item.VolumeID,
				DeploymentID:  item.DeploymentID,
				InstanceID:    item.InstanceID,
				ProjectID:     item.ProjectID,
				ServiceID:     item.ServiceID,
				EnvironmentID: item.EnvironmentID,
				Source:        item.Source,
				MountPath:     item.MountPath,
				ReadOnly:      item.ReadOnly,
			})
		}
		if err := volumeManager.ReconcileObserved(ctx, recovered); err != nil {
			return fmt.Errorf("reconcile Docker volume attachments: %w", err)
		}
	}
	baseExecutor.Volumes = volumeManager
	// Runtime actions share the deploy queue but are a different durable job
	// schema. Keep their executor separate from the immutable deploy executor so
	// stop/restart/remove can never be mistaken for a deploy that should promote
	// a new active pointer.
	controlRuntime, ok := baseExecutor.Runtime.(deploy.RuntimeActionRuntime)
	if !ok {
		return errors.New("worker runtime does not implement durable runtime actions")
	}
	actionExecutor := deploy.ActionExecutor{
		Store:         api.NewPostgresDeploymentActionStore(pool),
		Runtime:       controlRuntime,
		Instances:     baseExecutor.Instances,
		Operations:    workerOperationUpdater{store: operationStore},
		Cancels:       workerOperationUpdater{store: operationStore},
		StopGrace:     defaultDeployDrainGrace,
		Configuration: baseExecutor.Configuration,
		Readiness:     baseExecutor.Readiness,
	}
	if cfg.TCPProxyBindHost != "" {
		networkClient, clientErr := tcpproxy.NewDockerNetworkClient(cfg.DockerHost)
		if clientErr != nil {
			return fmt.Errorf("TCP proxy Docker network client: %w", clientErr)
		}
		defer networkClient.Close()
		controller := &tcpproxy.Controller{
			Store:        tcpproxy.NewPostgresStore(pool),
			Resolver:     tcpproxy.PostgresBackendResolver{Pool: pool},
			Enroller:     tcpproxy.DockerNetworkEnroller{Client: networkClient, Container: cfg.TCPProxyContainer},
			BindHost:     cfg.TCPProxyBindHost,
			ControllerID: cfg.TCPProxyController,
		}
		go tcpproxy.RunController(ctx, controller, cfg.TCPProxyInterval, func(err error) {
			fmt.Fprintf(os.Stderr, "TCP proxy reconciliation failed: %v\n", err)
		})
	}
	var routeStore *router.PostgresDurableRouteStore
	if strings.TrimSpace(cfg.RouteConfigDir) != "" || cfg.ACMEEnabled {
		routeStore = router.NewPostgresDurableRouteStore(pool)
		repair, repairErr := routeStore.ReconcileManagedDeploymentRouteState(ctx)
		if repairErr != nil {
			return fmt.Errorf("reconcile managed deployment route state: %w", repairErr)
		}
		if repair.IntentBindingsAdded > 0 || repair.BackendGroupsChanged > 0 {
			fmt.Fprintf(
				os.Stderr,
				"reconciled managed deployment route state groups=%d bindings=%d backend_groups=%d\n",
				repair.GroupsScanned,
				repair.IntentBindingsAdded,
				repair.BackendGroupsChanged,
			)
		}
	}
	if cfg.DNSEnabled {
		provider, providerErr := midnightdns.NewCloudflareProvider(midnightdns.CloudflareConfig{
			Token: cfg.DNSCloudflareToken, Timeout: cfg.DNSHTTPTimeout,
		})
		if providerErr != nil {
			return fmt.Errorf("configure Cloudflare DNS provider: %w", providerErr)
		}
		var ipv4, ipv6 netip.Addr
		if cfg.DNSIPv4 != "" {
			ipv4 = netip.MustParseAddr(cfg.DNSIPv4)
		}
		if cfg.DNSIPv6 != "" {
			ipv6 = netip.MustParseAddr(cfg.DNSIPv6)
		}
		ownershipStore := midnightdns.NewPostgresOwnershipStore(pool)
		controller := &midnightdns.Controller{
			Reconciler: &midnightdns.Reconciler{
				Provider: provider,
				Store:    ownershipStore,
				Audit: midnightdns.ChainAuditSink{
					Chain: audit.NewPostgresChain(pool), ActorID: cfg.WorkerID,
				},
			},
			Intents: ownershipStore,
			Config: midnightdns.ControllerConfig{
				ZoneID: cfg.DNSZoneID, ZoneName: cfg.DNSZoneName,
				RouteTarget: cfg.DNSRouteTarget, IPv4: ipv4, IPv6: ipv6,
				TTL: cfg.DNSTTL, DryRun: !cfg.DNSApply,
			},
		}
		go midnightdns.RunController(ctx, controller, cfg.DNSInterval, func(err error) {
			fmt.Fprintf(os.Stderr, "DNS reconciliation failed: %v\n", err)
		})
	}
	if cfg.ACMEEnabled {
		lifecycle, lifecycleErr := newWorkerDomainLifecycleController(cfg, routeStore)
		if lifecycleErr != nil {
			return lifecycleErr
		}
		go router.RunDomainLifecycleController(ctx, lifecycle, cfg.ACMEInterval, func(err error) {
			fmt.Fprintf(os.Stderr, "domain lifecycle reconciliation failed: %v\n", err)
		})
	}
	if strings.TrimSpace(cfg.RouteConfigDir) != "" {
		directory, err := router.NewTraefikDynamicPublicationDirectory(cfg.RouteConfigDir)
		if err != nil {
			return fmt.Errorf("durable route publication directory: %w", err)
		}
		reconciler := &router.DurableRouteReconciler{
			Store: routeStore, Publisher: directory, CertResolver: cfg.RouteCertResolver,
			Verifier: &router.HTTPTraefikPublicationVerifier{
				TraefikAPIBaseURL: cfg.TraefikAPIURL,
				IngressBaseURL:    cfg.IngressProbeURL,
			},
		}
		// Ingress enrollment attaches a containerised reverse proxy to each
		// per-environment Docker bridge so it can reach backends. An ingress
		// that is not a container — Traefik run as a host service, which is
		// how the Debian package installs it — already reaches container
		// addresses directly and has nothing to enroll. Wiring the enroller
		// unconditionally made every publish fail with "No such container"
		// on those hosts, because the enroller rejects both a missing
		// container and an empty name. Leaving MIDNIGHT_WORKER_INGRESS_CONTAINER
		// empty now selects the host-ingress mode; the HTTP publication
		// verifier still has to see the route live before it is accepted, so
		// the proof of reachability is unchanged.
		if cfg.RuntimeBackend == "docker" && strings.TrimSpace(cfg.IngressContainer) != "" {
			ingressClient, clientErr := router.NewDockerTrustedIngressClient(cfg.DockerHost)
			if clientErr != nil {
				return fmt.Errorf("trusted ingress Docker client: %w", clientErr)
			}
			defer ingressClient.Close()
			reconciler.Enroller = &router.DockerTrustedIngressEnroller{
				Client: ingressClient, Store: routeStore, IngressContainer: cfg.IngressContainer,
			}
		}
		baseExecutor.RouteIntent = deploy.DurableRouteIntentCoordinator{
			Store: routeStore,
		}
		baseExecutor.Promoter = deploy.DurableRoutePromoter{
			Store: routeStore, Reconciler: reconciler,
		}
		actionExecutor.Routes = deploy.DurableActionRouteWithdrawer{
			Store: routeStore, Reconciler: reconciler,
			Readiness: baseExecutor.Readiness,
		}
		go router.RunDurableRouteController(ctx, reconciler, router.DefaultDurableRouteReconcileInterval, func(err error) {
			fmt.Fprintf(os.Stderr, "durable route reconciliation failed: %v\n", err)
		})
	}
	executor := multiplexDeployExecutor{deploy: baseExecutor, action: actionExecutor}
	fmt.Printf("midnight-worker serving %s as %s (dsn_source=%s runtime=%s ingress=%s route_configured=%t acme_configured=%t dns_configured=%t dns_apply=%t tcp_proxy_configured=%t)\n", version.Version, cfg.WorkerID, loaded.Source, cfg.RuntimeBackend, ingressModeLabel(cfg), cfg.RouteConfigDir != "", cfg.ACMEEnabled, cfg.DNSEnabled, cfg.DNSEnabled && cfg.DNSApply, cfg.TCPProxyBindHost != "")
	if _, err := store.Reclaim(ctx, time.Now().UTC()); err != nil {
		return err
	}
	reclaimTicker := time.NewTicker(cfg.ReclaimInterval)
	defer reclaimTicker.Stop()
	pollTicker := time.NewTicker(cfg.PollInterval)
	defer pollTicker.Stop()
	monitorTicker := time.NewTicker(15 * time.Second)
	defer monitorTicker.Stop()
	if _, err := monitorEvaluator.RunOnce(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "monitor evaluation failed: %v\n", err)
	}
	if _, err := monitorDeliveries.RunOnce(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "monitor delivery failed: %v\n", err)
	}
	if _, err := volumeRecoveryWorker.enqueueDuePolicies(ctx, time.Now().UTC()); err != nil {
		fmt.Fprintf(os.Stderr, "volume backup policy scheduling failed: %v\n", err)
	}
	deployPool := newDeployJobPool(cfg.DeployConcurrency)
	reportDeployJobError := func(err error) {
		fmt.Fprintf(os.Stderr, "deploy job processing failed: %v\n", err)
	}
	for {
		select {
		case <-ctx.Done():
			// In-flight jobs observe the canceled context and return; wait so
			// their lease bookkeeping and canonical reconciliation finish.
			deployPool.wait()
			if telemetryDone != nil {
				select {
				case <-telemetryDone:
				case <-time.After(2 * time.Second):
				}
			}
			return nil
		case now := <-reclaimTicker.C:
			if _, err := store.Reclaim(ctx, now.UTC()); err != nil {
				return err
			}
		case now := <-pollTicker.C:
			if _, err := pumpDeployJobs(ctx, deployPool, store, cfg.WorkerID, cfg.LeaseTTL, now.UTC(), executor, deploymentStore, reportDeployJobError); err != nil {
				fmt.Fprintf(os.Stderr, "deploy job leasing failed: %v\n", err)
			}
			if _, err := processOneVolumeBackupJob(ctx, store, cfg.WorkerID, cfg.LeaseTTL, now.UTC(), volumeRecoveryWorker); err != nil {
				fmt.Fprintf(os.Stderr, "volume backup job processing failed: %v\n", err)
			}
		case <-monitorTicker.C:
			if instanceResolver != nil {
				if err := baseExecutor.Instances.ReconcileRuntime(ctx, instanceResolver); err != nil {
					fmt.Fprintf(os.Stderr, "deployment instance reconciliation failed: %v\n", err)
				}
			}
			if _, err := monitorEvaluator.RunOnce(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "monitor evaluation failed: %v\n", err)
			}
			if _, err := monitorDeliveries.RunOnce(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "monitor delivery failed: %v\n", err)
			}
			if _, err := volumeRecoveryWorker.enqueueDuePolicies(ctx, time.Now().UTC()); err != nil {
				fmt.Fprintf(os.Stderr, "volume backup policy scheduling failed: %v\n", err)
			}
		}
	}
}

type workerUsageReconciler interface {
	ReconcileScheduled(context.Context, metering.ScheduledReconcileRequest) (metering.ScheduledReconcileResult, error)
}

func runWorkerUsageReconciler(
	ctx context.Context,
	reconciler workerUsageReconciler,
	cfg workerUsageReconcileConfig,
	report func(metering.ScheduledReconcileResult, error),
) {
	runOnce := func() {
		now := time.Now().UTC()
		passCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
		result, err := reconciler.ReconcileScheduled(passCtx, metering.ScheduledReconcileRequest{
			Since:         now.Add(-cfg.Lookback),
			Until:         now,
			MaxWorkspaces: cfg.WorkspaceBatch,
			StageTimeout:  cfg.StageTimeout,
		})
		if ctx.Err() == nil && report != nil {
			report(result, err)
		}
	}
	runOnce()
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

type workerVolumeRuntime interface {
	VolumeCreate(context.Context, docker.VolumeCreateOptions) (dockervolume.Volume, error)
	VolumeInspect(context.Context, string) (dockervolume.Volume, error)
	VolumeRemove(context.Context, string, bool) error
}

type workerVolumeObserver interface {
	ObserveVolumeAttachments(context.Context) ([]runtime.ObservedVolumeAttachment, error)
}

type workerVolumeClient struct {
	runtime workerVolumeRuntime
}

func (c workerVolumeClient) VolumeCreate(ctx context.Context, name, driver string, labels, opts map[string]string) (midnightvolume.DockerInfo, error) {
	created, err := c.runtime.VolumeCreate(ctx, docker.VolumeCreateOptions{
		Name:       name,
		Driver:     driver,
		Labels:     labels,
		DriverOpts: opts,
	})
	if err != nil {
		return midnightvolume.DockerInfo{}, err
	}
	return workerDockerInfo(created), nil
}

func (c workerVolumeClient) VolumeInspect(ctx context.Context, name string) (midnightvolume.DockerInfo, error) {
	observed, err := c.runtime.VolumeInspect(ctx, name)
	if err != nil {
		return midnightvolume.DockerInfo{}, err
	}
	return workerDockerInfo(observed), nil
}

func (c workerVolumeClient) VolumeRemove(ctx context.Context, name string, force bool) error {
	return c.runtime.VolumeRemove(ctx, name, force)
}

func workerDockerInfo(observed dockervolume.Volume) midnightvolume.DockerInfo {
	return midnightvolume.DockerInfo{
		Name:       observed.Name,
		Driver:     observed.Driver,
		Labels:     observed.Labels,
		CreatedAt:  observed.CreatedAt,
		Mountpoint: observed.Mountpoint,
	}
}

type executorWiringDeps struct {
	newLocalRuntime  func() deploy.ActivationRuntime
	newDockerEngine  func(host string) (docker.EngineClient, error)
	newDockerRuntime func(context.Context, docker.Config) (deploy.ActivationRuntime, error)
	newRoutePromoter func(workerConfig) (deploy.RoutePromoter, error)
}

func defaultExecutorWiringDeps() executorWiringDeps {
	return executorWiringDeps{
		newLocalRuntime: func() deploy.ActivationRuntime { return local.New() },
		newDockerEngine: docker.NewEngineClient,
		newDockerRuntime: func(ctx context.Context, cfg docker.Config) (deploy.ActivationRuntime, error) {
			adapter, report, err := docker.New(ctx, cfg)
			if err != nil {
				return nil, err
			}
			for _, warning := range report.Warnings {
				fmt.Fprintf(os.Stderr, "docker runtime adapter: %s\n", warning)
			}
			return adapter, nil
		},
		newRoutePromoter: newWorkerRoutePromoter,
	}
}

func buildDeployExecutor(ctx context.Context, cfg workerConfig, operations deploy.OperationUpdater, deps executorWiringDeps) (deploy.Executor, func(), error) {
	if deps.newLocalRuntime == nil || deps.newDockerEngine == nil || deps.newDockerRuntime == nil || deps.newRoutePromoter == nil {
		return deploy.Executor{}, nil, errors.New("worker deploy executor: wiring dependencies are required")
	}
	runtimeAdapter, cleanup, err := buildWorkerRuntime(ctx, cfg, deps)
	if err != nil {
		return deploy.Executor{}, nil, err
	}
	promoter := deploy.RoutePromoter(deploy.NoopRoutePromoter{})
	if strings.TrimSpace(cfg.RouteConfigDir) != "" {
		promoter, err = deps.newRoutePromoter(cfg)
		if err != nil {
			cleanup()
			return deploy.Executor{}, nil, err
		}
	}
	sched, err := buildWorkerScheduler(cfg, runtimeAdapter)
	if err != nil {
		cleanup()
		return deploy.Executor{}, nil, err
	}
	executor := deploy.Executor{
		Scheduler:  sched,
		Runtime:    runtimeAdapter,
		Promoter:   promoter,
		Operations: operations,
		DrainGrace: defaultDeployDrainGrace,
	}
	if cancels, ok := operations.(deploy.CancelChecker); ok {
		executor.Cancels = cancels
	}
	return executor, cleanup, nil
}

// buildWorkerScheduler resolves the MIDNIGHT_SCHEDULER selector. The default
// is LocalDockerScheduler, which preserves byte-identical behavior with the
// pre-ADR-0018 executor. The "multihost" selector wires the stub package so
// developers can exercise placement + desired-state writes against the
// existing hosts table; it is intentionally NOT a silent fallback to local
// — Reconcile/Drain return scheduler.ErrNotImplemented until the agent
// status callback path lands.
func buildWorkerScheduler(cfg workerConfig, runtimeAdapter deploy.ActivationRuntime) (deployScheduler, error) {
	selector, err := scheduler.ParseSelector(cfg.SchedulerBackend)
	if err != nil {
		return nil, fmt.Errorf("scheduler backend %q: %w", cfg.SchedulerBackend, err)
	}
	switch selector {
	case scheduler.SelectorLocal:
		return schedulerlocal.New(runtimeAdapter), nil
	case scheduler.SelectorMultiHost:
		// The multihost stub requires concrete HostRepository +
		// DesiredStateWriter wiring against Postgres. The wiring will be
		// added when the multi-host execution path is filled in
		// (ADR-0018). Until then this returns the stub with nil
		// dependencies so Schedule() fails loudly at the boundary.
		return multihost.New(nil, nil), nil
	default:
		return nil, fmt.Errorf("scheduler backend %q: %w", selector, scheduler.ErrUnknownSelector)
	}
}

// deployScheduler is the local alias for scheduler.Scheduler. It exists so
// the wiring code does not have to repeat the long import path on the
// return type.
type deployScheduler = scheduler.Scheduler

func buildWorkerRuntime(ctx context.Context, cfg workerConfig, deps executorWiringDeps) (deploy.ActivationRuntime, func(), error) {
	switch cfg.RuntimeBackend {
	case "local":
		return deps.newLocalRuntime(), func() {}, nil
	case "docker":
		engine, err := deps.newDockerEngine(cfg.DockerHost)
		if err != nil {
			return nil, nil, fmt.Errorf("docker engine client: %w", err)
		}
		cleanup := func() { _ = engine.Close() }
		baselineMode, err := docker.ParseBaselineMode(cfg.DockerBaseline)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		adapter, err := deps.newDockerRuntime(ctx, docker.Config{
			AgentID:        cfg.AgentID,
			HostID:         cfg.HostID,
			Engine:         engine,
			BaselineMode:   baselineMode,
			ProbeContainer: strings.TrimSpace(cfg.ProbeContainer),
		})
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("docker runtime adapter: %w", err)
		}
		if verifier := signatureVerifierFromConfig(cfg); verifier != nil {
			target, ok := adapter.(interface {
				SetSignatureVerifier(docker.SignatureVerifier)
			})
			if !ok {
				cleanup()
				return nil, nil, errors.New("docker runtime adapter does not support signature verifier injection")
			}
			target.SetSignatureVerifier(verifier)
		}
		if err := recoverWorkerRuntimeState(ctx, adapter); err != nil {
			cleanup()
			return nil, nil, err
		}
		return adapter, cleanup, nil
	default:
		return nil, nil, errors.New("runtime backend must be local or docker")
	}
}

func recoverWorkerRuntimeState(ctx context.Context, adapter deploy.ActivationRuntime) error {
	reconciler, ok := adapter.(interface {
		Reconcile(context.Context) error
	})
	if !ok {
		return nil
	}
	if err := reconciler.Reconcile(ctx); err != nil {
		return fmt.Errorf("docker runtime recovery reconcile: %w", err)
	}
	return nil
}

func signatureVerifierFromConfig(cfg workerConfig) docker.SignatureVerifier {
	if cfg.VerifySignatures {
		return docker.CosignExecVerifier{
			BinaryPath:                cfg.CosignBinaryPath,
			PublicKeyPath:             cfg.CosignPublicKey,
			CertificateIdentityRegexp: cfg.CosignIdentity,
			CertificateOIDCIssuer:     cfg.CosignOIDCIssuer,
			FulcioRoot:                cfg.CosignFulcioRoot,
			VerifyTimeout:             cfg.CosignTimeout,
		}
	}
	var verifiers []docker.SignatureVerifier
	if cfg.TrustLocalRegistry {
		verifiers = append(verifiers, docker.LocalRegistryDigestVerifier{Endpoint: cfg.LocalRegistry})
	}
	if len(cfg.TrustedImages) > 0 {
		signed := make(map[string]struct{}, len(cfg.TrustedImages))
		for _, image := range cfg.TrustedImages {
			signed[image] = struct{}{}
		}
		verifiers = append(verifiers, docker.StaticSignatureVerifier{Signed: signed})
	}
	if len(verifiers) == 0 {
		// No image-trust policy is configured, so enforcement stays off
		// exactly as before. Composing the official-image policy here would
		// silently fail-close every deploy on an instance that never asked
		// for image trust.
		return nil
	}
	// A trust policy is active, so Midnight's own shipped digests join it.
	// They are compiled into this binary from internal/templates, which means
	// the pin *is* the integrity guarantee: re-declaring the same digest in
	// MIDNIGHT_WORKER_TRUSTED_IMAGES adds no security, and omitting it breaks
	// the one-click official Postgres/Redis/MySQL/MongoDB templates on a
	// default install. The operator allowlist and the local-registry policy
	// keep working unchanged alongside it.
	verifiers = append(verifiers, officialImageSignatureVerifier())
	for _, line := range imageTrustPolicySummary(cfg) {
		fmt.Println(line)
	}
	if len(verifiers) == 1 {
		return verifiers[0]
	}
	return docker.AnySignatureVerifier{Verifiers: verifiers}
}

// officialImageSignatureVerifier trusts the exact digest-pinned images this
// release ships (the official template catalogue in internal/templates).
func officialImageSignatureVerifier() docker.SignatureVerifier {
	references := templatepkg.OfficialImageReferences()
	signed := make(map[string]struct{}, len(references))
	for _, reference := range references {
		signed[reference] = struct{}{}
	}
	return docker.StaticSignatureVerifier{Signed: signed}
}

// imageTrustPolicySummary renders the active trust policy so an operator can
// see what is trusted and why without reading the source. It is printed once,
// at verifier construction.
func imageTrustPolicySummary(cfg workerConfig) []string {
	lines := []string{"image trust policy: composed (an image is admitted when any listed policy accepts it)"}
	if cfg.TrustLocalRegistry {
		lines = append(lines, fmt.Sprintf(
			"  local-registry digests: endpoint=%s reason=MIDNIGHT_WORKER_TRUST_LOCAL_REGISTRY",
			cfg.LocalRegistry,
		))
	}
	if len(cfg.TrustedImages) > 0 {
		lines = append(lines, fmt.Sprintf(
			"  operator allowlist: %d image(s) reason=MIDNIGHT_WORKER_TRUSTED_IMAGES",
			len(cfg.TrustedImages),
		))
		for _, image := range cfg.TrustedImages {
			lines = append(lines, "    "+image)
		}
	}
	official := templatepkg.OfficialImages()
	lines = append(lines, fmt.Sprintf(
		"  official shipped digests: %d image(s) reason=compiled into this release from internal/templates",
		len(official),
	))
	for _, image := range official {
		lines = append(lines, fmt.Sprintf("    %s (%s)", image.Reference, strings.Join(image.Sources, ", ")))
	}
	return lines
}

type deployJobExecutor interface {
	Execute(ctx context.Context, job queue.Job) (deploy.ExecutionOutcome, error)
}

// multiplexDeployExecutor routes by the versioned queue payload rather than
// by a mutable job attribute. This lets both immutable image replays and
// runtime-control actions use the durable deploy queue without widening the
// deploy executor's contract.
type multiplexDeployExecutor struct {
	deploy deployJobExecutor
	action deployJobExecutor
}

// workerCanonicalCommitter is the required CAS boundary between a ready
// candidate route and retiring the prior deployment. DurableRoutePromoter
// invokes it while it still retains the prior backend set for rollback.
type workerCanonicalCommitter struct{ store api.DeploymentStore }

type templatePromotionStore interface {
	PromoteTemplateDeployment(context.Context, string, string) error
}

type cronPromotionStore interface {
	PromoteCronDeployment(context.Context, string, string, deploy.CronActivation) error
}

type deploymentPromotionProofStore interface {
	DeploymentPromotionCommitted(context.Context, string, string, string) (bool, error)
}

type templateDeploymentPromotionProofStore interface {
	TemplateDeploymentPromotionCommitted(context.Context, string, string, string) (bool, error)
}

func (c workerCanonicalCommitter) CommitCanonicalDeployment(ctx context.Context, operationID string) error {
	if c.store == nil || operationID == "" {
		return errors.New("canonical deployment store or operation id is missing")
	}
	record, err := c.store.GetDeploymentByOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if record.State == deploy.DeploymentActive {
		matched, verifyErr := verifyCommittedDeploymentPromotion(
			ctx,
			c.store,
			operationID,
			record,
		)
		if verifyErr != nil {
			return verifyErr
		}
		if matched {
			return nil
		}
	}
	if record.State != deploy.DeploymentDeploying {
		return fmt.Errorf("canonical deployment %s is %s, want deploying", record.ID, record.State)
	}
	err = c.store.PromoteDeployment(ctx, record.ID, record.ExpectedActiveID)
	if !errors.Is(err, pg.ErrTxCommitIndeterminate) {
		return err
	}
	matched, verifyErr := verifyCommittedDeploymentPromotion(
		ctx,
		c.store,
		operationID,
		record,
	)
	if verifyErr != nil {
		return errors.Join(err, verifyErr)
	}
	if matched {
		return nil
	}
	return err
}

func (c workerCanonicalCommitter) CommitCanonicalTemplateDeployment(ctx context.Context, operationID, deploymentID string) error {
	if c.store == nil || operationID == "" || deploymentID == "" {
		return errors.New("canonical template deployment store, operation id, or deployment id is missing")
	}
	record, err := c.store.GetDeploymentByOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if record.ID != deploymentID {
		return fmt.Errorf("canonical template deployment %s does not match verified deployment %s", record.ID, deploymentID)
	}
	if record.State == deploy.DeploymentActive {
		matched, verifyErr := verifyCommittedTemplateDeploymentPromotion(
			ctx,
			c.store,
			operationID,
			record,
		)
		if verifyErr != nil {
			return verifyErr
		}
		if matched {
			return nil
		}
	}
	if record.State != deploy.DeploymentDeploying {
		return fmt.Errorf("canonical template deployment %s is %s, want deploying", record.ID, record.State)
	}
	store, ok := c.store.(templatePromotionStore)
	if !ok {
		return errors.New("canonical deployment store does not support atomic template promotion")
	}
	err = store.PromoteTemplateDeployment(ctx, record.ID, record.ExpectedActiveID)
	if !errors.Is(err, pg.ErrTxCommitIndeterminate) {
		return err
	}
	matched, verifyErr := verifyCommittedTemplateDeploymentPromotion(
		ctx,
		c.store,
		operationID,
		record,
	)
	if verifyErr != nil {
		return errors.Join(err, verifyErr)
	}
	if matched {
		return nil
	}
	return err
}

func verifyCommittedDeploymentPromotion(
	ctx context.Context,
	store api.DeploymentStore,
	operationID string,
	record api.DeploymentRecord,
) (bool, error) {
	verifier, ok := store.(deploymentPromotionProofStore)
	if !ok {
		return false, nil
	}
	return verifier.DeploymentPromotionCommitted(
		ctx,
		operationID,
		record.ID,
		record.ExpectedActiveID,
	)
}

func verifyCommittedTemplateDeploymentPromotion(
	ctx context.Context,
	store api.DeploymentStore,
	operationID string,
	record api.DeploymentRecord,
) (bool, error) {
	verifier, ok := store.(templateDeploymentPromotionProofStore)
	if !ok {
		return false, nil
	}
	return verifier.TemplateDeploymentPromotionCommitted(
		ctx,
		operationID,
		record.ID,
		record.ExpectedActiveID,
	)
}

func (c workerCanonicalCommitter) CommitCanonicalCronDeployment(ctx context.Context, operationID string, activation deploy.CronActivation) error {
	if c.store == nil || operationID == "" {
		return errors.New("canonical cron deployment store or operation id is missing")
	}
	record, err := c.store.GetDeploymentByOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if record.ID != activation.Candidate.DeploymentID || record.ID != activation.Snapshot.DeploymentID {
		return fmt.Errorf("canonical cron deployment %s does not match activation", record.ID)
	}
	if record.State != deploy.DeploymentDeploying && record.State != deploy.DeploymentActive {
		return fmt.Errorf("canonical cron deployment %s is %s, want deploying or matching active retry", record.ID, record.State)
	}
	store, ok := c.store.(cronPromotionStore)
	if !ok {
		return errors.New("canonical deployment store does not support atomic cron promotion")
	}
	return store.PromoteCronDeployment(ctx, record.ID, record.ExpectedActiveID, activation)
}

func (e multiplexDeployExecutor) Execute(ctx context.Context, job queue.Job) (deploy.ExecutionOutcome, error) {
	if deploy.IsDeploymentActionJob(job.Payload) {
		if e.action == nil {
			return deploy.ExecutionOutcome{}, errors.New("worker runtime action executor is not configured")
		}
		return e.action.Execute(ctx, job)
	}
	if e.deploy == nil {
		return deploy.ExecutionOutcome{}, errors.New("worker deploy executor is not configured")
	}
	return e.deploy.Execute(ctx, job)
}

func processLeasedDeployJob(ctx context.Context, store queue.Store, job queue.Job, leaseTTL time.Duration, executor deployJobExecutor, deploymentStore api.DeploymentStore) (deploy.ExecutionOutcome, error) {
	return processLeasedDeployJobSerialized(ctx, store, job, leaseTTL, executor, deploymentStore, nil)
}

// processLeasedDeployJobSerialized executes one leased job. When ticket is
// non-nil the job first waits for its turn on the per-service FIFO while its
// lease heartbeat is already running, so sibling rollouts queue behind each
// other in lease order without losing their lease.
func processLeasedDeployJobSerialized(ctx context.Context, store queue.Store, job queue.Job, leaseTTL time.Duration, executor deployJobExecutor, deploymentStore api.DeploymentStore, ticket *keyTicket) (deploy.ExecutionOutcome, error) {
	if store == nil {
		return deploy.ExecutionOutcome{}, errors.New("worker deploy job: queue store is required")
	}
	if executor == nil {
		return deploy.ExecutionOutcome{}, errors.New("worker deploy job: executor is required")
	}
	execCtx, stopHeartbeat, err := startDeployLeaseHeartbeat(ctx, store, job, leaseTTL)
	if err != nil {
		return deploy.ExecutionOutcome{}, err
	}
	if ticket != nil {
		select {
		case <-ticket.ready:
			defer ticket.release()
		case <-execCtx.Done():
			ticket.abandon()
			_ = stopHeartbeat()
			return deploy.ExecutionOutcome{}, execCtx.Err()
		}
	}
	if err := prepareCanonicalDeployment(execCtx, deploymentStore, job); err != nil {
		_ = stopHeartbeat()
		if failErr := store.Fail(ctx, job.ID, job.LeaseID, string(failurecodes.ReconcileStepFailed)); failErr != nil {
			return deploy.ExecutionOutcome{}, errors.Join(err, failErr)
		}
		return deploy.ExecutionOutcome{}, err
	}
	outcome, err := executor.Execute(execCtx, job)
	heartbeatErr := stopHeartbeat()
	// Success is committed by the executor's canonical-commit boundary before
	// it drains prior replicas. Reconciliation records only failures; it must
	// never re-promote or silently ignore a successful CAS.
	if err != nil {
		reconcileCanonicalDeployment(ctx, deploymentStore, outcome)
	}
	if err != nil {
		reason := string(outcome.FailureCode)
		if reason == "" {
			reason = string(failurecodes.InternalError)
		}
		if failErr := store.Fail(ctx, job.ID, job.LeaseID, reason); failErr != nil {
			return outcome, errors.Join(err, failErr)
		}
		return outcome, errors.Join(err, heartbeatErr)
	}
	if err := store.Complete(ctx, job.ID, job.LeaseID); err != nil {
		return outcome, err
	}
	return outcome, heartbeatErr
}

func prepareCanonicalDeployment(ctx context.Context, store api.DeploymentStore, job queue.Job) error {
	if store == nil || deploy.IsDeploymentActionJob(job.Payload) {
		return nil
	}
	payload, _, err := deploy.DecodeDeployJobPayload(job.Payload)
	if err != nil {
		return err
	}
	record, err := store.GetDeploymentByOperation(ctx, payload.OperationID)
	if err != nil {
		return err
	}
	switch record.State {
	case deploy.DeploymentQueued, deploy.DeploymentBuilding:
		return store.MarkDeploymentDeploying(ctx, record.ID, record.State)
	case deploy.DeploymentDeploying:
		return nil
	default:
		return fmt.Errorf("canonical deployment %s is %s, cannot execute", record.ID, record.State)
	}
}

func processOneDeployJob(ctx context.Context, store queue.Store, workerID string, leaseTTL time.Duration, now time.Time, executor deployJobExecutor, deploymentStore api.DeploymentStore) (bool, error) {
	if store == nil {
		return false, errors.New("worker deploy job: queue store is required")
	}
	job, err := store.Lease(ctx, queue.LeaseRequest{
		Queue:     queue.Deploy,
		WorkerID:  workerID,
		LeaseTime: leaseTTL,
		Now:       now,
	})
	if errors.Is(err, queue.ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = processLeasedDeployJob(ctx, store, job, leaseTTL, executor, deploymentStore)
	return true, err
}

// reconcileCanonicalDeployment records a deploy job's outcome on the canonical
// `deployments` row linked to the operation. Best-effort and nil-safe: it drives
// queued -> deploying -> active on success, or queued|deploying -> failed on
// failure, and no-ops when there is no linked mirror (memory-mode / source-build
// re-entry) or the row already moved past the expected state.
func reconcileCanonicalDeployment(ctx context.Context, store api.DeploymentStore, outcome deploy.ExecutionOutcome) {
	if store == nil || outcome.OperationID == "" {
		return
	}
	dep, err := store.GetDeploymentByOperation(ctx, outcome.OperationID)
	if err != nil {
		return
	}
	if outcome.Succeeded {
		return
	}
	code := string(outcome.FailureCode)
	if code == "" {
		code = "deploy_failed"
	}
	_ = store.FailDeployment(ctx, dep.ID, dep.State, code, outcome.FailureReason)
}

func startDeployLeaseHeartbeat(ctx context.Context, store queue.Store, job queue.Job, leaseTTL time.Duration) (context.Context, func() error, error) {
	interval, err := queue.HeartbeatInterval(leaseTTL)
	if err != nil {
		return nil, nil, err
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case now := <-ticker.C:
				if _, err := queue.HeartbeatLease(heartbeatCtx, store, job, leaseTTL, now.UTC()); err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	stop := func() error {
		cancel()
		<-done
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	}
	return heartbeatCtx, stop, nil
}

type workerOperationEventObserver struct {
	store       api.OperationStore
	deployments api.DeploymentStore
	telemetry   observability.Store
}

func (o workerOperationEventObserver) ObserveDeployStep(ctx context.Context, event deploy.DeployStepEvent) {
	if o.store == nil || event.OperationID == "" || event.Step == "" || event.State == "" {
		return
	}
	message := "deploy worker step " + string(event.Step) + " " + string(event.State)
	if strings.TrimSpace(event.JobID) != "" {
		message += " job_id=" + strings.TrimSpace(event.JobID)
	}
	if strings.TrimSpace(event.DeploymentID) != "" {
		message += " deployment_id=" + strings.TrimSpace(event.DeploymentID)
	}
	if strings.TrimSpace(event.InstanceID) != "" {
		message += " instance_id=" + strings.TrimSpace(event.InstanceID)
	}
	if event.FailureCode != "" {
		message += ": code=" + string(event.FailureCode)
	}
	if strings.TrimSpace(event.FailureReason) != "" {
		message += ": " + strings.TrimSpace(event.FailureReason)
	}
	if event.Route != nil {
		if strings.TrimSpace(event.Route.ServiceID) != "" {
			message += " service_id=" + strings.TrimSpace(event.Route.ServiceID)
		}
		if strings.TrimSpace(event.Route.RouteGroup) != "" {
			message += " route_group=" + strings.TrimSpace(event.Route.RouteGroup)
		}
		if strings.TrimSpace(event.Route.RouteName) != "" {
			message += " route_name=" + strings.TrimSpace(event.Route.RouteName)
		}
		if strings.TrimSpace(event.Route.Host) != "" {
			message += " host=" + strings.TrimSpace(event.Route.Host)
		}
		if strings.TrimSpace(event.Route.PathPrefix) != "" {
			message += " path_prefix=" + strings.TrimSpace(event.Route.PathPrefix)
		}
		if strings.TrimSpace(event.Route.HostID) != "" {
			message += " host_id=" + strings.TrimSpace(event.Route.HostID)
		}
		if strings.TrimSpace(event.Route.RoutingTargetID) != "" {
			message += " routing_target_id=" + strings.TrimSpace(event.Route.RoutingTargetID)
		}
		if strings.TrimSpace(event.Route.ContainerID) != "" {
			message += " container_id=" + strings.TrimSpace(event.Route.ContainerID)
		}
		if strings.TrimSpace(event.Route.ContainerIP) != "" {
			message += " container_ip=" + strings.TrimSpace(event.Route.ContainerIP)
		}
		if event.Route.InternalPort > 0 {
			message += " internal_port=" + strconv.Itoa(event.Route.InternalPort)
		}
	}
	state := orailtypes.OperationRunning
	switch event.Step {
	case deploy.DeployStepSucceeded:
		state = orailtypes.OperationSucceeded
	case deploy.DeployStepFailed:
		state = orailtypes.OperationFailed
	case deploy.DeployStepCanceled:
		state = orailtypes.OperationCanceled
	}
	saved, err := o.store.AppendEvent(ctx, api.OperationEvent{
		OperationID: orailtypes.ID(event.OperationID),
		State:       state,
		Message:     message,
	})
	if err != nil || o.deployments == nil || o.telemetry == nil {
		return
	}
	deployment, err := o.deployments.GetDeploymentByOperation(ctx, event.OperationID)
	if err != nil {
		return
	}
	level := "info"
	if event.Step == deploy.DeployStepFailed {
		level = "error"
	}
	attributes := map[string]any{
		"step":  string(event.Step),
		"state": string(event.State),
	}
	if event.JobID != "" {
		attributes["job_id"] = event.JobID
	}
	if event.FailureCode != "" {
		attributes["failure_code"] = string(event.FailureCode)
	}
	if event.FailureReason != "" {
		attributes["failure_reason"] = event.FailureReason
	}
	_, _ = o.telemetry.IngestLog(ctx, observability.LogRecord{
		ObservedAt: saved.CreatedAt,
		Scope: observability.Scope{
			ProjectID:     deployment.ProjectID,
			EnvironmentID: deployment.EnvironmentID,
			ServiceID:     deployment.ServiceID,
			DeploymentID:  deployment.ID,
			OperationID:   event.OperationID,
			ReplicaID:     event.InstanceID,
		},
		Surface:        "platform",
		Stream:         "deploy",
		Level:          level,
		Component:      "midnight-worker",
		Message:        message,
		Attributes:     attributes,
		SourceKey:      "deploy-operation:" + event.OperationID,
		SourceSequence: strconv.Itoa(saved.Seq),
	})
}

type workerOperationUpdater struct {
	store       api.OperationStore
	deployments api.DeploymentStore
}

func (u workerOperationUpdater) Transition(ctx context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	var operation orailtypes.Operation
	var err error
	cancelStore, coordinatedCancel := u.deployments.(api.CanonicalDeploymentOperationCancelStore)
	if state == orailtypes.OperationCanceled && coordinatedCancel {
		operation, err = cancelStore.CancelDeploymentOperation(ctx, id)
	} else {
		operation, err = u.store.Transition(ctx, id, state)
	}
	if err != nil {
		return orailtypes.Operation{}, err
	}
	_, _ = u.store.AppendEvent(ctx, api.OperationEvent{OperationID: operation.ID, State: operation.State, Message: "deploy worker transitioned operation to " + string(state)})
	return operation, nil
}

func (u workerOperationUpdater) Fail(ctx context.Context, id orailtypes.ID, code orailtypes.FailureCode, reason string) (orailtypes.Operation, error) {
	operation, err := u.store.Fail(ctx, id, code, reason)
	if err != nil {
		return orailtypes.Operation{}, err
	}
	_, _ = u.store.AppendEvent(ctx, api.OperationEvent{OperationID: operation.ID, State: operation.State, Message: "deploy worker failed operation: " + reason})
	return operation, nil
}

func (u workerOperationUpdater) CancelRequested(ctx context.Context, operationID string) (bool, error) {
	events, err := u.store.ListEvents(ctx, orailtypes.ID(operationID))
	if err != nil {
		return false, err
	}
	for _, event := range events {
		message := strings.ToLower(strings.TrimSpace(event.Message))
		if strings.Contains(message, "cancel requested") && strings.Contains(message, "before traffic promotion") {
			return true, nil
		}
	}
	return false, nil
}

func newWorkerRoutePromoter(cfg workerConfig) (deploy.RoutePromoter, error) {
	directory, err := router.NewTraefikDynamicPublicationDirectory(cfg.RouteConfigDir)
	if err != nil {
		return nil, fmt.Errorf("route publication directory: %w", err)
	}
	if strings.TrimSpace(cfg.RoutePublicHost) == "" {
		// Production replaces this compatibility promoter with the durable
		// route controller after Postgres is available.
		return deploy.NoopRoutePromoter{}, nil
	}
	targetPort := cfg.RouteTargetPort
	if targetPort <= 0 {
		targetPort = defaultWorkerRouteTargetPort
	}
	return deploy.TraefikRoutePromoter{
		Publisher: directory,
		Metadata: deploy.RoutePromotionMetadata{
			RouteGroup:   cfg.RouteGroup,
			Host:         cfg.RoutePublicHost,
			PathPrefix:   cfg.RoutePublicPath,
			TLS:          cfg.RoutePublicTLS,
			CertResolver: cfg.RouteCertResolver,
			InternalPort: targetPort,
		},
	}, nil
}

func loadWorkerDSN(cfg workerConfig) (pg.LoadedDSN, error) {
	if strings.TrimSpace(cfg.PostgresDSN) != "" {
		return pg.LoadedDSN{DSN: strings.TrimSpace(cfg.PostgresDSN), Source: pg.DSNSourceEnv}, nil
	}
	return (&pg.FileDSNLoader{EnvVar: pg.EnvDSN, EnvVarRef: pg.EnvDSNFile, Path: cfg.PostgresDSNFile}).Load()
}

func envFirst(fallback string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return fallback
}

func envBool(fallback bool, names ...string) bool {
	for _, name := range names {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			continue
		}
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func envInt(fallback int, names ...string) int {
	for _, name := range names {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			continue
		}
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func envDuration(fallback time.Duration, names ...string) time.Duration {
	for _, name := range names {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			continue
		}
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func envList(names ...string) []string {
	for _, name := range names {
		values := splitStringList(os.Getenv(name))
		if len(values) > 0 {
			return values
		}
	}
	return nil
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, splitStringList(value)...)
	return nil
}

func splitStringList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func normalizeStringList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, item := range splitStringList(value) {
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

// registerWorkerLifecycleObservers attaches the diagnosis runner (and any
// future operation-failure observers) to the worker's observed store.
//
// The runner stays off unless MIDNIGHT_AGENT_DIAGNOSIS_ENABLED is set,
// which is the default: a build failure should not silently start
// spending money on a host nobody configured for it. When the toggle is
// on, the runner needs both an agent Caller and somewhere to persist the
// diagnosis — it refuses to run with either missing — so both are wired
// here rather than left as a follow-up. If the provider key is absent
// the runner is registered in its inert state and says so once at
// startup, instead of failing every deployment's failure path silently.
func registerWorkerLifecycleObservers(store *api.ObservedOperationStore, pool *pg.Pool) {
	if store == nil {
		return
	}
	opts := diagnosis.Options{}
	if enabled := diagnosis.NewRunner(diagnosis.Options{}).Enabled(); enabled {
		caller, model, err := newWorkerDiagnosisCaller(tools.NewDefaultRegistry())
		opts.Model = model
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr,
				"build-failure diagnosis is enabled (%s) but no agent provider is configured (%v); diagnosis will not run\n",
				diagnosis.EnvEnabled, err)
		case pool == nil:
			fmt.Fprintf(os.Stderr,
				"build-failure diagnosis is enabled (%s) but no database pool is available; diagnosis will not run\n",
				diagnosis.EnvEnabled)
		default:
			diagnosisCaller, adapterErr := diagnosis.NewAgentCaller(caller, diagnosis.AgentCallerOptions{})
			if adapterErr != nil {
				fmt.Fprintf(os.Stderr, "build-failure diagnosis caller adapter failed: %v\n", adapterErr)
				break
			}
			service, svcErr := conversation.NewService(conversation.Config{
				Store: conversation.NewPostgresStore(pool),
			})
			if svcErr != nil {
				fmt.Fprintf(os.Stderr, "build-failure diagnosis conversation store failed: %v\n", svcErr)
				break
			}
			opts.Caller = diagnosisCaller
			opts.Conversations = diagnosis.NewConversationStore(service, "agent:diagnosis", model)
		}
	}
	runner := diagnosis.NewRunner(opts)
	store.Register(runner)
}

// ingressModeLabel reports how the worker expects to reach the ingress, so an
// operator can see at a glance whether route publication will try to enroll a
// container into each environment bridge.
func ingressModeLabel(cfg workerConfig) string {
	if strings.TrimSpace(cfg.IngressContainer) == "" {
		return "host"
	}
	return "container:" + strings.TrimSpace(cfg.IngressContainer)
}
