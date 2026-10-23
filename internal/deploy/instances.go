package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/runtime"
)

// ReplicaInstanceID is deterministic across worker retries and is never
// derived from a container id. r0 retains the historical deployment identity.
func ReplicaInstanceID(deploymentID string, ordinal int) string {
	return fmt.Sprintf("%s-r%d", deploymentID, ordinal)
}

type DeploymentInstance struct {
	DeploymentID, InstanceID, DesiredState, ObservedState, ContainerID, ContainerIP string
	ReplicaOrdinal                                                                  int
	Ready                                                                           bool
}

type DeploymentInstanceStore interface {
	Ensure(context.Context, string, int) ([]DeploymentInstance, error)
	Observe(context.Context, runtime.RoutingTarget) error
	SetDeploymentState(context.Context, string, string, string) error
	ReconcileRuntime(context.Context, InstanceRuntimeResolver) error
}

type InstanceRuntimeResolver interface {
	ResolveRoutingTarget(context.Context, string, string) (runtime.RoutingTarget, error)
}

type InstanceStore struct {
	Pool *pg.Pool
	Now  func() time.Time
}

func NewInstanceStore(pool *pg.Pool) *InstanceStore {
	return &InstanceStore{Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
}
func (s *InstanceStore) Ensure(ctx context.Context, deploymentID string, replicas int) ([]DeploymentInstance, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("instance store: pool required")
	}
	if replicas < 1 {
		return nil, errors.New("instance store: replicas must be positive")
	}
	now := s.Now()
	// The desired replica set is written as one statement. A retry either sees
	// the entire deterministic set or writes the remaining rows; it never
	// exposes a partially committed desired count to a second worker.
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO deployment_instances(deployment_id,instance_id,replica_ordinal,created_at,updated_at)
		SELECT $1, $1 || '-r' || ordinal::text, ordinal, $3, $3
		FROM generate_series(0, $2 - 1) AS series(ordinal)
		ON CONFLICT(deployment_id,instance_id) DO NOTHING`, deploymentID, replicas, now); err != nil {
		return nil, err
	}
	return s.List(ctx, deploymentID)
}
func (s *InstanceStore) List(ctx context.Context, deploymentID string) ([]DeploymentInstance, error) {
	rows, err := s.Pool.Query(ctx, `SELECT deployment_id,instance_id,replica_ordinal,desired_state,observed_state,container_id,container_ip,ready FROM deployment_instances WHERE deployment_id=$1 ORDER BY replica_ordinal`, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeploymentInstance
	for rows.Next() {
		var x DeploymentInstance
		if err := rows.Scan(&x.DeploymentID, &x.InstanceID, &x.ReplicaOrdinal, &x.DesiredState, &x.ObservedState, &x.ContainerID, &x.ContainerIP, &x.Ready); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *InstanceStore) Observe(ctx context.Context, target runtime.RoutingTarget) error {
	now := s.Now()
	ready := target.Ready && strings.TrimSpace(target.ContainerIP) != ""
	state := "running"
	if !ready {
		state = "starting"
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE deployment_instances SET observed_state=$3,container_id=$4,container_ip=$5,ready=$6,updated_at=$7 WHERE deployment_id=$1 AND instance_id=$2`, target.DeploymentID, target.InstanceID, state, target.ContainerID, strings.TrimSpace(target.ContainerIP), ready, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("deployment instance %s/%s was not found", target.DeploymentID, target.InstanceID)
	}
	return nil
}

// SetDeploymentState records action and rollback intent for every replica.
// Observed state is deliberately reset when a container is stopped/removed so
// a later worker reconciliation cannot mistake old ready evidence for a live
// backend.
func (s *InstanceStore) SetDeploymentState(ctx context.Context, deploymentID, desiredState, observedState string) error {
	now := s.Now()
	_, err := s.Pool.Exec(ctx, `UPDATE deployment_instances
		SET desired_state=$2, observed_state=$3, ready=FALSE,
			container_ip=CASE WHEN $3='running' THEN container_ip ELSE '' END,
			updated_at=$4
		WHERE deployment_id=$1`, deploymentID, desiredState, observedState, now)
	return err
}

func (s *InstanceStore) markUnresolved(ctx context.Context, deploymentID, instanceID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE deployment_instances
		SET observed_state='unavailable',ready=FALSE,container_ip='',updated_at=$3
		WHERE deployment_id=$1 AND instance_id=$2`, deploymentID, instanceID, s.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("deployment instance %s/%s was not found", deploymentID, instanceID)
	}
	return nil
}

// ReconcileRuntime repairs persisted instance evidence after a worker restart.
// Docker's adapter has already rebuilt its label cache before this runs.
func (s *InstanceStore) ReconcileRuntime(ctx context.Context, resolver InstanceRuntimeResolver) error {
	if s == nil || s.Pool == nil || resolver == nil {
		return nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT deployment_id,instance_id FROM deployment_instances WHERE desired_state='running'`)
	if err != nil {
		return err
	}
	type instanceRef struct{ deploymentID, instanceID string }
	var refs []instanceRef
	for rows.Next() {
		var dep, id string
		if err := rows.Scan(&dep, &id); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, instanceRef{dep, id})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, ref := range refs {
		target, err := resolver.ResolveRoutingTarget(ctx, ref.deploymentID, ref.instanceID)
		if err != nil {
			if markErr := s.markUnresolved(ctx, ref.deploymentID, ref.instanceID); markErr != nil {
				return errors.Join(err, markErr)
			}
			continue
		}
		if err = s.Observe(ctx, target); err != nil {
			return err
		}
	}
	return nil
}
