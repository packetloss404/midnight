package docker

import (
	"strings"
	"testing"
)

func TestContainerNameFormat(t *testing.T) {
	got := ContainerName("svc_01HAA0AAAA000000000000001", 42, 7)
	want := "or-01haa0aa-42-7"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
	if len(got) > MaxContainerNameLength {
		t.Fatalf("name too long: %d", len(got))
	}
}

func TestContainerNameTruncates(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := ContainerName(long, 999999999, 999999999)
	if len(got) > MaxContainerNameLength {
		t.Fatalf("expected ≤63 chars, got %d (%s)", len(got), got)
	}
	if !strings.HasPrefix(got, "or-") {
		t.Fatalf("expected or- prefix, got %q", got)
	}
}

func TestContainerNameDeterministic(t *testing.T) {
	a := ContainerName("svc_abc123def", 1, 1)
	b := ContainerName("svc_abc123def", 1, 1)
	if a != b {
		t.Fatalf("expected deterministic names; %q vs %q", a, b)
	}
}

func TestNetworkNameFormat(t *testing.T) {
	got := NetworkName("prj_alpha", "env_production")
	if !strings.HasPrefix(got, NetworkNamePrefix+"-") {
		t.Fatalf("expected or-net- prefix, got %q", got)
	}
	if !strings.Contains(got, "alpha") {
		t.Fatalf("expected project token, got %q", got)
	}
	if len(got) > MaxContainerNameLength {
		t.Fatalf("name too long: %d", len(got))
	}
}

func TestLabelSetCanonicalKeys(t *testing.T) {
	ls := LabelSet{
		ProjectID:      "prj1",
		EnvironmentID:  "env1",
		ServiceID:      "svc1",
		DeploymentID:   "dep1",
		InstanceID:     "inst1",
		AgentID:        "agent1",
		HostID:         "host1",
		IdempotencyKey: "key1",
		Extra:          map[string]string{"midnight.project_id": "should-not-overwrite"},
	}
	got := ls.AsMap()
	for _, k := range RequiredLabelKeys() {
		if _, ok := got[k]; !ok {
			t.Errorf("missing required label %q", k)
		}
	}
	if got[LabelManagedBy] != LabelManagedByV {
		t.Fatalf("expected managed_by=midnight-agent, got %q", got[LabelManagedBy])
	}
	if got[LabelProject] != "prj1" {
		t.Fatalf("expected project label, got %q", got[LabelProject])
	}
}

func TestSequenceFromInstanceIDStable(t *testing.T) {
	if SequenceFromInstanceID("inst_abc") != SequenceFromInstanceID("inst_abc") {
		t.Fatal("expected deterministic sequence")
	}
	if SequenceFromInstanceID("") != 0 {
		t.Fatal("empty id should return 0")
	}
}
