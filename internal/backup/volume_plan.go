package backup

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// VolumeBackendCapabilities describes whether an execution backend can perform
// volume attachment changes. The planner is offline; callers execute actions.
type VolumeBackendCapabilities struct {
	AttachDetach    bool
	SharedReadWrite bool
}

type VolumeAttachmentIntent struct {
	DeploymentID string
	InstanceID   string
	VolumeName   string
	MountPath    string
	ReadOnly     bool
}

type VolumeAttachmentPlanInput struct {
	BackendCapabilities VolumeBackendCapabilities
	Observed            []VolumeAttachmentIntent
	Desired             []VolumeAttachmentIntent
	Now                 time.Time
}

type VolumeAttachmentActionKind string

const (
	VolumeAttachmentKeep   VolumeAttachmentActionKind = "keep"
	VolumeAttachmentAttach VolumeAttachmentActionKind = "attach"
	VolumeAttachmentDetach VolumeAttachmentActionKind = "detach"
	VolumeAttachmentBlock  VolumeAttachmentActionKind = "block"
)

type VolumeAttachmentAction struct {
	Kind         VolumeAttachmentActionKind `json:"kind"`
	DeploymentID string                     `json:"deploymentId"`
	InstanceID   string                     `json:"instanceId"`
	VolumeName   string                     `json:"volumeName"`
	MountPath    string                     `json:"mountPath,omitempty"`
	ReadOnly     bool                       `json:"readOnly,omitempty"`
	Reason       string                     `json:"reason"`
}

type VolumeAttachmentPlan struct {
	GeneratedAt time.Time                `json:"generatedAt"`
	Actions     []VolumeAttachmentAction `json:"actions"`
}

// PlanVolumeAttachments returns deterministic attach/detach/keep/block actions
// without contacting Docker, Kubernetes, filesystems, or storage APIs.
func PlanVolumeAttachments(input VolumeAttachmentPlanInput) VolumeAttachmentPlan {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	observed := normalizeVolumeIntents(input.Observed)
	desired := normalizeVolumeIntents(input.Desired)
	observedByKey := map[string]VolumeAttachmentIntent{}
	desiredByKey := map[string]VolumeAttachmentIntent{}
	for _, intent := range observed {
		observedByKey[volumeAttachmentKey(intent)] = intent
	}
	for _, intent := range desired {
		desiredByKey[volumeAttachmentKey(intent)] = intent
	}

	actions := make([]VolumeAttachmentAction, 0, len(observed)+len(desired))
	for _, intent := range desired {
		key := volumeAttachmentKey(intent)
		if key == "" {
			actions = append(actions, volumeAttachmentAction(VolumeAttachmentBlock, intent, "deployment, instance, and volume are required"))
			continue
		}
		observedIntent, ok := observedByKey[key]
		if ok && observedIntent.MountPath == intent.MountPath && observedIntent.ReadOnly == intent.ReadOnly {
			actions = append(actions, volumeAttachmentAction(VolumeAttachmentKeep, intent, "already attached"))
			continue
		}
		if !input.BackendCapabilities.AttachDetach {
			actions = append(actions, volumeAttachmentAction(VolumeAttachmentBlock, intent, "backend does not allow volume attach/detach"))
			continue
		}
		if !input.BackendCapabilities.SharedReadWrite && hasConflictingWriter(intent, observed, desired) {
			actions = append(actions, volumeAttachmentAction(VolumeAttachmentBlock, intent, "volume is already attached to another writer"))
			continue
		}
		actions = append(actions, volumeAttachmentAction(VolumeAttachmentAttach, intent, "backend allows volume attach"))
	}
	for _, intent := range observed {
		key := volumeAttachmentKey(intent)
		if key == "" {
			continue
		}
		if _, ok := desiredByKey[key]; ok {
			continue
		}
		if !input.BackendCapabilities.AttachDetach {
			actions = append(actions, volumeAttachmentAction(VolumeAttachmentBlock, intent, "backend does not allow volume attach/detach"))
			continue
		}
		actions = append(actions, volumeAttachmentAction(VolumeAttachmentDetach, intent, "backend allows volume detach"))
	}
	sort.SliceStable(actions, func(i, j int) bool {
		left := volumeAttachmentActionKey(actions[i])
		right := volumeAttachmentActionKey(actions[j])
		if left != right {
			return left < right
		}
		return actions[i].Kind < actions[j].Kind
	})
	return VolumeAttachmentPlan{GeneratedAt: now, Actions: actions}
}

func normalizeVolumeIntents(intents []VolumeAttachmentIntent) []VolumeAttachmentIntent {
	out := append([]VolumeAttachmentIntent(nil), intents...)
	for i := range out {
		out[i].DeploymentID = strings.TrimSpace(out[i].DeploymentID)
		out[i].InstanceID = strings.TrimSpace(out[i].InstanceID)
		out[i].VolumeName = strings.TrimSpace(out[i].VolumeName)
		out[i].MountPath = strings.TrimSpace(out[i].MountPath)
	}
	sort.SliceStable(out, func(i, j int) bool { return volumeAttachmentKey(out[i]) < volumeAttachmentKey(out[j]) })
	return out
}

func hasConflictingWriter(intent VolumeAttachmentIntent, observed, desired []VolumeAttachmentIntent) bool {
	if intent.ReadOnly || strings.TrimSpace(intent.VolumeName) == "" {
		return false
	}
	for _, other := range append(append([]VolumeAttachmentIntent(nil), observed...), desired...) {
		if other.ReadOnly || other.VolumeName != intent.VolumeName {
			continue
		}
		if other.DeploymentID != intent.DeploymentID || other.InstanceID != intent.InstanceID {
			return true
		}
	}
	return false
}

func volumeAttachmentAction(kind VolumeAttachmentActionKind, intent VolumeAttachmentIntent, reason string) VolumeAttachmentAction {
	return VolumeAttachmentAction{Kind: kind, DeploymentID: intent.DeploymentID, InstanceID: intent.InstanceID, VolumeName: intent.VolumeName, MountPath: intent.MountPath, ReadOnly: intent.ReadOnly, Reason: reason}
}

func volumeAttachmentKey(intent VolumeAttachmentIntent) string {
	if intent.DeploymentID == "" || intent.InstanceID == "" || intent.VolumeName == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s", intent.DeploymentID, intent.InstanceID, intent.VolumeName)
}

func volumeAttachmentActionKey(action VolumeAttachmentAction) string {
	return fmt.Sprintf("%s/%s/%s", action.DeploymentID, action.InstanceID, action.VolumeName)
}
