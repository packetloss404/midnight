package readiness

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.now = c.now.Add(d)
	return ctx.Err()
}

func TestWaitAllowsSlowStartCandidate(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	attempts := 0
	probe := ProbeFunc(func(context.Context) error {
		attempts++
		if attempts < 4 {
			return errors.New("not ready yet")
		}
		return nil
	})

	err := Wait(context.Background(), probe, Options{
		Timeout:  10 * time.Second,
		Interval: time.Second,
		Now:      clock.Now,
		Sleep:    clock.Sleep,
	})
	if err != nil {
		t.Fatalf("Wait returned %v", err)
	}
	if attempts != 4 {
		t.Fatalf("Wait attempted %d probes, want 4", attempts)
	}
}

func TestWaitTimesOutNeverReadyCandidate(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	probeErr := errors.New("connection refused")
	probe := ProbeFunc(func(context.Context) error {
		return probeErr
	})

	err := Wait(context.Background(), probe, Options{
		Timeout:  3 * time.Second,
		Interval: time.Second,
		Now:      clock.Now,
		Sleep:    clock.Sleep,
	})
	if !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Wait returned %v, want ErrTimedOut", err)
	}

	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("Wait returned %T, want *TimeoutError", err)
	}
	if timeoutErr.Attempts != 4 {
		t.Fatalf("timeout attempts = %d, want 4", timeoutErr.Attempts)
	}
	if timeoutErr.LastError != probeErr {
		t.Fatalf("timeout last error = %v, want %v", timeoutErr.LastError, probeErr)
	}
}

func TestWaitReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Wait(ctx, ProbeFunc(func(context.Context) error {
		t.Fatal("probe should not run for canceled context")
		return nil
	}), Options{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait returned %v, want context.Canceled", err)
	}
}

func TestDefaultTimeoutMatchesDeploymentPolicy(t *testing.T) {
	if DefaultTimeout != 300*time.Second {
		t.Fatalf("DefaultTimeout = %s, want 300s", DefaultTimeout)
	}
}

func TestHTTPProbeRequiresExactStatus200AndRefusesRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ready":
			w.WriteHeader(http.StatusOK)
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
		case "/redirect":
			http.Redirect(w, r, "/ready", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	if err := (HTTPProbe{URL: "http://" + host + "/ready"}).CheckReady(context.Background()); err != nil {
		t.Fatalf("ready probe: %v", err)
	}
	for _, path := range []string{"/empty", "/redirect"} {
		err := (HTTPProbe{URL: "http://" + host + path}).CheckReady(context.Background())
		if !errors.Is(err, ErrHTTPNotReady) {
			t.Fatalf("%s error = %v, want ErrHTTPNotReady", path, err)
		}
	}
}

func TestHTTPProbeRejectsUnsafeOrAmbiguousTargets(t *testing.T) {
	for _, target := range []string{
		"",
		"https://127.0.0.1:8443/ready",
		"http://user:pass@127.0.0.1:8080/ready",
		"http://127.0.0.1/ready",
		"http://127.0.0.1:8080/ready?token=value",
	} {
		err := (HTTPProbe{URL: target}).CheckReady(context.Background())
		if !errors.Is(err, ErrProbeInvalid) {
			t.Fatalf("target %q error = %v, want ErrProbeInvalid", target, err)
		}
	}
}

func TestTCPProbeRequiresSuccessfulConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			acceptErr = connection.Close()
		}
		accepted <- acceptErr
	}()

	if err := (TCPProbe{Address: listener.Addr().String()}).CheckReady(context.Background()); err != nil {
		t.Fatalf("TCP probe: %v", err)
	}
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
}

func TestTCPProbeRejectsInvalidAddress(t *testing.T) {
	for _, address := range []string{"", "localhost", "localhost:0", "localhost:70000"} {
		err := (TCPProbe{Address: address}).CheckReady(context.Background())
		if !errors.Is(err, ErrProbeInvalid) {
			t.Fatalf("address %q error = %v, want ErrProbeInvalid", address, err)
		}
	}
}

type errorDialer struct{}

func (errorDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, fmt.Errorf("dial refused")
}

func TestTCPProbeSurfacesDialFailure(t *testing.T) {
	err := (TCPProbe{Address: "127.0.0.1:8080", Dialer: errorDialer{}}).CheckReady(context.Background())
	if err == nil || errors.Is(err, ErrProbeInvalid) {
		t.Fatalf("error = %v, want runtime dial failure", err)
	}
}

func TestWaitStopsImmediatelyOnFatalProbeResult(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	attempts := 0
	cause := errors.New("container exited with code 1")
	probe := ProbeFunc(func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("not ready yet")
		}
		return Fatal(cause)
	})

	start := clock.now
	err := Wait(context.Background(), probe, Options{
		Timeout:  5 * time.Minute,
		Interval: time.Second,
		Now:      clock.Now,
		Sleep:    clock.Sleep,
	})
	if !errors.Is(err, ErrFatal) {
		t.Fatalf("Wait returned %v, want a fatal probe result", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Wait discarded the probe cause: %v", err)
	}
	if errors.Is(err, ErrTimedOut) {
		t.Fatal("a fatal probe result must not be reported as a timeout")
	}
	if attempts != 3 {
		t.Fatalf("Wait attempted %d probes after a fatal result, want 3", attempts)
	}
	// The whole point is not spending the readiness budget on a dead
	// candidate: only the two retry sleeps may have elapsed.
	if elapsed := clock.now.Sub(start); elapsed != 2*time.Second {
		t.Fatalf("Wait burned %s before failing fast, want 2s", elapsed)
	}
}

func TestFatalIgnoresNilError(t *testing.T) {
	if err := Fatal(nil); err != nil {
		t.Fatalf("Fatal(nil) = %v, want nil so callers can wrap unconditionally", err)
	}
}

func TestFatalErrorReportsWrappedMessage(t *testing.T) {
	err := Fatal(errors.New("container stopped"))
	if err.Error() != "container stopped" {
		t.Fatalf("Fatal error message = %q, want the wrapped cause", err.Error())
	}
}
