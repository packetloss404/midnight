package metering

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/tenancy"
)

// Resource identifies a billable or limit-tracked usage dimension.
type Resource string

const (
	ResourceBuildSeconds      Resource = "build_seconds"
	ResourceCPUSeconds        Resource = "cpu_seconds"
	ResourceMemoryByteSeconds Resource = "memory_byte_seconds"
	ResourceEgressBytes       Resource = "egress_bytes"
	ResourceRequests          Resource = "requests"
)

// UsageEvent is the normalized metering input emitted by API, scheduler, build,
// or runtime components. Quantity must be positive and expressed in the unit
// named by Resource.
type UsageEvent struct {
	ID         string        `json:"id"`
	Tenant     tenancy.Scope `json:"tenant"`
	Resource   Resource      `json:"resource"`
	Quantity   int64         `json:"quantity"`
	ObservedAt time.Time     `json:"observed_at"`
	Source     string        `json:"source,omitempty"`
}

// Bucket is the deterministic aggregate for one tenant/resource/window.
type Bucket struct {
	Tenant       tenancy.Scope `json:"tenant"`
	Resource     Resource      `json:"resource"`
	Quantity     int64         `json:"quantity"`
	WindowStart  time.Time     `json:"window_start"`
	WindowEnd    time.Time     `json:"window_end"`
	EventCount   int           `json:"event_count"`
	FirstEventID string        `json:"first_event_id,omitempty"`
	LastEventID  string        `json:"last_event_id,omitempty"`
}

var (
	ErrEmptyEventID     = errors.New("metering: event id is required")
	ErrInvalidTenant    = errors.New("metering: tenant scope is required")
	ErrEmptyResource    = errors.New("metering: resource is required")
	ErrInvalidQuantity  = errors.New("metering: quantity must be positive")
	ErrQuantityOverflow = errors.New("metering: quantity overflow")
	ErrZeroObservedAt   = errors.New("metering: observed_at is required")
	ErrInvalidWindow    = errors.New("metering: window end must be after start")
)

// Validate rejects events that would make downstream billing ambiguous.
func (e UsageEvent) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return ErrEmptyEventID
	}
	if err := e.Tenant.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTenant, err)
	}
	if strings.TrimSpace(string(e.Resource)) == "" {
		return ErrEmptyResource
	}
	if e.Quantity <= 0 {
		return ErrInvalidQuantity
	}
	if e.ObservedAt.IsZero() {
		return ErrZeroObservedAt
	}
	return nil
}

// Aggregate folds valid events in [windowStart, windowEnd) into stable buckets.
// Events outside the window are ignored but still validated, so corrupt inputs
// cannot hide behind time filtering.
func Aggregate(events []UsageEvent, windowStart, windowEnd time.Time) ([]Bucket, error) {
	if !windowEnd.After(windowStart) {
		return nil, ErrInvalidWindow
	}

	ordered := append([]UsageEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].ObservedAt.Equal(ordered[j].ObservedAt) {
			return ordered[i].ObservedAt.Before(ordered[j].ObservedAt)
		}
		return ordered[i].ID < ordered[j].ID
	})

	buckets := make(map[string]*Bucket)
	for i, event := range ordered {
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("event[%d]: %w", i, err)
		}
		if event.ObservedAt.Before(windowStart) || !event.ObservedAt.Before(windowEnd) {
			continue
		}

		key := event.Tenant.Key() + "\x00" + string(event.Resource)
		bucket := buckets[key]
		if bucket == nil {
			bucket = &Bucket{
				Tenant:       event.Tenant,
				Resource:     event.Resource,
				WindowStart:  windowStart,
				WindowEnd:    windowEnd,
				FirstEventID: event.ID,
			}
			buckets[key] = bucket
		}
		if bucket.Quantity > math.MaxInt64-event.Quantity {
			return nil, fmt.Errorf("event[%d]: %w", i, ErrQuantityOverflow)
		}
		bucket.Quantity += event.Quantity
		bucket.EventCount++
		bucket.LastEventID = event.ID
	}

	out := make([]Bucket, 0, len(buckets))
	for _, bucket := range buckets {
		out = append(out, *bucket)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tenant.Key() != out[j].Tenant.Key() {
			return out[i].Tenant.Key() < out[j].Tenant.Key()
		}
		return out[i].Resource < out[j].Resource
	})
	return out, nil
}
