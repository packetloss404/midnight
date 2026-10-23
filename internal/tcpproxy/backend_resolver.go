package tcpproxy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	dbpg "github.com/midnight/midnight/internal/db/pg"
)

type PostgresBackendResolver struct {
	Pool *dbpg.Pool
}

func (r PostgresBackendResolver) ResolveBackends(ctx context.Context, scope Scope) ([]Backend, error) {
	if r.Pool == nil {
		return nil, errors.New("tcp proxy backend resolver pool is required")
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT i.deployment_id, i.instance_id, i.container_ip
		  FROM service_environment_state ses
		  JOIN deployments d
		    ON d.id = ses.active_deployment_id
		   AND d.project_id = $1
		   AND d.environment_id = $2
		   AND d.service_id = $3
		   AND d.state = 'active'
		   AND d.active = TRUE
		  JOIN deployment_instances i
		    ON i.deployment_id = d.id
		   AND i.desired_state = 'running'
		   AND i.observed_state = 'running'
		   AND i.ready = TRUE
		 WHERE ses.service_id = $3
		   AND ses.environment_id = $2
		   AND i.container_ip <> ''
		 ORDER BY i.replica_ordinal, i.instance_id`,
		scope.ProjectID, scope.EnvironmentID, scope.ServiceID,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve tcp proxy active backends: %w", err)
	}
	defer rows.Close()
	var backends []Backend
	for rows.Next() {
		var backend Backend
		if err := rows.Scan(&backend.DeploymentID, &backend.InstanceID, &backend.Host); err != nil {
			return nil, fmt.Errorf("resolve tcp proxy active backends: %w", err)
		}
		address, err := netip.ParseAddr(strings.TrimSpace(backend.Host))
		if err != nil || !address.IsPrivate() || address.IsLoopback() || address.IsUnspecified() ||
			address.IsMulticast() || address.IsLinkLocalUnicast() {
			return nil, ErrBackendUnavailable
		}
		backend.Host = address.String()
		backends = append(backends, backend)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve tcp proxy active backends: %w", err)
	}
	if len(backends) == 0 {
		return nil, ErrBackendUnavailable
	}
	return backends, nil
}
