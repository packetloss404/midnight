package enterprise

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

const ControlsPlanSchemaVersion = "midnight.dev/enterprise-controls-plan/v1alpha1"

type PlanStatus string

const (
	PlanStatusReady       PlanStatus = "ready"
	PlanStatusNeedsReview PlanStatus = "needs-review"
	PlanStatusBlocked     PlanStatus = "blocked"
)

type Issue struct {
	Field    string `json:"field"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type ControlsPlan struct {
	SchemaVersion string        `json:"schema_version"`
	Status        PlanStatus    `json:"status"`
	CustomerID    string        `json:"customer_id"`
	BYOK          BYOKPolicy    `json:"byok"`
	LTS           LTSPolicy     `json:"lts"`
	SupportSLA    SupportSLA    `json:"support_sla"`
	DPA           DPAControls   `json:"dpa"`
	DataResidency DataResidency `json:"data_residency"`
	Issues        []Issue       `json:"issues,omitempty"`
}

type BYOKPolicy struct {
	Mode         string   `json:"mode"`
	KeyRefs      []KeyRef `json:"key_refs,omitempty"`
	RotationDays int      `json:"rotation_days"`
}

type KeyRef struct {
	Provider string `json:"provider"`
	Region   string `json:"region"`
	URI      string `json:"uri"`
}

type LTSPolicy struct {
	Channel          string `json:"channel"`
	SupportedUntil   string `json:"supported_until,omitempty"`
	PatchCadenceDays int    `json:"patch_cadence_days"`
}

type SupportSLA struct {
	Tier            string         `json:"tier"`
	ResponseMinutes map[string]int `json:"response_minutes"`
}

type DPAControls struct {
	DPAAccepted            bool     `json:"dpa_accepted"`
	SubprocessorNoticeDays int      `json:"subprocessor_notice_days"`
	SubprocessorListURL    string   `json:"subprocessor_list_url"`
	ApprovedSubprocessors  []string `json:"approved_subprocessors,omitempty"`
}

type DataResidency struct {
	Mode            string   `json:"mode"`
	AllowedRegions  []string `json:"allowed_regions"`
	TelemetryExport string   `json:"telemetry_export"`
}

func NewControlsPlan(plan ControlsPlan) ControlsPlan {
	plan.SchemaVersion = ControlsPlanSchemaVersion
	plan.Issues = collectIssues(plan)
	plan.Status = statusForIssues(plan.Issues)
	return normalizePlan(plan)
}

func ValidateControlsPlan(plan ControlsPlan) error {
	if plan.SchemaVersion != ControlsPlanSchemaVersion {
		return fmt.Errorf("enterprise controls schema_version %q is invalid", plan.SchemaVersion)
	}
	if !validStatus(plan.Status) {
		return fmt.Errorf("enterprise controls status %q is invalid", plan.Status)
	}
	wantIssues := collectIssues(plan)
	if plan.Status != statusForIssues(wantIssues) {
		return fmt.Errorf("enterprise controls status %q does not match validation issues", plan.Status)
	}
	if !sameIssues(plan.Issues, wantIssues) {
		return fmt.Errorf("enterprise controls issues do not match validation result")
	}
	return nil
}

func MarshalControlsPlanJSON(plan ControlsPlan) ([]byte, error) {
	plan = normalizePlan(plan)
	if err := ValidateControlsPlan(plan); err != nil {
		return nil, err
	}
	return json.MarshalIndent(plan, "", "  ")
}

func UnmarshalControlsPlanJSON(data []byte) (ControlsPlan, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan ControlsPlan
	if err := decoder.Decode(&plan); err != nil {
		return ControlsPlan{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("enterprise controls JSON must contain one document")
		}
		return ControlsPlan{}, err
	}
	if err := ValidateControlsPlan(plan); err != nil {
		return ControlsPlan{}, err
	}
	return plan, nil
}

func collectIssues(plan ControlsPlan) []Issue {
	var issues []Issue
	add := func(field, severity, message string) {
		issues = append(issues, Issue{Field: field, Severity: severity, Message: message})
	}
	if strings.TrimSpace(plan.CustomerID) == "" {
		add("customer_id", "fatal", "customer_id is required")
	}
	collectBYOKIssues(plan.BYOK, add)
	collectLTSIssues(plan.LTS, add)
	collectSLAIssues(plan.SupportSLA, add)
	collectDPAIssues(plan.DPA, add)
	collectResidencyIssues(plan.DataResidency, add)
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Field == issues[j].Field {
			return issues[i].Message < issues[j].Message
		}
		return issues[i].Field < issues[j].Field
	})
	return issues
}

func collectBYOKIssues(policy BYOKPolicy, add func(string, string, string)) {
	switch policy.Mode {
	case "platform-managed":
		if len(policy.KeyRefs) > 0 {
			add("byok.key_refs", "warn", "platform-managed mode ignores key_refs")
		}
	case "customer-managed":
		if len(policy.KeyRefs) == 0 {
			add("byok.key_refs", "fatal", "customer-managed mode requires at least one external key reference")
		}
	default:
		add("byok.mode", "fatal", "mode must be platform-managed or customer-managed")
	}
	if policy.RotationDays <= 0 || policy.RotationDays > 366 {
		add("byok.rotation_days", "fatal", "rotation_days must be between 1 and 366")
	}
	for i, key := range policy.KeyRefs {
		prefix := fmt.Sprintf("byok.key_refs[%d]", i)
		if strings.TrimSpace(key.Provider) == "" {
			add(prefix+".provider", "fatal", "provider is required")
		}
		if strings.TrimSpace(key.Region) == "" {
			add(prefix+".region", "fatal", "region is required")
		}
		uri := strings.TrimSpace(key.URI)
		if uri == "" {
			add(prefix+".uri", "fatal", "uri is required")
			continue
		}
		parsed, err := url.Parse(uri)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			add(prefix+".uri", "fatal", "uri must be an external key reference")
		}
		if strings.Contains(strings.ToLower(uri), "secret=") || strings.Contains(strings.ToLower(uri), "private_key") {
			add(prefix+".uri", "fatal", "uri must not include secret material")
		}
	}
}

func collectLTSIssues(policy LTSPolicy, add func(string, string, string)) {
	switch policy.Channel {
	case "standard":
	case "enterprise-lts":
		if strings.TrimSpace(policy.SupportedUntil) == "" {
			add("lts.supported_until", "fatal", "enterprise-lts requires supported_until")
		}
	default:
		add("lts.channel", "fatal", "channel must be standard or enterprise-lts")
	}
	if policy.SupportedUntil != "" {
		if _, err := time.Parse("2006-01-02", policy.SupportedUntil); err != nil {
			add("lts.supported_until", "fatal", "supported_until must use YYYY-MM-DD")
		}
	}
	if policy.PatchCadenceDays <= 0 || policy.PatchCadenceDays > 90 {
		add("lts.patch_cadence_days", "fatal", "patch_cadence_days must be between 1 and 90")
	}
}

func collectSLAIssues(sla SupportSLA, add func(string, string, string)) {
	switch sla.Tier {
	case "standard", "premium", "mission-critical":
	default:
		add("support_sla.tier", "fatal", "tier must be standard, premium, or mission-critical")
	}
	for _, severity := range []string{"sev1", "sev2", "sev3", "sev4"} {
		minutes, ok := sla.ResponseMinutes[severity]
		if !ok {
			add("support_sla.response_minutes."+severity, "fatal", "response target is required")
			continue
		}
		if minutes <= 0 {
			add("support_sla.response_minutes."+severity, "fatal", "response target must be positive")
		}
	}
}

func collectDPAIssues(dpa DPAControls, add func(string, string, string)) {
	if !dpa.DPAAccepted {
		add("dpa.dpa_accepted", "fatal", "DPA must be accepted before enterprise controls are ready")
	}
	if dpa.SubprocessorNoticeDays < 30 {
		add("dpa.subprocessor_notice_days", "fatal", "subprocessor notice must be at least 30 days")
	}
	parsed, err := url.Parse(strings.TrimSpace(dpa.SubprocessorListURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		add("dpa.subprocessor_list_url", "fatal", "subprocessor_list_url must be an absolute URL")
	}
	if len(dpa.ApprovedSubprocessors) == 0 {
		add("dpa.approved_subprocessors", "warn", "no approved subprocessors recorded")
	}
}

func collectResidencyIssues(res DataResidency, add func(string, string, string)) {
	switch res.Mode {
	case "single-region", "region-set":
	default:
		add("data_residency.mode", "fatal", "mode must be single-region or region-set")
	}
	if len(res.AllowedRegions) == 0 {
		add("data_residency.allowed_regions", "fatal", "at least one allowed region is required")
	}
	if res.Mode == "single-region" && len(res.AllowedRegions) != 1 {
		add("data_residency.allowed_regions", "fatal", "single-region mode requires exactly one allowed region")
	}
	for i, region := range res.AllowedRegions {
		if strings.TrimSpace(region) == "" {
			add(fmt.Sprintf("data_residency.allowed_regions[%d]", i), "fatal", "region must not be empty")
		}
	}
	switch res.TelemetryExport {
	case "disabled", "metadata-only", "allowed-region-only":
	default:
		add("data_residency.telemetry_export", "fatal", "telemetry_export must be disabled, metadata-only, or allowed-region-only")
	}
}

func normalizePlan(plan ControlsPlan) ControlsPlan {
	plan.Issues = append([]Issue(nil), plan.Issues...)
	plan.BYOK.KeyRefs = append([]KeyRef(nil), plan.BYOK.KeyRefs...)
	plan.DataResidency.AllowedRegions = append([]string(nil), plan.DataResidency.AllowedRegions...)
	plan.DPA.ApprovedSubprocessors = append([]string(nil), plan.DPA.ApprovedSubprocessors...)
	if plan.SupportSLA.ResponseMinutes != nil {
		copied := make(map[string]int, len(plan.SupportSLA.ResponseMinutes))
		for k, v := range plan.SupportSLA.ResponseMinutes {
			copied[k] = v
		}
		plan.SupportSLA.ResponseMinutes = copied
	}
	sort.Strings(plan.DataResidency.AllowedRegions)
	sort.Strings(plan.DPA.ApprovedSubprocessors)
	return plan
}

func statusForIssues(issues []Issue) PlanStatus {
	status := PlanStatusReady
	for _, issue := range issues {
		if issue.Severity == "fatal" {
			return PlanStatusBlocked
		}
		if issue.Severity == "warn" {
			status = PlanStatusNeedsReview
		}
	}
	return status
}

func validStatus(status PlanStatus) bool {
	switch status {
	case PlanStatusReady, PlanStatusNeedsReview, PlanStatusBlocked:
		return true
	default:
		return false
	}
}

func sameIssues(a, b []Issue) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
