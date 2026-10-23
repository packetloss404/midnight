package scheduler

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ServiceTypeCron = "cron"

	CronReasonInvocationFailed = "CRON_INVOCATION_FAILED"
	CronReasonWindowMissed     = "CRON_WINDOW_MISSED"
	CronReasonScheduleInvalid  = "CRON_SCHEDULE_INVALID"
	CronReasonTimezoneInvalid  = "CRON_TIMEZONE_INVALID"

	DefaultCronTimezone          = "UTC"
	DefaultCronTimeout           = time.Hour
	MaxCronTimeout               = 24 * time.Hour
	DefaultCronMaxAttempts       = 1
	DefaultCronBackoff           = "exponential"
	DefaultCronBackoffBase       = 30 * time.Second
	DefaultCronMaxParallel       = 1
	DefaultCronConcurrencyPolicy = CronConcurrencyForbid
	// Cron schedules are deliberately five-field UTC schedules with a five
	// minute minimum cadence. This avoids a second-level or per-minute
	// control-plane loop that the durable worker does not promise to operate.
	MinCronInterval    = 5 * time.Minute
	cronEvaluationStep = time.Minute
	// A recovered scheduler never replays an unbounded historical backlog. A
	// caller must explicitly decide how to recover a schedule that was not
	// evaluated for longer than this window.
	MaxCronDueWindow = 15 * time.Minute

	maxCronDueWindows = 10_000
)

type CronConcurrencyPolicy string

const (
	CronConcurrencyAllow   CronConcurrencyPolicy = "allow"
	CronConcurrencyForbid  CronConcurrencyPolicy = "forbid"
	CronConcurrencyReplace CronConcurrencyPolicy = "replace"
)

type CronRetryPolicy struct {
	MaxAttempts int
	Backoff     string
	BackoffBase time.Duration
}

type CronConfig struct {
	Schedule          string
	Timezone          string
	Timeout           time.Duration
	RetryPolicy       CronRetryPolicy
	MaxParallel       int
	ConcurrencyPolicy CronConcurrencyPolicy
}

type CronValidationError struct {
	Code  string
	Field string
	Err   error
}

// CronDueWindowError is returned instead of silently replaying a potentially
// enormous missed backlog after an outage. It is intentionally distinct from
// malformed schedule validation so callers can alert an operator and advance
// the durable cursor deliberately.
type CronDueWindowError struct {
	LastEvaluatedAt time.Time
	Now             time.Time
}

func (e *CronDueWindowError) Error() string {
	return fmt.Sprintf("cron due window %s exceeds maximum %s", e.Now.Sub(e.LastEvaluatedAt), MaxCronDueWindow)
}

func (e *CronValidationError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("cron %s is invalid", e.Field)
	}
	return fmt.Sprintf("cron %s is invalid: %v", e.Field, e.Err)
}

func (e *CronValidationError) Unwrap() error { return e.Err }

func NormalizeCronConfig(config CronConfig) (CronConfig, *time.Location, error) {
	if _, err := parseCronSchedule(config.Schedule); err != nil {
		return CronConfig{}, nil, &CronValidationError{Code: CronReasonScheduleInvalid, Field: "schedule", Err: err}
	}
	config.Timezone = strings.TrimSpace(config.Timezone)
	if config.Timezone == "" {
		config.Timezone = DefaultCronTimezone
	}
	if config.Timezone != DefaultCronTimezone {
		return CronConfig{}, nil, &CronValidationError{Code: CronReasonTimezoneInvalid, Field: "timezone", Err: fmt.Errorf("must be %q", DefaultCronTimezone)}
	}
	loc := time.UTC
	if config.Timeout == 0 {
		config.Timeout = DefaultCronTimeout
	}
	if config.Timeout < 0 || config.Timeout > MaxCronTimeout {
		return CronConfig{}, nil, &CronValidationError{Code: CronReasonScheduleInvalid, Field: "timeout", Err: fmt.Errorf("must be between 0 and %s", MaxCronTimeout)}
	}
	if config.RetryPolicy.MaxAttempts == 0 {
		config.RetryPolicy.MaxAttempts = DefaultCronMaxAttempts
	}
	if config.RetryPolicy.MaxAttempts < 0 {
		return CronConfig{}, nil, &CronValidationError{Code: CronReasonScheduleInvalid, Field: "retryPolicy.maxAttempts", Err: fmt.Errorf("must be non-negative")}
	}
	if strings.TrimSpace(config.RetryPolicy.Backoff) == "" {
		config.RetryPolicy.Backoff = DefaultCronBackoff
	}
	if config.RetryPolicy.Backoff != DefaultCronBackoff {
		return CronConfig{}, nil, &CronValidationError{Code: CronReasonScheduleInvalid, Field: "retryPolicy.backoff", Err: fmt.Errorf("must be %q", DefaultCronBackoff)}
	}
	if config.RetryPolicy.BackoffBase == 0 {
		config.RetryPolicy.BackoffBase = DefaultCronBackoffBase
	}
	if config.RetryPolicy.BackoffBase < 0 {
		return CronConfig{}, nil, &CronValidationError{Code: CronReasonScheduleInvalid, Field: "retryPolicy.backoffBase", Err: fmt.Errorf("must be non-negative")}
	}
	if config.MaxParallel == 0 {
		config.MaxParallel = DefaultCronMaxParallel
	}
	if config.MaxParallel < 0 {
		return CronConfig{}, nil, &CronValidationError{Code: CronReasonScheduleInvalid, Field: "maxParallel", Err: fmt.Errorf("must be non-negative")}
	}
	if config.ConcurrencyPolicy == "" {
		config.ConcurrencyPolicy = DefaultCronConcurrencyPolicy
	}
	switch config.ConcurrencyPolicy {
	case CronConcurrencyForbid:
		return config, loc, nil
	default:
		return CronConfig{}, nil, &CronValidationError{Code: CronReasonScheduleInvalid, Field: "concurrencyPolicy", Err: fmt.Errorf("must be %q until an execution runtime can safely replace or parallelize runs", CronConcurrencyForbid)}
	}
}

func CronDueWindows(config CronConfig, lastEvaluatedAt, now time.Time) ([]time.Time, error) {
	config, loc, err := NormalizeCronConfig(config)
	if err != nil {
		return nil, err
	}
	spec, err := parseCronSchedule(config.Schedule)
	if err != nil {
		return nil, err
	}
	if !now.After(lastEvaluatedAt) {
		return nil, nil
	}
	lastEvaluatedAt = lastEvaluatedAt.UTC()
	now = now.UTC()
	if now.Sub(lastEvaluatedAt) > MaxCronDueWindow {
		return nil, &CronDueWindowError{LastEvaluatedAt: lastEvaluatedAt, Now: now}
	}
	start := lastEvaluatedAt.In(loc).Truncate(cronEvaluationStep).Add(cronEvaluationStep)
	end := now.In(loc)
	due := make([]time.Time, 0)
	for t := start; !t.After(end); t = t.Add(cronEvaluationStep) {
		if spec.matches(t) {
			due = append(due, t.UTC())
			if len(due) > maxCronDueWindows {
				return nil, fmt.Errorf("cron due window exceeds %d matches", maxCronDueWindows)
			}
		}
	}
	return due, nil
}

type cronField struct {
	values   map[int]struct{}
	wildcard bool
}

type cronSchedule struct {
	minute     cronField
	hour       cronField
	dayOfMonth cronField
	month      cronField
	dayOfWeek  cronField
}

func parseCronSchedule(schedule string) (cronSchedule, error) {
	parts := strings.Fields(schedule)
	if len(parts) != 5 {
		return cronSchedule{}, fmt.Errorf("expected exactly 5 fields")
	}
	fields := make([]cronField, len(parts))
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i := range parts {
		field, err := parseCronField(parts[i], ranges[i][0], ranges[i][1], i == 4)
		if err != nil {
			return cronSchedule{}, fmt.Errorf("field %d: %w", i+1, err)
		}
		fields[i] = field
	}
	if cronMinuteCadence(fields[0]) < MinCronInterval {
		return cronSchedule{}, fmt.Errorf("minimum cadence is %s", MinCronInterval)
	}
	return cronSchedule{minute: fields[0], hour: fields[1], dayOfMonth: fields[2], month: fields[3], dayOfWeek: fields[4]}, nil
}

// cronMinuteCadence gives a conservative lower bound: the rest of a cron
// expression can only make invocations less frequent, never more frequent.
func cronMinuteCadence(field cronField) time.Duration {
	values := make([]int, 0, len(field.values))
	for value := range field.values {
		values = append(values, value)
	}
	if len(values) <= 1 {
		return time.Hour
	}
	sort.Ints(values)
	minGap := 60
	for index := range values {
		next := values[(index+1)%len(values)]
		if index == len(values)-1 {
			next += 60
		}
		if gap := next - values[index]; gap < minGap {
			minGap = gap
		}
	}
	return time.Duration(minGap) * time.Minute
}

func parseCronField(expr string, min, max int, sundayAlias bool) (cronField, error) {
	field := cronField{values: make(map[int]struct{})}
	for _, item := range strings.Split(expr, ",") {
		if item == "" {
			return cronField{}, fmt.Errorf("empty list item")
		}
		base, step := item, 1
		if strings.Contains(item, "/") {
			parts := strings.Split(item, "/")
			if len(parts) != 2 {
				return cronField{}, fmt.Errorf("invalid step")
			}
			base = parts[0]
			parsed, err := strconv.Atoi(parts[1])
			if err != nil || parsed <= 0 {
				return cronField{}, fmt.Errorf("invalid step")
			}
			step = parsed
		}
		start, end, wildcard, err := parseCronRange(base, min, max)
		if err != nil {
			return cronField{}, err
		}
		field.wildcard = field.wildcard || wildcard
		for v := start; v <= end; v += step {
			if sundayAlias && v == 7 {
				field.values[0] = struct{}{}
				continue
			}
			field.values[v] = struct{}{}
		}
	}
	return field, nil
}

func parseCronRange(expr string, min, max int) (int, int, bool, error) {
	if expr == "*" {
		return min, max, true, nil
	}
	if strings.Contains(expr, "-") {
		parts := strings.Split(expr, "-")
		if len(parts) != 2 {
			return 0, 0, false, fmt.Errorf("invalid range")
		}
		start, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, false, fmt.Errorf("invalid number")
		}
		end, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, false, fmt.Errorf("invalid number")
		}
		if start < min || end > max || start > end {
			return 0, 0, false, fmt.Errorf("range out of bounds")
		}
		return start, end, false, nil
	}
	value, err := strconv.Atoi(expr)
	if err != nil {
		return 0, 0, false, fmt.Errorf("invalid number")
	}
	if value < min || value > max {
		return 0, 0, false, fmt.Errorf("value out of bounds")
	}
	return value, value, false, nil
}

func (s cronSchedule) matches(t time.Time) bool {
	if !s.minute.contains(t.Minute()) || !s.hour.contains(t.Hour()) || !s.month.contains(int(t.Month())) {
		return false
	}
	dayOfMonth := s.dayOfMonth.contains(t.Day())
	dayOfWeek := s.dayOfWeek.contains(int(t.Weekday()))
	if !s.dayOfMonth.wildcard && !s.dayOfWeek.wildcard {
		return dayOfMonth || dayOfWeek
	}
	return dayOfMonth && dayOfWeek
}

func (f cronField) contains(value int) bool {
	_, ok := f.values[value]
	return ok
}
