package runtime

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeHealthChecksDefaultsStartupTimeout(t *testing.T) {
	checks := NormalizeHealthChecks([]HealthCheckSpec{{Type: HealthCheckStartup, Command: []string{"/health"}}})

	if len(checks) != 1 || checks[0].Timeout != DefaultStartupTimeout {
		t.Fatalf("checks = %+v", checks)
	}
}

func TestValidateHealthChecksAcceptsTaxonomy(t *testing.T) {
	checks := []HealthCheckSpec{
		{Type: HealthCheckReadiness, HTTPPath: "/ready", HTTPMethod: "GET"},
		{Type: HealthCheckLiveness, TCPPort: 8080},
		{Type: HealthCheckStartup, Command: []string{"/bin/check"}, Timeout: 3 * time.Minute},
	}

	if err := ValidateHealthChecks(checks); err != nil {
		t.Fatalf("ValidateHealthChecks() error = %v", err)
	}
}

func TestValidateHealthChecksRejectsShortStartupTimeout(t *testing.T) {
	err := ValidateHealthChecks([]HealthCheckSpec{{Type: HealthCheckStartup, Command: []string{"/bin/check"}, Timeout: time.Minute}})

	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("ValidateHealthChecks() error = %v", err)
	}
}

func TestValidateHealthChecksRejectsDuplicateAndAmbiguousTargets(t *testing.T) {
	if err := ValidateHealthChecks([]HealthCheckSpec{{Type: HealthCheckReadiness, HTTPPath: "/ready"}, {Type: HealthCheckReadiness, TCPPort: 8080}}); err == nil {
		t.Fatal("expected duplicate health check type error")
	}
	if err := ValidateHealthChecks([]HealthCheckSpec{{Type: HealthCheckLiveness, HTTPPath: "/live", TCPPort: 8080}}); err == nil {
		t.Fatal("expected ambiguous probe target error")
	}
}

func TestParseHTTPHealthCheck(t *testing.T) {
	check, err := ParseHTTPHealthCheck(HealthCheckReadiness, "http://localhost:8080/healthz", "GET")
	if err != nil {
		t.Fatalf("ParseHTTPHealthCheck: %v", err)
	}
	if check.HTTPPath != "/healthz" || check.HTTPPort != 8080 || check.HTTPMethod != "GET" {
		t.Fatalf("unexpected http check: %+v", check)
	}
	if check.Shape() != HealthCheckShapeHTTP {
		t.Fatalf("expected http shape, got %s", check.Shape())
	}
}

func TestParseHTTPHealthCheckRejectsNonHTTPScheme(t *testing.T) {
	if _, err := ParseHTTPHealthCheck(HealthCheckReadiness, "tcp://localhost:80/", "GET"); err == nil {
		t.Fatal("expected scheme error")
	}
}

func TestParseTCPHealthCheck(t *testing.T) {
	check, err := ParseTCPHealthCheck(HealthCheckLiveness, "127.0.0.1:5432")
	if err != nil {
		t.Fatalf("ParseTCPHealthCheck: %v", err)
	}
	if check.TCPPort != 5432 || check.Shape() != HealthCheckShapeTCP {
		t.Fatalf("unexpected tcp check: %+v", check)
	}
}

func TestParseTCPHealthCheckRequiresPort(t *testing.T) {
	if _, err := ParseTCPHealthCheck(HealthCheckLiveness, "127.0.0.1"); err == nil {
		t.Fatal("expected error for missing port")
	}
	if _, err := ParseTCPHealthCheck(HealthCheckLiveness, "127.0.0.1:0"); err == nil {
		t.Fatal("expected error for zero port")
	}
}

func TestParseExecHealthCheck(t *testing.T) {
	check, err := ParseExecHealthCheck(HealthCheckReadiness, []string{"/bin/check", "--ping"})
	if err != nil {
		t.Fatalf("ParseExecHealthCheck: %v", err)
	}
	if check.Shape() != HealthCheckShapeExec || len(check.Command) != 2 {
		t.Fatalf("unexpected exec check: %+v", check)
	}
}

func TestValidateHealthChecksRejectsBadHTTPPath(t *testing.T) {
	err := ValidateHealthChecks([]HealthCheckSpec{{Type: HealthCheckReadiness, HTTPPath: "noslash"}})
	if err == nil || !strings.Contains(err.Error(), "must start with /") {
		t.Fatalf("expected http path error, got %v", err)
	}
}

func TestValidateHealthChecksRejectsBadHTTPMethod(t *testing.T) {
	err := ValidateHealthChecks([]HealthCheckSpec{{Type: HealthCheckReadiness, HTTPPath: "/ok", HTTPMethod: "POST"}})
	if err == nil || !strings.Contains(err.Error(), "method") {
		t.Fatalf("expected http method error, got %v", err)
	}
}
