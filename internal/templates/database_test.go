package templates

import "testing"

func TestOfficialDatabaseTemplatesValidate(t *testing.T) {
	templates := OfficialDatabaseTemplates()
	if len(templates) != 5 {
		t.Fatalf("OfficialDatabaseTemplates() length = %d, want 5", len(templates))
	}
	seen := map[string]bool{}
	for _, tmpl := range templates {
		if seen[tmpl.ID] {
			t.Fatalf("duplicate template id %q", tmpl.ID)
		}
		seen[tmpl.ID] = true
		if err := ValidateOfficialDatabaseTemplate(tmpl); err != nil {
			t.Fatalf("ValidateOfficialDatabaseTemplate(%q) error = %v", tmpl.ID, err)
		}
	}
	for _, want := range []string{"postgres-16", "redis-7-cache", "redis-7-durable", "mysql-8", "mongodb-7-single-node"} {
		if !seen[want] {
			t.Fatalf("OfficialDatabaseTemplates() missing %q", want)
		}
	}
}

func TestValidateOfficialDatabaseTemplateRejectsFailedSupportGate(t *testing.T) {
	tmpl := mysqlTemplate()
	tmpl.SupportGates["restore"] = false
	if err := ValidateOfficialDatabaseTemplate(tmpl); err == nil {
		t.Fatal("ValidateOfficialDatabaseTemplate() error = nil, want failed gate error")
	}
}

func TestValidateOfficialDatabaseTemplateRequiresPinnedDigest(t *testing.T) {
	tmpl := redisDurableTemplate()
	tmpl.Image = "redis:7.2.5"
	if err := ValidateOfficialDatabaseTemplate(tmpl); err == nil {
		t.Fatal("ValidateOfficialDatabaseTemplate() error = nil, want digest pin error")
	}
}

func TestOfficialDatabaseTemplatesDoNotExposeTCPByDefault(t *testing.T) {
	for _, tmpl := range OfficialDatabaseTemplates() {
		if tmpl.TCPProxyDefaultEnabled {
			t.Fatalf("template %q TCPProxyDefaultEnabled = true, want false", tmpl.ID)
		}
	}
}

func TestValidateOfficialDatabaseTemplateRejectsDefaultTCPProxyExposure(t *testing.T) {
	tmpl := postgresTemplate()
	tmpl.TCPProxyDefaultEnabled = true
	if err := ValidateOfficialDatabaseTemplate(tmpl); err == nil {
		t.Fatal("ValidateOfficialDatabaseTemplate() error = nil, want TCP proxy default exposure error")
	}
}

func TestRedisTemplatesDeclareDurabilityMode(t *testing.T) {
	for _, tmpl := range OfficialDatabaseTemplates() {
		if tmpl.Engine != DatabaseEngineRedis {
			continue
		}
		if tmpl.RedisMode != RedisModeCache && tmpl.RedisMode != RedisModeDurable {
			t.Fatalf("redis template %q mode = %q", tmpl.ID, tmpl.RedisMode)
		}
	}
}

func TestMongoDBTemplateRejectsUnsupportedClusterModes(t *testing.T) {
	tmpl := mongoSingleNodeTemplate()
	tmpl.UnsupportedModes = []string{"replica-set"}
	if err := ValidateOfficialDatabaseTemplate(tmpl); err == nil {
		t.Fatal("ValidateOfficialDatabaseTemplate() error = nil, want sharded-cluster rejection requirement")
	}
}

func TestRequiredSupportGatesReturnsCopy(t *testing.T) {
	gates := RequiredSupportGates()
	gates[0] = "mutated"
	if RequiredSupportGates()[0] == "mutated" {
		t.Fatal("RequiredSupportGates() returned mutable package slice")
	}
}
