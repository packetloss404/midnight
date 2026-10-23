package readiness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultTimeout  = 300 * time.Second
	DefaultInterval = time.Second
	maxProbeBody    = 4 << 10
)

var (
	ErrProbeRequired = errors.New("readiness probe is required")
	ErrTimedOut      = errors.New("candidate did not become ready before timeout")
	ErrProbeInvalid  = errors.New("readiness probe configuration is invalid")
	ErrHTTPNotReady  = errors.New("HTTP readiness probe did not return status 200")
	// ErrFatal marks a probe result that further waiting cannot change. Wait
	// returns immediately when a probe reports it.
	ErrFatal = errors.New("readiness probe reported a terminal condition")
)

// FatalError wraps a probe error that waiting cannot resolve — the workload
// has already exited, or is restart-looping, so retrying until the timeout
// only delays a failure the caller can already report.
type FatalError struct{ Err error }

// Fatal marks err as terminal for Wait. A nil err is returned unchanged so
// callers can wrap a probe result unconditionally.
func Fatal(err error) error {
	if err == nil {
		return nil
	}
	return &FatalError{Err: err}
}

func (e *FatalError) Error() string {
	if e == nil || e.Err == nil {
		return ErrFatal.Error()
	}
	return e.Err.Error()
}

func (e *FatalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *FatalError) Is(target error) bool {
	return target == ErrFatal
}

type Probe interface {
	CheckReady(ctx context.Context) error
}

type ProbeFunc func(ctx context.Context) error

func (f ProbeFunc) CheckReady(ctx context.Context) error {
	return f(ctx)
}

// HTTPProbe checks one internal workload URL. Midnight intentionally accepts
// only an exact 200 response: redirects, authentication challenges, and other
// 2xx statuses do not prove that the configured application endpoint is ready.
// The default client refuses redirects so a candidate cannot make readiness
// depend on an unrelated host.
type HTTPProbe struct {
	URL    string
	Client *http.Client
}

func (probe HTTPProbe) CheckReady(ctx context.Context) error {
	target, err := validateHTTPProbeURL(probe.URL)
	if err != nil {
		return err
	}
	client := probe.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultInterval}
	} else {
		cloned := *client
		client = &cloned
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProbeInvalid, err)
	}
	request.Header.Set("User-Agent", "midnight-readiness/1")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxProbeBody))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: got %d", ErrHTTPNotReady, response.StatusCode)
	}
	return nil
}

func validateHTTPProbeURL(raw string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid HTTP URL", ErrProbeInvalid)
	}
	if target.Scheme != "http" || target.Hostname() == "" || target.User != nil ||
		target.Fragment != "" || target.RawQuery != "" {
		return nil, fmt.Errorf("%w: HTTP probe requires an http URL without credentials, query, or fragment", ErrProbeInvalid)
	}
	if target.Port() == "" {
		return nil, fmt.Errorf("%w: HTTP probe requires an explicit port", ErrProbeInvalid)
	}
	port, err := strconv.Atoi(target.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("%w: HTTP probe port is out of range", ErrProbeInvalid)
	}
	if target.Path == "" {
		target.Path = "/"
	}
	return target, nil
}

type contextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// TCPProbe proves that a workload is accepting TCP connections on its
// selected internal address. A successful dial is sufficient; the connection
// is closed immediately and no application bytes are sent.
type TCPProbe struct {
	Address string
	Dialer  contextDialer
}

func (probe TCPProbe) CheckReady(ctx context.Context) error {
	host, rawPort, err := net.SplitHostPort(strings.TrimSpace(probe.Address))
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("%w: TCP probe requires host:port", ErrProbeInvalid)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%w: TCP probe port is out of range", ErrProbeInvalid)
	}
	dialer := probe.Dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: DefaultInterval}
	}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, rawPort))
	if err != nil {
		return err
	}
	return connection.Close()
}

type Options struct {
	Timeout  time.Duration
	Interval time.Duration
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
}

type TimeoutError struct {
	Timeout   time.Duration
	Attempts  int
	LastError error
}

func (e *TimeoutError) Error() string {
	if e.LastError == nil {
		return fmt.Sprintf("%s after %s", ErrTimedOut, e.Timeout)
	}
	return fmt.Sprintf("%s after %s: last probe error: %v", ErrTimedOut, e.Timeout, e.LastError)
}

func (e *TimeoutError) Is(target error) bool {
	return target == ErrTimedOut
}

func (e *TimeoutError) Unwrap() error {
	return e.LastError
}

func Wait(ctx context.Context, probe Probe, options Options) error {
	if probe == nil {
		return ErrProbeRequired
	}

	options = normalizeOptions(options)
	deadline := options.Now().Add(options.Timeout)
	attempts := 0
	var lastErr error

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		attempts++
		lastErr = probe.CheckReady(ctx)
		if lastErr == nil {
			return nil
		}
		// A terminal probe result ends the wait now. Retrying a workload
		// that has already exited only spends the caller's timeout.
		if errors.Is(lastErr, ErrFatal) {
			return lastErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		remaining := deadline.Sub(options.Now())
		if remaining <= 0 {
			return &TimeoutError{
				Timeout:   options.Timeout,
				Attempts:  attempts,
				LastError: lastErr,
			}
		}

		sleepFor := options.Interval
		if sleepFor > remaining {
			sleepFor = remaining
		}
		if err := options.Sleep(ctx, sleepFor); err != nil {
			return err
		}
	}
}

func normalizeOptions(options Options) Options {
	if options.Timeout <= 0 {
		options.Timeout = DefaultTimeout
	}
	if options.Interval <= 0 {
		options.Interval = DefaultInterval
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Sleep == nil {
		options.Sleep = realSleep
	}
	return options
}

func realSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
