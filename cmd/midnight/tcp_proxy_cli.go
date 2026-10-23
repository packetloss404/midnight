// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type cliTCPProxy struct {
	ID                         string     `json:"id"`
	ProjectID                  string     `json:"project_id"`
	EnvironmentID              string     `json:"environment_id"`
	ServiceID                  string     `json:"service_id"`
	Hostname                   string     `json:"hostname"`
	ExternalPort               int        `json:"external_port"`
	PublicAddress              string     `json:"public_address"`
	BackendPort                int        `json:"backend_port"`
	AllowedSourceCIDRs         []string   `json:"allowed_source_cidrs"`
	DesiredEnabled             bool       `json:"desired_enabled"`
	ApplyState                 string     `json:"apply_state"`
	ListenerBound              bool       `json:"listener_bound"`
	BackendReachable           bool       `json:"backend_reachable"`
	DNSProvisioned             bool       `json:"dns_provisioned"`
	PublicReachabilityVerified bool       `json:"public_reachability_verified"`
	ErrorCode                  string     `json:"error_code,omitempty"`
	ErrorDetail                string     `json:"error_detail,omitempty"`
	ObservedAt                 *time.Time `json:"observed_at,omitempty"`
}

func runTCPProxy(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printTCPProxyUsage(stderr)
		return 64
	}
	switch args[0] {
	case "list":
		return runTCPProxyList(args[1:], stdout, stderr)
	case "create":
		return runTCPProxyCreate(args[1:], stdout, stderr)
	case "status":
		return runTCPProxyStatus(args[1:], stdout, stderr)
	case "withdraw":
		return runTCPProxyWithdraw(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight tcp-proxy: unknown subcommand %q\n", args[0])
		printTCPProxyUsage(stderr)
		return 64
	}
}

func runTCPProxyList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight tcp-proxy list", stderr)
	f := defineTCPProxyListFlags(fs)
	projectID, environmentID, serviceID := f.projectID, f.environmentID, f.serviceID
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*projectID) == "" || strings.TrimSpace(*environmentID) == "" || strings.TrimSpace(*serviceID) == "" {
		fmt.Fprintln(stderr, "usage: midnight tcp-proxy list --project-id ID --environment-id ID --service-id ID")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight tcp-proxy list: %s\n", err)
		return 64
	}
	query := url.Values{
		"project_id": []string{strings.TrimSpace(*projectID)}, "environment_id": []string{strings.TrimSpace(*environmentID)},
		"service_id": []string{strings.TrimSpace(*serviceID)},
	}
	var values []cliTCPProxy
	raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/tcp-proxies?"+query.Encode(), nil, "", &values)
	if err != nil {
		fmt.Fprintf(stderr, "midnight tcp-proxy list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, values,
		func(value cliTCPProxy) string { return value.ID },
		func(output io.Writer, rows []cliTCPProxy) {
			for _, value := range rows {
				writeTCPProxyLine(output, value)
			}
		},
	)
}

func runTCPProxyCreate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight tcp-proxy create", stderr)
	f := defineTCPProxyCreateFlags(fs)
	projectID, environmentID, serviceID, hostname := f.projectID, f.environmentID, f.serviceID, f.hostname
	externalPort, backendPort, key := f.externalPort, f.backendPort, f.key
	var allowed cidrListFlag
	fs.Var(&allowed, "allow-cidr", "optional observed-peer CIDR restriction; repeat for multiple networks")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*projectID) == "" || strings.TrimSpace(*environmentID) == "" ||
		strings.TrimSpace(*serviceID) == "" ||
		*backendPort < 1 || *backendPort > 65535 {
		fmt.Fprintln(stderr, "usage: midnight tcp-proxy create --project-id ID --environment-id ID --service-id ID --target-port PORT [--allow-cidr CIDR]")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight tcp-proxy create: %s\n", err)
		return 64
	}
	request := map[string]any{
		"project_id": strings.TrimSpace(*projectID), "environment_id": strings.TrimSpace(*environmentID),
		"service_id": strings.TrimSpace(*serviceID), "hostname": strings.TrimSpace(*hostname),
		"external_port": *externalPort, "backend_port": *backendPort,
		"allowed_source_cidrs": append([]string{}, allowed...),
	}
	path := "/api/v1alpha/tcp-proxies"
	resolvedKey, err := resolveMutationIdempotencyKey(*key, client.token, "tcp-proxy-create", http.MethodPost, path, request)
	if err != nil {
		fmt.Fprintf(stderr, "midnight tcp-proxy create: resolve idempotency key: %s\n", err)
		return 64
	}
	var value cliTCPProxy
	raw, err := client.doJSON(http.MethodPost, path, request, resolvedKey, &value)
	if err != nil {
		writeMutationFailure(stderr, "tcp-proxy create", err, resolvedKey)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "TCP endpoint allocated: %s\n", value.ID)
	writeTCPProxyLine(stdout, value)
	fmt.Fprintln(stdout, "DNS and external firewall/NAT reachability are operator-managed and remain unverified.")
	if len(value.AllowedSourceCIDRs) == 0 {
		fmt.Fprintln(stdout, "No observed-peer CIDR restriction is configured; the listener accepts every peer address it observes.")
	}
	return 0
}

func runTCPProxyStatus(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight tcp-proxy status", stderr)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: midnight tcp-proxy status <endpoint-id>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight tcp-proxy status: %s\n", err)
		return 64
	}
	var value cliTCPProxy
	raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/tcp-proxies/"+url.PathEscape(fs.Arg(0)), nil, "", &value)
	if err != nil {
		fmt.Fprintf(stderr, "midnight tcp-proxy status: %s\n", err)
		return 1
	}
	return renderCLISingle(stdout, stderr, cfg.JSON, raw, value,
		func(value cliTCPProxy) string { return value.ID },
		func(output io.Writer, value cliTCPProxy) {
			writeTCPProxyLine(output, value)
			fmt.Fprintf(output, "dns_provisioned=%t\tpublic_reachability_verified=%t\n", value.DNSProvisioned, value.PublicReachabilityVerified)
			if value.ErrorCode != "" || value.ErrorDetail != "" {
				fmt.Fprintf(output, "error\t%s\t%s\n", value.ErrorCode, value.ErrorDetail)
			}
		},
	)
}

func runTCPProxyWithdraw(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight tcp-proxy withdraw", stderr)
	f := defineTCPProxyWithdrawFlags(fs)
	key := f.key
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: midnight tcp-proxy withdraw <endpoint-id>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight tcp-proxy withdraw: %s\n", err)
		return 64
	}
	path := "/api/v1alpha/tcp-proxies/" + url.PathEscape(fs.Arg(0))
	resolvedKey, err := resolveMutationIdempotencyKey(*key, client.token, "tcp-proxy-withdraw", http.MethodDelete, path, nil)
	if err != nil {
		fmt.Fprintf(stderr, "midnight tcp-proxy withdraw: resolve idempotency key: %s\n", err)
		return 64
	}
	var value cliTCPProxy
	raw, err := client.doJSON(http.MethodDelete, path, nil, resolvedKey, &value)
	if err != nil {
		writeMutationFailure(stderr, "tcp-proxy withdraw", err, resolvedKey)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "TCP endpoint withdrawal requested: %s (state=%s listener_bound=%t)\n", value.ID, value.ApplyState, value.ListenerBound)
	return 0
}

func writeTCPProxyLine(w io.Writer, value cliTCPProxy) {
	fmt.Fprintf(w, "%s\t%s\tstate=%s\tlistener=%t\tbackend=%t\tdesired=%t\n",
		value.ID, value.PublicAddress, value.ApplyState, value.ListenerBound, value.BackendReachable, value.DesiredEnabled)
}

func printTCPProxyUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midnight tcp-proxy list|create|status|withdraw")
}

type cidrListFlag []string

func (f *cidrListFlag) String() string { return strings.Join(*f, ",") }
func (f *cidrListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("CIDR cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

type tcpProxyListFlags struct {
	projectID     *string
	environmentID *string
	serviceID     *string
}

func defineTCPProxyListFlags(fs *flag.FlagSet) tcpProxyListFlags {
	return tcpProxyListFlags{
		projectID:     cliProjectIDFlag(fs, "owning project id"),
		environmentID: cliEnvironmentIDFlag(fs, "owning environment id"),
		serviceID:     cliServiceIDFlag(fs, "owning service id"),
	}
}

type tcpProxyCreateFlags struct {
	projectID     *string
	environmentID *string
	serviceID     *string
	hostname      *string
	externalPort  *int
	backendPort   *int
	key           *string
}

func defineTCPProxyCreateFlags(fs *flag.FlagSet) tcpProxyCreateFlags {
	return tcpProxyCreateFlags{
		projectID:     cliProjectIDFlag(fs, "owning project id"),
		environmentID: cliEnvironmentIDFlag(fs, "owning environment id"),
		serviceID:     cliServiceIDFlag(fs, "owning service id"),
		hostname:      fs.String("hostname", "", "descriptive TCP hostname; DNS remains operator-managed"),
		externalPort:  fs.Int("external-port", 0, "requested external port; zero allocates one"),
		backendPort:   fs.Int("target-port", 0, "internal TCP port on the ready active deployment"),
		key:           cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact create intent when omitted)"),
	}
}

type tcpProxyWithdrawFlags struct {
	key *string
}

func defineTCPProxyWithdrawFlags(fs *flag.FlagSet) tcpProxyWithdrawFlags {
	return tcpProxyWithdrawFlags{
		key: cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact withdrawal intent when omitted)"),
	}
}
