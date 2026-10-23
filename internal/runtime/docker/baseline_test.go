package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestEnforceEngineAPIFloor(t *testing.T) {
	cases := []struct {
		name       string
		apiVersion string
		wantErr    bool
	}{
		{"floor", MinEngineAPIVersion, false},
		{"newer", "1.50", false},
		{"newer-major", "2.0", false},
		{"older-minor", "1.42", true},
		{"older-major", "0.99", true},
		{"empty", "", true},
		{"garbage", "v1.43", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := EnforceEngineAPIFloor(tc.apiVersion)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if CodeOf(err) != failurecodes.DockerEngineUnsupported {
					t.Fatalf("expected DOCKER_ENGINE_UNSUPPORTED, got %s", CodeOf(err))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCheckBaselineAcceptsCompliantHost(t *testing.T) {
	fake := newFakeEngine()
	report, err := CheckBaseline(context.Background(), fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", report.Conflicts)
	}
	if !report.LiveRestore {
		t.Fatal("expected LiveRestore=true")
	}
	if !report.NoNewPrivileges {
		t.Fatal("expected NoNewPrivileges=true")
	}
}

func TestCheckBaselineFlagsConflicts(t *testing.T) {
	fake := newFakeEngine()
	fake.info.Driver = "devicemapper"
	fake.info.LiveRestoreEnabled = false
	fake.info.LoggingDriver = "journald"
	fake.info.SecurityOptions = []string{"name=apparmor"}
	fake.info.CgroupVersion = "1"

	report, err := CheckBaseline(context.Background(), fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Conflicts) != 5 {
		t.Fatalf("expected 5 conflicts, got %d: %+v", len(report.Conflicts), report.Conflicts)
	}
	keys := make(map[string]string)
	for _, c := range report.Conflicts {
		keys[c.Key] = c.Required
	}
	for _, key := range []string{"storage-driver", "live-restore", "log-driver", "no-new-privileges", "cgroup-version"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("missing conflict for key %q", key)
		}
	}
}

func TestEnforceBaselineReturnsTypedError(t *testing.T) {
	report := BaselineReport{
		Conflicts: []BaselineRequirement{{Key: "storage-driver", Required: "overlay2", Observed: "devicemapper"}},
	}
	err := EnforceBaseline(report)
	if err == nil {
		t.Fatal("expected error")
	}
	if CodeOf(err) != failurecodes.DockerDaemonBaselineConflict {
		t.Fatalf("expected DOCKER_DAEMON_BASELINE_CONFLICT, got %s", CodeOf(err))
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatal("expected typed *Error")
	}
	if typed.Detail == "" {
		t.Fatal("expected detail to include conflict description")
	}
}

func TestEnforceBaselineNoConflicts(t *testing.T) {
	if err := EnforceBaseline(BaselineReport{}); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestParseBaselineMode(t *testing.T) {
	cases := map[string]BaselineMode{"": BaselineEnforce, "enforce": BaselineEnforce, " WARN ": BaselineWarn}
	for raw, want := range cases {
		got, err := ParseBaselineMode(raw)
		if err != nil {
			t.Fatalf("ParseBaselineMode(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseBaselineMode(%q) = %q, want %q", raw, got, want)
		}
	}
	if _, err := ParseBaselineMode("lenient"); err == nil {
		t.Fatal("expected error for unknown baseline mode")
	}
}

func TestNewWarnModeToleratesBaselineConflicts(t *testing.T) {
	fake := newFakeEngine()
	fake.info.Driver = "overlayfs"
	fake.info.LiveRestoreEnabled = false
	fake.info.SecurityOptions = []string{"name=seccomp,profile=builtin"}

	if _, _, err := New(context.Background(), Config{Engine: fake, CGroupMode: CGroupModeV2}); err == nil {
		t.Fatal("enforce mode must refuse a non-compliant daemon")
	}
	adapter, report, err := New(context.Background(), Config{Engine: fake, CGroupMode: CGroupModeV2, BaselineMode: BaselineWarn})
	if err != nil {
		t.Fatalf("warn mode should tolerate baseline conflicts: %v", err)
	}
	if adapter == nil {
		t.Fatal("expected adapter")
	}
	if len(report.Conflicts) == 0 || len(report.Warnings) != len(report.Conflicts) {
		t.Fatalf("expected one warning per conflict, got conflicts=%d warnings=%d", len(report.Conflicts), len(report.Warnings))
	}
}
