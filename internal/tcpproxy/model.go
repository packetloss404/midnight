package tcpproxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrEndpointNotFound   = errors.New("tcp proxy endpoint not found")
	ErrEndpointConflict   = errors.New("tcp proxy endpoint conflicts with an existing allocation")
	ErrEndpointInvalid    = errors.New("tcp proxy endpoint is invalid")
	ErrBackendUnavailable = errors.New("tcp proxy active deployment backend is unavailable")
)

type ApplyState string

const (
	ApplyPending   ApplyState = "pending"
	ApplyActive    ApplyState = "active"
	ApplyWithdrawn ApplyState = "withdrawn"
	ApplyFailed    ApplyState = "failed"
)

type Scope struct {
	ProjectID     string
	EnvironmentID string
	ServiceID     string
}

func (s Scope) Validate() error {
	if strings.TrimSpace(s.ProjectID) == "" ||
		strings.TrimSpace(s.EnvironmentID) == "" ||
		strings.TrimSpace(s.ServiceID) == "" {
		return fmt.Errorf("%w: project, environment, and service are required", ErrEndpointInvalid)
	}
	return nil
}

// Endpoint is durable desired state plus evidence observed by the single-host
// listener controller. Active means the local listener was bound and its
// backend accepted a TCP health connection. It does not assert DNS publication
// or that traffic can cross an operator firewall/NAT boundary.
type Endpoint struct {
	ID string
	Scope
	Host             string
	ExternalPort     int
	BackendPort      int
	AllowedCIDRs     []string
	DesiredEnabled   bool
	ApplyState       ApplyState
	ListenerBound    bool
	BackendReachable bool
	ControllerID     string
	LastErrorCode    string
	LastErrorDetail  string
	ObservedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (e Endpoint) PublicAddress() string {
	if strings.TrimSpace(e.Host) == "" || e.ExternalPort <= 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", e.Host, e.ExternalPort)
}

type CreateEndpointInput struct {
	Scope
	BaseDomain            string
	RequestedHost         string
	RequestedExternalPort int
	BackendPort           int
	AllowedCIDRs          []string
}

type Backend struct {
	DeploymentID string
	InstanceID   string
	Host         string
}

// BackendResolver converts a validated service/environment scope into a ready,
// active runtime destination. API callers never supply the backend host.
type BackendResolver interface {
	ResolveBackends(context.Context, Scope) ([]Backend, error)
}

// NetworkEnroller converges the trusted proxy process onto exactly the
// environment networks needed by current desired endpoints. Production Docker
// wiring uses this before backend resolution and detaches stale networks.
type NetworkEnroller interface {
	ReconcileNetworks(context.Context, []Scope) error
}

type Observation struct {
	State            ApplyState
	ListenerBound    bool
	BackendReachable bool
	ControllerID     string
	ErrorCode        string
	ErrorDetail      string
	ObservedAt       time.Time
}

// Store is shared by REST/CLI/GUI and the runtime controller. CreateEndpoint
// must serialize allocation so two callers cannot claim the same host/port.
// RecordObservation is a desired-state CAS: a stale reconcile pass cannot mark
// an endpoint active after an API withdrawal.
type Store interface {
	CreateEndpoint(context.Context, CreateEndpointInput) (Endpoint, error)
	GetEndpoint(context.Context, string) (Endpoint, error)
	ListEndpoints(context.Context, Scope) ([]Endpoint, error)
	ListRuntimeEndpoints(context.Context) ([]Endpoint, error)
	DisableEndpoint(context.Context, string) (Endpoint, error)
	RecordObservation(context.Context, string, bool, Observation) (bool, error)
}
