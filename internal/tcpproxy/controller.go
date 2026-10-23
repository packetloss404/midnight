package tcpproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const DefaultReconcileInterval = 10 * time.Second

type DialContextFunc func(context.Context, string, string) (net.Conn, error)
type ListenFunc func(string, string) (net.Listener, error)

type Controller struct {
	Store        Store
	Resolver     BackendResolver
	Enroller     NetworkEnroller
	BindHost     string
	ControllerID string
	DialTimeout  time.Duration
	DialContext  DialContextFunc
	Listen       ListenFunc
	ObservePeer  func(net.Addr)
	Now          func() time.Time

	mu        sync.Mutex
	listeners map[string]*endpointListener
}

type endpointListener struct {
	endpoint Endpoint
	listener net.Listener
	cancel   context.CancelFunc
	backends atomic.Value
	next     atomic.Uint64
}

func (c *Controller) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Controller) ReconcileAll(ctx context.Context) error {
	if c == nil || c.Store == nil || c.Resolver == nil {
		return errors.New("tcp proxy controller store and backend resolver are required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listeners == nil {
		c.listeners = make(map[string]*endpointListener)
	}
	values, err := c.Store.ListRuntimeEndpoints(ctx)
	if err != nil {
		return err
	}
	desiredScopes := make([]Scope, 0, len(values))
	for _, endpoint := range values {
		if endpoint.DesiredEnabled {
			desiredScopes = append(desiredScopes, endpoint.Scope)
		}
	}
	if c.Enroller != nil {
		if enrollErr := c.Enroller.ReconcileNetworks(ctx, desiredScopes); enrollErr != nil {
			var errs []error
			for _, endpoint := range values {
				if !endpoint.DesiredEnabled {
					continue
				}
				if handle := c.listeners[endpoint.ID]; handle != nil {
					handle.cancel()
					_ = handle.listener.Close()
					delete(c.listeners, endpoint.ID)
				}
				_, recordErr := c.Store.RecordObservation(ctx, endpoint.ID, true, Observation{
					State: ApplyFailed, ControllerID: c.ControllerID,
					ErrorCode:   "tcp_network_enrollment_failed",
					ErrorDetail: "trusted proxy could not join the scoped environment network", ObservedAt: c.now(),
				})
				errs = append(errs, recordErr)
			}
			return errors.Join(append(errs, enrollErr)...)
		}
	}
	seen := make(map[string]bool, len(values))
	var errs []error
	for _, endpoint := range values {
		seen[endpoint.ID] = true
		if !endpoint.DesiredEnabled {
			if handle := c.listeners[endpoint.ID]; handle != nil {
				handle.cancel()
				_ = handle.listener.Close()
				delete(c.listeners, endpoint.ID)
			}
			_, recordErr := c.Store.RecordObservation(ctx, endpoint.ID, false, Observation{
				State: ApplyWithdrawn, ControllerID: c.ControllerID, ObservedAt: c.now(),
			})
			if recordErr != nil {
				errs = append(errs, recordErr)
			}
			continue
		}
		backends, resolveErr := c.Resolver.ResolveBackends(ctx, endpoint.Scope)
		if resolveErr != nil {
			if handle := c.listeners[endpoint.ID]; handle != nil {
				handle.backends.Store([]Backend(nil))
			}
			_, recordErr := c.Store.RecordObservation(ctx, endpoint.ID, true, Observation{
				State: ApplyFailed, ListenerBound: c.listeners[endpoint.ID] != nil,
				ControllerID: c.ControllerID, ErrorCode: "tcp_backend_resolution_failed",
				ErrorDetail: "no ready active deployment backend is available", ObservedAt: c.now(),
			})
			errs = append(errs, errors.Join(fmt.Errorf("resolve TCP endpoint %s backends: %w", endpoint.ID, resolveErr), recordErr))
			continue
		}
		if handle := c.listeners[endpoint.ID]; handle != nil {
			if sameRuntimeEndpoint(handle.endpoint, endpoint) {
				if err := c.observeBackends(ctx, handle, backends, true); err != nil {
					errs = append(errs, err)
				}
				continue
			}
			handle.cancel()
			_ = handle.listener.Close()
			delete(c.listeners, endpoint.ID)
		}
		if err := c.bind(ctx, endpoint, backends); err != nil {
			errs = append(errs, err)
		}
	}
	for id, handle := range c.listeners {
		if seen[id] {
			continue
		}
		handle.cancel()
		_ = handle.listener.Close()
		delete(c.listeners, id)
	}
	return errors.Join(errs...)
}

func (c *Controller) bind(ctx context.Context, endpoint Endpoint, backends []Backend) error {
	listen := c.Listen
	if listen == nil {
		listen = net.Listen
	}
	address := net.JoinHostPort(strings.TrimSpace(c.BindHost), strconv.Itoa(endpoint.ExternalPort))
	listener, err := listen("tcp", address)
	if err != nil {
		_, recordErr := c.Store.RecordObservation(ctx, endpoint.ID, true, Observation{
			State: ApplyFailed, ControllerID: c.ControllerID,
			ErrorCode: "tcp_listener_bind_failed", ErrorDetail: "configured listener address is unavailable", ObservedAt: c.now(),
		})
		return errors.Join(fmt.Errorf("bind TCP endpoint %s: %w", endpoint.ID, err), recordErr)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	handle := &endpointListener{endpoint: endpoint, listener: listener, cancel: cancel}
	handle.backends.Store([]Backend(nil))
	c.listeners[endpoint.ID] = handle
	go c.accept(runCtx, handle)
	return c.observeBackends(ctx, handle, backends, true)
}

func (c *Controller) observeBackends(ctx context.Context, handle *endpointListener, backends []Backend, listenerBound bool) error {
	timeout := c.DialTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	reachable := make([]Backend, 0, len(backends))
	for _, backend := range backends {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		conn, err := c.dial(probeCtx, handle.endpoint, backend)
		cancel()
		if err != nil {
			continue
		}
		_ = conn.Close()
		reachable = append(reachable, backend)
	}
	handle.backends.Store(reachable)
	if len(reachable) == 0 {
		_, recordErr := c.Store.RecordObservation(ctx, handle.endpoint.ID, true, Observation{
			State: ApplyFailed, ListenerBound: listenerBound, ControllerID: c.ControllerID,
			ErrorCode: "tcp_backend_unreachable", ErrorDetail: "active deployment backend did not accept a TCP connection", ObservedAt: c.now(),
		})
		return errors.Join(fmt.Errorf("probe TCP endpoint %s backends: none reachable", handle.endpoint.ID), recordErr)
	}
	applied, err := c.Store.RecordObservation(ctx, handle.endpoint.ID, true, Observation{
		State: ApplyActive, ListenerBound: listenerBound, BackendReachable: true,
		ControllerID: c.ControllerID, ObservedAt: c.now(),
	})
	if err != nil {
		return err
	}
	if !applied {
		handle.cancel()
		_ = handle.listener.Close()
		delete(c.listeners, handle.endpoint.ID)
	}
	return nil
}

func (c *Controller) dial(ctx context.Context, endpoint Endpoint, backend Backend) (net.Conn, error) {
	address := net.JoinHostPort(backend.Host, strconv.Itoa(endpoint.BackendPort))
	if c.DialContext != nil {
		return c.DialContext(ctx, "tcp", address)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", address)
}

func (c *Controller) accept(ctx context.Context, handle *endpointListener) {
	allowed := allowedPrefixes(handle.endpoint.AllowedCIDRs)
	for {
		conn, err := handle.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go c.proxyConnection(ctx, handle, allowed, conn)
	}
}

func (c *Controller) proxyConnection(ctx context.Context, handle *endpointListener, allowed []netip.Prefix, inbound net.Conn) {
	defer inbound.Close()
	if c.ObservePeer != nil {
		c.ObservePeer(inbound.RemoteAddr())
	}
	if !sourceAllowed(inbound.RemoteAddr(), allowed) {
		return
	}
	endpoint := handle.endpoint
	backends, _ := handle.backends.Load().([]Backend)
	outbound, err := c.dialAny(ctx, endpoint, backends, handle.next.Add(1)-1)
	if err != nil {
		return
	}
	defer outbound.Close()
	done := make(chan struct{}, 2)
	copyStream := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyStream(outbound, inbound)
	go copyStream(inbound, outbound)
	select {
	case <-ctx.Done():
	case <-done:
	}
}

func (c *Controller) dialAny(ctx context.Context, endpoint Endpoint, backends []Backend, sequence uint64) (net.Conn, error) {
	if len(backends) == 0 {
		return nil, ErrBackendUnavailable
	}
	start := int(sequence % uint64(len(backends)))
	var errs []error
	for offset := range backends {
		backend := backends[(start+offset)%len(backends)]
		conn, err := c.dial(ctx, endpoint, backend)
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

func (c *Controller) Shutdown(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for id, handle := range c.listeners {
		handle.cancel()
		if err := handle.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
		_, err := c.Store.RecordObservation(ctx, id, true, Observation{
			State: ApplyPending, ControllerID: c.ControllerID,
			ErrorCode: "tcp_controller_stopped", ErrorDetail: "listener closed during controller shutdown",
			ObservedAt: c.now(),
		})
		if err != nil {
			errs = append(errs, err)
		}
		delete(c.listeners, id)
	}
	if c.Enroller != nil {
		if err := c.Enroller.ReconcileNetworks(ctx, nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func RunController(ctx context.Context, controller *Controller, interval time.Duration, onError func(error)) {
	if controller == nil {
		if onError != nil {
			onError(errors.New("tcp proxy controller is required"))
		}
		return
	}
	if interval <= 0 {
		interval = DefaultReconcileInterval
	}
	reconcile := func() {
		if err := controller.ReconcileAll(ctx); err != nil && !errors.Is(err, context.Canceled) && onError != nil {
			onError(err)
		}
	}
	reconcile()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = controller.Shutdown(shutdownCtx)
			cancel()
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func sameRuntimeEndpoint(left, right Endpoint) bool {
	if left.ExternalPort != right.ExternalPort ||
		left.BackendPort != right.BackendPort || len(left.AllowedCIDRs) != len(right.AllowedCIDRs) {
		return false
	}
	for i := range left.AllowedCIDRs {
		if left.AllowedCIDRs[i] != right.AllowedCIDRs[i] {
			return false
		}
	}
	return true
}

func allowedPrefixes(values []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		if prefix, err := netip.ParsePrefix(value); err == nil {
			out = append(out, prefix.Masked())
		}
	}
	return out
}

func sourceAllowed(address net.Addr, allowed []netip.Prefix) bool {
	// No restriction is the explicit public TCP behavior. When restrictions
	// exist they apply to the peer address observed at this listener; a Docker
	// published port may present its bridge gateway rather than the end client.
	if len(allowed) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range allowed {
		if prefix.Contains(ip) || prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}
