package scheduler

import (
	"testing"
	"time"
)

func TestNormalizeCronConfigDefaults(t *testing.T) {
	config, loc, err := NormalizeCronConfig(CronConfig{Schedule: "*/5 * * * *"})
	if err != nil {
		prettyCronValidationError(t, err)
	}
	if loc.String() != "UTC" {
		t.Fatalf("timezone = %s, want UTC", loc)
	}
	if config.Timeout != time.Hour || config.RetryPolicy.MaxAttempts != 1 || config.RetryPolicy.BackoffBase != 30*time.Second {
		t.Fatalf("defaults not applied: %#v", config)
	}
	if config.MaxParallel != 1 || config.ConcurrencyPolicy != CronConcurrencyForbid {
		t.Fatalf("concurrency defaults not applied: %#v", config)
	}
}

func TestNormalizeCronConfigRejectsInvalidSchedule(t *testing.T) {
	_, _, err := NormalizeCronConfig(CronConfig{Schedule: "@hourly"})
	cronErr, ok := err.(*CronValidationError)
	if !ok || cronErr.Code != CronReasonScheduleInvalid {
		t.Fatalf("NormalizeCronConfig error = %#v", err)
	}
}

func TestNormalizeCronConfigRejectsInvalidTimezone(t *testing.T) {
	_, _, err := NormalizeCronConfig(CronConfig{Schedule: "0 * * * *", Timezone: "Missing/Zone"})
	cronErr, ok := err.(*CronValidationError)
	if !ok || cronErr.Code != CronReasonTimezoneInvalid {
		t.Fatalf("NormalizeCronConfig error = %#v", err)
	}
}

func TestCronDueWindowsFiveFieldSchedule(t *testing.T) {
	last := time.Date(2026, 4, 26, 10, 0, 30, 0, time.UTC)
	now := time.Date(2026, 4, 26, 10, 5, 0, 0, time.UTC)
	due, err := CronDueWindows(CronConfig{Schedule: "*/5 * * * *"}, last, now)
	if err != nil {
		t.Fatalf("CronDueWindows: %v", err)
	}
	want := []time.Time{
		time.Date(2026, 4, 26, 10, 5, 0, 0, time.UTC),
	}
	assertTimes(t, due, want)
}

func TestNormalizeCronConfigRejectsCadenceBelowFiveMinutes(t *testing.T) {
	_, _, err := NormalizeCronConfig(CronConfig{Schedule: "* * * * *"})
	cronErr, ok := err.(*CronValidationError)
	if !ok || cronErr.Code != CronReasonScheduleInvalid {
		t.Fatalf("NormalizeCronConfig error = %#v", err)
	}
}

func TestNormalizeCronConfigRejectsSixFieldSchedule(t *testing.T) {
	_, _, err := NormalizeCronConfig(CronConfig{Schedule: "*/5 * * * * *"})
	cronErr, ok := err.(*CronValidationError)
	if !ok || cronErr.Code != CronReasonScheduleInvalid {
		t.Fatalf("NormalizeCronConfig error = %#v", err)
	}
}

func TestCronDueWindowsRejectsMissedBacklog(t *testing.T) {
	last := time.Date(2026, 4, 26, 7, 0, 0, 0, time.UTC)
	now := last.Add(MaxCronDueWindow + time.Minute)
	_, err := CronDueWindows(CronConfig{Schedule: "*/5 * * * *"}, last, now)
	if _, ok := err.(*CronDueWindowError); !ok {
		t.Fatalf("CronDueWindows error = %#v, want CronDueWindowError", err)
	}
}

func TestNormalizeCronConfigRejectsOverlappingPolicies(t *testing.T) {
	_, _, err := NormalizeCronConfig(CronConfig{Schedule: "*/5 * * * *", ConcurrencyPolicy: CronConcurrencyAllow})
	cronErr, ok := err.(*CronValidationError)
	if !ok || cronErr.Field != "concurrencyPolicy" {
		t.Fatalf("NormalizeCronConfig error = %#v", err)
	}
}

func prettyCronValidationError(t *testing.T, err error) {
	t.Helper()
	if cronErr, ok := err.(*CronValidationError); ok {
		t.Fatalf("NormalizeCronConfig code=%s field=%s err=%v", cronErr.Code, cronErr.Field, cronErr.Err)
	}
	t.Fatalf("NormalizeCronConfig: %v", err)
}

func assertTimes(t *testing.T, got, want []time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("times = %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("times[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}
