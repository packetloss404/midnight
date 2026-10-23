// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package topology

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreCreateReplaceAndCAS(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	want := Layout{WorkspaceID: "wrk_1", ProjectID: "prj_1", EnvironmentID: "env_1", Nodes: []Node{{ServiceID: "svc_1", X: 20, Y: 30}}}
	created, err := store.Replace(context.Background(), want, 0)
	if err != nil || created.Version != 1 {
		t.Fatalf("create=(%+v,%v)", created, err)
	}
	if _, err := store.Replace(context.Background(), want, 0); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale create err=%v", err)
	}
	now = now.Add(time.Minute)
	want.Nodes[0].X = 70
	updated, err := store.Replace(context.Background(), want, 1)
	if err != nil || updated.Version != 2 || updated.Nodes[0].X != 70 {
		t.Fatalf("update=(%+v,%v)", updated, err)
	}
	got, err := store.Get(context.Background(), "prj_1", "env_1")
	if err != nil || got.Version != 2 || !got.CreatedAt.Before(got.UpdatedAt) {
		t.Fatalf("get=(%+v,%v)", got, err)
	}
}

func TestValidateRejectsDuplicatesAndBounds(t *testing.T) {
	if err := Validate([]Node{{ServiceID: "svc_1"}, {ServiceID: "svc_1"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate err=%v", err)
	}
	if err := Validate([]Node{{ServiceID: "svc_1", X: MaxCoordinate + 1}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bounds err=%v", err)
	}
}
