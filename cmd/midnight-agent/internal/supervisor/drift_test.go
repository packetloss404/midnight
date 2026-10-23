package supervisor

import (
	"testing"
	"time"
)

func TestDriftDetectorClassifiesConvergedAndDetected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	d := DriftDetector{}
	expected := []StateInput{{Key: "container.c1.image", Value: "img:v1"}, {Key: "container.c1.health", Value: "ready"}}

	if event := d.Compare(now, expected, expected); event.Classification != DriftConverged {
		t.Fatalf("expected converged, got %+v", event)
	}
	observed := []StateInput{{Key: "container.c1.image", Value: "img:v1"}, {Key: "container.c1.health", Value: "unhealthy"}}
	event := d.Compare(now, expected, observed)
	if event.Classification != DriftDetected {
		t.Fatalf("expected detected, got %+v", event)
	}
	if event.ExpectedHash == event.ObservedHash {
		t.Fatalf("expected and observed hash should differ, got %s == %s", event.ExpectedHash, event.ObservedHash)
	}
}

func TestDriftDetectorUnknownWithoutExpected(t *testing.T) {
	d := DriftDetector{}
	event := d.Compare(time.Unix(1, 0).UTC(), nil, []StateInput{{Key: "k", Value: "v"}})
	if event.Classification != DriftUnknown {
		t.Fatalf("expected unknown, got %+v", event)
	}
}

func TestDriftDetectorHashIsOrderInsensitive(t *testing.T) {
	d := DriftDetector{}
	a := []StateInput{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}}
	b := []StateInput{{Key: "b", Value: "2"}, {Key: "a", Value: "1"}}
	if d.HashState(a) != d.HashState(b) {
		t.Fatalf("hash should be order-insensitive: %s vs %s", d.HashState(a), d.HashState(b))
	}
}

func TestSelfHealCooldownOnAllowsAfterWindow(t *testing.T) {
	cooldown := NewSelfHealCooldown(10 * time.Second)
	start := time.Unix(1_700_000_000, 0).UTC()

	first := cooldown.Try("inst-1", start)
	if first.Decision != SelfHealAllow {
		t.Fatalf("first attempt should be allowed, got %+v", first)
	}
	second := cooldown.Try("inst-1", start.Add(time.Second))
	if second.Decision != SelfHealCooldownOn || second.WaitFor != 9*time.Second {
		t.Fatalf("second attempt should be in cooldown for 9s, got %+v", second)
	}
	third := cooldown.Try("inst-1", start.Add(11*time.Second))
	if third.Decision != SelfHealAllow {
		t.Fatalf("third attempt should be allowed, got %+v", third)
	}
}

func TestSelfHealCooldownOnRequiresInstanceID(t *testing.T) {
	cooldown := NewSelfHealCooldown(time.Second)
	event := cooldown.Try(" ", time.Unix(1, 0).UTC())
	if event.Decision != SelfHealCooldownOn {
		t.Fatalf("missing instance id should be denied, got %+v", event)
	}
}
