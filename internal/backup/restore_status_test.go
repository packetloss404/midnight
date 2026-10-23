package backup

import "testing"

func TestBuildRestoreAPIStatusReportsExecutorAndOffhostUnavailable(t *testing.T) {
	t.Parallel()

	status := BuildRestoreAPIStatus(RestoreAPIStatusInput{})

	if status.Status != RestoreAPIStatusUnavailable || status.Available {
		t.Fatalf("status = %#v, want unavailable", status)
	}
	if status.ReasonCode != RestoreAPIReasonUnavailable {
		t.Fatalf("reason_code = %q, want %q", status.ReasonCode, RestoreAPIReasonUnavailable)
	}
	if status.RestoreExecution.Available {
		t.Fatalf("restore execution = %#v, want unavailable", status.RestoreExecution)
	}
	if status.RestoreExecution.ReasonCode != RestoreAPIReasonUnavailable {
		t.Fatalf("restore execution reason = %q, want %q", status.RestoreExecution.ReasonCode, RestoreAPIReasonUnavailable)
	}
	if status.Offhost.Configured || status.Offhost.ReasonCode != RestoreAPIReasonOffhostMissing {
		t.Fatalf("offhost = %#v, want missing", status.Offhost)
	}
	if !hasRestoreStatusReason(status, RestoreAPIReasonUnavailable) {
		t.Fatalf("reasons = %#v, want %q", status.Reasons, RestoreAPIReasonUnavailable)
	}
	if !hasRestoreStatusReason(status, RestoreAPIReasonOffhostMissing) {
		t.Fatalf("reasons = %#v, want %q", status.Reasons, RestoreAPIReasonOffhostMissing)
	}
}

func TestBuildRestoreAPIStatusAcceptsConfiguredOffhostButStillBlocksWithoutExecutor(t *testing.T) {
	t.Parallel()

	status := BuildRestoreAPIStatus(RestoreAPIStatusInput{DestinationURI: "s3://midnight-backups/platform"})

	if status.Status != RestoreAPIStatusUnavailable || status.Available {
		t.Fatalf("status = %#v, want unavailable", status)
	}
	if status.ReasonCode != RestoreAPIReasonUnavailable {
		t.Fatalf("reason_code = %q, want %q", status.ReasonCode, RestoreAPIReasonUnavailable)
	}
	if !status.Offhost.Configured || status.Offhost.Scheme != "s3" {
		t.Fatalf("offhost = %#v, want configured s3", status.Offhost)
	}
	if hasRestoreStatusReason(status, RestoreAPIReasonOffhostMissing) {
		t.Fatalf("reasons = %#v, did not expect offhost missing", status.Reasons)
	}
}

func TestBuildRestoreAPIStatusReadyWhenExecutorAndOffhostAreAvailable(t *testing.T) {
	t.Parallel()

	status := BuildRestoreAPIStatus(RestoreAPIStatusInput{
		DestinationURI:           "sftp://backup.example/midnight",
		RestoreExecutorAvailable: true,
	})

	if status.Status != RestoreAPIStatusAvailable || !status.Available {
		t.Fatalf("status = %#v, want available", status)
	}
	if status.ReasonCode != "" || len(status.Reasons) != 0 {
		t.Fatalf("reasons = %#v / %q, want empty", status.Reasons, status.ReasonCode)
	}
}

func hasRestoreStatusReason(status RestoreAPIStatus, code string) bool {
	for _, reason := range status.Reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}
