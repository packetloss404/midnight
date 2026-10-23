// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCLITransportRefusesRedirectWithoutReplayingCredentials(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, source.URL+"/mutate", strings.NewReader(`{"secret":"value"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer secret-token")
	_, _, err = executeCLIRequest(source.Client(), request, maxCLIResponseBytes)
	if !errors.Is(err, errCLIRedirect) {
		t.Fatalf("executeCLIRequest error = %v, want redirect refusal", err)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, want 0", got)
	}
}

func TestCLITransportRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 33))
	}))
	defer server.Close()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/large", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = executeCLIRequest(server.Client(), request, 32)
	var tooLarge *cliResponseTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Limit != 32 {
		t.Fatalf("executeCLIRequest error = %#v, want typed 32-byte limit", err)
	}
}

func TestCLITransportAppliesRESTTimeout(t *testing.T) {
	original := cliRESTTimeout
	cliRESTTimeout = 20 * time.Millisecond
	t.Cleanup(func() { cliRESTTimeout = original })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = executeCLIRequest(server.Client(), request, maxCLIResponseBytes)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("executeCLIRequest error = %v, want deadline exceeded", err)
	}
}

func TestCLIStreamSurvivesBeyondRESTTimeout(t *testing.T) {
	originalREST, originalIdle := cliRESTTimeout, cliStreamIdleTimeout
	cliRESTTimeout = 20 * time.Millisecond
	cliStreamIdleTimeout = 500 * time.Millisecond
	t.Cleanup(func() {
		cliRESTTimeout = originalREST
		cliStreamIdleTimeout = originalIdle
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(75 * time.Millisecond)
		_, _ = io.WriteString(w, "event: ready\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := executeCLIStream(server.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "event: ready\n" {
		t.Fatalf("stream line = %q, error = %v", line, err)
	}
}

func TestCLIStreamAppliesResponseHeaderTimeout(t *testing.T) {
	original := cliStreamHeaderTimeout
	cliStreamHeaderTimeout = 20 * time.Millisecond
	t.Cleanup(func() { cliStreamHeaderTimeout = original })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "event: late\n\n")
	}))
	defer server.Close()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeCLIStream(server.Client(), request); err == nil {
		t.Fatal("executeCLIStream succeeded, want response-header timeout")
	}
}

func TestCLITransportSetsVersionedUserAgent(t *testing.T) {
	userAgent := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent <- r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/identity", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeCLIRequest(server.Client(), request, maxCLIResponseBytes); err != nil {
		t.Fatal(err)
	}
	if got := <-userAgent; got != cliUserAgent() || !strings.HasPrefix(got, "midnight-cli/") {
		t.Fatalf("User-Agent = %q, want %q", got, cliUserAgent())
	}
}

func TestCLIAPIErrorIsBoundedAndTerminalSafe(t *testing.T) {
	body := []byte(`{"error":{"code":"BAD\u001b[31m","message":"visible\u001b]52;c;hidden\u0007` + strings.Repeat("x", 10<<10) + `"}}`)
	err := apiStatusError(http.MethodPost, "/unsafe\x1b[31m", "400 Bad Request\x1b]0;title\x07", body)
	message := err.Error()
	if strings.ContainsRune(message, '\x1b') || strings.Contains(message, "hidden") {
		t.Fatalf("unsafe API error = %q", message)
	}
	if len(message) > (8 << 10) {
		t.Fatalf("API error length = %d, want bounded", len(message))
	}
}
