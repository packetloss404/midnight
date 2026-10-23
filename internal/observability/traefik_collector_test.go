// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package observability

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type recordingTraefikFileStore struct {
	records     []LogRecord
	cursorAt    time.Time
	cursor      traefikFileCursor
	cursorFound bool
}

func (s *recordingTraefikFileStore) IngestLog(_ context.Context, record LogRecord) (IngestResult, error) {
	s.records = append(s.records, record)
	return IngestResult{Stored: true, LogID: int64(len(s.records))}, nil
}

func (s *recordingTraefikFileStore) CollectorCursor(_ context.Context, _ string, out any) (time.Time, bool, error) {
	if !s.cursorFound {
		return time.Time{}, false, nil
	}
	cursor, ok := out.(*traefikFileCursor)
	if !ok {
		return time.Time{}, false, errors.New("unexpected cursor type")
	}
	*cursor = s.cursor
	return s.cursorAt, true, nil
}

func (s *recordingTraefikFileStore) SaveCollectorCursor(_ context.Context, _ string, at time.Time, cursor any) error {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, &s.cursor); err != nil {
		return err
	}
	s.cursorAt = at
	s.cursorFound = true
	return nil
}

func (*recordingTraefikFileStore) HeartbeatCollector(context.Context, string, string, error) error {
	return nil
}

type selectiveTraefikScope struct {
	scope Scope
}

func (r selectiveTraefikScope) ResolveTraefikScope(_ context.Context, access TraefikAccessRecord) (Scope, error) {
	if access.RequestHost != "app.example.test" {
		return Scope{}, ErrTraefikScopeNotFound
	}
	return r.scope, nil
}

func TestTraefikFileCollectorDurablyResumesAndSkipsUnscopedTraffic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.jsonl")
	first := `{"StartUTC":"2026-07-27T20:00:00Z","RequestHost":"dashboard.example.test","RequestPath":"/","RequestMethod":"GET","DownstreamStatus":200}`
	second := `{"StartUTC":"2026-07-27T20:00:01Z","RequestHost":"app.example.test","RequestPath":"/api","RequestMethod":"GET","DownstreamStatus":201}`
	if err := os.WriteFile(path, []byte(first+"\n"+second+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &recordingTraefikFileStore{}
	collector := TraefikFileCollector{
		Path: path, ID: "edge-test", Store: store,
		Resolver: selectiveTraefikScope{scope: Scope{
			ProjectID: "prj_1", EnvironmentID: "env_1", ServiceID: "svc_1",
			DeploymentID: "dep_1",
		}},
	}
	if err := collector.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 1 || store.records[0].Message != "GET /api 201" {
		t.Fatalf("records = %#v", store.records)
	}

	third := `{"StartUTC":"2026-07-27T20:00:02Z","RequestHost":"app.example.test","RequestPath":"/next","RequestMethod":"POST","DownstreamStatus":503}`
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(third + "\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	// A replacement collector gets its position from the store and only
	// persists the newly completed line.
	restarted := TraefikFileCollector{
		Path: path, ID: "edge-test", Store: store, Resolver: collector.Resolver,
	}
	if err := restarted.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 2 || store.records[1].Message != "POST /next 503" {
		t.Fatalf("records after restart = %#v", store.records)
	}
}

func TestTraefikFileCollectorDoesNotAdvancePastMalformedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.jsonl")
	if err := os.WriteFile(path, []byte("{malformed}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &recordingTraefikFileStore{}
	collector := TraefikFileCollector{
		Path: path, Store: store,
		Resolver: selectiveTraefikScope{scope: Scope{ProjectID: "prj_1"}},
	}
	if err := collector.CollectOnce(context.Background()); err == nil {
		t.Fatal("expected malformed access log error")
	}
	if store.cursorFound {
		t.Fatalf("malformed record advanced cursor to %#v", store.cursor)
	}
}

func TestTraefikFileCollectorWaitsForCompletedBoundedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.jsonl")
	line := `{"StartUTC":"2026-07-27T20:00:00Z","RequestHost":"app.example.test","RequestPath":"/partial","RequestMethod":"GET","DownstreamStatus":200}`
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &recordingTraefikFileStore{}
	collector := TraefikFileCollector{
		Path: path, Store: store,
		Resolver: selectiveTraefikScope{scope: Scope{ProjectID: "prj_1"}},
	}
	if err := collector.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 0 || store.cursorFound {
		t.Fatalf("partial line was consumed: records=%#v cursor=%#v", store.records, store.cursor)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := collector.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 1 || !store.cursorFound {
		t.Fatalf("completed line was not consumed: records=%#v cursor=%#v", store.records, store.cursor)
	}
}
