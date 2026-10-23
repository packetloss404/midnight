package observability

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// ParsePrometheusAlerts extracts the locked alert metadata from the packaged YAML.
func ParsePrometheusAlerts(data []byte) ([]AlertRule, error) {
	var alerts []AlertRule
	var current *AlertRule
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "- alert:"):
			if current != nil {
				alerts = append(alerts, *current)
			}
			current = &AlertRule{Name: strings.TrimSpace(strings.TrimPrefix(line, "- alert:"))}
		case current != nil && strings.HasPrefix(line, "expr:"):
			current.Expression = strings.TrimSpace(strings.TrimPrefix(line, "expr:"))
		case current != nil && strings.HasPrefix(line, "severity:"):
			current.Severity = strings.TrimSpace(strings.TrimPrefix(line, "severity:"))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if current != nil {
		alerts = append(alerts, *current)
	}
	return alerts, nil
}

// ValidatePrometheusAlerts verifies the packaged alert file matches the locked alert set.
func ValidatePrometheusAlerts(data []byte) error {
	alerts, err := ParsePrometheusAlerts(data)
	if err != nil {
		return err
	}
	if err := ValidateLockedAlerts(alerts); err != nil {
		return err
	}
	want := make(map[string]AlertRule, len(LockedAlerts))
	for _, alert := range LockedAlerts {
		want[alert.Name] = alert
	}
	for _, alert := range alerts {
		expected := want[alert.Name]
		if alert.Expression != expected.Expression || alert.Severity != expected.Severity {
			return fmt.Errorf("locked alert %s does not match expected expression and severity", alert.Name)
		}
	}
	return nil
}
