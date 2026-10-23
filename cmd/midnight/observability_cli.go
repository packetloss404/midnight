// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

type cliObservabilityScope struct {
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id,omitempty"`
	ServiceID     string `json:"service_id,omitempty"`
	DeploymentID  string `json:"deployment_id,omitempty"`
	OperationID   string `json:"operation_id,omitempty"`
	ReplicaID     string `json:"replica_id,omitempty"`
}

type cliObservabilityLog struct {
	ObservedAt time.Time             `json:"observed_at"`
	Scope      cliObservabilityScope `json:"scope"`
	Surface    string                `json:"surface"`
	Stream     string                `json:"stream"`
	Level      string                `json:"level"`
	Component  string                `json:"component"`
	Message    string                `json:"message"`
	Attributes map[string]any        `json:"attributes"`
}

type cliObservabilityLogsResponse struct {
	Records         []cliObservabilityLog   `json:"records"`
	NextCursor      *cliObservabilityCursor `json:"next_cursor,omitempty"`
	Truncated       bool                    `json:"truncated"`
	DroppedAtIngest int64                   `json:"dropped_at_ingest"`
	CollectorHealth []cliCollectorHealth    `json:"collector_health"`
}

type cliCollectorHealth struct {
	Kind         string     `json:"kind"`
	Status       string     `json:"status"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	HeartbeatGap bool       `json:"heartbeat_gap"`
	Reason       string     `json:"reason"`
}

type cliObservabilityCursor struct {
	Token string `json:"token"`
}

type cliMetricPoint struct {
	ObservedAt      time.Time `json:"observed_at"`
	ReplicaID       string    `json:"replica_id"`
	CPUPercent      float64   `json:"cpu_percent"`
	MemoryBytes     int64     `json:"memory_bytes"`
	NetworkRXBytes  int64     `json:"network_rx_bytes"`
	NetworkTXBytes  int64     `json:"network_tx_bytes"`
	BlockReadBytes  int64     `json:"block_read_bytes"`
	BlockWriteBytes int64     `json:"block_write_bytes"`
	DiskUsageBytes  *int64    `json:"disk_usage_bytes"`
	Replicas        int       `json:"replicas"`
}

type cliMetricSeries struct {
	Mode            string               `json:"mode"`
	Step            string               `json:"step"`
	Points          []cliMetricPoint     `json:"points"`
	Collectors      []string             `json:"collectors"`
	CollectorHealth []cliCollectorHealth `json:"collector_health"`
}

func runObservability(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printObservabilityUsage(stderr)
		return 64
	}
	switch args[0] {
	case "logs":
		return runObservabilityLogs(args[1:], stdout, stderr)
	case "metrics":
		return runObservabilityMetrics(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight observability: unknown subcommand %q\n", args[0])
		printObservabilityUsage(stderr)
		return 64
	}
}

func runObservabilityLogs(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight observability logs", stderr)
	f := defineObservabilityLogsFlags(fs)
	projectID, environmentID, serviceID, deploymentID := f.projectID, f.environmentID, f.serviceID, f.deploymentID
	operationID, replicaID, surface, level := f.operationID, f.replicaID, f.surface, f.level
	component, search, since, limit := f.component, f.search, f.since, f.limit
	cursor, follow := f.cursor, f.follow
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*projectID) == "" {
		fmt.Fprintln(stderr, "midnight observability logs: --project-id is required")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight observability logs: %s\n", err)
		return 64
	}
	values := scopedObservabilityValues(*projectID, *environmentID, *serviceID, *deploymentID, *replicaID)
	if *operationID != "" {
		values.Set("operation_id", *operationID)
	}
	if *surface != "" {
		values.Set("surface", *surface)
	}
	if *level != "" {
		values.Set("level", *level)
	}
	if *component != "" {
		values.Set("component", *component)
	}
	if *search != "" {
		values.Set("q", *search)
	}
	if *since != "" {
		values.Set("since", *since)
	}
	values.Set("limit", strconv.Itoa(*limit))
	if *cursor != "" {
		values.Set("cursor", *cursor)
	}
	if *follow {
		return streamObservabilityLogs(context.Background(), client,
			"/api/v1alpha/observability/logs/stream?"+values.Encode(),
			cfg.JSON, stdout, stderr)
	}
	var response cliObservabilityLogsResponse
	raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/observability/logs?"+values.Encode(), nil, "", &response)
	if err != nil {
		fmt.Fprintf(stderr, "midnight observability logs: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	for _, record := range response.Records {
		fmt.Fprintf(stdout, "%s %-7s %-10s %-16s %s\n",
			record.ObservedAt.UTC().Format(time.RFC3339), record.Level,
			record.Surface, record.Component, record.Message)
	}
	if response.DroppedAtIngest > 0 {
		fmt.Fprintf(stdout, "ingestion drops in scope/window: %d\n", response.DroppedAtIngest)
	}
	writeCollectorHeartbeatGaps(stdout, response.CollectorHealth)
	if response.Truncated && response.NextCursor != nil {
		fmt.Fprintf(stdout, "next cursor: --cursor %s\n", response.NextCursor.Token)
	}
	return 0
}

func runObservabilityMetrics(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight observability metrics", stderr)
	f := defineObservabilityMetricsFlags(fs)
	projectID, environmentID, serviceID, deploymentID := f.projectID, f.environmentID, f.serviceID, f.deploymentID
	replicaID, since, step, mode := f.replicaID, f.since, f.step, f.mode
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if strings.TrimSpace(*projectID) == "" || strings.TrimSpace(*serviceID) == "" {
		fmt.Fprintln(stderr, "midnight observability metrics: --project-id and --service-id are required")
		return 64
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight observability metrics: %s\n", err)
		return 64
	}
	values := scopedObservabilityValues(*projectID, *environmentID, *serviceID, *deploymentID, *replicaID)
	values.Set("mode", *mode)
	if *since != "" {
		values.Set("since", *since)
	}
	if *step != "" {
		values.Set("step", *step)
	}
	var response cliMetricSeries
	raw, err := client.doJSON(http.MethodGet, "/api/v1alpha/observability/metrics?"+values.Encode(), nil, "", &response)
	if err != nil {
		fmt.Fprintf(stderr, "midnight observability metrics: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	if len(response.Collectors) == 0 {
		fmt.Fprintln(stdout, "No active metrics collector has reported recently.")
	}
	writeCollectorHeartbeatGaps(stdout, response.CollectorHealth)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tREPLICA\tCPU %\tMEMORY\tRX\tTX\tBLOCK READ\tBLOCK WRITE")
	for _, point := range response.Points {
		replica := point.ReplicaID
		if replica == "" {
			replica = fmt.Sprintf("sum(%d)", point.Replicas)
		}
		fmt.Fprintf(tw, "%s\t%s\t%.2f\t%d\t%d\t%d\t%d\t%d\n",
			point.ObservedAt.UTC().Format(time.RFC3339), replica, point.CPUPercent,
			point.MemoryBytes, point.NetworkRXBytes, point.NetworkTXBytes,
			point.BlockReadBytes, point.BlockWriteBytes)
	}
	_ = tw.Flush()
	return 0
}

func writeCollectorHeartbeatGaps(w io.Writer, health []cliCollectorHealth) {
	for _, collector := range health {
		if !collector.HeartbeatGap {
			continue
		}
		reason := strings.TrimSpace(collector.Reason)
		if reason == "" {
			reason = "collector heartbeat is unavailable"
		}
		fmt.Fprintf(w, "collector heartbeat gap: %s (%s): %s\n", collector.Kind, collector.Status, reason)
	}
}

func scopedObservabilityValues(projectID, environmentID, serviceID, deploymentID, replicaID string) url.Values {
	values := url.Values{"project_id": []string{projectID}}
	for key, value := range map[string]string{
		"environment_id": environmentID, "service_id": serviceID,
		"deployment_id": deploymentID, "replica_id": replicaID,
	} {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
	return values
}

func printObservabilityUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  midnight observability logs --project-id ID [--environment-id ID] [--service-id ID] [--surface runtime,http] [--cursor TOKEN] [--follow] [--json]")
	fmt.Fprintln(w, "  midnight observability metrics --project-id ID --service-id ID [--environment-id ID] [--mode sum|replica] [--step 1m] [--json]")
}

func streamObservabilityLogs(
	ctx context.Context,
	client *cliAPIClient,
	path string,
	jsonOutput bool,
	stdout io.Writer,
	stderr io.Writer,
) int {
	if client == nil || client.client == nil {
		fmt.Fprintln(stderr, "midnight observability logs: API client is unavailable")
		return 1
	}
	lastEventID := ""
	failures := 0
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
		if err != nil {
			fmt.Fprintf(stderr, "midnight observability logs: %s\n", err)
			return 1
		}
		request.Header.Set("Accept", "text/event-stream")
		if client.token != "" {
			request.Header.Set("Authorization", "Bearer "+client.token)
		}
		if lastEventID != "" {
			request.Header.Set("Last-Event-ID", lastEventID)
		}
		response, err := executeCLIStream(client.client, request)
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			failures++
			if !waitObservabilityReconnect(ctx, failures) {
				return 0
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			body, readErr := readBoundedCLIResponse(http.MethodGet, path, response.Body, maxCLIErrorResponseBytes)
			_ = response.Body.Close()
			if readErr != nil {
				fmt.Fprintf(stderr, "midnight observability logs: %s\n", readErr)
				return 1
			}
			fmt.Fprintf(stderr, "midnight observability logs: %s\n",
				apiStatusError(http.MethodGet, path, response.Status, body))
			return 1
		}
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		eventName, eventID := "", ""
		data := make([]string, 0, 1)
		delivered := false
		dispatch := func() {
			if len(data) == 0 {
				eventName, eventID, data = "", "", data[:0]
				return
			}
			raw := strings.Join(data, "\n")
			switch eventName {
			case "log":
				var record cliObservabilityLog
				if err := json.Unmarshal([]byte(raw), &record); err == nil {
					if jsonOutput {
						fmt.Fprintln(stdout, raw)
					} else {
						fmt.Fprintf(stdout, "%s %-7s %-10s %-16s %s\n",
							record.ObservedAt.UTC().Format(time.RFC3339), record.Level,
							record.Surface, record.Component, record.Message)
					}
					if eventID != "" {
						lastEventID = eventID
					}
					delivered = true
				}
			case "collector-health":
				var health []cliCollectorHealth
				if err := json.Unmarshal([]byte(raw), &health); err == nil {
					if jsonOutput {
						encoded, _ := json.Marshal(map[string]any{"event": "collector_health", "collectors": health})
						fmt.Fprintln(stdout, string(encoded))
					} else {
						writeCollectorHeartbeatGaps(stdout, health)
					}
					delivered = true
				}
			}
			eventName, eventID, data = "", "", data[:0]
		}
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				dispatch()
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue
			}
			field, value, found := strings.Cut(line, ":")
			if found {
				value = strings.TrimPrefix(value, " ")
			}
			switch field {
			case "event":
				eventName = value
			case "id":
				eventID = value
			case "data":
				data = append(data, value)
			}
		}
		dispatch()
		scanErr := scanner.Err()
		_ = response.Body.Close()
		if ctx.Err() != nil {
			return 0
		}
		if delivered {
			failures = 0
		} else {
			failures++
		}
		if scanErr != nil {
			fmt.Fprintf(stderr, "midnight observability logs: stream interrupted; resuming: %s\n", scanErr)
		}
		if !waitObservabilityReconnect(ctx, failures) {
			return 0
		}
	}
}

func waitObservabilityReconnect(ctx context.Context, failures int) bool {
	if failures < 0 {
		failures = 0
	}
	if failures > 5 {
		failures = 5
	}
	delay := 250 * time.Millisecond * time.Duration(1<<failures)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type observabilityLogsFlags struct {
	projectID     *string
	environmentID *string
	serviceID     *string
	deploymentID  *string
	operationID   *string
	replicaID     *string
	surface       *string
	level         *string
	component     *string
	search        *string
	since         *string
	limit         *int
	cursor        *string
	follow        *bool
}

func defineObservabilityLogsFlags(fs *flag.FlagSet) observabilityLogsFlags {
	return observabilityLogsFlags{
		projectID:     cliProjectIDFlag(fs, "required project scope"),
		environmentID: cliEnvironmentIDFlag(fs, "environment scope"),
		serviceID:     cliServiceIDFlag(fs, "service scope"),
		deploymentID:  fs.String("deployment-id", "", "deployment scope"),
		operationID:   fs.String("operation-id", "", "operation scope"),
		replicaID:     fs.String("replica-id", "", "replica scope"),
		surface:       fs.String("surface", "", "comma-separated build,runtime,http,platform surfaces"),
		level:         fs.String("level", "", "comma-separated log levels"),
		component:     fs.String("component", "", "comma-separated source components"),
		search:        cliSearchFlag(fs, "message or structured-attribute search"),
		since:         fs.String("since", "", "RFC3339 lower time bound"),
		limit:         cliLimitFlag(fs, 500, "maximum records"),
		cursor:        fs.String("cursor", "", "opaque continuation cursor"),
		follow:        cliFollowFlag(fs, "replay and follow logs, resuming after a dropped connection"),
	}
}

type observabilityMetricsFlags struct {
	projectID     *string
	environmentID *string
	serviceID     *string
	deploymentID  *string
	replicaID     *string
	since         *string
	step          *string
	mode          *string
}

func defineObservabilityMetricsFlags(fs *flag.FlagSet) observabilityMetricsFlags {
	return observabilityMetricsFlags{
		projectID:     cliProjectIDFlag(fs, "required project scope"),
		environmentID: cliEnvironmentIDFlag(fs, "environment scope"),
		serviceID:     cliServiceIDFlag(fs, "required service scope"),
		deploymentID:  fs.String("deployment-id", "", "deployment scope"),
		replicaID:     fs.String("replica-id", "", "replica scope"),
		since:         fs.String("since", "", "RFC3339 lower time bound"),
		step:          fs.String("step", "", "requested aggregation step, e.g. 1m"),
		mode:          fs.String("mode", "sum", "sum or replica"),
	}
}
