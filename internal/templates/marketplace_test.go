package templates

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestMarketplaceCardsExposeOfficialDatabaseTemplates(t *testing.T) {
	cards := MarketplaceCards(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))
	if len(cards) != len(OfficialDatabaseTemplates()) {
		t.Fatalf("cards len = %d, want %d", len(cards), len(OfficialDatabaseTemplates()))
	}
	for _, card := range cards {
		if card.TrustClass != TrustClassOfficial || card.MaintenanceStatus != MaintenanceActive || card.LastUpdated.IsZero() {
			t.Fatalf("card = %#v", card)
		}
		if !hasTag(card.Tags, "database") || !hasTag(card.Tags, "official") {
			t.Fatalf("card tags = %#v", card.Tags)
		}
	}
}

func TestSearchMarketplaceFiltersByQueryAndTag(t *testing.T) {
	cards := MarketplaceCards(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))
	redis := SearchMarketplace(cards, MarketplaceFilter{Query: "redis", Tag: "durable"})
	if len(redis) != 1 || redis[0].ID != "redis-7-durable" {
		t.Fatalf("redis search = %#v", redis)
	}
	mongo := SearchMarketplace(cards, MarketplaceFilter{Tag: "single-node"})
	if len(mongo) != 1 || mongo[0].Engine != DatabaseEngineMongoDB {
		t.Fatalf("mongo search = %#v", mongo)
	}
}

func TestDeployButtonURL(t *testing.T) {
	got, err := DeployButtonURL("https://midnight.example.com", "https://github.com/acme/app", "abc123", "wrk_acme")
	if err != nil {
		t.Fatalf("DeployButtonURL: %v", err)
	}
	if !strings.HasPrefix(got, "https://midnight.example.com/new/template?") || !strings.Contains(got, "workspace=wrk_acme") || !strings.Contains(got, "ref=abc123") {
		t.Fatalf("deploy url = %q", got)
	}
	if _, err := DeployButtonURL("http://midnight.example.com", "https://github.com/acme/app", "abc123", "wrk_acme"); err == nil {
		t.Fatal("expected non-https dashboard URL to be rejected")
	}
	if _, err := DeployButtonURL("https://midnight.example.com", "https://github.com/acme/app?token=secret", "abc123", "wrk_acme"); err == nil {
		t.Fatal("expected source URL with query to be rejected")
	}
}

func TestPlanInstantiationBuildsOfflineDatabasePlan(t *testing.T) {
	plan, err := PlanInstantiation(InstantiationRequest{WorkspaceID: "wrk_acme", TemplateID: "postgres-16", ServiceName: "checkout-db", DatabaseName: "checkout", Variables: map[string]string{"APP_LABEL": "checkout"}}) // not-a-failure-code
	if err != nil {
		t.Fatalf("PlanInstantiation: %v", err)
	}
	if plan.Engine != DatabaseEnginePostgres || plan.ExposeTCP || plan.ImageDigest == "" || plan.Environment["APP_LABEL"] != "checkout" || plan.Status != "planned" || plan.Provisioned { // not-a-failure-code
		t.Fatalf("plan = %#v", plan)
	}
	if !strings.Contains(plan.ConnectionURL, "checkout") || strings.Contains(plan.ConnectionURL, "${DATABASE_PASSWORD}") {
		t.Fatalf("connection url = %q", plan.ConnectionURL)
	}
}

func TestPlanInstantiationOutputsRuntimeEnvironmentAndVolumeIntent(t *testing.T) {
	plan, err := PlanInstantiation(InstantiationRequest{WorkspaceID: "wrk_acme", TemplateID: "postgres-16", ServiceName: "Checkout DB", DatabaseName: "checkout"})
	if err != nil {
		t.Fatalf("PlanInstantiation: %v", err)
	}
	if plan.Environment["SERVICE_HOST"] != "checkout-db.internal" || plan.Environment["SERVICE_PORT"] != "5432" {
		t.Fatalf("environment = %#v", plan.Environment)
	}
	if plan.Environment["DATABASE_URL"] != "postgres://${SERVICE_HOST}:${SERVICE_PORT}/checkout?user=${DATABASE_USER}" {
		t.Fatalf("DATABASE_URL = %q", plan.Environment["DATABASE_URL"])
	}
	if !strings.HasPrefix(plan.Environment["DATABASE_PASSWORD"], "${credential:generated://credential/wrk-acme/checkout-db/database-password}") {
		t.Fatalf("DATABASE_PASSWORD = %q", plan.Environment["DATABASE_PASSWORD"])
	}
	if plan.Volume.Name != "midnight-wrk-acme-checkout-db-data" || plan.Volume.MountPath != "/var/lib/postgresql/data" || !plan.Volume.AttachRequired || plan.Volume.Status != "planned" {
		t.Fatalf("volume = %#v", plan.Volume)
	}
	if plan.Volume.Labels["midnight.dev/provisioned"] != "false" || plan.Volume.Labels["midnight.dev/template"] != "postgres-16" {
		t.Fatalf("volume labels = %#v", plan.Volume.Labels)
	}
	if !hasRuntimeEnv(plan.RuntimeEnv, "DATABASE_PASSWORD", true, "generated-credential") || !hasRuntimeEnv(plan.RuntimeEnv, "VOLUME_NAME", false, "volume-plan") {
		t.Fatalf("runtime env = %#v", plan.RuntimeEnv)
	}
}

func TestPlanInstantiationOutputsOfficialPostgresServicePlanEvidence(t *testing.T) {
	plan, err := PlanInstantiation(InstantiationRequest{WorkspaceID: "wrk_acme", TemplateID: "postgres-16", ServiceName: "Postgres 16 MVP", DatabaseName: "mvp"})
	if err != nil {
		t.Fatalf("PlanInstantiation: %v", err)
	}
	if !plan.Durable || plan.DatabaseName != "mvp" || plan.Status != "planned" || plan.Provisioned {
		t.Fatalf("plan state = %#v", plan)
	}
	if len(plan.CredentialRefs) != 2 ||
		!hasCredentialRef(plan.CredentialRefs, "database-user", "DATABASE_USER", "sealed") ||
		!hasCredentialRef(plan.CredentialRefs, "database-password", "DATABASE_PASSWORD", "sealed") {
		t.Fatalf("credential refs = %#v", plan.CredentialRefs)
	}
	if plan.ConnectionGuidance.Visibility != "private" ||
		plan.ConnectionGuidance.Internal != "postgres://${SERVICE_HOST}:${SERVICE_PORT}/mvp?user=${DATABASE_USER}" ||
		len(plan.ConnectionGuidance.CredentialEnvVars) != 2 {
		t.Fatalf("connection guidance = %#v", plan.ConnectionGuidance)
	}
	if plan.Networking.PrivateByDefault != true ||
		plan.Networking.InternalOnly != true ||
		plan.Networking.TCPProxyDefaultEnabled ||
		plan.Networking.ExternalTCP.Status != "disabled" ||
		!plan.Networking.ExternalTCP.SourceAllowListRequired {
		t.Fatalf("networking = %#v", plan.Networking)
	}
	if !plan.Volume.AttachRequired || plan.Volume.Status != "planned" {
		t.Fatalf("volume = %#v", plan.Volume)
	}
	if plan.Backup.Status != "needs-runtime" || plan.RestorePlanGuidance.Status != "operator-plan-required" || !plan.RestorePlanGuidance.PlanRequired {
		t.Fatalf("backup/restore = %#v / %#v", plan.Backup, plan.RestorePlanGuidance)
	}
}

func TestPlanInstantiationPostgresEvidenceDoesNotExposePlaintextSecrets(t *testing.T) {
	plan, err := PlanInstantiation(InstantiationRequest{WorkspaceID: "wrk_acme", TemplateID: "postgres-16", ServiceName: "Postgres 16 MVP", DatabaseName: "mvp"})
	if err != nil {
		t.Fatalf("PlanInstantiation: %v", err)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	if regexp.MustCompile(`postgres://[^\s/:]+:[^@\s]+@`).MatchString(encoded) {
		t.Fatalf("plan evidence contains credential-bearing postgres URL: %s", encoded)
	}
	for _, leaked := range []string{"hunter2", "super-secret", "midnight:secret", "midnight:password"} {
		if strings.Contains(encoded, leaked) {
			t.Fatalf("plan evidence leaked %q: %s", leaked, encoded)
		}
	}
}

func TestPlanInstantiationOutputsPendingBackupReadiness(t *testing.T) {
	plan, err := PlanInstantiation(InstantiationRequest{WorkspaceID: "wrk_acme", TemplateID: "redis-7-durable", ServiceName: "cache"})
	if err != nil {
		t.Fatalf("PlanInstantiation: %v", err)
	}
	if plan.Backup.Status != "needs-runtime" || plan.Backup.Policy == "" || plan.Backup.BackupCommand == "" || plan.Backup.RestoreCommand == "" || plan.Backup.SmokeTestCommand == "" {
		t.Fatalf("backup = %#v", plan.Backup)
	}
	if len(plan.Backup.Readiness) == 0 {
		t.Fatal("backup readiness is empty")
	}
	for _, item := range plan.Backup.Readiness {
		if item.Status == "ready" {
			t.Fatalf("metadata-only plan reported ready backup item: %#v", item)
		}
	}
}

func TestPlanInstantiationRejectsUnknownTemplate(t *testing.T) {
	_, err := PlanInstantiation(InstantiationRequest{WorkspaceID: "wrk_acme", TemplateID: "unknown", ServiceName: "db"})
	if err == nil {
		t.Fatal("expected unknown template to be rejected")
	}
}

func TestPlanInstantiationRejectsReservedRuntimeVariableOverrides(t *testing.T) {
	_, err := PlanInstantiation(InstantiationRequest{
		WorkspaceID: "wrk_acme",
		TemplateID:  "postgres-16",
		ServiceName: "db",
		Variables:   map[string]string{"VOLUME_NAME": "surprise"},
	})
	if err == nil {
		t.Fatal("expected reserved runtime variable override to be rejected")
	}
}

func TestPlanInstantiationReturnsMutableCopies(t *testing.T) {
	plan, err := PlanInstantiation(InstantiationRequest{WorkspaceID: "wrk_acme", TemplateID: "mysql-8", ServiceName: "db"})
	if err != nil {
		t.Fatalf("PlanInstantiation: %v", err)
	}
	plan.SupportGates[0] = "mutated"
	again, err := PlanInstantiation(InstantiationRequest{WorkspaceID: "wrk_acme", TemplateID: "mysql-8", ServiceName: "db"})
	if err != nil {
		t.Fatalf("PlanInstantiation: %v", err)
	}
	if again.SupportGates[0] == "mutated" {
		t.Fatal("PlanInstantiation returned shared support gates")
	}
	plan.Volume.Labels["midnight.dev/template"] = "mutated"
	again, err = PlanInstantiation(InstantiationRequest{WorkspaceID: "wrk_acme", TemplateID: "mysql-8", ServiceName: "db"})
	if err != nil {
		t.Fatalf("PlanInstantiation: %v", err)
	}
	if again.Volume.Labels["midnight.dev/template"] == "mutated" {
		t.Fatal("PlanInstantiation returned shared volume labels")
	}
	plan.Backup.Readiness[0].Status = "mutated"
	again, err = PlanInstantiation(InstantiationRequest{WorkspaceID: "wrk_acme", TemplateID: "mysql-8", ServiceName: "db"})
	if err != nil {
		t.Fatalf("PlanInstantiation: %v", err)
	}
	if again.Backup.Readiness[0].Status == "mutated" {
		t.Fatal("PlanInstantiation returned shared backup readiness")
	}
}

func hasRuntimeEnv(vars []RuntimeEnvVar, name string, secret bool, source string) bool {
	for _, variable := range vars {
		if variable.Name == name && variable.Secret == secret && variable.Source == source {
			return true
		}
	}
	return false
}

func hasCredentialRef(refs []CredentialRef, name, envVar, scope string) bool {
	for _, ref := range refs {
		if ref.Name == name && ref.EnvVar == envVar && ref.Scope == scope && ref.Generated && strings.HasPrefix(ref.Reference, "generated://credential/") {
			return true
		}
	}
	return false
}
