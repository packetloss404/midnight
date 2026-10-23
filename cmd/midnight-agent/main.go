// Package main is the midnight-agent entry point.
//
// Phase 1 / Lane 2 wires the binary to a real Docker Engine adapter and the
// HTTPS+HMAC control-plane loop. The boot sequence is:
//
//  1. Load configuration from the environment.
//  2. Detect cgroup v2 and refuse with CGROUP_V2_REQUIRED otherwise.
//  3. Open a Docker Engine API client, run /info + /version, refuse with
//     DOCKER_ENGINE_UNSUPPORTED or DOCKER_DAEMON_BASELINE_CONFLICT on
//     misconfigured hosts.
//  4. Build the HTTPS (mTLS-capable) HTTP/2 client and announce ourselves
//     to the control plane via POST /api/v1alpha/agents/register.
//  5. Run the heartbeat + reconcile + status loop until SIGTERM.
//
// The binary refuses to start when any of the gates above fail; misuse
// surfaces a typed reason code on stderr without crash-looping.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/config"
	"github.com/midnight/midnight/cmd/midnight-agent/internal/control"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/runtime/docker"
	"github.com/midnight/midnight/internal/version"
)

const programName = "midnight-agent"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", programName, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "version" {
		fmt.Println(version.Version)
		return nil
	}

	flags := flag.NewFlagSet(programName, flag.ContinueOnError)
	agentCredential := flags.String("credential", os.Getenv("MIDNIGHT_AGENT_CREDENTIAL"), "agent-scoped credential for authentication and request signing") // not-a-failure-code
	agentCredentialFile := flags.String("credential-file", os.Getenv("MIDNIGHT_AGENT_CREDENTIAL_FILE"), "file containing the agent-scoped credential")      // not-a-failure-code
	legacyHMACSecret := flags.String("hmac-secret", os.Getenv("MIDNIGHT_AGENT_HMAC_SECRET"), "deprecated shared development credential")
	caBundle := flags.String("ca-bundle", os.Getenv("MIDNIGHT_AGENT_CA_BUNDLE"), "PEM bundle of the control plane's CA")
	clientCert := flags.String("client-cert", os.Getenv("MIDNIGHT_AGENT_CLIENT_CERT"), "Client certificate path for mTLS")
	clientKey := flags.String("client-key", os.Getenv("MIDNIGHT_AGENT_CLIENT_KEY"), "Client key path for mTLS")
	insecure := flags.Bool("insecure", false, "Skip TLS verification (development only)")
	dryRun := flags.Bool("dry-run", false, "Verify configuration and exit without dialing the control plane")
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	hostname, _ := os.Hostname()
	fmt.Fprintf(os.Stderr, "%s %s booting agent=%s server=%s\n", programName, version.Version, cfg.AgentID, cfg.ServerURL)

	if *dryRun {
		fmt.Fprintln(os.Stderr, "dry-run: configuration validated; refusing to dial control plane")
		return nil
	}

	credential, err := loadAgentCredential(*agentCredential, *agentCredentialFile, *legacyHMACSecret)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	bootCtx, bootCancel := context.WithTimeout(ctx, 30*time.Second)
	defer bootCancel()

	engine, err := docker.NewEngineClient(os.Getenv("DOCKER_HOST"))
	if err != nil {
		return fmt.Errorf("docker engine client: %w", err)
	}
	defer engine.Close()

	adapter, baseline, err := docker.New(bootCtx, docker.Config{
		AgentID: cfg.AgentID,
		HostID:  cfg.AgentID,
		Engine:  engine,
	})
	if err != nil {
		return fmt.Errorf("runtime adapter: %w", err)
	}
	if err := adapter.Reconcile(bootCtx); err != nil {
		return fmt.Errorf("runtime recovery reconcile: %w", err)
	}

	httpClient, err := control.NewHTTPClient(control.HTTPClientConfig{
		CABundlePath:   *caBundle,
		ClientCertPath: *clientCert,
		ClientKeyPath:  *clientKey,
		Insecure:       *insecure,
	}, cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("control http client: %w", err)
	}

	if strings.HasPrefix(strings.ToLower(cfg.ServerURL), "https://") && *insecure {
		fmt.Fprintln(os.Stderr, "warning: TLS verification disabled; do not use in production")
	}

	client := control.NewAgentClient(httpClient, cfg.ServerURL, credential, programName+"/"+version.Version)

	loop, err := control.NewLoop(control.LoopConfig{
		AgentID:           cfg.AgentID,
		Hostname:          hostname,
		AgentVersion:      version.Version,
		HMACSecret:        []byte(credential),
		HeartbeatInterval: 5 * time.Second,
		Adapter:           adapter,
		Baseline:          baseline,
		CGroupMode:        docker.CGroupModeV2,
	}, client)
	if err != nil {
		return fmt.Errorf("control loop: %w", err)
	}

	if _, err := loop.Register(bootCtx); err != nil {
		if control.CodeOf(err) == failurecodes.VersionSkewRejected {
			return errors.New(string(failurecodes.VersionSkewRejected) + ": " + err.Error())
		}
		return fmt.Errorf("register agent: %w", err)
	}

	fmt.Fprintln(os.Stderr, "agent registered; entering reconcile loop")
	if err := loop.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func loadAgentCredential(value, path, legacyDevelopmentValue string) (string, error) {
	value = strings.TrimSpace(value)
	path = strings.TrimSpace(path)
	legacyDevelopmentValue = strings.TrimSpace(legacyDevelopmentValue)
	if value != "" && path != "" {
		return "", errors.New("configure only one of --credential and --credential-file")
	}
	if path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("stat agent credential file: %w", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("agent credential file permissions %04o expose credential outside the owning user", info.Mode().Perm())
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read agent credential file: %w", err)
		}
		value = strings.TrimSpace(string(raw))
	}
	legacyUsed := false
	if value == "" {
		value = legacyDevelopmentValue
		if value != "" {
			legacyUsed = true
			fmt.Fprintln(os.Stderr, "warning: MIDNIGHT_AGENT_HMAC_SECRET is a shared development compatibility setting; migrate to MIDNIGHT_AGENT_CREDENTIAL_FILE")
		}
	}
	if value == "" {
		return "", errors.New("--credential, --credential-file, or MIDNIGHT_AGENT_CREDENTIAL[_FILE] is required")
	}
	if strings.ContainsAny(value, " \t\r\n,") {
		return "", errors.New("agent credential contains HTTP bearer delimiters")
	}
	if !legacyUsed && len(value) < 32 {
		return "", errors.New("agent credential must contain at least 32 bytes")
	}
	return value, nil
}
