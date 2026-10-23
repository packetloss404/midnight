// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package observability

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/db/pg"
)

var ErrTraefikScopeNotFound = errors.New("no ready durable route matches the Traefik access record")

type PostgresTraefikScopeResolver struct {
	Pool *pg.Pool
}

func (r PostgresTraefikScopeResolver) ResolveTraefikScope(ctx context.Context, access TraefikAccessRecord) (Scope, error) {
	if r.Pool == nil {
		return Scope{}, errors.New("Traefik route scope resolver is unavailable")
	}
	host := strings.ToLower(strings.TrimSpace(access.RequestHost))
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	path := strings.TrimSpace(access.RequestPath)
	if path == "" {
		path = "/"
	}
	var scope Scope
	err := r.Pool.QueryRow(ctx, `
SELECT d.project_id, d.environment_id, d.service_id,
       b.deployment_id, COALESCE(dep.operation_id,''), b.instance_id
FROM domains d
JOIN http_routes route
  ON route.domain_id = d.id AND route.public_enabled = TRUE
JOIN route_backends b
  ON b.route_id = route.id AND b.ready = TRUE
JOIN deployments dep ON dep.id = b.deployment_id
WHERE d.normalized_hostname = $1
  AND ($2 = route.path_prefix OR $2 LIKE rtrim(route.path_prefix, '/') || '/%')
ORDER BY length(route.path_prefix) DESC, b.observed_at DESC
LIMIT 1`, host, path).Scan(
		&scope.ProjectID, &scope.EnvironmentID, &scope.ServiceID,
		&scope.DeploymentID, &scope.OperationID, &scope.ReplicaID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Scope{}, ErrTraefikScopeNotFound
	}
	if err != nil {
		return Scope{}, fmt.Errorf("resolve Traefik durable route scope: %w", err)
	}
	return scope, nil
}

type traefikFileCursor struct {
	Identity string `json:"identity"`
	Offset   int64  `json:"offset"`
}

type TraefikFileCollectorStore interface {
	IngestLog(context.Context, LogRecord) (IngestResult, error)
	CollectorCursor(context.Context, string, any) (time.Time, bool, error)
	SaveCollectorCursor(context.Context, string, time.Time, any) error
	HeartbeatCollector(context.Context, string, string, error) error
}

type TraefikFileCollector struct {
	Path     string
	ID       string
	Store    TraefikFileCollectorStore
	Resolver TraefikScopeResolver
	Interval time.Duration
	Now      func() time.Time
}

func (c *TraefikFileCollector) Run(ctx context.Context, report func(error)) {
	if report == nil {
		report = func(error) {}
	}
	collect := func() {
		err := c.CollectOnce(ctx)
		if c.Store == nil {
			report(err)
			return
		}
		if heartbeatErr := c.Store.HeartbeatCollector(ctx, c.collectorID(), "traefik", err); heartbeatErr != nil {
			report(heartbeatErr)
		}
		if err != nil {
			report(err)
		}
	}
	collect()
	interval := c.Interval
	if interval <= 0 {
		interval = 5 * time.Second
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

func (c *TraefikFileCollector) CollectOnce(ctx context.Context) error {
	if c == nil || c.Store == nil || c.Resolver == nil || strings.TrimSpace(c.Path) == "" {
		return errors.New("Traefik file collector is not configured")
	}
	file, err := os.Open(filepath.Clean(c.Path))
	if err != nil {
		return fmt.Errorf("open Traefik access log: %w", err)
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	identity := traefikFileIdentity(stat)
	checkpointKey := "traefik-file:" + hashString(filepath.Clean(c.Path))
	var cursor traefikFileCursor
	_, found, err := c.Store.CollectorCursor(ctx, checkpointKey, &cursor)
	if err != nil {
		return err
	}
	if !found || cursor.Identity != identity || cursor.Offset < 0 || cursor.Offset > stat.Size() {
		cursor = traefikFileCursor{Identity: identity}
	}
	if _, err := file.Seek(cursor.Offset, io.SeekStart); err != nil {
		return err
	}
	ingestor := TraefikAccessIngestor{
		Store: c.Store, Resolver: c.Resolver,
		SourceID: c.collectorID() + ":" + identity, Now: c.Now,
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	scanner.Split(splitCompletedTraefikLine)
	for scanner.Scan() {
		lineStart := cursor.Offset
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) > 0 {
			if err := ingestor.IngestLine(ctx, line, lineStart); err != nil {
				// Traefik also logs requests for its own dashboard and
				// unmatched hosts. They have no tenant scope, so advance
				// the durable file cursor without persisting them.
				if !errors.Is(err, ErrTraefikScopeNotFound) {
					return err
				}
			}
		}
		cursor.Offset = lineStart + int64(len(line)) + 1
		if err := c.Store.SaveCollectorCursor(ctx, checkpointKey, c.now(), cursor); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// splitCompletedTraefikLine deliberately withholds a trailing partial record.
// Traefik may still be writing it; the next collection resumes from the same
// durable offset. Scanner's maximum token size bounds hostile or corrupt lines.
func splitCompletedTraefikLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return index + 1, data[:index], nil
	}
	if atEOF {
		return 0, nil, nil
	}
	return 0, nil, nil
}

func traefikFileIdentity(info os.FileInfo) string {
	// Stat_t is intentionally inspected without a platform-specific type
	// assertion so the API/CLI repository still cross-compiles for Windows.
	// Linux production yields its stable inode here; unsupported filesystems
	// fall back to the stable name and still recover when truncation is seen.
	value := reflect.ValueOf(info.Sys())
	if value.IsValid() && value.Kind() == reflect.Ptr && !value.IsNil() {
		value = value.Elem()
	}
	if value.IsValid() && value.Kind() == reflect.Struct {
		inode := value.FieldByName("Ino")
		if inode.IsValid() {
			switch inode.Kind() {
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				return fmt.Sprintf("%d", inode.Uint())
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				return fmt.Sprintf("%d", inode.Int())
			}
		}
	}
	return info.Name()
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func (c *TraefikFileCollector) now() time.Time {
	if c.Now == nil {
		return time.Now().UTC()
	}
	return c.Now().UTC()
}

func (c *TraefikFileCollector) collectorID() string {
	if strings.TrimSpace(c.ID) != "" {
		return strings.TrimSpace(c.ID)
	}
	return "traefik-access"
}
