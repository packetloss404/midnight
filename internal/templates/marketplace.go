package templates

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type MaintenanceStatus string

const (
	MaintenanceActive MaintenanceStatus = "active"
	MaintenanceReview MaintenanceStatus = "needs-review"
)

type MarketplaceCard struct {
	ID                string            `json:"id"`
	Title             string            `json:"title"`
	Description       string            `json:"description"`
	TrustClass        TrustClass        `json:"trust_class"`
	MaintenanceStatus MaintenanceStatus `json:"maintenance_status"`
	Engine            DatabaseEngine    `json:"engine"`
	EngineVersion     string            `json:"engine_version"`
	Tags              []string          `json:"tags"`
	LastUpdated       time.Time         `json:"last_updated"`
}

type MarketplaceFilter struct {
	Query             string
	Tag               string
	TrustClass        TrustClass
	MaintenanceStatus MaintenanceStatus
}

type InstantiationRequest struct {
	WorkspaceID  string            `json:"workspace_id"`
	TemplateID   string            `json:"template_id"`
	ServiceName  string            `json:"service_name"`
	DatabaseName string            `json:"database_name,omitempty"`
	Variables    map[string]string `json:"variables,omitempty"`
}

type InstantiationPlan struct {
	WorkspaceID         string              `json:"workspace_id"`
	TemplateID          string              `json:"template_id"`
	ServiceName         string              `json:"service_name"`
	DatabaseName        string              `json:"database_name"`
	Status              string              `json:"status"`
	Provisioned         bool                `json:"provisioned"`
	Durable             bool                `json:"durable"`
	Engine              DatabaseEngine      `json:"engine"`
	EngineVersion       string              `json:"engine_version"`
	Image               string              `json:"image"`
	ImageDigest         string              `json:"image_digest"`
	MountPath           string              `json:"mount_path"`
	InternalPort        int                 `json:"internal_port"`
	ConnectionURL       string              `json:"connection_url"`
	ConnectionGuidance  ConnectionGuidance  `json:"connection_guidance"`
	RequiredSecrets     []string            `json:"required_secrets"`
	CredentialRefs      []CredentialRef     `json:"credential_refs"`
	Environment         map[string]string   `json:"environment"`
	RuntimeEnv          []RuntimeEnvVar     `json:"runtime_env"`
	Volume              VolumePlan          `json:"volume"`
	Backup              BackupPlan          `json:"backup"`
	RestorePlanGuidance RestorePlanGuidance `json:"restore_plan_guidance"`
	Networking          NetworkPlan         `json:"networking"`
	SupportGates        []string            `json:"support_gates"`
	PostCreateActions   []string            `json:"post_create_actions"`
	ExposeTCP           bool                `json:"expose_tcp"`
}

type CredentialRef struct {
	Name      string `json:"name"`
	EnvVar    string `json:"env_var"`
	Reference string `json:"reference"`
	Scope     string `json:"scope"`
	Generated bool   `json:"generated"`
}

type ConnectionGuidance struct {
	Internal          string   `json:"internal"`
	Visibility        string   `json:"visibility"`
	Host              string   `json:"host"`
	Port              int      `json:"port"`
	Database          string   `json:"database"`
	CredentialEnvVars []string `json:"credential_env_vars"`
	SecretTransport   string   `json:"secret_transport"`
	Guidance          string   `json:"guidance"`
}

type RuntimeEnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
	Source string `json:"source"`
}

type VolumePlan struct {
	Name           string            `json:"name"`
	MountPath      string            `json:"mount_path"`
	Driver         string            `json:"driver"`
	ReadOnly       bool              `json:"read_only"`
	Status         string            `json:"status"`
	AttachRequired bool              `json:"attach_required"`
	Labels         map[string]string `json:"labels"`
}

type BackupPlan struct {
	Status           string                `json:"status"`
	Policy           string                `json:"policy"`
	BackupCommand    string                `json:"backup_command"`
	RestoreCommand   string                `json:"restore_command"`
	SmokeTestCommand string                `json:"smoke_test_command"`
	Readiness        []BackupReadinessItem `json:"readiness"`
}

type BackupReadinessItem struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type RestorePlanGuidance struct {
	Status       string   `json:"status"`
	Endpoint     string   `json:"endpoint"`
	PlanRequired bool     `json:"plan_required"`
	Inputs       []string `json:"inputs"`
	Guidance     string   `json:"guidance"`
}

type NetworkPlan struct {
	PrivateByDefault       bool            `json:"private_by_default"`
	InternalOnly           bool            `json:"internal_only"`
	TCPProxyDefaultEnabled bool            `json:"tcp_proxy_default_enabled"`
	ExternalTCP            ExternalTCPPlan `json:"external_tcp"`
}

type ExternalTCPPlan struct {
	Status                  string `json:"status"`
	RequiredPlan            string `json:"required_plan"`
	SourceAllowListRequired bool   `json:"source_allow_list_required"`
	Guidance                string `json:"guidance"`
}

func MarketplaceCards(now time.Time) []MarketplaceCard {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cards := make([]MarketplaceCard, 0, len(OfficialDatabaseTemplates()))
	for _, tmpl := range OfficialDatabaseTemplates() {
		cards = append(cards, cardFromDatabaseTemplate(tmpl, now.UTC()))
	}
	return cards
}

func SearchMarketplace(cards []MarketplaceCard, filter MarketplaceFilter) []MarketplaceCard {
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	tag := strings.ToLower(strings.TrimSpace(filter.Tag))
	result := make([]MarketplaceCard, 0, len(cards))
	for _, card := range cards {
		if filter.TrustClass != "" && card.TrustClass != filter.TrustClass {
			continue
		}
		if filter.MaintenanceStatus != "" && card.MaintenanceStatus != filter.MaintenanceStatus {
			continue
		}
		if tag != "" && !hasTag(card.Tags, tag) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(card.ID+" "+card.Title+" "+card.Description+" "+string(card.Engine)), query) {
			continue
		}
		card.Tags = append([]string(nil), card.Tags...)
		result = append(result, card)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func DeployButtonURL(dashboardBase, source, ref, workspace string) (string, error) {
	base, err := parseDeployURL("dashboard", dashboardBase)
	if err != nil {
		return "", err
	}
	sourceURL, err := parseDeployURL("source", source)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(ref) == "" {
		return "", errors.New("ref is required")
	}
	if strings.TrimSpace(workspace) == "" {
		return "", errors.New("workspace is required")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/new/template"
	base.RawQuery = ""
	base.Fragment = ""
	query := base.Query()
	query.Set("source", sourceURL.String())
	query.Set("ref", strings.TrimSpace(ref))
	query.Set("workspace", strings.TrimSpace(workspace))
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func PlanInstantiation(request InstantiationRequest) (InstantiationPlan, error) {
	if strings.TrimSpace(request.WorkspaceID) == "" || strings.TrimSpace(request.TemplateID) == "" || strings.TrimSpace(request.ServiceName) == "" {
		return InstantiationPlan{}, errors.New("workspace_id, template_id, and service_name are required")
	}
	tmpl, ok := findOfficialDatabaseTemplate(request.TemplateID)
	if !ok {
		return InstantiationPlan{}, fmt.Errorf("template %q is not available", request.TemplateID)
	}
	if err := ValidateOfficialDatabaseTemplate(tmpl); err != nil {
		return InstantiationPlan{}, err
	}
	databaseName := strings.TrimSpace(request.DatabaseName)
	if databaseName == "" {
		databaseName = "app"
	}
	serviceName := strings.TrimSpace(request.ServiceName)
	workspaceID := strings.TrimSpace(request.WorkspaceID)
	serviceHost := planHost(serviceName)
	servicePort := fmt.Sprintf("%d", tmpl.InternalPort)
	credentials := credentialRefs(workspaceID, serviceName)
	connectionURL := renderConnectionURL(tmpl.ConnectionURLFormat, databaseName)
	env := map[string]string{
		"MIDNIGHT_TEMPLATE_ID": tmpl.ID,                                              // not-a-failure-code
		"MIDNIGHT_ENGINE":      string(tmpl.Engine),                                  // not-a-failure-code
		"DATABASE_NAME":        databaseName,                                         // not-a-failure-code
		"DATABASE_USER":        credentialEnvValue(credentials, "database-user"),     // not-a-failure-code
		"DATABASE_PASSWORD":    credentialEnvValue(credentials, "database-password"), // not-a-failure-code
		"SERVICE_HOST":         serviceHost,                                          // not-a-failure-code
		"SERVICE_PORT":         servicePort,                                          // not-a-failure-code
		"BACKUP_POLICY":        tmpl.BackupPolicy,                                    // not-a-failure-code
		"VOLUME_MOUNT_PATH":    tmpl.MountPath,                                       // not-a-failure-code
		"VOLUME_NAME":          volumeName(workspaceID, serviceName),                 // not-a-failure-code
	}
	env[connectionEnvName(tmpl.Engine)] = connectionURL
	for key, value := range request.Variables {
		key = strings.TrimSpace(key)
		if key == "" {
			return InstantiationPlan{}, errors.New("variable names must be non-empty")
		}
		if reservedRuntimeEnvName(key) {
			return InstantiationPlan{}, fmt.Errorf("variable %q is reserved by the template plan", key)
		}
		env[key] = strings.TrimSpace(value)
	}
	return InstantiationPlan{
		WorkspaceID:         workspaceID,
		TemplateID:          tmpl.ID,
		ServiceName:         serviceName,
		DatabaseName:        databaseName,
		Status:              "planned",
		Provisioned:         false,
		Durable:             templateDurable(tmpl),
		Engine:              tmpl.Engine,
		EngineVersion:       tmpl.EngineVersion,
		Image:               tmpl.Image,
		ImageDigest:         tmpl.ImageDigest,
		MountPath:           tmpl.MountPath,
		InternalPort:        tmpl.InternalPort,
		ConnectionURL:       connectionURL,
		ConnectionGuidance:  connectionGuidance(tmpl, databaseName, serviceHost, connectionURL),
		RequiredSecrets:     []string{"database-password"},
		CredentialRefs:      credentials,
		Environment:         env,
		RuntimeEnv:          runtimeEnvVars(env, tmpl.Engine),
		Volume:              volumePlan(workspaceID, serviceName, tmpl),
		Backup:              backupPlan(tmpl),
		RestorePlanGuidance: restorePlanGuidance(tmpl),
		Networking:          networkPlan(tmpl),
		SupportGates:        RequiredSupportGates(),
		PostCreateActions:   []string{"run-healthcheck", "run-smoke-test", "schedule-backup-policy"},
		ExposeTCP:           false,
	}, nil
}

func cardFromDatabaseTemplate(tmpl DatabaseTemplate, now time.Time) MarketplaceCard {
	status := MaintenanceActive
	if err := ValidateOfficialDatabaseTemplate(tmpl); err != nil {
		status = MaintenanceReview
	}
	description := fmt.Sprintf("Official %s %s single-node database template with gated backup, restore, upgrade, UID/GID, and smoke-test evidence.", tmpl.Engine, tmpl.EngineVersion)
	if tmpl.Engine == DatabaseEngineRedis && tmpl.RedisMode != "" {
		description = fmt.Sprintf("Official Redis %s %s template with gated support evidence.", tmpl.EngineVersion, tmpl.RedisMode)
	}
	return MarketplaceCard{
		ID:                tmpl.ID,
		Title:             titleForTemplate(tmpl),
		Description:       description,
		TrustClass:        tmpl.TrustClass,
		MaintenanceStatus: status,
		Engine:            tmpl.Engine,
		EngineVersion:     tmpl.EngineVersion,
		Tags:              tagsForTemplate(tmpl),
		LastUpdated:       now,
	}
}

func titleForTemplate(tmpl DatabaseTemplate) string {
	switch tmpl.Engine {
	case DatabaseEnginePostgres:
		return "Postgres " + tmpl.EngineVersion
	case DatabaseEngineRedis:
		return "Redis " + tmpl.EngineVersion + " " + string(tmpl.RedisMode)
	case DatabaseEngineMySQL:
		return "MySQL " + tmpl.EngineVersion
	case DatabaseEngineMongoDB:
		return "MongoDB " + tmpl.EngineVersion + " single-node"
	default:
		return tmpl.ID
	}
}

func tagsForTemplate(tmpl DatabaseTemplate) []string {
	tags := []string{"database", string(tmpl.Engine), "official"}
	if tmpl.Engine == DatabaseEngineRedis && tmpl.RedisMode != "" {
		tags = append(tags, string(tmpl.RedisMode))
	}
	if tmpl.Engine == DatabaseEngineMongoDB {
		tags = append(tags, "single-node")
	}
	sort.Strings(tags)
	return tags
}

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if strings.EqualFold(tag, want) {
			return true
		}
	}
	return false
}

func parseDeployURL(name, raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%s url: %w", name, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("%s url must be absolute https", name)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s url must not include query or fragment", name)
	}
	return parsed, nil
}

func findOfficialDatabaseTemplate(id string) (DatabaseTemplate, bool) {
	for _, tmpl := range OfficialDatabaseTemplates() {
		if tmpl.ID == strings.TrimSpace(id) {
			return tmpl, true
		}
	}
	return DatabaseTemplate{}, false
}

func renderConnectionURL(format, databaseName string) string {
	replacer := strings.NewReplacer(
		"${USER}", "${DATABASE_USER}",
		"${PASSWORD}", "${DATABASE_PASSWORD}",
		"${HOST}", "${SERVICE_HOST}",
		"${PORT}", "${SERVICE_PORT}",
		"${DATABASE}", databaseName,
	)
	return replacer.Replace(format)
}

func connectionEnvName(engine DatabaseEngine) string {
	switch engine {
	case DatabaseEngineRedis:
		return "REDIS_URL"
	case DatabaseEngineMySQL:
		return "MYSQL_URL"
	case DatabaseEngineMongoDB:
		return "MONGODB_URL"
	default:
		return "DATABASE_URL"
	}
}

func runtimeEnvVars(env map[string]string, engine DatabaseEngine) []RuntimeEnvVar {
	names := []string{
		"MIDNIGHT_TEMPLATE_ID",
		"MIDNIGHT_ENGINE",
		"DATABASE_NAME",
		"DATABASE_USER",
		"DATABASE_PASSWORD",
		"SERVICE_HOST",
		"SERVICE_PORT",
		connectionEnvName(engine),
		"VOLUME_NAME",
		"VOLUME_MOUNT_PATH",
		"BACKUP_POLICY",
	}
	out := make([]RuntimeEnvVar, 0, len(names))
	for _, name := range names {
		value, ok := env[name]
		if !ok {
			continue
		}
		out = append(out, RuntimeEnvVar{
			Name:   name,
			Value:  value,
			Secret: name == "DATABASE_PASSWORD",
			Source: runtimeEnvSource(name),
		})
	}
	return out
}

func reservedRuntimeEnvName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "MIDNIGHT_TEMPLATE_ID",
		"MIDNIGHT_ENGINE",
		"DATABASE_NAME",
		"DATABASE_USER",
		"DATABASE_PASSWORD",
		"SERVICE_HOST",
		"SERVICE_PORT",
		"DATABASE_URL",
		"REDIS_URL",
		"MYSQL_URL",
		"MONGODB_URL",
		"VOLUME_NAME",
		"VOLUME_MOUNT_PATH",
		"BACKUP_POLICY":
		return true
	default:
		return false
	}
}

func runtimeEnvSource(name string) string {
	switch name {
	case "DATABASE_USER", "DATABASE_PASSWORD":
		return "generated-credential"
	case "VOLUME_NAME", "VOLUME_MOUNT_PATH":
		return "volume-plan"
	case "BACKUP_POLICY":
		return "backup-plan"
	default:
		return "template-plan"
	}
}

func credentialRefs(workspaceID, serviceName string) []CredentialRef {
	base := "generated://credential/" + planSlug(workspaceID, "workspace") + "/" + planSlug(serviceName, "service")
	return []CredentialRef{
		{Name: "database-user", EnvVar: "DATABASE_USER", Reference: base + "/database-user", Scope: "sealed", Generated: true},
		{Name: "database-password", EnvVar: "DATABASE_PASSWORD", Reference: base + "/database-password", Scope: "sealed", Generated: true},
	}
}

func credentialEnvValue(credentials []CredentialRef, name string) string {
	for _, credential := range credentials {
		if credential.Name == name {
			return "${credential:" + credential.Reference + "}"
		}
	}
	return "${credential:" + name + "}"
}

func templateDurable(tmpl DatabaseTemplate) bool {
	return tmpl.Engine != DatabaseEngineRedis || tmpl.RedisMode == RedisModeDurable
}

func connectionGuidance(tmpl DatabaseTemplate, databaseName, serviceHost, connectionURL string) ConnectionGuidance {
	return ConnectionGuidance{
		Internal:          connectionURL,
		Visibility:        "private",
		Host:              serviceHost,
		Port:              tmpl.InternalPort,
		Database:          databaseName,
		CredentialEnvVars: []string{"DATABASE_USER", "DATABASE_PASSWORD"},
		SecretTransport:   "generated sealed credential references; password is injected separately and is not embedded in URLs",
		Guidance:          "Use the internal service DNS name from workloads on the private Midnight network. Do not publish this database externally unless a TCP proxy plan with a source allow-list is approved.",
	}
}

func volumePlan(workspaceID, serviceName string, tmpl DatabaseTemplate) VolumePlan {
	name := volumeName(workspaceID, serviceName)
	return VolumePlan{
		Name:           name,
		MountPath:      tmpl.MountPath,
		Driver:         "local",
		ReadOnly:       false,
		Status:         "planned",
		AttachRequired: true,
		Labels: map[string]string{
			"midnight.dev/workspace":   workspaceID,
			"midnight.dev/service":     serviceName,
			"midnight.dev/template":    tmpl.ID,
			"midnight.dev/engine":      string(tmpl.Engine),
			"midnight.dev/mount-path":  tmpl.MountPath,
			"midnight.dev/provisioned": "false",
		},
	}
}

func backupPlan(tmpl DatabaseTemplate) BackupPlan {
	return BackupPlan{
		Status:           "needs-runtime",
		Policy:           tmpl.BackupPolicy,
		BackupCommand:    tmpl.BackupCommand,
		RestoreCommand:   tmpl.RestoreCommand,
		SmokeTestCommand: tmpl.SmokeTestCommand,
		Readiness: []BackupReadinessItem{
			{Code: "volume-created", Status: "pending", Detail: "create the planned volume before running backup jobs"},
			{Code: "volume-attached", Status: "pending", Detail: "attach the planned volume at the template mount path"},
			{Code: "smoke-test", Status: "pending", Detail: tmpl.SmokeTestCommand},
			{Code: "backup-policy", Status: "pending", Detail: tmpl.BackupPolicy},
		},
	}
}

func restorePlanGuidance(tmpl DatabaseTemplate) RestorePlanGuidance {
	return RestorePlanGuidance{
		Status:       "operator-plan-required",
		Endpoint:     "/api/v1alpha/backup/restore/plans",
		PlanRequired: true,
		Inputs:       []string{"backup_id", "operator", "rollback_owner", "notes"},
		Guidance:     "Create a restore plan against a selected " + string(tmpl.Engine) + " backup before any destructive restore. Restore into a replacement volume, validate with the template smoke test, then cut application traffic over explicitly.",
	}
}

func networkPlan(tmpl DatabaseTemplate) NetworkPlan {
	return NetworkPlan{
		PrivateByDefault:       true,
		InternalOnly:           true,
		TCPProxyDefaultEnabled: tmpl.TCPProxyDefaultEnabled,
		ExternalTCP: ExternalTCPPlan{
			Status:                  "disabled",
			RequiredPlan:            "tcp-proxy",
			SourceAllowListRequired: true,
			Guidance:                "External TCP remains disabled until an explicit proxy plan names source CIDR allow-list entries.",
		},
	}
}

func volumeName(workspaceID, serviceName string) string {
	return "midnight-" + planSlug(workspaceID, "workspace") + "-" + planSlug(serviceName, "service") + "-data"
}

func planHost(serviceName string) string {
	return planSlug(serviceName, "database") + ".internal"
}

func planSlug(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return fallback
	}
	return out
}
