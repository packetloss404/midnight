package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatRequestOmitsMaintenanceFieldsByDefault(t *testing.T) {
	data, err := json.Marshal(HeartbeatRequest{AgentID: "agent-a", Sequence: 1, Status: "ok"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(data), "maintenance_mode") || strings.Contains(string(data), "drain_mode") {
		t.Fatalf("heartbeat json = %s", data)
	}
}

func TestHeartbeatRequestIncludesMaintenanceFieldsWhenSet(t *testing.T) {
	data, err := json.Marshal(HeartbeatRequest{AgentID: "agent-a", Sequence: 1, Status: "draining", MaintenanceMode: true, DrainMode: true})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"maintenance_mode":true`) || !strings.Contains(string(data), `"drain_mode":true`) {
		t.Fatalf("heartbeat json = %s", data)
	}
}

func TestRuntimeObservationRouteTargetJSONFields(t *testing.T) {
	observedAt := time.Unix(100, 0).UTC()
	observation := RuntimeObservation{
		AgentID:    "agent-a",
		HostID:     "host-a",
		Runtime:    "docker",
		ObservedAt: observedAt,
		RouteTargets: []RouteTargetObservation{{
			ProjectID:           "project-a",
			EnvironmentID:       "env-a",
			DeploymentID:        "deployment-a",
			InstanceID:          "instance-a",
			ContainerID:         "container-a",
			ContainerIP:         "10.77.0.12",
			TargetProtocol:      "http",
			InternalServicePort: 8080,
			ReadinessStatus:     "ready",
			LastObservedAt:      observedAt,
		}},
	}

	data, err := json.Marshal(observation)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	targets, ok := got["route_targets"].([]any)
	if !ok || len(targets) != 1 {
		t.Fatalf("route_targets = %#v", got["route_targets"])
	}
	target, ok := targets[0].(map[string]any)
	if !ok {
		t.Fatalf("target = %#v", targets[0])
	}
	if target["container_ip"] != "10.77.0.12" || target["internal_service_port"] != float64(8080) || target["readiness_status"] != "ready" {
		t.Fatalf("target = %#v", target)
	}
}
