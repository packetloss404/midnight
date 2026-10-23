package volume

import (
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestVolumeValidate(t *testing.T) {
	t.Parallel()

	good := Volume{
		ID:            "vol_01",
		ProjectID:     "prj_01",
		EnvironmentID: "env_01",
		ServiceID:     "svc_01",
		SizeBytes:     1024,
		Status:        StatusReady,
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("good Volume.Validate: %v", err)
	}

	tests := []Volume{
		{},
		{ID: "vol", ProjectID: "p", EnvironmentID: "e", ServiceID: "s", SizeBytes: -1, Status: StatusReady},
		{ID: "vol", ProjectID: "p", EnvironmentID: "e", ServiceID: "s", Status: "totally-bogus"},
	}
	for i, v := range tests {
		if err := v.Validate(); err == nil {
			t.Fatalf("test %d: expected validate error", i)
		}
	}
}

func TestSoftDeleteAndReap(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	v := &Volume{ID: "vol", ProjectID: "p", EnvironmentID: "e", ServiceID: "s", Status: StatusReady}
	v.MarkSoftDeleted(now)
	if v.Status != StatusSoftDeleted {
		t.Fatalf("Status = %s, want soft_deleted", v.Status)
	}
	if !v.HardDeleteAt.Equal(now.Add(SoftDeleteGrace)) {
		t.Fatalf("HardDeleteAt = %v, want now+7d", v.HardDeleteAt)
	}
	if v.CanReap(now) {
		t.Fatal("CanReap should be false immediately after MarkSoftDeleted")
	}
	if !v.CanReap(now.Add(SoftDeleteGrace).Add(time.Second)) {
		t.Fatal("CanReap should be true after the grace expires")
	}
}

func TestSingleWriterAdvisoryKey(t *testing.T) {
	t.Parallel()

	got := SingleWriterAdvisoryKey("svc", "env")
	want := "midnight_volume:svc:env"
	if got != want {
		t.Fatalf("SingleWriterAdvisoryKey = %q, want %q", got, want)
	}
}

func TestArbitrateSupersedePicksHighestSequence(t *testing.T) {
	t.Parallel()

	out, err := ArbitrateSupersede([]SupersedeCandidate{
		{DeploymentID: "dep_a", Sequence: 1},
		{DeploymentID: "dep_b", Sequence: 3},
		{DeploymentID: "dep_c", Sequence: 2},
	})
	if err != nil {
		t.Fatalf("ArbitrateSupersede: %v", err)
	}
	if out.Winner.DeploymentID != "dep_b" {
		t.Fatalf("Winner = %s, want dep_b", out.Winner.DeploymentID)
	}
	if len(out.Losers) != 2 {
		t.Fatalf("len(Losers) = %d, want 2", len(out.Losers))
	}
	for _, l := range out.Losers {
		if l.Code != failurecodes.DeploySuperseded {
			t.Fatalf("loser %s has code %s, want DEPLOY_SUPERSEDED", l.Candidate.DeploymentID, l.Code)
		}
	}
}

func TestArbitrateSupersedeBreaksTiesDeterministically(t *testing.T) {
	t.Parallel()

	// Same sequence; lexicographic id wins.
	out, err := ArbitrateSupersede([]SupersedeCandidate{
		{DeploymentID: "dep_b", Sequence: 5},
		{DeploymentID: "dep_a", Sequence: 5},
	})
	if err != nil {
		t.Fatalf("ArbitrateSupersede: %v", err)
	}
	if out.Winner.DeploymentID != "dep_a" {
		t.Fatalf("Winner = %s, want dep_a", out.Winner.DeploymentID)
	}
}

func TestArbitrateSupersedeRefusesEmpty(t *testing.T) {
	t.Parallel()
	_, err := ArbitrateSupersede(nil)
	if err == nil {
		t.Fatal("expected error for empty candidate list")
	}
}

func TestEvaluateQuotaSupport(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		fs        string
		opts      string
		wantReady bool
	}{
		{name: "xfs prjquota", fs: "xfs", opts: "rw,prjquota,relatime", wantReady: true},
		{name: "xfs no prjquota", fs: "xfs", opts: "rw,relatime", wantReady: false},
		{name: "ext4 quota", fs: "ext4", opts: "rw,quota,relatime", wantReady: true},
		{name: "ext4 nothing", fs: "ext4", opts: "rw,relatime", wantReady: false},
		{name: "tmpfs", fs: "tmpfs", opts: "rw", wantReady: false},
		{name: "empty", fs: "", opts: "", wantReady: false},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := EvaluateQuotaSupport(tt.fs, tt.opts)
			if got.ProjectQuotaReady != tt.wantReady {
				t.Fatalf("ProjectQuotaReady = %v, want %v (%s)", got.ProjectQuotaReady, tt.wantReady, got.Message)
			}
		})
	}
}
