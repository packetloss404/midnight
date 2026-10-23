// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package observability

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	runtimedocker "github.com/midnight/midnight/internal/runtime/docker"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
)

const DefaultDockerCollectionInterval = 15 * time.Second

type DockerTelemetryClient interface {
	ContainerList(context.Context, dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error)
	ContainerLogs(context.Context, string, dockerclient.ContainerLogsOptions) (dockerclient.ContainerLogsResult, error)
	ContainerStats(context.Context, string, dockerclient.ContainerStatsOptions) (dockerclient.ContainerStatsResult, error)
	ContainerInspect(context.Context, string, dockerclient.ContainerInspectOptions) (dockerclient.ContainerInspectResult, error)
	Close() error
}

type DockerCollectorStore interface {
	IngestLog(context.Context, LogRecord) (IngestResult, error)
	IngestMetric(context.Context, MetricSample) error
	RecordDeploymentMarker(context.Context, Scope, string, time.Time) error
	CollectorCheckpoint(context.Context, string) (time.Time, bool, error)
	SaveCollectorCheckpoint(context.Context, string, time.Time) error
	HeartbeatCollector(context.Context, string, string, error) error
}

type DockerCollector struct {
	Client    DockerTelemetryClient
	Store     DockerCollectorStore
	ID        string
	Interval  time.Duration
	Now       func() time.Time
	Retention time.Duration
}

func NewDockerCollector(host string, store DockerCollectorStore, id string) (*DockerCollector, error) {
	options := []dockerclient.Opt{
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	}
	if strings.TrimSpace(host) != "" {
		options = append(options, dockerclient.WithHost(strings.TrimSpace(host)))
	}
	client, err := dockerclient.NewClientWithOpts(options...)
	if err != nil {
		return nil, fmt.Errorf("observability Docker client: %w", err)
	}
	return &DockerCollector{
		Client: client, Store: store, ID: strings.TrimSpace(id),
		Interval:  DefaultDockerCollectionInterval,
		Now:       func() time.Time { return time.Now().UTC() },
		Retention: DefaultRetention,
	}, nil
}

func (c *DockerCollector) Close() error {
	if c == nil || c.Client == nil {
		return nil
	}
	return c.Client.Close()
}

func (c *DockerCollector) Run(ctx context.Context, report func(error)) {
	if report == nil {
		report = func(error) {}
	}
	collect := func() {
		err := c.CollectOnce(ctx)
		if c == nil || c.Store == nil {
			report(err)
			return
		}
		if heartbeatErr := c.Store.HeartbeatCollector(ctx, c.collectorID(), "docker", err); heartbeatErr != nil {
			report(heartbeatErr)
		}
		if err != nil {
			report(err)
		}
	}
	collect()
	interval := c.Interval
	if interval <= 0 {
		interval = DefaultDockerCollectionInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}

func (c *DockerCollector) CollectOnce(ctx context.Context) error {
	if c == nil || c.Client == nil || c.Store == nil {
		return errors.New("Docker observability collector is not configured")
	}
	labelFilter := make(dockerclient.Filters).Add(
		"label", runtimedocker.LabelManagedBy+"="+runtimedocker.LabelManagedByV,
	)
	containers, err := c.Client.ContainerList(ctx, dockerclient.ContainerListOptions{
		All: true, Filters: labelFilter,
	})
	if err != nil {
		return fmt.Errorf("list managed containers for telemetry: %w", err)
	}
	var errs []error
	for _, item := range containers.Items {
		scope, ok := scopeFromDockerLabels(item.Labels)
		if !ok {
			errs = append(errs, fmt.Errorf("container %s has incomplete Midnight scope labels", shortID(item.ID)))
			continue
		}
		if err := c.collectLogs(ctx, item, scope); err != nil {
			errs = append(errs, err)
		}
		if item.State == "running" {
			if err := c.collectStats(ctx, item, scope); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

type dockerLogLine struct {
	at      time.Time
	stream  string
	message string
	index   int
}

func (c *DockerCollector) collectLogs(ctx context.Context, item container.Summary, scope Scope) error {
	sourceKey := "docker-log:" + item.ID
	since, found, err := c.Store.CollectorCheckpoint(ctx, sourceKey)
	if err != nil {
		return fmt.Errorf("read log checkpoint for %s: %w", shortID(item.ID), err)
	}
	if !found {
		retention := c.Retention
		if retention <= 0 {
			retention = DefaultRetention
		}
		since = c.now().Add(-retention)
	}
	reader, err := c.Client.ContainerLogs(ctx, item.ID, dockerclient.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Timestamps: true,
		Since: since.UTC().Format(time.RFC3339Nano), Tail: "all",
	})
	if err != nil {
		return fmt.Errorf("read Docker logs for %s: %w", shortID(item.ID), err)
	}
	defer func() { _ = reader.Close() }()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, reader); err != nil {
		return fmt.Errorf("decode Docker logs for %s: %w", shortID(item.ID), err)
	}
	lines := append(
		parseDockerLogStream(stdout.Bytes(), "stdout"),
		parseDockerLogStream(stderr.Bytes(), "stderr")...,
	)
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].at.Equal(lines[j].at) {
			if lines[i].stream == lines[j].stream {
				return lines[i].index < lines[j].index
			}
			return lines[i].stream < lines[j].stream
		}
		return lines[i].at.Before(lines[j].at)
	})
	latest := since
	for _, line := range lines {
		if line.at.After(latest) {
			latest = line.at
		}
		sum := sha256.Sum256([]byte(line.message))
		// Docker's Since boundary can replay the last timestamp. Keep the
		// durable identity independent of its position in this particular
		// response so an inclusive replay cannot mint a duplicate row.
		sequence := line.at.Format(time.RFC3339Nano) + ":" + line.stream + ":" +
			hex.EncodeToString(sum[:8])
		_, err := c.Store.IngestLog(ctx, LogRecord{
			ObservedAt: line.at, Scope: scope, Surface: "runtime",
			Stream: line.stream, Level: dockerLogLevel(line.stream, line.message),
			Component: "app.runtime", Message: line.message,
			Attributes: map[string]any{"container_id": shortID(item.ID)},
			SourceKey:  sourceKey, SourceSequence: sequence,
		})
		if err != nil {
			return fmt.Errorf("persist Docker log for %s: %w", shortID(item.ID), err)
		}
	}
	if latest.After(since) {
		// The checkpoint is an exclusive boundary. Docker accepts RFC3339Nano
		// Since values, so advancing one nanosecond avoids rereading the final
		// line forever while the source-sequence key still defends inclusive
		// daemon implementations.
		if err := c.Store.SaveCollectorCheckpoint(ctx, sourceKey, latest.Add(time.Nanosecond)); err != nil {
			return fmt.Errorf("save Docker log checkpoint for %s: %w", shortID(item.ID), err)
		}
	}
	return nil
}

func (c *DockerCollector) collectStats(ctx context.Context, item container.Summary, scope Scope) error {
	response, err := c.Client.ContainerStats(ctx, item.ID, dockerStatsRequestOptions())
	if err != nil {
		return fmt.Errorf("read Docker stats for %s: %w", shortID(item.ID), err)
	}
	defer func() { _ = response.Body.Close() }()
	var stats container.StatsResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&stats); err != nil {
		return fmt.Errorf("decode Docker stats for %s: %w", shortID(item.ID), err)
	}
	inspectResult, err := c.Client.ContainerInspect(ctx, item.ID, dockerclient.ContainerInspectOptions{Size: true})
	if err != nil {
		return fmt.Errorf("inspect Docker container %s for telemetry: %w", shortID(item.ID), err)
	}
	inspect := inspectResult.Container
	at := stats.Read.UTC()
	if at.IsZero() {
		at = c.now()
	}
	var rx, tx int64
	for _, network := range stats.Networks {
		rx += safeUint64(network.RxBytes)
		tx += safeUint64(network.TxBytes)
	}
	var read, write int64
	for _, entry := range stats.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(entry.Op) {
		case "read":
			read += safeUint64(entry.Value)
		case "write":
			write += safeUint64(entry.Value)
		}
	}
	if stats.StorageStats.ReadSizeBytes > 0 {
		read = safeUint64(stats.StorageStats.ReadSizeBytes)
	}
	if stats.StorageStats.WriteSizeBytes > 0 {
		write = safeUint64(stats.StorageStats.WriteSizeBytes)
	}
	memory := stats.MemoryStats.Usage
	cache := stats.MemoryStats.Stats["inactive_file"]
	if cache == 0 {
		cache = stats.MemoryStats.Stats["cache"]
	}
	if memory >= cache {
		memory -= cache
	}
	var disk *int64
	if inspect.SizeRw != nil && *inspect.SizeRw >= 0 {
		value := *inspect.SizeRw
		disk = &value
	}
	sample := MetricSample{
		ObservedAt: at, Scope: scope, Source: "docker", Resolution: "raw",
		CPUPercent:  dockerCPUPercent(stats),
		MemoryBytes: safeUint64(memory), MemoryLimitBytes: safeUint64(stats.MemoryStats.Limit),
		NetworkRXBytes: rx, NetworkTXBytes: tx,
		BlockReadBytes: read, BlockWriteBytes: write, DiskUsageBytes: disk,
		RestartCount: int64(inspect.RestartCount),
	}
	if err := c.Store.IngestMetric(ctx, sample); err != nil {
		return fmt.Errorf("persist Docker stats for %s: %w", shortID(item.ID), err)
	}
	if err := c.Store.RecordDeploymentMarker(ctx, scope, "container-observed", at); err != nil {
		return fmt.Errorf("persist deployment marker for %s: %w", shortID(item.ID), err)
	}
	return nil
}

func dockerStatsRequestOptions() dockerclient.ContainerStatsOptions {
	return dockerclient.ContainerStatsOptions{
		Stream:                false,
		IncludePreviousSample: true,
	}
}

func parseDockerLogStream(raw []byte, stream string) []dockerLogLine {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), MaxMessageBytes+128)
	out := make([]dockerLogLine, 0)
	index := 0
	for scanner.Scan() {
		text := scanner.Text()
		at, message := splitDockerTimestamp(text)
		out = append(out, dockerLogLine{at: at, stream: stream, message: message, index: index})
		index++
	}
	return out
}

func splitDockerTimestamp(line string) (time.Time, string) {
	timestamp, message, found := strings.Cut(line, " ")
	if found {
		if at, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			return at.UTC(), message
		}
	}
	return time.Now().UTC(), line
}

func scopeFromDockerLabels(labels map[string]string) (Scope, bool) {
	scope := Scope{
		ProjectID:     labels[runtimedocker.LabelProject],
		EnvironmentID: labels[runtimedocker.LabelEnvironment],
		ServiceID:     labels[runtimedocker.LabelService],
		DeploymentID:  labels[runtimedocker.LabelDeployment],
		ReplicaID:     labels[runtimedocker.LabelInstance],
	}
	return scope, scope.ProjectID != "" && scope.EnvironmentID != "" &&
		scope.ServiceID != "" && scope.DeploymentID != "" && scope.ReplicaID != ""
}

func dockerCPUPercent(stats container.StatsResponse) float64 {
	if stats.CPUStats.CPUUsage.TotalUsage < stats.PreCPUStats.CPUUsage.TotalUsage ||
		stats.CPUStats.SystemUsage < stats.PreCPUStats.SystemUsage {
		return 0
	}
	cpuDelta := stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage
	systemDelta := stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage
	if cpuDelta == 0 || systemDelta == 0 {
		return 0
	}
	cpus := stats.CPUStats.OnlineCPUs
	if cpus == 0 {
		cpus = uint32(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpus == 0 {
		cpus = 1
	}
	return float64(cpuDelta) / float64(systemDelta) * float64(cpus) * 100
}

func dockerLogLevel(stream, message string) string {
	value := strings.ToLower(message)
	switch {
	case stream == "stderr" || strings.Contains(value, "error") || strings.Contains(value, "fatal"):
		return "error"
	case strings.Contains(value, "warn"):
		return "warn"
	case strings.Contains(value, "debug") || strings.Contains(value, "trace"):
		return "debug"
	default:
		return "info"
	}
}

func safeUint64(value uint64) int64 {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if value > maxInt64 {
		return int64(maxInt64)
	}
	return int64(value)
}

func shortID(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func (c *DockerCollector) now() time.Time {
	if c.Now == nil {
		return time.Now().UTC()
	}
	return c.Now().UTC()
}

func (c *DockerCollector) collectorID() string {
	if c.ID != "" {
		return c.ID
	}
	return "docker-collector"
}
