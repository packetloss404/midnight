// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package observability

import (
	"context"
	"testing"
	"time"
)

type recordingTraefikStore struct {
	record LogRecord
}

func (s *recordingTraefikStore) IngestLog(_ context.Context, record LogRecord) (IngestResult, error) {
	s.record = record
	return IngestResult{Stored: true}, nil
}

type fixedTraefikScope struct {
	scope Scope
}

func (r fixedTraefikScope) ResolveTraefikScope(context.Context, TraefikAccessRecord) (Scope, error) {
	return r.scope, nil
}

func TestTraefikAccessIngestorPersistsStructuredHTTPFields(t *testing.T) {
	store := &recordingTraefikStore{}
	scope := Scope{
		ProjectID: "prj_1", EnvironmentID: "env_1", ServiceID: "svc_1",
		DeploymentID: "dep_1", ReplicaID: "rep_1",
	}
	ingestor := TraefikAccessIngestor{
		Store: store, Resolver: fixedTraefikScope{scope: scope}, SourceID: "edge-1",
	}
	raw := []byte(`{
		"StartUTC":"2026-07-27T20:00:00.123456789Z",
		"RequestHost":"app.example.test",
		"RequestPath":"/health",
		"RequestMethod":"GET",
		"DownstreamStatus":503,
		"Duration":1234000,
		"RequestContentSize":12,
		"OriginContentSize":42,
		"RouterName":"midnight-router",
		"ServiceName":"midnight-service"
	}`)
	if err := ingestor.IngestLine(context.Background(), raw, 7); err != nil {
		t.Fatal(err)
	}
	if store.record.Scope != scope || store.record.Surface != "http" ||
		store.record.Stream != "access" || store.record.Level != "error" {
		t.Fatalf("record identity = %#v", store.record)
	}
	for key, want := range map[string]any{
		"status": int64(503), "latency_ns": int64(1234000),
		"rx_bytes": int64(12), "tx_bytes": int64(42),
	} {
		if got := store.record.Attributes[key]; got != want {
			t.Fatalf("attribute %s = %#v, want %#v", key, got, want)
		}
	}
	if store.record.ObservedAt != time.Date(2026, 7, 27, 20, 0, 0, 123456789, time.UTC) {
		t.Fatalf("observed_at = %s", store.record.ObservedAt)
	}
}

func TestParseTraefikAccessJSONRejectsMalformedInput(t *testing.T) {
	if _, err := ParseTraefikAccessJSON([]byte(`{"DownstreamStatus":`)); err == nil {
		t.Fatal("expected malformed Traefik JSON rejection")
	}
}
