package timeline

import "time"

type EventType string

const (
	EventIngressRouteRendered        EventType = "ingress.route.rendered"
	EventIngressRouteArtifactWritten EventType = "ingress.route.artifact.written"
	EventACMEDNSPending              EventType = "acme.dns.pending"
	EventACMECertificateReady        EventType = "acme.certificate.ready"
	EventIngressRouteActive          EventType = "ingress.route.active"
)

type Event struct {
	DeploymentID string
	EventSeq     int64
	Type         EventType
	TenantID     string
	RouteName    string
	Host         string
	At           time.Time
	Message      string
}
