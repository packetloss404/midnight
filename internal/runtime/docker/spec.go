package docker

import (
	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/strslice"
)

// PanelCapAllowlist is the panel-locked add-back capability set (line 91 +
// ADR-004 Compliance). The order is alphabetic so HostConfig.CapAdd comes
// out deterministically across platforms and Engine versions.
var PanelCapAllowlist = []string{
	"CHOWN",
	"DAC_OVERRIDE",
	"FOWNER",
	"FSETID",
	"KILL",
	"NET_BIND_SERVICE",
	"SETGID",
	"SETPCAP",
	"SETUID",
	"SYS_CHROOT",
}

// DefaultPidsLimit is the panel-cited Sprint 4 / S4-06 template default for
// `HostConfig.PidsLimit`.
const DefaultPidsLimit int64 = 1024

// DefaultRestartPolicy keeps application containers running across process
// crashes and daemon/host restarts while respecting an explicit operator stop.
const DefaultRestartPolicy = container.RestartPolicyUnlessStopped

// HardenedHostConfig returns a HostConfig populated with the panel-locked
// hardening defaults. Lane 2 callers compose this with an image-specific
// HostConfig (mounts, networks). Lane 4's secrets layer never touches this
// surface.
func HardenedHostConfig() *container.HostConfig {
	pidsLimit := DefaultPidsLimit
	hc := &container.HostConfig{
		Init: boolPtr(true),
		SecurityOpt: []string{
			"no-new-privileges:true",
			"apparmor=docker-default",
		},
		CapDrop: strslice.StrSlice{"ALL"},
		CapAdd:  append(strslice.StrSlice{}, PanelCapAllowlist...),
		LogConfig: container.LogConfig{
			Type: "json-file",
			Config: map[string]string{
				"max-size": "10m",
				"max-file": "5",
			},
		},
		RestartPolicy: container.RestartPolicy{Name: DefaultRestartPolicy},
		Resources: container.Resources{
			PidsLimit: &pidsLimit,
			Ulimits: []*container.Ulimit{
				{Name: "nofile", Soft: 65536, Hard: 1048576},
				{Name: "nproc", Soft: 1024, Hard: 4096},
			},
		},
	}
	return hc
}

// HardenedContainerConfig builds a Config with image, labels, and a sealed
// envelope. Callers append entrypoint/command/env outside this helper so the
// hardening defaults are not accidentally overwritten.
func HardenedContainerConfig(image string, labels map[string]string) *container.Config {
	cfg := &container.Config{
		Image:        image,
		Labels:       labels,
		AttachStdin:  false,
		AttachStdout: false,
		AttachStderr: false,
		Tty:          false,
		ExposedPorts: dockernetwork.PortSet{},
	}
	return cfg
}

func boolPtr(v bool) *bool {
	p := v
	return &p
}
