package timeline

import "fmt"

var eventSequence = map[EventType]int{
	EventIngressRouteRendered:        0,
	EventIngressRouteArtifactWritten: 1,
	EventACMEDNSPending:              2,
	EventACMECertificateReady:        3,
	EventIngressRouteActive:          4,
}

// ValidateEventSequence verifies ingress lifecycle events are emitted in order per route.
func ValidateEventSequence(events []Event) error {
	lastByRoute := make(map[string]int)
	for _, event := range events {
		step, ok := eventSequence[event.Type]
		if !ok {
			return fmt.Errorf("unknown event type %q", event.Type)
		}

		key := event.TenantID + "\x00" + event.RouteName + "\x00" + event.Host
		last, seen := lastByRoute[key]
		if !seen && step != 0 {
			return fmt.Errorf("event %q is missing prior events for route %q", event.Type, event.RouteName)
		}
		if seen && step < last {
			return fmt.Errorf("event %q is out of order for route %q", event.Type, event.RouteName)
		}
		if seen && step > last+1 {
			return fmt.Errorf("event %q is missing prior events for route %q", event.Type, event.RouteName)
		}
		lastByRoute[key] = step
	}
	return nil
}
