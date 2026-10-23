package tcpproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestControllerBindsProxiesRestartsAndWithdraws(t *testing.T) {
	backend := startEchoBackend(t)
	backendHost, backendPortText, _ := net.SplitHostPort(backend.Addr().String())
	backendPort, _ := strconv.Atoi(backendPortText)
	externalPort := reservePort(t)
	store := newRuntimeMemoryStore(Endpoint{
		ID: "tcp_one", Scope: Scope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"},
		Host: "svc.tcp.example.test", ExternalPort: externalPort, BackendPort: backendPort,
		AllowedCIDRs: []string{"127.0.0.1/32"}, DesiredEnabled: true, ApplyState: ApplyPending,
	})
	resolver := &runtimeBackendResolver{backends: []Backend{{Host: backendHost}}}
	controller := &Controller{Store: store, Resolver: resolver, BindHost: "127.0.0.1", ControllerID: "controller-a"}
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatalf("ReconcileAll: %v", err)
	}
	assertEndpointActive(t, store)
	assertEcho(t, externalPort, "first")

	if err := controller.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	value, _ := store.GetEndpoint(context.Background(), "tcp_one")
	if value.ApplyState != ApplyPending || value.ListenerBound {
		t.Fatalf("shutdown state = %#v, want pending/unbound", value)
	}

	restarted := &Controller{Store: store, Resolver: resolver, BindHost: "127.0.0.1", ControllerID: "controller-b"}
	if err := restarted.ReconcileAll(context.Background()); err != nil {
		t.Fatalf("restart ReconcileAll: %v", err)
	}
	assertEndpointActive(t, store)
	assertEcho(t, externalPort, "after-restart")

	if _, err := store.DisableEndpoint(context.Background(), "tcp_one"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ReconcileAll(context.Background()); err != nil {
		t.Fatalf("withdraw ReconcileAll: %v", err)
	}
	value, _ = store.GetEndpoint(context.Background(), "tcp_one")
	if value.ApplyState != ApplyWithdrawn || value.ListenerBound {
		t.Fatalf("withdrawn state = %#v", value)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(externalPort)), 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("withdrawn listener still accepted a connection")
	}
}

func TestControllerEmptyPeerPolicyIsPublic(t *testing.T) {
	backend := startEchoBackend(t)
	backendHost, backendPortText, _ := net.SplitHostPort(backend.Addr().String())
	backendPort, _ := strconv.Atoi(backendPortText)
	externalPort := reservePort(t)
	store := newRuntimeMemoryStore(Endpoint{
		ID: "tcp_public", Scope: Scope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"},
		Host: "svc.tcp.example.test", ExternalPort: externalPort, BackendPort: backendPort,
		AllowedCIDRs: []string{}, DesiredEnabled: true, ApplyState: ApplyPending,
	})
	controller := &Controller{
		Store: store, Resolver: &runtimeBackendResolver{backends: []Backend{{Host: backendHost}}},
		BindHost: "127.0.0.1", ControllerID: "controller",
	}
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatalf("ReconcileAll: %v", err)
	}
	defer controller.Shutdown(context.Background())
	assertEcho(t, externalPort, "public")
}

func TestControllerRecordsBindCollision(t *testing.T) {
	collision, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer collision.Close()
	_, portText, _ := net.SplitHostPort(collision.Addr().String())
	port, _ := strconv.Atoi(portText)
	store := newRuntimeMemoryStore(Endpoint{
		ID: "tcp_collision", Scope: Scope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"},
		Host: "svc.tcp.example.test", ExternalPort: port, BackendPort: 1,
		AllowedCIDRs: []string{"127.0.0.1/32"}, DesiredEnabled: true, ApplyState: ApplyPending,
	})
	controller := &Controller{
		Store: store, Resolver: &runtimeBackendResolver{backends: []Backend{{Host: "10.0.0.2"}}},
		BindHost: "127.0.0.1", ControllerID: "controller",
	}
	if err := controller.ReconcileAll(context.Background()); err == nil {
		t.Fatal("ReconcileAll collision error = nil")
	}
	value, _ := store.GetEndpoint(context.Background(), "tcp_collision")
	if value.ApplyState != ApplyFailed || value.ListenerBound || value.LastErrorCode != "tcp_listener_bind_failed" ||
		value.LastErrorDetail != "configured listener address is unavailable" ||
		strings.Contains(value.LastErrorDetail, portText) {
		t.Fatalf("collision observation = %#v", value)
	}
}

func TestControllerRejectsSourceOutsideAllowlist(t *testing.T) {
	backend := startEchoBackend(t)
	host, portText, _ := net.SplitHostPort(backend.Addr().String())
	port, _ := strconv.Atoi(portText)
	externalPort := reservePort(t)
	store := newRuntimeMemoryStore(Endpoint{
		ID: "tcp_deny", Scope: Scope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"},
		Host: "svc.tcp.example.test", ExternalPort: externalPort, BackendPort: port,
		AllowedCIDRs: []string{"10.0.0.0/8"}, DesiredEnabled: true, ApplyState: ApplyPending,
	})
	controller := &Controller{
		Store: store, Resolver: &runtimeBackendResolver{backends: []Backend{{Host: host}}},
		BindHost: "127.0.0.1", ControllerID: "controller",
	}
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer controller.Shutdown(context.Background())
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(externalPort)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := conn.Write([]byte("blocked")); err != nil {
		return
	}
	buffer := make([]byte, 7)
	if _, err := io.ReadFull(conn, buffer); err == nil {
		t.Fatalf("disallowed source received proxy response %q", buffer)
	}
}

func TestControllerAtomicallySwitchesToRedeployedBackendSet(t *testing.T) {
	externalPort := reservePort(t)
	store := newRuntimeMemoryStore(Endpoint{
		ID: "tcp_redeploy", Scope: Scope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"},
		Host: "svc.tcp.example.test", ExternalPort: externalPort, BackendPort: 7000,
		AllowedCIDRs: []string{"127.0.0.1/32"}, DesiredEnabled: true, ApplyState: ApplyPending,
	})
	resolver := &runtimeBackendResolver{backends: []Backend{{
		DeploymentID: "dep_old", InstanceID: "dep_old-r0", Host: "10.0.0.2",
	}}}
	dialer := &recordingEchoDialer{}
	controller := &Controller{
		Store: store, Resolver: resolver, BindHost: "127.0.0.1",
		ControllerID: "controller", DialContext: dialer.DialContext,
	}
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer controller.Shutdown(context.Background())
	dialer.Reset()
	assertEcho(t, externalPort, "old")
	if got := dialer.Addresses(); len(got) != 1 || got[0] != "10.0.0.2:7000" {
		t.Fatalf("old backend dials = %v", got)
	}

	resolver.Set([]Backend{{
		DeploymentID: "dep_new", InstanceID: "dep_new-r0", Host: "10.0.0.9",
	}})
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	dialer.Reset()
	assertEcho(t, externalPort, "new")
	if got := dialer.Addresses(); len(got) != 1 || got[0] != "10.0.0.9:7000" {
		t.Fatalf("new backend dials = %v", got)
	}
}

func TestControllerBalancesConnectionsAcrossReadyReplicas(t *testing.T) {
	externalPort := reservePort(t)
	store := newRuntimeMemoryStore(Endpoint{
		ID: "tcp_replicas", Scope: Scope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"},
		Host: "svc.tcp.example.test", ExternalPort: externalPort, BackendPort: 7000,
		AllowedCIDRs: []string{"127.0.0.1/32"}, DesiredEnabled: true, ApplyState: ApplyPending,
	})
	dialer := &recordingEchoDialer{}
	controller := &Controller{
		Store: store, Resolver: &runtimeBackendResolver{backends: []Backend{
			{InstanceID: "r0", Host: "10.0.0.2"}, {InstanceID: "r1", Host: "10.0.0.3"},
		}},
		BindHost: "127.0.0.1", ControllerID: "controller", DialContext: dialer.DialContext,
	}
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer controller.Shutdown(context.Background())
	dialer.Reset()
	assertEcho(t, externalPort, "one")
	assertEcho(t, externalPort, "two")
	got := dialer.Addresses()
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("replica dials = %v, want two distinct round-robin targets", got)
	}
}

func startEchoBackend(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	_ = listener.Close()
	port, _ := strconv.Atoi(portText)
	return port
}

func assertEndpointActive(t *testing.T, store *runtimeMemoryStore) {
	t.Helper()
	value, _ := store.GetEndpoint(context.Background(), "tcp_one")
	if value.ApplyState != ApplyActive || !value.ListenerBound || !value.BackendReachable {
		t.Fatalf("endpoint state = %#v, want active/bound/reachable", value)
	}
}

func assertEcho(t *testing.T, port int, message string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len(message))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != message {
		t.Fatalf("echo = %q, want %q", buffer, message)
	}
}

type runtimeMemoryStore struct {
	mu       sync.Mutex
	endpoint Endpoint
}

type runtimeBackendResolver struct {
	mu       sync.Mutex
	backends []Backend
	err      error
}

func (r *runtimeBackendResolver) ResolveBackends(context.Context, Scope) ([]Backend, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Backend(nil), r.backends...), r.err
}

func (r *runtimeBackendResolver) Set(backends []Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends = append([]Backend(nil), backends...)
}

type recordingEchoDialer struct {
	mu        sync.Mutex
	addresses []string
}

func (d *recordingEchoDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.mu.Lock()
	d.addresses = append(d.addresses, address)
	d.mu.Unlock()
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

func (d *recordingEchoDialer) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addresses = nil
}

func (d *recordingEchoDialer) Addresses() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addresses...)
}

func newRuntimeMemoryStore(endpoint Endpoint) *runtimeMemoryStore {
	return &runtimeMemoryStore{endpoint: endpoint}
}

func (s *runtimeMemoryStore) CreateEndpoint(context.Context, CreateEndpointInput) (Endpoint, error) {
	return Endpoint{}, errors.New("not implemented")
}

func (s *runtimeMemoryStore) GetEndpoint(_ context.Context, id string) (Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endpoint.ID != id {
		return Endpoint{}, ErrEndpointNotFound
	}
	return s.endpoint, nil
}

func (s *runtimeMemoryStore) ListEndpoints(context.Context, Scope) ([]Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []Endpoint{s.endpoint}, nil
}

func (s *runtimeMemoryStore) ListRuntimeEndpoints(context.Context) ([]Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.endpoint.DesiredEnabled && !s.endpoint.ListenerBound && s.endpoint.ApplyState == ApplyWithdrawn {
		return nil, nil
	}
	return []Endpoint{s.endpoint}, nil
}

func (s *runtimeMemoryStore) DisableEndpoint(_ context.Context, id string) (Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endpoint.ID != id {
		return Endpoint{}, ErrEndpointNotFound
	}
	s.endpoint.DesiredEnabled = false
	s.endpoint.ApplyState = ApplyPending
	return s.endpoint, nil
}

func (s *runtimeMemoryStore) RecordObservation(_ context.Context, id string, expectedDesired bool, observation Observation) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endpoint.ID != id || s.endpoint.DesiredEnabled != expectedDesired {
		return false, nil
	}
	s.endpoint.ApplyState = observation.State
	s.endpoint.ListenerBound = observation.ListenerBound
	s.endpoint.BackendReachable = observation.BackendReachable
	s.endpoint.ControllerID = observation.ControllerID
	s.endpoint.LastErrorCode = observation.ErrorCode
	s.endpoint.LastErrorDetail = observation.ErrorDetail
	at := observation.ObservedAt
	s.endpoint.ObservedAt = &at
	return true, nil
}
