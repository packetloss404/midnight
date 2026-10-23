package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/midnight/midnight/internal/agent"
	agentauth "github.com/midnight/midnight/internal/agent/auth"
	"github.com/midnight/midnight/internal/agent/claude"
	"github.com/midnight/midnight/internal/agent/conversation"
	agentopenai "github.com/midnight/midnight/internal/agent/openai"
	"github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/api"
	apimiddleware "github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/auth"
	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/backup"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/graphql"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	logrecords "github.com/midnight/midnight/internal/logs"
	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/internal/redaction"
	"github.com/midnight/midnight/internal/scheduler"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/internal/tcpproxy"
	"github.com/midnight/midnight/internal/variableintent"
	"github.com/midnight/midnight/internal/version"
	"github.com/midnight/midnight/internal/webhook"
	"github.com/midnight/midnight/migrations"
)

const defaultListenAddr = "127.0.0.1:8080"
const defaultStorageMode = "auto"

// envAllowMemoryFallback opts `--storage=auto` in to the ephemeral in-memory
// platform when no Postgres DSN can be resolved. See buildServerHandler.
const envAllowMemoryFallback = "MIDNIGHT_ALLOW_MEMORY_FALLBACK" // not-a-failure-code

// memoryModeBanner is printed on every start that runs without Postgres. It is
// deliberately loud and unabbreviated: memory mode looks healthy from the
// outside (the listener accepts, reads answer) while every durable write path
// is absent, so an operator who lands here by accident must be told.
const memoryModeBanner = "WARNING: storage=memory — ALL DATA IS EPHEMERAL and is lost when this process exits.\n" +
	"WARNING: canonical deployments and their state machine, deployment actions (rollback/restart), the\n" +
	"WARNING: observability store, tenant human-write coordinators, GitHub linking, template forks and the\n" +
	"WARNING: durable deploy outbox are DISABLED; those write paths return 501. Do not use this for a real\n" +
	"WARNING: install — run with --storage=postgres (or a resolvable $MIDNIGHT_DB_DSN) instead."
const defaultLogPattern = "*.ndjson"
const serverShutdownTimeout = 30 * time.Second
const serverUpReadTimeout = 5 * time.Minute

func main() {
	if err := run("midnight-server", os.Args[1:]); err != nil {
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
		return runServe(name, args[1:])
	}
	if len(args) > 0 && args[0] == "--listen" {
		return runServe(name, args)
	}

	fmt.Printf("%s %s\n", name, version.Version)
	return nil
}

type serveConfig struct {
	Listen                 string
	Storage                string
	PostgresDSN            string
	PostgresDSNFile        string
	CursorKeyPath          string
	AutomationTokenKeyPath string
	InvitationKeyPath      string
	VariableIntentKeyPath  string
	AgentHMACSecret        string
	AgentCredentialsFile   string
	AgentAuthDevelopment   bool
	APIBearerToken         string
	LogDir                 string
	LogPattern             string
	SchedulerBackend       string
	SkipMigrations         bool
	GraphQLPlayground      bool
	AppsDomain             string
	TraefikAccessLog       string
	RecoveryExecutor       bool
	TCPProxyBaseDomain     string
	TCPProxyPortStart      int
	TCPProxyPortEnd        int
	SourceArtifactQuota    sourceartifact.QuotaConfig
	// AllowMemoryFallback opts `--storage=auto` in to the ephemeral in-memory
	// platform when no Postgres DSN can be resolved. Without it, auto refuses
	// to start rather than booting a server whose data disappears on restart.
	AllowMemoryFallback bool
}

func runServe(name string, args []string) error {
	cfg, err := parseServeConfig(args)
	if err != nil {
		return err
	}
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	handler, cleanup, storage, err := buildServerHandler(startupCtx, cfg)
	startupCancel()
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	if storage == "memory" {
		fmt.Fprintln(os.Stderr, memoryModeBanner)
	}
	fmt.Printf(
		"%s serving %s on %s (storage=%s, console=%s)\n",
		name, version.Version, cfg.Listen, storage, api.WebConsoleStatus(),
	)
	server := newHTTPServer(cfg.Listen, handler)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		err := <-serverErr
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           apimiddleware.SecurityHeaders(serviceUpReadDeadlineMiddleware(handler)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
}

// serviceUpReadDeadlineMiddleware extends only the exact local-source upload
// body deadline. The server-wide 30-second bound remains in force for every
// other route, while the CLI's documented five-minute upload window can
// actually reach the handler on slower links.
func serviceUpReadDeadlineMiddleware(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isServiceUpRequest(r) {
			if err := http.NewResponseController(w).SetReadDeadline(
				time.Now().Add(serverUpReadTimeout),
			); err != nil {
				http.Error(w, "source upload transport is unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isServiceUpRequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodPost {
		return false
	}
	const prefix = "/api/v1alpha/services/"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == r.URL.Path || rest == "" {
		return false
	}
	serviceID, suffix, found := strings.Cut(rest, "/")
	return found && serviceID != "" && suffix == "up"
}

func parseServeConfig(args []string) (serveConfig, error) {
	cfg := serveConfig{
		Listen:          envFirst(defaultListenAddr, "MIDNIGHT_SERVER_ADDR", "MIDNIGHT_LISTEN_ADDR"),
		Storage:         envFirst(defaultStorageMode, "MIDNIGHT_STORAGE"),
		PostgresDSN:     envFirst("", pg.EnvDSN, "MIDNIGHT_DATABASE_URL"),
		PostgresDSNFile: envFirst(pg.DefaultDSNFile, pg.EnvDSNFile),
		CursorKeyPath:   envFirst(api.DefaultCursorKeyPath, api.EnvCursorKeyPath),
		AutomationTokenKeyPath: envFirst(
			automationtoken.DefaultMACKeyPath,
			automationtoken.EnvMACKeyPath,
		),
		InvitationKeyPath: envFirst(
			identity.DefaultInvitationMACKeyPath,
			identity.EnvInvitationMACKeyPath,
		),
		VariableIntentKeyPath: envFirst(
			variableintent.DefaultMACKeyPath,
			variableintent.EnvMACKeyPath,
		),
		AgentHMACSecret:      envFirst("", "MIDNIGHT_AGENT_HMAC_SECRET"),
		AgentCredentialsFile: envFirst("", "MIDNIGHT_AGENT_CREDENTIALS_FILE"), // not-a-failure-code
		AgentAuthDevelopment: envEnabled("MIDNIGHT_AGENT_AUTH_DEVELOPMENT"),   // not-a-failure-code
		APIBearerToken:       envFirst("", "MIDNIGHT_API_BEARER_TOKEN"),
		LogDir:               envFirst("", "MIDNIGHT_LOG_DIR"),
		LogPattern:           envFirst(defaultLogPattern, "MIDNIGHT_LOG_PATTERN"),
		SchedulerBackend:     envFirst(string(scheduler.SelectorLocal), scheduler.SelectorEnv),
		GraphQLPlayground:    envFirst("", "MIDNIGHT_GRAPHQL_PLAYGROUND") != "",
		AppsDomain:           envFirst("", "MIDNIGHT_APPS_DOMAIN"), // not-a-failure-code
		TraefikAccessLog:     envFirst("", "MIDNIGHT_TRAEFIK_ACCESS_LOG_PATH"),
		RecoveryExecutor:     envEnabled("MIDNIGHT_BACKUP_EXECUTOR_AVAILABLE"), // not-a-failure-code
		TCPProxyBaseDomain:   envFirst("", "MIDNIGHT_TCP_PROXY_BASE_DOMAIN"),
		TCPProxyPortStart:    envInt(tcpproxy.DefaultExternalPortStart, "MIDNIGHT_TCP_PROXY_PORT_START"),
		TCPProxyPortEnd:      envInt(tcpproxy.DefaultExternalPortEnd, "MIDNIGHT_TCP_PROXY_PORT_END"),
		AllowMemoryFallback:  envEnabled(envAllowMemoryFallback),
	}
	quotaConfig, err := sourceartifact.LoadQuotaConfig(
		os.LookupEnv,
		sourceartifact.DefaultLimits.MaxCompressedBytes,
		sourceartifact.DefaultUploadLease,
	)
	if err != nil {
		return serveConfig{}, err
	}
	cfg.SourceArtifactQuota = quotaConfig
	fs := flag.NewFlagSet("midnight-server serve", flag.ContinueOnError)
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "address to listen on")
	fs.StringVar(&cfg.Storage, "storage", cfg.Storage, "storage backend: auto, memory, or postgres")
	fs.BoolVar(
		&cfg.AllowMemoryFallback,
		"allow-memory-fallback",
		cfg.AllowMemoryFallback,
		"with --storage=auto, allow starting on ephemeral in-memory storage when no Postgres DSN is available "+
			"(default: $"+envAllowMemoryFallback+"); intended for local smoke runs only",
	)
	fs.StringVar(&cfg.PostgresDSN, "postgres-dsn", cfg.PostgresDSN, "Postgres DSN (default: $MIDNIGHT_DB_DSN)")
	fs.StringVar(&cfg.PostgresDSNFile, "postgres-dsn-file", cfg.PostgresDSNFile, "Postgres DSN file (default: $MIDNIGHT_DB_DSN_FILE or /etc/midnight/db.dsn)")
	fs.StringVar(&cfg.CursorKeyPath, "cursor-key", cfg.CursorKeyPath, "cursor signing key path")
	fs.StringVar(
		&cfg.AutomationTokenKeyPath,
		"automation-token-key",
		cfg.AutomationTokenKeyPath,
		"project automation-token MAC key path",
	)
	fs.StringVar(
		&cfg.InvitationKeyPath,
		"invitation-key",
		cfg.InvitationKeyPath,
		"identity invitation MAC key path",
	)
	fs.StringVar(
		&cfg.VariableIntentKeyPath,
		"variable-intent-key",
		cfg.VariableIntentKeyPath,
		"tenant variable intent MAC key path",
	)
	fs.StringVar(&cfg.AgentHMACSecret, "agent-hmac-secret", cfg.AgentHMACSecret, "deprecated shared agent development credential")
	fs.StringVar(&cfg.AgentCredentialsFile, "agent-credentials", cfg.AgentCredentialsFile, "JSON file containing per-agent credentials")
	fs.BoolVar(&cfg.AgentAuthDevelopment, "agent-auth-development", cfg.AgentAuthDevelopment, "allow one shared agent credential (development only)")
	fs.StringVar(&cfg.APIBearerToken, "api-bearer-token", cfg.APIBearerToken, "bearer token for dashboard and product API routes; required before production mutations")
	fs.StringVar(&cfg.LogDir, "log-dir", cfg.LogDir, "structured NDJSON log directory (default: $MIDNIGHT_LOG_DIR)")
	fs.StringVar(&cfg.LogPattern, "log-pattern", cfg.LogPattern, "structured log filename glob")
	fs.StringVar(&cfg.SchedulerBackend, "scheduler", cfg.SchedulerBackend, "scheduler backend the worker should use: local (default) or multihost; multihost is stubbed per ADR-0018 and only validates the placement path")
	fs.BoolVar(&cfg.SkipMigrations, "skip-migrations", false, "skip applying pending schema migrations at startup")
	fs.BoolVar(&cfg.GraphQLPlayground, "graphql-playground", cfg.GraphQLPlayground, "enable the GraphiQL playground at /graphiql (dev only)")
	fs.StringVar(&cfg.AppsDomain, "apps-domain", cfg.AppsDomain, "base domain for generated service hostnames (default: $MIDNIGHT_APPS_DOMAIN)")
	fs.StringVar(&cfg.TraefikAccessLog, "traefik-access-log", cfg.TraefikAccessLog, "Traefik JSON access log path (default: $MIDNIGHT_TRAEFIK_ACCESS_LOG_PATH)")
	fs.BoolVar(&cfg.RecoveryExecutor, "volume-recovery-executor", cfg.RecoveryExecutor, "report the separately configured Docker volume recovery worker as available")
	fs.StringVar(&cfg.TCPProxyBaseDomain, "tcp-proxy-base-domain", cfg.TCPProxyBaseDomain, "base domain used to describe generated TCP addresses; DNS remains operator-managed")
	fs.IntVar(&cfg.TCPProxyPortStart, "tcp-proxy-port-start", cfg.TCPProxyPortStart, "first allocatable external TCP port")
	fs.IntVar(&cfg.TCPProxyPortEnd, "tcp-proxy-port-end", cfg.TCPProxyPortEnd, "last allocatable external TCP port")
	if err := fs.Parse(args); err != nil {
		return serveConfig{}, err
	}
	cfg.APIBearerToken = strings.TrimSpace(cfg.APIBearerToken)
	if err := validateOperatorRecoveryConfig(cfg.APIBearerToken); err != nil {
		return serveConfig{}, err
	}
	cfg.Storage = strings.ToLower(strings.TrimSpace(cfg.Storage))
	if cfg.Storage == "" {
		cfg.Storage = defaultStorageMode
	}
	switch cfg.Storage {
	case "auto", "memory", "postgres":
	default:
		return serveConfig{}, fmt.Errorf("unsupported storage backend %q", cfg.Storage)
	}
	if _, err := scheduler.ParseSelector(cfg.SchedulerBackend); err != nil {
		return serveConfig{}, fmt.Errorf("scheduler backend %q: %w (valid: local, multihost)", cfg.SchedulerBackend, err)
	}
	if cfg.TCPProxyPortStart < 1024 || cfg.TCPProxyPortEnd > 65535 || cfg.TCPProxyPortStart > cfg.TCPProxyPortEnd {
		return serveConfig{}, fmt.Errorf("TCP proxy port range must be within 1024-65535 with start <= end")
	}
	if strings.TrimSpace(cfg.TCPProxyBaseDomain) == "" {
		cfg.TCPProxyBaseDomain = cfg.AppsDomain
	}
	return cfg, nil
}

func buildServerHandler(ctx context.Context, cfg serveConfig) (http.Handler, func(), string, error) {
	cfg.APIBearerToken = strings.TrimSpace(cfg.APIBearerToken)
	if err := validateOperatorRecoveryConfig(cfg.APIBearerToken); err != nil {
		return nil, nil, "", err
	}
	logStore, err := buildLogStore(cfg)
	if err != nil {
		return nil, nil, "", err
	}
	if cfg.Storage == "memory" {
		stack := api.DataStack{APIBearerToken: cfg.APIBearerToken, LogStore: logStore}
		stack, err = configureAgentControlCredentials(stack, cfg, false)
		if err != nil {
			return nil, nil, "", err
		}
		stack, err = configureGitHubApp(stack)
		if err != nil {
			return nil, nil, "", err
		}
		stack = wireAgent(stack, nil)
		stack, err := attachGraphQL(stack, cfg)
		if err != nil {
			return nil, nil, "", err
		}
		return api.NewRouterWithDataStack(stack), nil, "memory", nil
	}
	loaded, err := loadServeDSN(cfg)
	if err != nil {
		if cfg.Storage == "auto" && errors.Is(err, pg.ErrDSNUnavailable) {
			// storage=auto used to degrade to memory here, silently. A host
			// with an unmounted secret or a missing /etc/midnight/db.dsn then
			// came up "healthy" on storage that disappears on restart, with
			// most human write paths answering 501. Refuse instead, unless the
			// operator explicitly asked for the throwaway platform.
			if !cfg.AllowMemoryFallback {
				return nil, nil, "", fmt.Errorf(
					"storage=auto found no Postgres DSN (checked --postgres-dsn/$%s, $%s, and %s): "+
						"refusing to start on ephemeral in-memory storage, where deployments, rollbacks, "+
						"observability history, tenant writes, GitHub linking and the deploy outbox are all "+
						"disabled. Configure a DSN for a real install, or opt in to the throwaway in-memory "+
						"platform with --storage=memory (or %s=1): %w",
					pg.EnvDSN, pg.EnvDSNFile, cfg.PostgresDSNFile, envAllowMemoryFallback, err,
				)
			}
			stack := api.DataStack{APIBearerToken: cfg.APIBearerToken, LogStore: logStore}
			stack, err = configureAgentControlCredentials(stack, cfg, false)
			if err != nil {
				return nil, nil, "", err
			}
			stack, err = configureGitHubApp(stack)
			if err != nil {
				return nil, nil, "", err
			}
			stack = wireAgent(stack, nil)
			stack, err := attachGraphQL(stack, cfg)
			if err != nil {
				return nil, nil, "", err
			}
			return api.NewRouterWithDataStack(stack), nil, "memory", nil
		}
		return nil, nil, "", err
	}
	pool, err := pg.Open(ctx, pg.PoolConfig{
		DSN:            loaded.DSN,
		MaxConns:       16,
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		return nil, nil, "", err
	}
	cleanup := func() { pool.Close() }
	if err := pool.Ping(ctx); err != nil {
		cleanup()
		return nil, nil, "", fmt.Errorf("postgres ping: %w", err)
	}
	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	if !cfg.SkipMigrations {
		if _, err := runner.Up(ctx); err != nil {
			cleanup()
			return nil, nil, "", fmt.Errorf("apply migrations: %w", err)
		}
	}
	cursorSecret, err := (&api.FileCursorSecretSource{Path: cfg.CursorKeyPath, EnvVar: api.EnvCursorKeyPath}).Load()
	if err != nil {
		cleanup()
		return nil, nil, "", err
	}
	automationTokenMACKey, err := (&automationtoken.FileMACKeySource{
		Path: cfg.AutomationTokenKeyPath,
	}).Load()
	if err != nil {
		cleanup()
		return nil, nil, "", err
	}
	defer clear(automationTokenMACKey)
	invitationMACKey, err := (&identity.FileInvitationMACKeySource{
		Path: cfg.InvitationKeyPath,
	}).Load()
	if err != nil {
		cleanup()
		return nil, nil, "", err
	}
	defer clear(invitationMACKey)
	variableIntentMACKey, err := (&variableintent.FileMACKeySource{
		Path: cfg.VariableIntentKeyPath,
	}).Load()
	if err != nil {
		cleanup()
		return nil, nil, "", err
	}
	defer clear(variableIntentMACKey)
	variableKeyProvider, err := secrets.NewFileKeyProvider(secrets.FileKEKConfig{})
	if err != nil {
		cleanup()
		return nil, nil, "", fmt.Errorf("load variable KEK: %w", err)
	}
	sourceArtifactRoot := strings.TrimSpace(os.Getenv("MIDNIGHT_UP_EXTRACTION_ROOT"))
	stack, err := api.NewPostgresDataStack(api.PostgresDataStackConfig{
		Pool:                  pool,
		MigrationsFS:          migrations.FS,
		ManifestPath:          "manifest.yaml",
		CursorSecret:          cursorSecret,
		AgentHMACSecret:       []byte(cfg.AgentHMACSecret),
		AutomationTokenMACKey: automationTokenMACKey,
		InvitationMACKey:      invitationMACKey,
		VariableIntentMACKey:  variableIntentMACKey,
		VariableKeyProvider:   variableKeyProvider,
		SourceArtifactRoot:    sourceArtifactRoot,
		SourceArtifactQuota:   cfg.SourceArtifactQuota,
		AppsDomain:            cfg.AppsDomain,
		TCPProxyBaseDomain:    cfg.TCPProxyBaseDomain,
		TCPProxyPorts: tcpproxy.PortRange{
			Start: cfg.TCPProxyPortStart,
			End:   cfg.TCPProxyPortEnd,
		},
	})
	if err != nil {
		cleanup()
		return nil, nil, "", err
	}
	stack.APIBearerToken = cfg.APIBearerToken
	stack.ProductionAuthRequired = true
	stack, err = configureAgentControlCredentials(stack, cfg, true)
	if err != nil {
		cleanup()
		return nil, nil, "", err
	}
	stack.BackupDestinationURI = strings.TrimSpace(os.Getenv(backup.EnvBackupDestinationURI))
	stack.VolumeRecoveryExecutorAvailable = cfg.RecoveryExecutor
	if logStore != nil {
		log.Printf("structured file log store is retained for memory mode; Postgres mode uses the durable scoped observability store")
	}
	stack, err = configureGitHubApp(stack)
	if err != nil {
		cleanup()
		return nil, nil, "", err
	}
	if stack.WebhookInbox != nil {
		stack.WebhookDispatcher = api.NewDefaultGitHubWebhookDispatcherForStack(stack)
	}
	stack = wireAgent(stack, pool)
	stack, gerr := attachGraphQL(stack, cfg)
	if gerr != nil {
		cleanup()
		return nil, nil, "", gerr
	}
	dispatcherCtx, stopDispatcher := context.WithCancel(context.Background())
	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		var dispatchers sync.WaitGroup
		dispatcherCount := 3
		if stack.PreviewLifecycle != nil {
			dispatcherCount++
		}
		var webhookWorker *webhook.Worker
		var webhookPayloadPurger webhook.TerminalPayloadPurger
		if stack.WebhookInbox != nil {
			planner, plannerOK := stack.WebhookDispatcher.(webhook.Planner)
			targetDispatcher, dispatcherOK := stack.WebhookDispatcher.(webhook.TargetDispatcher)
			if plannerOK && dispatcherOK {
				webhookWorker = &webhook.Worker{
					Store:      stack.WebhookInbox,
					Planner:    planner,
					Dispatcher: targetDispatcher,
					OnError: func(err error) {
						log.Printf("webhook inbox worker: %v", err)
					},
				}
				dispatcherCount++
			}
			if purger, ok := stack.WebhookInbox.(webhook.TerminalPayloadPurger); ok {
				webhookPayloadPurger = purger
				dispatcherCount++
			}
		}
		if strings.TrimSpace(cfg.TraefikAccessLog) != "" {
			dispatcherCount++
		}
		dispatchers.Add(dispatcherCount)
		go func() {
			defer dispatchers.Done()
			api.RunDeployOutboxDispatcher(dispatcherCtx, stack.DeployIntakeOutbox, 0, func(err error) {
				log.Printf("deploy outbox dispatcher: %v", err)
			})
		}()
		if stack.PreviewLifecycle != nil {
			go func() {
				defer dispatchers.Done()
				api.RunPreviewLifecycleReconciler(dispatcherCtx, stack.PreviewLifecycle, 0, func(err error) {
					log.Printf("preview lifecycle reconciler: %v", err)
				})
			}()
		}
		go func() {
			defer dispatchers.Done()
			api.RunStagedWorkDispatcher(dispatcherCtx, stack.StagedWorkOutbox, 0, func(err error) {
				log.Printf("staged work dispatcher: %v", err)
			})
		}()
		go func() {
			defer dispatchers.Done()
			observability.RunRetention(dispatcherCtx, stack.ObservabilityStore, observability.DefaultRetention, func(err error) {
				log.Printf("observability retention: %v", err)
			})
		}()
		if webhookWorker != nil {
			go func() {
				defer dispatchers.Done()
				webhookWorker.Run(dispatcherCtx)
			}()
		}
		if webhookPayloadPurger != nil {
			go func() {
				defer dispatchers.Done()
				webhook.RunPayloadRetention(
					dispatcherCtx,
					webhookPayloadPurger,
					webhook.DefaultTerminalPayloadRetention,
					func(err error) {
						log.Printf("webhook payload retention: %v", err)
					},
				)
			}()
		}
		if strings.TrimSpace(cfg.TraefikAccessLog) != "" {
			go func() {
				defer dispatchers.Done()
				store, ok := stack.ObservabilityStore.(*observability.PostgresStore)
				if !ok {
					log.Printf("Traefik observability collector: Postgres observability store is unavailable")
					return
				}
				collector := &observability.TraefikFileCollector{
					Path:  cfg.TraefikAccessLog,
					ID:    "midnight-server",
					Store: store,
					Resolver: observability.PostgresTraefikScopeResolver{
						Pool: pool,
					},
				}
				collector.Run(dispatcherCtx, func(err error) {
					log.Printf("Traefik observability collector: %v", err)
				})
			}()
		}
		dispatchers.Wait()
	}()
	closePool := cleanup
	cleanup = func() {
		stopDispatcher()
		select {
		case <-dispatcherDone:
		case <-time.After(2 * time.Second):
			log.Printf("background dispatchers did not stop before pool shutdown")
		}
		closePool()
	}
	return api.NewRouterWithDataStack(stack), cleanup, fmt.Sprintf("postgres:%s", loaded.Source), nil
}

const defaultDevelopmentAgentCredential = "midnight-local-development-agent-credential"

func configureAgentControlCredentials(stack api.DataStack, cfg serveConfig, production bool) (api.DataStack, error) {
	if path := strings.TrimSpace(cfg.AgentCredentialsFile); path != "" {
		credentials, err := api.LoadAgentCredentialFile(path)
		if err != nil {
			return api.DataStack{}, err
		}
		stack.AgentCredentials = credentials
		stack.AgentHMACSecret = nil
		return stack, nil
	}
	if production && !cfg.AgentAuthDevelopment {
		return api.DataStack{}, errors.New("production agent authentication requires --agent-credentials or MIDNIGHT_AGENT_CREDENTIALS_FILE")
	}
	credential := strings.TrimSpace(cfg.AgentHMACSecret)
	if credential == "" {
		credential = defaultDevelopmentAgentCredential
	}
	stack.AgentCredentials = api.NewDevelopmentAgentCredentials([]byte(credential))
	stack.AgentHMACSecret = []byte(credential)
	return stack, nil
}

func validateOperatorRecoveryConfig(token string) error {
	if err := auth.ValidateOperatorRecoveryToken(token); err != nil {
		return fmt.Errorf("invalid MIDNIGHT_API_BEARER_TOKEN/--api-bearer-token configuration: %w", err)
	}
	return nil
}

// configureGitHubApp wires the production REST discovery client and a stable
// inbound webhook endpoint from one operator-owned App configuration. It is
// optional when every App variable is absent. A partial configuration fails
// startup instead of advertising a GitHub connection that cannot authenticate
// or verify deliveries.
func configureGitHubApp(stack api.DataStack) (api.DataStack, error) {
	appID := strings.TrimSpace(os.Getenv("MIDNIGHT_GITHUB_APP_ID"))
	keyPath := strings.TrimSpace(os.Getenv("MIDNIGHT_GITHUB_PRIVATE_KEY_PATH"))
	webhookSecret := strings.TrimSpace(os.Getenv("MIDNIGHT_GITHUB_WEBHOOK_SECRET"))
	setupVariables := []string{
		"MIDNIGHT_GITHUB_CLIENT_ID",
		"MIDNIGHT_GITHUB_CLIENT_SECRET",
		"MIDNIGHT_GITHUB_APP_SLUG",
		"MIDNIGHT_GITHUB_CALLBACK_URL",
		"MIDNIGHT_GITHUB_WEB_BASE_URL",
	}
	setupConfigured := false
	for _, name := range setupVariables {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			setupConfigured = true
			break
		}
	}
	if appID == "" && keyPath == "" && webhookSecret == "" && !setupConfigured {
		return stack, nil
	}
	cfg, err := githubapp.LoadAppConfig(os.Getenv, os.ReadFile)
	if err != nil {
		return api.DataStack{}, fmt.Errorf("github app configuration: %w", err)
	}
	client, err := githubapp.NewAppClient(cfg)
	if err != nil {
		return api.DataStack{}, fmt.Errorf("github app client: %w", err)
	}
	stack.GitHubApp = client
	stack.GitHubAppConfig = &cfg
	if coordinator, ok := stack.TenantGitHubSourceLinks.(*api.PostgresTenantGitHubSourceLinkCoordinator); ok {
		coordinator.Verifier = client
	}
	if coordinator, ok := stack.GitHubGrantRecovery.(*api.PostgresGitHubGrantRecoveryCoordinator); ok {
		coordinator.Verifier = client
	}
	if coordinator, ok := stack.GitHubSetup.(*api.PostgresGitHubSetupCoordinator); ok {
		coordinator.Verifier = client
		coordinator.Config = cfg
	}
	if setupConfigured {
		if err := cfg.ValidateSetup(); err != nil {
			return api.DataStack{}, fmt.Errorf("github app setup configuration: %w", err)
		}
	}
	if stack.WebhookEndpoints == nil {
		endpointID := envFirst("github", "MIDNIGHT_GITHUB_WEBHOOK_ENDPOINT_ID")
		resolver := webhook.NewMemoryEndpointResolver()
		resolver.Set(endpointID, webhook.EndpointConfig{
			Provider: webhook.ProviderGitHub,
			Keys: []webhook.Key{{
				KID:      "github-app",
				Material: []byte(cfg.WebhookSecret),
			}},
		})
		stack.WebhookEndpoints = resolver
	}
	return stack, nil
}

// wireAgent populates the agent-related fields on the supplied DataStack so
// the chat panel works end-to-end whenever the selected provider's API key is
// set.
//
// When pool is non-nil, the conversation store is the Postgres-backed
// implementation; otherwise the in-memory store is used (suitable for
// `--storage=memory` mode and the auto-fallback path).
//
// When the selected provider key is unset, the caller is left nil and a
// single warning is logged at startup. The chat panel then surfaces the
// "caller is not configured" error from conversation.Service.Send;
// every other surface (REST CRUD, audit, etc.) stays usable.
func wireAgent(stack api.DataStack, pool *pg.Pool) api.DataStack {
	// 1. Construct the shared tools registry once.
	registry := tools.NewDefaultRegistry()

	// 2. Construct the Caller. Failures are non-fatal — the server
	// stays usable for everything except chat sends.
	provider := agentProvider()
	caller, err := newAgentCaller(provider, registry)
	switch {
	case errors.Is(err, agent.AgentAPIKeyMissing):
		log.Print(agentKeyMissingWarning(provider))
		caller = nil
	case err != nil:
		log.Printf("agent: failed to construct %s caller: %v; chat panel will return caller-not-configured on send", provider, err)
		caller = nil
	}
	stack.AgentCaller = caller
	stack.AgentChatModel = agentChatModel(provider)
	// Mount the MCP surface on the same registry so external MCP clients and
	// the in-product chat share one tool set. Without this the documented
	// /mcp/v1 and /.well-known/mcp routes were never registered.
	if stack.MCPRegistry == nil {
		stack.MCPRegistry = registry
	}

	// 3. Construct the conversation store.
	if stack.ConversationStore == nil {
		if pool != nil {
			stack.ConversationStore = conversation.NewPostgresStore(pool)
		} else {
			stack.ConversationStore = conversation.NewMemoryStore(nil)
		}
	}

	// 4. Wire the auth seams. The router fills these in with the same
	// defaults if left nil, but pinning them here keeps the bootstrap
	// path explicit and gives operators a single source of truth.
	if stack.AgentAuthorizer == nil {
		stack.AgentAuthorizer = agentauth.OpenAuthorizer{}
	}
	if stack.AgentMutationGate == nil {
		stack.AgentMutationGate = agentauth.NewEnvMutationGate()
	}
	if stack.AgentBudget == nil {
		stack.AgentBudget = agentauth.NewMemoryBudget(agentauth.LoadBudgetConfig())
	}
	if stack.AgentRevoker == nil {
		stack.AgentRevoker = agentauth.NewMemorySessionRevoker()
	}
	// The audit sink writes through the existing AuditChain so the
	// tamper-evident chain stays the single source of truth. When the
	// stack was assembled without an AuditChain (e.g. --storage=memory
	// before the router has filled in its default), leave the field nil
	// and let the router's nil-default code path cover it.
	if stack.AgentAuditSink == nil && stack.AuditChain != nil {
		stack.AgentAuditSink = agentauth.NewAuditChainSink(stack.AuditChain)
	}

	// 5. Principal extractor for the chat handler. The protected route
	// middleware derives this actor from verified credentials and binds it to
	// the request context; caller-controlled headers are not an identity seam.
	if stack.AgentPrincipalFromRequest == nil {
		stack.AgentPrincipalFromRequest = func(r *http.Request) string {
			return string(idempotency.ActorFromContext(r.Context()))
		}
	}

	return stack
}

const (
	agentProviderEnv  = "MIDNIGHT_AGENT_PROVIDER"
	providerAnthropic = "anthropic"
	providerClaude    = "claude"
	providerOpenAI    = "openai"
	providerMiniMax   = "minimax"

	envMiniMaxAPIKey    = "MINIMAX_API_KEY"
	envMiniMaxBaseURL   = "MIDNIGHT_MINIMAX_BASE_URL"
	envMiniMaxChatModel = "MIDNIGHT_MINIMAX_CHAT_MODEL"

	defaultMiniMaxBaseURL   = "https://api.minimax.io/anthropic"
	defaultMiniMaxChatModel = "MiniMax-M2.7"
)

func agentProvider() string {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv(agentProviderEnv)))
	if provider == "" {
		return providerAnthropic
	}
	return provider
}

func newAgentCaller(provider string, registry *tools.Registry) (agent.Caller, error) {
	metrics := agent.MetricsEmitterFunc(logAgentUsage)
	switch provider {
	case providerAnthropic, providerClaude:
		return claude.NewCaller(claude.Options{
			APIKey:       os.Getenv(claude.EnvAPIKey),
			Registry:     registry,
			DefaultModel: envFirst(string(claude.DefaultChatModel), claude.EnvChatModel),
			Metrics:      metrics,
		})
	case providerOpenAI:
		return agentopenai.NewCaller(agentopenai.Options{
			APIKey:       os.Getenv(agentopenai.EnvAPIKey),
			BaseURL:      os.Getenv(agentopenai.EnvBaseURL),
			Registry:     registry,
			DefaultModel: envFirst(agentopenai.DefaultChatModel, agentopenai.EnvChatModel),
			Metrics:      metrics,
		})
	case providerMiniMax:
		return claude.NewCaller(claude.Options{
			APIKey:       os.Getenv(envMiniMaxAPIKey),
			BaseURL:      envFirst(defaultMiniMaxBaseURL, envMiniMaxBaseURL),
			Registry:     registry,
			DefaultModel: envFirst(defaultMiniMaxChatModel, envMiniMaxChatModel),
			Metrics:      metrics,
		})
	default:
		return nil, fmt.Errorf("unsupported agent provider %q (valid: anthropic, openai, minimax)", provider)
	}
}

// agentChatModel resolves the provider's default chat model with the same
// precedence newAgentCaller uses, so new conversations are stamped with a
// model the configured provider actually serves.
func agentChatModel(provider string) string {
	switch provider {
	case providerOpenAI:
		return envFirst(agentopenai.DefaultChatModel, agentopenai.EnvChatModel)
	case providerMiniMax:
		return envFirst(defaultMiniMaxChatModel, envMiniMaxChatModel)
	default:
		return envFirst(string(claude.DefaultChatModel), claude.EnvChatModel)
	}
}

// agentKeyMissingWarning is the operator-facing message logged when the
// selected provider key is unset. The default Anthropic text is part of the
// bootstrap contract — operator runbooks grep for it.
func agentKeyMissingWarning(provider string) string {
	switch provider {
	case providerOpenAI:
		return "agent: OPENAI_API_KEY not set; chat panel will return caller-not-configured on send"
	case providerMiniMax:
		return "agent: MINIMAX_API_KEY not set; chat panel will return caller-not-configured on send"
	default:
		return "agent: ANTHROPIC_API_KEY not set; chat panel will return caller-not-configured on send"
	}
}

// logAgentUsage is the default metrics emitter for the chat path. It
// writes a single structured line per turn so operators can correlate
// chat traffic with token spend without a separate telemetry pipeline.
func logAgentUsage(_ context.Context, u agent.Usage) {
	log.Printf("agent: usage model=%s in=%d out=%d cache_create=%d cache_read=%d cost_usd=%.4f iterations=%d",
		u.Model, u.InputTokens, u.OutputTokens, u.CacheCreationTokens, u.CacheReadTokens, u.CostUSDEstimated, u.IterationCount)
}

// attachGraphQL builds the GraphQL gateway handler from the supplied
// stack and writes it back onto stack.GraphQLHandler so the router
// mounts it at /graphql/v2. When -graphql-playground / the env flag is
// set, the GraphiQL HTML handler is wired at /graphiql.
func attachGraphQL(stack api.DataStack, cfg serveConfig) (api.DataStack, error) {
	graph, playground, err := graphql.Mount(graphql.MountConfig{Stack: &stack, DevPlayground: cfg.GraphQLPlayground})
	if err != nil {
		return stack, fmt.Errorf("graphql mount: %w", err)
	}
	stack.GraphQLHandler = graph
	stack.GraphQLPlaygroundHandler = playground
	return stack, nil
}

func buildLogStore(cfg serveConfig) (api.LogQueryStore, error) {
	if strings.TrimSpace(cfg.LogDir) == "" {
		return nil, nil
	}
	pattern := strings.TrimSpace(cfg.LogPattern)
	if pattern == "" {
		pattern = defaultLogPattern
	}
	store, err := logrecords.NewFileStore(cfg.LogDir, pattern, logrecords.WithRedactor(redaction.DefaultRedactor()))
	if err != nil {
		return nil, fmt.Errorf("log store: %w", err)
	}
	return store, nil
}

func loadServeDSN(cfg serveConfig) (pg.LoadedDSN, error) {
	if strings.TrimSpace(cfg.PostgresDSN) != "" {
		return pg.LoadedDSN{DSN: strings.TrimSpace(cfg.PostgresDSN), Source: pg.DSNSourceEnv}, nil
	}
	return (&pg.FileDSNLoader{
		EnvVar:    pg.EnvDSN,
		EnvVarRef: pg.EnvDSNFile,
		Path:      cfg.PostgresDSNFile,
	}).Load()
}

func listenAddress(args []string) (string, bool) {
	if len(args) == 0 || (args[0] != "serve" && args[0] != "--listen") {
		return "", false
	}
	cfg, err := parseServeConfig(argsForListenAddress(args))
	if err != nil {
		return "", false
	}
	return cfg.Listen, true
}

func argsForListenAddress(args []string) []string {
	if len(args) > 0 && args[0] == "serve" {
		return args[1:]
	}
	return args
}

func envFirst(fallback string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return fallback
}

func envEnabled(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt(fallback int, name string) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
