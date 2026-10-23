package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRunWaitSucceedsAndNormalizesWildcardTCP(t *testing.T) {
	original := waitDialContext
	t.Cleanup(func() { waitDialContext = original })
	waitDialContext = func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "127.0.0.1:8080" {
			t.Fatalf("dial = %s %s", network, address)
		}
		return waitTestNetConn{}, nil
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"wait", "--tcp", "0.0.0.0:8080", "--timeout", "1s"}, &stdout, &stderr); err != nil {
		t.Fatalf("run wait error = %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "tcp listener ready") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunWaitRejectsUnsafeInputs(t *testing.T) {
	for _, args := range [][]string{
		{"wait"},
		{"wait", "--tcp", "127.0.0.1:8080", "--unix", "/run/test.sock"},
		{"wait", "--tcp", "not-an-address"},
		{"wait", "--unix", "relative.sock"},
		{"wait", "--unix", "/run/test.sock", "--timeout", "3m"},
	} {
		var stdout, stderr bytes.Buffer
		err := run(args, &stdout, &stderr)
		if exit, ok := err.(*exitError); !ok || exit.code != 3 {
			t.Fatalf("run(%v) error = %v, want exit 3", args, err)
		}
	}
}

func TestRunWaitTimesOutWithoutDisclosingTarget(t *testing.T) {
	original := waitDialContext
	t.Cleanup(func() { waitDialContext = original })
	waitDialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("SECRET-SENTINEL")
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{"wait", "--unix", "/run/midnight/test.sock", "--timeout", "1ms"}, &stdout, &stderr)
	if exit, ok := err.(*exitError); !ok || exit.code != 2 {
		t.Fatalf("run wait error = %v, want exit 2", err)
	}
	if strings.Contains(stderr.String(), "SECRET-SENTINEL") {
		t.Fatalf("stderr disclosed dial error: %q", stderr.String())
	}
}

type waitTestNetConn struct{}

func (waitTestNetConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (waitTestNetConn) Write(p []byte) (int, error)      { return len(p), nil }
func (waitTestNetConn) Close() error                     { return nil }
func (waitTestNetConn) LocalAddr() net.Addr              { return waitTestAddr("local") }
func (waitTestNetConn) RemoteAddr() net.Addr             { return waitTestAddr("remote") }
func (waitTestNetConn) SetDeadline(time.Time) error      { return nil }
func (waitTestNetConn) SetReadDeadline(time.Time) error  { return nil }
func (waitTestNetConn) SetWriteDeadline(time.Time) error { return nil }

type waitTestAddr string

func (a waitTestAddr) Network() string { return string(a) }
func (a waitTestAddr) String() string  { return string(a) }
