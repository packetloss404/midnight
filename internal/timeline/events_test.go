package timeline

import "testing"

func TestEventTypesAreStable(t *testing.T) {
	tests := map[EventType]string{
		EventIngressRouteRendered:        "ingress.route.rendered",
		EventIngressRouteArtifactWritten: "ingress.route.artifact.written",
		EventACMEDNSPending:              "acme.dns.pending",
		EventACMECertificateReady:        "acme.certificate.ready",
		EventIngressRouteActive:          "ingress.route.active",
	}
	for eventType, want := range tests {
		if string(eventType) != want {
			t.Fatalf("event type = %q, want %q", eventType, want)
		}
	}
}

func TestDeploymentHeartbeatGapStatusesAreStable(t *testing.T) {
	tests := map[DeploymentHeartbeatGapStatus]string{
		DeploymentHeartbeatGapOK:      "ok",
		DeploymentHeartbeatGapWarning: "warning",
		DeploymentHeartbeatGapStuck:   "stuck",
	}
	for status, want := range tests {
		if string(status) != want {
			t.Fatalf("heartbeat gap status = %q, want %q", status, want)
		}
	}
}

func TestDeploymentHeartbeatGapIssueCodesAreStable(t *testing.T) {
	tests := map[DeploymentHeartbeatGapIssueCode]string{
		DeploymentHeartbeatIssueClockSkew: "deployment_heartbeat_clock_skew",
		DeploymentHeartbeatIssueMissing:   "deployment_heartbeat_missing",
		DeploymentHeartbeatIssueWarning:   "deployment_heartbeat_gap_warning",
		DeploymentHeartbeatIssueStuck:     "deployment_heartbeat_gap_stuck",
	}
	for code, want := range tests {
		if string(code) != want {
			t.Fatalf("heartbeat gap issue code = %q, want %q", code, want)
		}
	}
}
