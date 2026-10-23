package install

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/midnight/midnight/internal/preflight"
)

const (
	ApplyModeManual  = "manual"
	ApplyModeSafeFix = "safe_fix"
)

// SafetyClassification is the safe/requires-confirm flag attached to each step.
type SafetyClassification string

const (
	// SafetySafe indicates the step is idempotent and reversible without operator confirmation.
	SafetySafe SafetyClassification = "safe"
	// SafetyRequiresConfirm indicates the step requires explicit operator confirmation
	// because it changes shared host state, restarts services, or triggers reboots.
	SafetyRequiresConfirm SafetyClassification = "requires-confirm"
)

// RemediationPlan is a dry-run description of operator actions for failed preflight checks.
type RemediationPlan struct {
	Steps []RemediationStep `json:"steps"`
}

// RemediationStep is data only. It describes a possible fix but never executes it.
type RemediationStep struct {
	Command         string               `json:"command"`
	Title           string               `json:"title"`
	Risk            string               `json:"risk"`
	RequiresRestart bool                 `json:"requiresRestart"`
	ApplyMode       string               `json:"applyMode"`
	Safety          SafetyClassification `json:"safety"`
	CheckID         string               `json:"checkId,omitempty"`
}

// PlanPreflightRemediation converts preflight results into dry-run remediation steps.
func PlanPreflightRemediation(results []preflight.Result) RemediationPlan {
	plan := RemediationPlan{Steps: make([]RemediationStep, 0, len(results))}
	for _, result := range results {
		switch result.ID {
		case "PRE-CGROUP-001":
			if result.Status == preflight.StatusFail {
				plan.Steps = append(plan.Steps, RemediationStep{
					Title:           "Enable cgroup v2",
					Command:         "sudoedit /etc/default/grub && sudo update-grub && sudo reboot",
					Risk:            "host_reboot",
					RequiresRestart: true,
					ApplyMode:       ApplyModeManual,
					Safety:          SafetyRequiresConfirm,
					CheckID:         result.ID,
				})
			}
		case "PRE-DOCKER-API-001":
			if result.Status == preflight.StatusFail {
				plan.Steps = append(plan.Steps, RemediationStep{
					Title:           "Upgrade Docker API provider",
					Command:         "sudo apt-get update && sudo apt-get install --only-upgrade docker-ce docker-ce-cli containerd.io",
					Risk:            "package_upgrade",
					RequiresRestart: true,
					ApplyMode:       ApplyModeManual,
					Safety:          SafetyRequiresConfirm,
					CheckID:         result.ID,
				})
			}
		case "PRE-DOCKER-DAEMON-001":
			if result.Status == preflight.StatusWarn || result.Status == preflight.StatusFail {
				applyMode := ApplyModeSafeFix
				risk := "daemon_config_change"
				safety := SafetySafe
				if isDockerDaemonConflict(result) {
					applyMode = ApplyModeManual
					risk = "operator_config_conflict"
					safety = SafetyRequiresConfirm
				}
				plan.Steps = append(plan.Steps, RemediationStep{
					Title:           "Resolve Docker daemon.json baseline",
					Command:         "sudoedit /etc/docker/daemon.json && sudo systemctl restart docker",
					Risk:            risk,
					RequiresRestart: true,
					ApplyMode:       applyMode,
					Safety:          safety,
					CheckID:         result.ID,
				})
			}
		case "PRE-INGRESS-PORTS-001":
			if result.Status == preflight.StatusFail {
				plan.Steps = append(plan.Steps, RemediationStep{
					Title:           "Free required ingress ports (80/443)",
					Command:         "sudo ss -ltnp 'sport = :80 or sport = :443' && sudo systemctl stop <conflicting-service>",
					Risk:            "ingress_port_conflict",
					RequiresRestart: false,
					ApplyMode:       ApplyModeManual,
					Safety:          SafetyRequiresConfirm,
					CheckID:         result.ID,
				})
			}
		case "PRE-TIMESYNC-001":
			if result.Status == preflight.StatusFail || result.Status == preflight.StatusWarn {
				safety := SafetySafe
				applyMode := ApplyModeSafeFix
				if result.Status == preflight.StatusFail {
					safety = SafetyRequiresConfirm
					applyMode = ApplyModeManual
				}
				plan.Steps = append(plan.Steps, RemediationStep{
					Title:           "Enable host time synchronization",
					Command:         "sudo systemctl enable --now systemd-timesyncd || sudo apt-get install --yes chrony",
					Risk:            "time_sync_drift",
					RequiresRestart: false,
					ApplyMode:       applyMode,
					Safety:          safety,
					CheckID:         result.ID,
				})
			}
		case "PRE-DISK-001":
			if result.Status == preflight.StatusFail || result.Status == preflight.StatusWarn {
				plan.Steps = append(plan.Steps, RemediationStep{
					Title:           "Reduce disk usage on /var/lib/midnight",
					Command:         "sudo midnight registry gc --apply && sudo journalctl --vacuum-time=7d",
					Risk:            "disk_pressure",
					RequiresRestart: false,
					ApplyMode:       ApplyModeManual,
					Safety:          SafetyRequiresConfirm,
					CheckID:         result.ID,
				})
			}
		}
	}
	return plan
}

// MarshalRemediationPlanJSON emits the stable remediation plan JSON representation.
func MarshalRemediationPlanJSON(plan RemediationPlan) ([]byte, error) {
	return json.Marshal(plan)
}

func isDockerDaemonConflict(result preflight.Result) bool {
	var conflict *preflight.DockerDaemonConflictError
	if errors.As(result.Err, &conflict) {
		return true
	}
	return strings.Contains(strings.ToLower(result.Message), "conflict")
}
