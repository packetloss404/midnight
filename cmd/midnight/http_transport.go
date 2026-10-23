// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/version"
)

const (
	maxCLIResponseBytes      = 1 << 20
	maxCLIErrorResponseBytes = 64 << 10
)

// These are variables so transport contract tests can exercise timeout and
// idle behavior without making the test suite sleep for production durations.
var (
	cliRESTTimeout         = 15 * time.Second
	cliStreamHeaderTimeout = 15 * time.Second
	cliStreamIdleTimeout   = 90 * time.Second
)

var errCLIRedirect = errors.New("API request redirected; refusing to forward credentials")

// Compatibility sentinel retained for tests and callers that classified the
// former auth-only redirect error. Redirect refusal is now shared by every
// credential-bearing CLI request.
var errAuthRedirect = errCLIRedirect

// authHTTPClient remains a package hook for existing callers and tests. All
// REST executions defensively clone even an injected client and install the
// redirect policy, so an injection cannot accidentally weaken credential
// handling.
var authHTTPClient = newCLIHTTPClient(cliStreamHeaderTimeout)

type cliResponseTooLargeError struct {
	Method string
	Path   string
	Limit  int64
}

func (e *cliResponseTooLargeError) Error() string {
	return fmt.Sprintf("%s %s response exceeds %d bytes", safeHTTPText(e.Method), safeHTTPText(e.Path), e.Limit)
}

type cliStreamIdleError struct {
	Timeout time.Duration
}

func (e *cliStreamIdleError) Error() string {
	return fmt.Sprintf("stream was idle for %s", e.Timeout)
}

func newCLIHTTPClient(responseHeaderTimeout time.Duration) *http.Client {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{
		Transport:     transport,
		CheckRedirect: rejectCLIRedirect,
	}
}

func rejectCLIRedirect(*http.Request, []*http.Request) error {
	return errCLIRedirect
}

func cloneCredentialSafeClient(client *http.Client, streaming bool) *http.Client {
	if client == nil {
		client = authHTTPClient
	}
	clone := *client
	clone.CheckRedirect = rejectCLIRedirect
	if transport, ok := clone.Transport.(*http.Transport); ok {
		transport = transport.Clone()
		transport.ResponseHeaderTimeout = cliStreamHeaderTimeout
		clone.Transport = transport
	}
	if streaming {
		// A streaming request is bounded by its context, response-header
		// timeout, and read-idle timeout. It deliberately has no total HTTP
		// client timeout because a healthy SSE connection is long-lived.
		clone.Timeout = 0
	}
	return &clone
}

func cliUserAgent() string {
	value := strings.TrimSpace(version.Version)
	if value == "" {
		value = version.DevelopmentVersion
	}
	return "midnight-cli/" + value
}

func prepareCLIRequest(request *http.Request) error {
	if request == nil || request.URL == nil {
		return errors.New("API request is unavailable")
	}
	if err := validateCLIRequestURL(request.URL); err != nil {
		return err
	}
	request.Header.Set("User-Agent", cliUserAgent())
	return nil
}

func validateCLIRequestURL(target *url.URL) error {
	if target == nil || target.Host == "" || target.User != nil || target.Fragment != "" {
		return errors.New("invalid API request URL")
	}
	switch strings.ToLower(target.Scheme) {
	case "https":
		return nil
	case "http":
		// Plain HTTP is useful for local development but must not send a
		// credential to a non-loopback network peer.
		host := target.Hostname()
		ip := net.ParseIP(host)
		if ip != nil && ip.IsLoopback() {
			return nil
		}
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		return errors.New("API requests require HTTPS; plain HTTP is allowed only for loopback hosts")
	default:
		return errors.New("API request URL must use HTTPS or loopback HTTP")
	}
}

// executeCLIRequest executes a bounded, non-streaming request. A caller's
// earlier context deadline wins; otherwise the shared REST deadline is added.
// The response body is always consumed and closed before this function returns.
func executeCLIRequest(client *http.Client, request *http.Request, limit int64) (*http.Response, []byte, error) {
	if err := prepareCLIRequest(request); err != nil {
		return nil, nil, err
	}
	if limit <= 0 {
		limit = maxCLIResponseBytes
	}

	ctx := request.Context()
	cancel := func() {}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && cliRESTTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, cliRESTTimeout)
	}
	defer cancel()
	request = request.Clone(ctx)

	response, err := cloneCredentialSafeClient(client, false).Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return response, nil, err
	}
	defer response.Body.Close()
	body, err := readBoundedCLIResponse(request.Method, request.URL.RequestURI(), response.Body, limit)
	if err != nil {
		return response, nil, err
	}
	return response, body, nil
}

func readBoundedCLIResponse(method, path string, body io.Reader, limit int64) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > limit {
		return nil, &cliResponseTooLargeError{Method: method, Path: path, Limit: limit}
	}
	return value, nil
}

// executeCLIStream performs only the SSE handshake. It intentionally does not
// add the REST total timeout. The returned body enforces inactivity between
// reads and remains owned by the caller.
func executeCLIStream(client *http.Client, request *http.Request) (*http.Response, error) {
	if err := prepareCLIRequest(request); err != nil {
		return nil, err
	}
	response, err := cloneCredentialSafeClient(client, true).Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return response, err
	}
	if response.Body != nil && cliStreamIdleTimeout > 0 {
		response.Body = &idleReadCloser{body: response.Body, timeout: cliStreamIdleTimeout}
	}
	return response, nil
}

type idleReadCloser struct {
	body    io.ReadCloser
	timeout time.Duration
}

type idleReadResult struct {
	n   int
	err error
}

func (r *idleReadCloser) Read(buffer []byte) (int, error) {
	result := make(chan idleReadResult, 1)
	readBuffer := make([]byte, len(buffer))
	go func() {
		n, err := r.body.Read(readBuffer)
		result <- idleReadResult{n: n, err: err}
	}()
	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	select {
	case value := <-result:
		copy(buffer, readBuffer[:value.n])
		return value.n, value.err
	case <-timer.C:
		_ = r.body.Close()
		return 0, &cliStreamIdleError{Timeout: r.timeout}
	}
}

func (r *idleReadCloser) Close() error {
	return r.body.Close()
}

func safeHTTPText(value string) string {
	const maxBytes = 4 << 10
	value = terminalSafeText(value)
	if len(value) <= maxBytes {
		return value
	}
	return strings.TrimSpace(strings.ToValidUTF8(value[:maxBytes], "\uFFFD")) + "…"
}
