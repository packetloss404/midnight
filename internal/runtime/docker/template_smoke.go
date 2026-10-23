// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package docker

import (
	"context"
	"fmt"
	"strings"
)

// CleanupTemplateSmoke removes retained terminal probe containers after their
// metadata-only result is durable. It refuses to remove foreign/service
// containers even if a caller supplies the wrong id.
func (a *Adapter) CleanupTemplateSmoke(ctx context.Context, containerIDs []string) error {
	if a == nil || a.client == nil {
		return fmt.Errorf("template smoke cleanup: Docker runtime is unavailable")
	}
	seen := make(map[string]struct{}, len(containerIDs))
	for _, id := range containerIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		inspect, err := a.client.ContainerInspect(ctx, id)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("template smoke cleanup inspect: %w", err)
		}
		if inspect.Config == nil || !ownedByMidnight(inspect.Config.Labels) ||
			inspect.Config.Labels[LabelWorkloadKind] != WorkloadKindTemplateSmoke {
			return fmt.Errorf("template smoke cleanup refused non-probe container %s", id)
		}
		if err := a.client.ContainerRemove(ctx, id, true); err != nil && !isNotFound(err) {
			return fmt.Errorf("template smoke cleanup remove: %w", err)
		}
	}
	return nil
}
