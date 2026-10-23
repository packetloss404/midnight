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

type cliMonitor struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Signal    string   `json:"signal"`
	Operator  string   `json:"operator"`
	Threshold float64  `json:"threshold"`
	State     string   `json:"state"`
	LastValue *float64 `json:"last_value,omitempty"`
}

type cliMonitorChannel struct {
	ID                 string `json:"id"`
	DisplayName        string `json:"display_name"`
	EndpointVariableID string `json:"endpoint_variable_id"`
	SigningVariableID  string `json:"signing_variable_id"`
}

type cliMonitorIncident struct {
	ID              string     `json:"id"`
	MonitorID       string     `json:"monitor_id"`
	MonitorName     string     `json:"monitor_name"`
	State           string     `json:"state"`
	OpenedAt        time.Time  `json:"opened_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	OpenValue       *float64   `json:"open_value,omitempty"`
	CloseValue      *float64   `json:"close_value,omitempty"`
	DeliveryCount   int        `json:"delivery_count"`
	DeadLetterCount int        `json:"dead_letter_count"`
}

type cliMonitorDelivery struct {
	ID             string     `json:"id"`
	IncidentID     string     `json:"incident_id"`
	MonitorID      string     `json:"monitor_id"`
	MonitorName    string     `json:"monitor_name"`
	ChannelID      string     `json:"channel_id"`
	ChannelName    string     `json:"channel_name"`
	EventType      string     `json:"event_type"`
	State          string     `json:"state"`
	Attempts       int        `json:"attempts"`
	ManualRetries  int        `json:"manual_retries"`
	AvailableAt    time.Time  `json:"available_at"`
	ResponseStatus *int       `json:"response_status,omitempty"`
	ErrorSummary   string     `json:"error_summary,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
}

func runMonitor(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printMonitorUsage(stderr)
		return 64
	}
	switch args[0] {
	case "list":
		return runMonitorList(args[1:], stdout, stderr)
	case "create":
		return runMonitorCreate(args[1:], stdout, stderr)
	case "delete":
		return runMonitorDelete(args[1:], stdout, stderr)
	case "subscribe":
		return runMonitorSubscribe(args[1:], stdout, stderr)
	case "channel":
		return runMonitorChannel(args[1:], stdout, stderr)
	case "incident":
		return runMonitorIncident(args[1:], stdout, stderr)
	case "delivery":
		return runMonitorDelivery(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight monitor: unknown subcommand %q\n", args[0])
		printMonitorUsage(stderr)
		return 64
	}
}

func runMonitorIncident(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "usage: midnight monitor incident list --project-id ID")
		return 64
	}
	fs, cfg := newAPIFlagSet("midnight monitor incident list", stderr)
	f := defineMonitorIncidentListFlags(fs)
	projectID, environmentID, serviceID, monitorID := f.projectID, f.environmentID, f.serviceID, f.monitorID
	state, limit := f.state, f.limit
	if err := fs.Parse(args[1:]); err != nil {
		return 64
	}
	if strings.TrimSpace(*projectID) == "" || *limit < 1 || *limit > 200 {
		fmt.Fprintln(stderr, "midnight monitor incident list: --project-id and a limit between 1 and 200 are required")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor incident list: %s\n", err)
		return 64
	}
	query := monitorStatusQuery(*projectID, *environmentID, *serviceID, *monitorID, "", *state, *limit)
	var incidents []cliMonitorIncident
	raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/monitor-incidents?"+query.Encode(), nil, "", &incidents)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor incident list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, incidents,
		func(value cliMonitorIncident) string { return value.ID },
		func(output io.Writer, rows []cliMonitorIncident) {
			for _, incident := range rows {
				resolved := "-"
				if incident.ResolvedAt != nil {
					resolved = incident.ResolvedAt.UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(output, "%s\t%s\t%s\topened=%s\tresolved=%s\tdeliveries=%d\tdead=%d\n",
					incident.ID, incident.MonitorName, incident.State,
					incident.OpenedAt.UTC().Format(time.RFC3339), resolved,
					incident.DeliveryCount, incident.DeadLetterCount,
				)
			}
		},
	)
}

func runMonitorDelivery(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: midnight monitor delivery list|retry")
		return 64
	}
	switch args[0] {
	case "list":
		return runMonitorDeliveryList(args[1:], stdout, stderr)
	case "retry":
		return runMonitorDeliveryRetry(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight monitor delivery: unknown subcommand %q\n", args[0])
		return 64
	}
}

func runMonitorDeliveryList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight monitor delivery list", stderr)
	f := defineMonitorDeliveryListFlags(fs)
	projectID, environmentID, serviceID, monitorID := f.projectID, f.environmentID, f.serviceID, f.monitorID
	incidentID, state, limit := f.incidentID, f.state, f.limit
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*projectID) == "" || *limit < 1 || *limit > 200 {
		fmt.Fprintln(stderr, "midnight monitor delivery list: --project-id and a limit between 1 and 200 are required")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor delivery list: %s\n", err)
		return 64
	}
	query := monitorStatusQuery(*projectID, *environmentID, *serviceID, *monitorID, *incidentID, *state, *limit)
	var deliveries []cliMonitorDelivery
	raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/monitor-deliveries?"+query.Encode(), nil, "", &deliveries)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor delivery list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, deliveries,
		func(value cliMonitorDelivery) string { return value.ID },
		func(output io.Writer, rows []cliMonitorDelivery) {
			for _, delivery := range rows {
				httpStatus := "-"
				if delivery.ResponseStatus != nil {
					httpStatus = fmt.Sprintf("%d", *delivery.ResponseStatus)
				}
				fmt.Fprintf(output, "%s\t%s\t%s\t%s\tattempts=%d\tretries=%d\thttp=%s\t%s\n",
					delivery.ID, delivery.MonitorName, delivery.ChannelName, delivery.State,
					delivery.Attempts, delivery.ManualRetries, httpStatus, delivery.ErrorSummary,
				)
			}
		},
	)
}

func runMonitorDeliveryRetry(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight monitor delivery retry", stderr)
	f := defineMonitorDeliveryRetryFlags(fs)
	projectID, idempotencyKey := f.projectID, f.idempotencyKey
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*projectID) == "" {
		fmt.Fprintln(stderr, "usage: midnight monitor delivery retry --project-id ID <delivery-id>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor delivery retry: %s\n", err)
		return 64
	}
	path := "/api/v1alpha/monitor-deliveries/" + url.PathEscape(fs.Arg(0)) + "/retry"
	body := map[string]string{"project_id": *projectID}
	attempt, err := beginPendingMutationAttempt(*idempotencyKey, client.token, "monitor-delivery-retry", http.MethodPost, path, body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor delivery retry: prepare retry-safe attempt: %s\n", err)
		return 1
	}
	var delivery cliMonitorDelivery
	raw, err := client.doJSON(
		http.MethodPost,
		path,
		body,
		attempt.Key,
		&delivery,
	)
	if finishErr := attempt.Finish(err); finishErr != nil {
		fmt.Fprintf(stderr, "midnight monitor delivery retry: %s\n", finishErr)
		return 1
	}
	if err != nil {
		writeMutationFailure(stderr, "monitor delivery retry", err, attempt.Key)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "monitor delivery requeued: %s (manual retry %d)\n", delivery.ID, delivery.ManualRetries)
	return 0
}

func monitorStatusQuery(
	projectID, environmentID, serviceID, monitorID, incidentID, state string,
	limit int,
) url.Values {
	query := url.Values{"project_id": []string{strings.TrimSpace(projectID)}}
	for key, value := range map[string]string{
		"environment_id": environmentID,
		"service_id":     serviceID,
		"monitor_id":     monitorID,
		"incident_id":    incidentID,
		"state":          state,
	} {
		if value = strings.TrimSpace(value); value != "" {
			query.Set(key, value)
		}
	}
	query.Set("limit", fmt.Sprintf("%d", limit))
	return query
}

func runMonitorList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight monitor list", stderr)
	f := defineMonitorListFlags(fs)
	projectID := f.projectID
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*projectID) == "" {
		fmt.Fprintln(stderr, "midnight monitor list: --project-id is required")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor list: %s\n", err)
		return 64
	}
	var monitors []cliMonitor
	raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/monitors?project_id="+url.QueryEscape(*projectID), nil, "", &monitors)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, monitors,
		func(value cliMonitor) string { return value.ID },
		func(output io.Writer, rows []cliMonitor) {
			for _, monitor := range rows {
				value := "no sample"
				if monitor.LastValue != nil {
					value = fmt.Sprintf("%.2f", *monitor.LastValue)
				}
				fmt.Fprintf(output, "%s\t%s\t%s %s %.2f\t%s\t%s\n",
					monitor.ID, monitor.Name, monitor.Signal, monitor.Operator,
					monitor.Threshold, monitor.State, value)
			}
		},
	)
}

func runMonitorCreate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight monitor create", stderr)
	f := defineMonitorCreateFlags(fs)
	projectID, environmentID, serviceID, name := f.projectID, f.environmentID, f.serviceID, f.name
	signal, operator, threshold, window := f.signal, f.operator, f.threshold, f.window
	interval, failures, recoveries, idempotencyKey := f.interval, f.failures, f.recoveries, f.idempotencyKey
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*projectID) == "" || strings.TrimSpace(*environmentID) == "" ||
		strings.TrimSpace(*serviceID) == "" || strings.TrimSpace(*name) == "" {
		fmt.Fprintln(stderr, "midnight monitor create: --project-id, --environment-id, --service-id, and --name are required")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor create: %s\n", err)
		return 64
	}
	body := map[string]any{
		"project_id": *projectID, "environment_id": *environmentID,
		"service_id": *serviceID, "name": *name, "signal": *signal,
		"operator": *operator, "threshold": *threshold,
		"window_seconds": *window, "evaluation_interval_seconds": *interval,
		"consecutive_failures": *failures, "consecutive_recoveries": *recoveries,
	}
	path := "/api/v1alpha/monitors"
	resolvedKey, err := resolveMutationIdempotencyKey(*idempotencyKey, client.token, "monitor-create", http.MethodPost, path, body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor create: resolve idempotency key: %s\n", err)
		return 64
	}
	var created cliMonitor
	raw, err := client.doJSON(http.MethodPost, path, body, resolvedKey, &created)
	if err != nil {
		writeMutationFailure(stderr, "monitor create", err, resolvedKey)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "monitor created: %s (%s)\n", created.ID, created.Name)
	return 0
}

func runMonitorDelete(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight monitor delete", stderr)
	f := defineMonitorDeleteFlags(fs)
	projectID, idempotencyKey := f.projectID, f.idempotencyKey
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*projectID) == "" {
		fmt.Fprintln(stderr, "usage: midnight monitor delete --project-id ID <monitor-id>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor delete: %s\n", err)
		return 64
	}
	path := "/api/v1alpha/monitors/" + url.PathEscape(fs.Arg(0)) + "?project_id=" + url.QueryEscape(*projectID)
	resolvedKey, err := resolveMutationIdempotencyKey(*idempotencyKey, client.token, "monitor-delete", http.MethodDelete, path, nil)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor delete: resolve idempotency key: %s\n", err)
		return 64
	}
	if _, err := client.doJSON(http.MethodDelete, path, nil, resolvedKey, nil); err != nil {
		writeMutationFailure(stderr, "monitor delete", err, resolvedKey)
		return 1
	}
	fmt.Fprintf(stdout, "monitor deleted: %s\n", fs.Arg(0))
	return 0
}

func runMonitorChannel(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: midnight monitor channel list|create")
		return 64
	}
	if args[0] == "list" {
		return runMonitorChannelList(args[1:], stdout, stderr)
	}
	if args[0] == "create" {
		return runMonitorChannelCreate(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "midnight monitor channel: unknown subcommand %q\n", args[0])
	return 64
}

func runMonitorChannelList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight monitor channel list", stderr)
	f := defineMonitorChannelListFlags(fs)
	projectID := f.projectID
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *projectID == "" {
		fmt.Fprintln(stderr, "midnight monitor channel list: --project-id is required")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor channel list: %s\n", err)
		return 64
	}
	var channels []cliMonitorChannel
	raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/monitor-channels?project_id="+url.QueryEscape(*projectID), nil, "", &channels)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor channel list: %s\n", err)
		return 1
	}
	return renderCLIList(stdout, stderr, cfg.JSON, raw, channels,
		func(value cliMonitorChannel) string { return value.ID },
		func(output io.Writer, rows []cliMonitorChannel) {
			for _, channel := range rows {
				fmt.Fprintf(output, "%s\t%s\tendpoint=%s\tsigning=%s\n",
					channel.ID, channel.DisplayName, channel.EndpointVariableID, channel.SigningVariableID)
			}
		},
	)
}

func runMonitorChannelCreate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight monitor channel create", stderr)
	f := defineMonitorChannelCreateFlags(fs)
	projectID, name, endpointVariable, signingVariable := f.projectID, f.name, f.endpointVariable, f.signingVariable
	keyID, idempotencyKey := f.keyID, f.idempotencyKey
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *projectID == "" || *name == "" || *endpointVariable == "" || *signingVariable == "" {
		fmt.Fprintln(stderr, "midnight monitor channel create: project, name, endpoint variable, and signing variable are required")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor channel create: %s\n", err)
		return 64
	}
	body := map[string]string{
		"project_id": *projectID, "display_name": *name,
		"endpoint_variable_id": *endpointVariable,
		"signing_variable_id":  *signingVariable, "signing_key_id": *keyID,
	}
	path := "/api/v1alpha/monitor-channels"
	resolvedKey, err := resolveMutationIdempotencyKey(*idempotencyKey, client.token, "monitor-channel-create", http.MethodPost, path, body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor channel create: resolve idempotency key: %s\n", err)
		return 64
	}
	var channel cliMonitorChannel
	raw, err := client.doJSON(http.MethodPost, path, body, resolvedKey, &channel)
	if err != nil {
		writeMutationFailure(stderr, "monitor channel create", err, resolvedKey)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(stdout, "monitor channel created: %s (%s)\n", channel.ID, channel.DisplayName)
	return 0
}

func runMonitorSubscribe(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight monitor subscribe", stderr)
	f := defineMonitorSubscribeFlags(fs)
	projectID, channelID, idempotencyKey := f.projectID, f.channelID, f.idempotencyKey
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || *projectID == "" || *channelID == "" {
		fmt.Fprintln(stderr, "usage: midnight monitor subscribe --project-id ID --channel-id ID <monitor-id>")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor subscribe: %s\n", err)
		return 64
	}
	path := "/api/v1alpha/monitors/" + url.PathEscape(fs.Arg(0)) + "/subscriptions"
	body := map[string]string{"project_id": *projectID, "channel_id": *channelID}
	resolvedKey, err := resolveMutationIdempotencyKey(*idempotencyKey, client.token, "monitor-subscribe", http.MethodPost, path, body)
	if err != nil {
		fmt.Fprintf(stderr, "midnight monitor subscribe: resolve idempotency key: %s\n", err)
		return 64
	}
	if _, err := client.doJSON(http.MethodPost, path, body, resolvedKey, nil); err != nil {
		writeMutationFailure(stderr, "monitor subscribe", err, resolvedKey)
		return 1
	}
	fmt.Fprintf(stdout, "monitor %s subscribed to %s\n", fs.Arg(0), *channelID)
	return 0
}

func printMonitorUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: midnight monitor list|create|delete|subscribe|channel|incident|delivery")
}

type monitorIncidentListFlags struct {
	projectID     *string
	environmentID *string
	serviceID     *string
	monitorID     *string
	state         *string
	limit         *int
}

func defineMonitorIncidentListFlags(fs *flag.FlagSet) monitorIncidentListFlags {
	return monitorIncidentListFlags{
		projectID:     cliProjectIDFlag(fs, "owning project id"),
		environmentID: cliEnvironmentIDFlag(fs, "environment id"),
		serviceID:     cliServiceIDFlag(fs, "service id"),
		monitorID:     fs.String("monitor-id", "", "monitor id"),
		state:         fs.String("state", "", "open|resolved"),
		limit:         cliLimitFlag(fs, 50, "maximum rows (1-200)"),
	}
}

type monitorDeliveryListFlags struct {
	projectID     *string
	environmentID *string
	serviceID     *string
	monitorID     *string
	incidentID    *string
	state         *string
	limit         *int
}

func defineMonitorDeliveryListFlags(fs *flag.FlagSet) monitorDeliveryListFlags {
	return monitorDeliveryListFlags{
		projectID:     cliProjectIDFlag(fs, "owning project id"),
		environmentID: cliEnvironmentIDFlag(fs, "environment id"),
		serviceID:     cliServiceIDFlag(fs, "service id"),
		monitorID:     fs.String("monitor-id", "", "monitor id"),
		incidentID:    fs.String("incident-id", "", "incident id"),
		state:         fs.String("state", "", "pending|leased|delivered|dead"),
		limit:         cliLimitFlag(fs, 50, "maximum rows (1-200)"),
	}
}

type monitorDeliveryRetryFlags struct {
	projectID      *string
	idempotencyKey *string
}

func defineMonitorDeliveryRetryFlags(fs *flag.FlagSet) monitorDeliveryRetryFlags {
	return monitorDeliveryRetryFlags{
		projectID:      cliProjectIDFlag(fs, "owning project id"),
		idempotencyKey: cliIdempotencyKeyFlag(fs, "stable retry key (a pending attempt is reused when omitted)"),
	}
}

type monitorListFlags struct {
	projectID *string
}

func defineMonitorListFlags(fs *flag.FlagSet) monitorListFlags {
	return monitorListFlags{
		projectID: cliProjectIDFlag(fs, "owning project id"),
	}
}

type monitorCreateFlags struct {
	projectID      *string
	environmentID  *string
	serviceID      *string
	name           *string
	signal         *string
	operator       *string
	threshold      *float64
	window         *int
	interval       *int
	failures       *int
	recoveries     *int
	idempotencyKey *string
}

func defineMonitorCreateFlags(fs *flag.FlagSet) monitorCreateFlags {
	return monitorCreateFlags{
		projectID:      cliProjectIDFlag(fs, "owning project id"),
		environmentID:  cliEnvironmentIDFlag(fs, "environment id"),
		serviceID:      cliServiceIDFlag(fs, "service id"),
		name:           fs.String("name", "", "monitor name"),
		signal:         fs.String("signal", "cpu_percent", "cpu_percent|memory_percent|restart_count"),
		operator:       fs.String("operator", "gt", "gt|gte"),
		threshold:      fs.Float64("threshold", 80, "firing threshold"),
		window:         fs.Int("window-seconds", 300, "evaluation window (minimum 60)"),
		interval:       fs.Int("interval-seconds", 60, "evaluation interval (minimum 60)"),
		failures:       fs.Int("consecutive-failures", 2, "breaches before firing"),
		recoveries:     fs.Int("consecutive-recoveries", 2, "healthy evaluations before resolving"),
		idempotencyKey: cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact create intent when omitted)"),
	}
}

type monitorDeleteFlags struct {
	projectID      *string
	idempotencyKey *string
}

func defineMonitorDeleteFlags(fs *flag.FlagSet) monitorDeleteFlags {
	return monitorDeleteFlags{
		projectID:      cliProjectIDFlag(fs, "owning project id"),
		idempotencyKey: cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact delete intent when omitted)"),
	}
}

type monitorChannelListFlags struct {
	projectID *string
}

func defineMonitorChannelListFlags(fs *flag.FlagSet) monitorChannelListFlags {
	return monitorChannelListFlags{
		projectID: cliProjectIDFlag(fs, "owning project id"),
	}
}

type monitorChannelCreateFlags struct {
	projectID        *string
	name             *string
	endpointVariable *string
	signingVariable  *string
	keyID            *string
	idempotencyKey   *string
}

func defineMonitorChannelCreateFlags(fs *flag.FlagSet) monitorChannelCreateFlags {
	return monitorChannelCreateFlags{
		projectID:        cliProjectIDFlag(fs, "owning project id"),
		name:             fs.String("name", "", "channel display name"),
		endpointVariable: fs.String("endpoint-variable-id", "", "sealed variable containing HTTPS endpoint"),
		signingVariable:  fs.String("signing-variable-id", "", "sealed variable containing HMAC key"),
		keyID:            fs.String("signing-key-id", "monitor", "public signing key identifier"),
		idempotencyKey:   cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact create intent when omitted)"),
	}
}

type monitorSubscribeFlags struct {
	projectID      *string
	channelID      *string
	idempotencyKey *string
}

func defineMonitorSubscribeFlags(fs *flag.FlagSet) monitorSubscribeFlags {
	return monitorSubscribeFlags{
		projectID:      cliProjectIDFlag(fs, "owning project id"),
		channelID:      fs.String("channel-id", "", "monitor channel id"),
		idempotencyKey: cliIdempotencyKeyFlag(fs, "stable retry key (derived from the exact subscription intent when omitted)"),
	}
}
