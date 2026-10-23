package backup

import (
	"testing"
	"time"
)

func TestPlanVolumeAttachmentsKeepsAttachesAndDetachesWhenBackendAllows(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	plan := PlanVolumeAttachments(VolumeAttachmentPlanInput{
		BackendCapabilities: VolumeBackendCapabilities{AttachDetach: true},
		Observed: []VolumeAttachmentIntent{
			{DeploymentID: "dep", InstanceID: "one", VolumeName: "data", MountPath: "/data"},
			{DeploymentID: "dep", InstanceID: "old", VolumeName: "scratch", MountPath: "/scratch"},
		},
		Desired: []VolumeAttachmentIntent{
			{DeploymentID: "dep", InstanceID: "one", VolumeName: "data", MountPath: "/data"},
			{DeploymentID: "dep", InstanceID: "two", VolumeName: "cache", MountPath: "/cache", ReadOnly: true},
		},
		Now: now,
	})

	if !plan.GeneratedAt.Equal(now) {
		t.Fatalf("GeneratedAt = %s, want %s", plan.GeneratedAt, now)
	}
	byKey := map[string]VolumeAttachmentAction{}
	for _, action := range plan.Actions {
		byKey[action.DeploymentID+"/"+action.InstanceID+"/"+action.VolumeName] = action
	}
	if byKey["dep/one/data"].Kind != VolumeAttachmentKeep {
		t.Fatalf("dep/one/data action = %+v", byKey["dep/one/data"])
	}
	if byKey["dep/two/cache"].Kind != VolumeAttachmentAttach {
		t.Fatalf("dep/two/cache action = %+v", byKey["dep/two/cache"])
	}
	if byKey["dep/old/scratch"].Kind != VolumeAttachmentDetach {
		t.Fatalf("dep/old/scratch action = %+v", byKey["dep/old/scratch"])
	}
}

func TestPlanVolumeAttachmentsBlocksChangesWhenBackendDoesNotAllow(t *testing.T) {
	plan := PlanVolumeAttachments(VolumeAttachmentPlanInput{
		Observed: []VolumeAttachmentIntent{{DeploymentID: "dep", InstanceID: "old", VolumeName: "data", MountPath: "/data"}},
		Desired:  []VolumeAttachmentIntent{{DeploymentID: "dep", InstanceID: "new", VolumeName: "data", MountPath: "/data"}},
	})

	if len(plan.Actions) != 2 {
		t.Fatalf("actions = %+v, want attach block and detach block", plan.Actions)
	}
	for _, action := range plan.Actions {
		if action.Kind != VolumeAttachmentBlock || action.Reason != "backend does not allow volume attach/detach" {
			t.Fatalf("action = %+v, want backend block", action)
		}
	}
}

func TestPlanVolumeAttachmentsBlocksConflictingWriters(t *testing.T) {
	plan := PlanVolumeAttachments(VolumeAttachmentPlanInput{
		BackendCapabilities: VolumeBackendCapabilities{AttachDetach: true},
		Observed:            []VolumeAttachmentIntent{{DeploymentID: "dep", InstanceID: "one", VolumeName: "data", MountPath: "/data"}},
		Desired:             []VolumeAttachmentIntent{{DeploymentID: "dep", InstanceID: "two", VolumeName: "data", MountPath: "/data"}},
	})

	byKey := map[string]VolumeAttachmentAction{}
	for _, action := range plan.Actions {
		byKey[action.DeploymentID+"/"+action.InstanceID+"/"+action.VolumeName] = action
	}
	blocked := byKey["dep/two/data"]
	if blocked.Kind != VolumeAttachmentBlock {
		t.Fatalf("dep/two/data action = %+v, want writer conflict block", blocked)
	}
	if blocked.Reason != "volume is already attached to another writer" {
		t.Fatalf("reason = %q", blocked.Reason)
	}
}
