package tcpproxy

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"

	midnightnetwork "github.com/midnight/midnight/internal/network"
)

func TestDockerNetworkEnrollerAttachesDesiredAndDetachesStaleManagedNetworks(t *testing.T) {
	desiredScope := Scope{ProjectID: "prj_one", EnvironmentID: "env_one", ServiceID: "svc_one"}
	staleScope := Scope{ProjectID: "prj_old", EnvironmentID: "env_old", ServiceID: "svc_old"}
	desiredName := (midnightnetwork.ProjectNetwork{ProjectID: desiredScope.ProjectID, EnvironmentID: desiredScope.EnvironmentID}).Name()
	staleName := (midnightnetwork.ProjectNetwork{ProjectID: staleScope.ProjectID, EnvironmentID: staleScope.EnvironmentID}).Name()
	client := &fakeDockerNetworkClient{
		containerID: "proxy-id",
		attached: map[string]bool{
			"midnight-control": true,
			staleName:          true,
		},
		networks: map[string]dockernetwork.Inspect{
			desiredName: managedNetwork("net-desired", desiredName, desiredScope),
			staleName:   managedNetwork("net-stale", staleName, staleScope),
			"midnight-control": {
				Network: dockernetwork.Network{ID: "net-control", Name: "midnight-control"},
			},
		},
	}
	enroller := DockerNetworkEnroller{Client: client, Container: "midnight-deploy-worker"}
	if err := enroller.ReconcileNetworks(context.Background(), []Scope{desiredScope}); err != nil {
		t.Fatal(err)
	}
	if !client.attached[desiredName] || client.attached[staleName] || !client.attached["midnight-control"] {
		t.Fatalf("attached networks = %#v", client.attached)
	}
}

func TestDockerNetworkEnrollerRejectsCrossScopeNetwork(t *testing.T) {
	scope := Scope{ProjectID: "prj_one", EnvironmentID: "env_one", ServiceID: "svc"}
	name := (midnightnetwork.ProjectNetwork{ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID}).Name()
	wrong := Scope{ProjectID: "prj_other", EnvironmentID: "env_one", ServiceID: "svc"}
	client := &fakeDockerNetworkClient{
		containerID: "proxy-id", attached: map[string]bool{},
		networks: map[string]dockernetwork.Inspect{name: managedNetwork("net", name, wrong)},
	}
	err := (DockerNetworkEnroller{Client: client, Container: "proxy"}).ReconcileNetworks(context.Background(), []Scope{scope})
	if err == nil {
		t.Fatal("cross-scope enrollment error = nil")
	}
}

type fakeDockerNetworkClient struct {
	containerID string
	attached    map[string]bool
	networks    map[string]dockernetwork.Inspect
}

func (f *fakeDockerNetworkClient) ContainerInspect(context.Context, string) (container.InspectResponse, error) {
	networks := map[string]*dockernetwork.EndpointSettings{}
	for name, attached := range f.attached {
		if attached {
			networks[name] = &dockernetwork.EndpointSettings{}
		}
	}
	return container.InspectResponse{
		ID: f.containerID,
		NetworkSettings: &container.NetworkSettings{
			Networks: networks,
		},
	}, nil
}

func (f *fakeDockerNetworkClient) NetworkInspect(_ context.Context, name string) (dockernetwork.Inspect, error) {
	value, ok := f.networks[name]
	if !ok {
		return dockernetwork.Inspect{}, errors.New("network not found")
	}
	return value, nil
}

func (f *fakeDockerNetworkClient) NetworkConnect(_ context.Context, networkID, _ string) error {
	for name, value := range f.networks {
		if value.ID == networkID {
			f.attached[name] = true
			return nil
		}
	}
	return errors.New("network not found")
}

func (f *fakeDockerNetworkClient) NetworkDisconnect(_ context.Context, networkID, _ string) error {
	for name, value := range f.networks {
		if value.ID == networkID {
			delete(f.attached, name)
			return nil
		}
	}
	return errors.New("network not found")
}

func managedNetwork(id, name string, scope Scope) dockernetwork.Inspect {
	return dockernetwork.Inspect{
		Network: dockernetwork.Network{
			ID: id, Name: name,
			Labels: map[string]string{
				midnightnetwork.LabelManagedBy:     midnightnetwork.LabelManagedByV,
				midnightnetwork.LabelProjectID:     scope.ProjectID,
				midnightnetwork.LabelEnvironmentID: scope.EnvironmentID,
			},
		},
	}
}
