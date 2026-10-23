package backup

import (
	"net/url"
	"strings"
)

const (
	// EnvBackupDestinationURI is the process-level destination URI used by
	// the API status endpoint until backup destination resources land.
	EnvBackupDestinationURI = "MIDNIGHT_BACKUP_DESTINATION_URI"

	RestoreAPIStatusAvailable   = "available"
	RestoreAPIStatusUnavailable = "unavailable"

	RestoreAPIReasonUnavailable    = "restore_api_unavailable"
	RestoreAPIReasonOffhostMissing = "offhost_missing"
	RestoreAPIReasonOffhostInvalid = "offhost_invalid"
)

// RestoreAPIStatusInput captures the concrete backend wiring visible to the API.
type RestoreAPIStatusInput struct {
	DestinationURI           string
	RestoreExecutorAvailable bool
}

// RestoreAPIStatus is the truthful backend shape the GUI can consume before
// deciding whether to show restore actions.
type RestoreAPIStatus struct {
	Status           string                   `json:"status"`
	Available        bool                     `json:"available"`
	ReasonCode       string                   `json:"reason_code,omitempty"`
	Reasons          []RestoreAPIStatusReason `json:"reasons"`
	RestoreExecution RestoreExecutionStatus   `json:"restore_execution"`
	Offhost          RestoreOffhostStatus     `json:"offhost"`
}

type RestoreAPIStatusReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RestoreExecutionStatus struct {
	Available  bool   `json:"available"`
	ReasonCode string `json:"reason_code,omitempty"`
}

type RestoreOffhostStatus struct {
	Configured     bool   `json:"configured"`
	DestinationURI string `json:"destination_uri,omitempty"`
	Scheme         string `json:"scheme,omitempty"`
	ReasonCode     string `json:"reason_code,omitempty"`
}

// BuildRestoreAPIStatus reports the real restore backend availability. Current
// backend code can generate offline restore plans and readiness summaries, but
// cannot execute a destructive restore, so callers should pass
// RestoreExecutorAvailable only after a real executor is wired.
func BuildRestoreAPIStatus(input RestoreAPIStatusInput) RestoreAPIStatus {
	execution := RestoreExecutionStatus{Available: input.RestoreExecutorAvailable}
	offhost := classifyRestoreOffhost(input.DestinationURI)

	status := RestoreAPIStatus{
		Status:           RestoreAPIStatusAvailable,
		Available:        true,
		Reasons:          []RestoreAPIStatusReason{},
		RestoreExecution: execution,
		Offhost:          offhost,
	}
	if !execution.Available {
		status.addReason(RestoreAPIReasonUnavailable, "restore execution is not wired; backend only exposes offline restore planning and readiness")
		status.RestoreExecution.ReasonCode = RestoreAPIReasonUnavailable
	}
	if !offhost.Configured {
		switch offhost.ReasonCode {
		case RestoreAPIReasonOffhostInvalid:
			status.addReason(RestoreAPIReasonOffhostInvalid, "off-host backup destination URI is invalid")
		default:
			status.addReason(RestoreAPIReasonOffhostMissing, "no off-host backup destination is configured")
		}
	}
	if len(status.Reasons) > 0 {
		status.Status = RestoreAPIStatusUnavailable
		status.Available = false
		status.ReasonCode = status.Reasons[0].Code
	}
	return status
}

func (s *RestoreAPIStatus) addReason(code, message string) {
	s.Reasons = append(s.Reasons, RestoreAPIStatusReason{Code: code, Message: message})
}

func classifyRestoreOffhost(raw string) RestoreOffhostStatus {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RestoreOffhostStatus{ReasonCode: RestoreAPIReasonOffhostMissing}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return RestoreOffhostStatus{DestinationURI: raw, ReasonCode: RestoreAPIReasonOffhostInvalid}
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	status := RestoreOffhostStatus{DestinationURI: raw, Scheme: scheme}
	switch scheme {
	case "s3", "gcs", "sftp", "rsync", "ssh":
		status.Configured = true
	case "file", "local", "":
		status.ReasonCode = RestoreAPIReasonOffhostMissing
	default:
		status.ReasonCode = RestoreAPIReasonOffhostInvalid
	}
	return status
}
