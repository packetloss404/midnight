// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package observability

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// TraefikScopeResolver binds a real access record to the durable route that
// served it. Routing owns the implementation because host/path publication is
// its source of truth; observability owns parsing, redaction, and persistence.
type TraefikScopeResolver interface {
	ResolveTraefikScope(context.Context, TraefikAccessRecord) (Scope, error)
}

type TraefikAccessRecord struct {
	StartedAt        time.Time
	RequestHost      string
	RequestPath      string
	RequestMethod    string
	Status           int64
	LatencyNanos     int64
	ReceivedBytes    int64
	TransmittedBytes int64
	RouterName       string
	ServiceName      string
	ClientHost       string
}

type TraefikAccessIngestor struct {
	Store interface {
		IngestLog(context.Context, LogRecord) (IngestResult, error)
	}
	Resolver TraefikScopeResolver
	SourceID string
	Now      func() time.Time
}

func (i TraefikAccessIngestor) IngestNDJSON(ctx context.Context, reader io.Reader) error {
	if i.Store == nil || i.Resolver == nil {
		return errors.New("Traefik access ingestion requires a store and route scope resolver")
	}
	scanner := bufio.NewScanner(io.LimitReader(reader, 128<<20))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var lineNumber int64
	for scanner.Scan() {
		lineNumber++
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		if err := i.IngestLine(ctx, []byte(scanner.Text()), lineNumber); err != nil {
			return fmt.Errorf("Traefik access line %d: %w", lineNumber, err)
		}
	}
	return scanner.Err()
}

func (i TraefikAccessIngestor) IngestLine(ctx context.Context, raw []byte, ordinal int64) error {
	record, err := ParseTraefikAccessJSON(raw)
	if err != nil {
		return err
	}
	if record.StartedAt.IsZero() {
		if i.Now == nil {
			record.StartedAt = time.Now().UTC()
		} else {
			record.StartedAt = i.Now().UTC()
		}
	}
	scope, err := i.Resolver.ResolveTraefikScope(ctx, record)
	if err != nil {
		return fmt.Errorf("resolve durable route scope: %w", err)
	}
	sum := sha256.Sum256(raw)
	sourceID := strings.TrimSpace(i.SourceID)
	if sourceID == "" {
		sourceID = "traefik"
	}
	_, err = i.Store.IngestLog(ctx, LogRecord{
		ObservedAt: record.StartedAt,
		Scope:      scope,
		Surface:    "http",
		Stream:     "access",
		Level:      httpLogLevel(record.Status),
		Component:  "traefik",
		Message: fmt.Sprintf("%s %s %d",
			record.RequestMethod, record.RequestPath, record.Status),
		Attributes: map[string]any{
			"request_host": record.RequestHost, "request_path": record.RequestPath,
			"request_method": record.RequestMethod, "status": record.Status,
			"latency_ns": record.LatencyNanos, "rx_bytes": record.ReceivedBytes,
			"tx_bytes": record.TransmittedBytes, "router_name": record.RouterName,
			"service_name": record.ServiceName, "client_host": record.ClientHost,
		},
		SourceKey: "traefik-access:" + sourceID,
		SourceSequence: record.StartedAt.Format(time.RFC3339Nano) + ":" +
			strconv.FormatInt(ordinal, 10) + ":" + hex.EncodeToString(sum[:8]),
	})
	return err
}

func ParseTraefikAccessJSON(raw []byte) (TraefikAccessRecord, error) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return TraefikAccessRecord{}, fmt.Errorf("decode Traefik JSON access log: %w", err)
	}
	startedAt := firstTime(fields, "StartUTC", "StartLocal")
	return TraefikAccessRecord{
		StartedAt:        startedAt,
		RequestHost:      firstString(fields, "RequestHost"),
		RequestPath:      firstString(fields, "RequestPath"),
		RequestMethod:    firstString(fields, "RequestMethod"),
		Status:           firstInt(fields, "DownstreamStatus", "OriginStatus"),
		LatencyNanos:     firstInt(fields, "Duration", "OriginDuration"),
		ReceivedBytes:    firstInt(fields, "RequestContentSize"),
		TransmittedBytes: firstInt(fields, "OriginContentSize", "DownstreamContentSize"),
		RouterName:       firstString(fields, "RouterName"),
		ServiceName:      firstString(fields, "ServiceName"),
		ClientHost:       firstString(fields, "ClientHost"),
	}, nil
}

func firstString(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := fields[key].(string); ok {
			return value
		}
	}
	return ""
}

func firstInt(fields map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch value := fields[key].(type) {
		case float64:
			return int64(value)
		case json.Number:
			out, _ := value.Int64()
			return out
		case int64:
			return value
		case string:
			out, _ := strconv.ParseInt(value, 10, 64)
			return out
		}
	}
	return 0
}

func firstTime(fields map[string]any, keys ...string) time.Time {
	for _, key := range keys {
		if raw, ok := fields[key].(string); ok {
			if at, err := time.Parse(time.RFC3339Nano, raw); err == nil {
				return at.UTC()
			}
		}
	}
	return time.Time{}
}

func httpLogLevel(status int64) string {
	switch {
	case status >= 500:
		return "error"
	case status >= 400:
		return "warn"
	default:
		return "info"
	}
}
