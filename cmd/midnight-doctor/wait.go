package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultWaitTimeout = 30 * time.Second
	maximumWaitTimeout = 2 * time.Minute
	waitPollInterval   = 200 * time.Millisecond
)

var waitDialContext = (&net.Dialer{Timeout: time.Second}).DialContext

func runWait(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("midnight-doctor wait", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tcpTarget := fs.String("tcp", "", "wait for a TCP listener (host:port)")
	unixTarget := fs.String("unix", "", "wait for an absolute Unix socket path or unix:// URL")
	timeout := fs.Duration("timeout", defaultWaitTimeout, "maximum readiness wait")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 3}
	}
	if fs.NArg() != 0 || (*tcpTarget == "") == (*unixTarget == "") {
		fmt.Fprintln(stderr, "midnight-doctor wait: configure exactly one of --tcp or --unix")
		return &exitError{code: 3}
	}
	if *timeout <= 0 || *timeout > maximumWaitTimeout {
		fmt.Fprintf(stderr, "midnight-doctor wait: --timeout must be greater than zero and at most %s\n", maximumWaitTimeout)
		return &exitError{code: 3}
	}

	network := "unix"
	target := strings.TrimPrefix(strings.TrimSpace(*unixTarget), "unix://")
	if *tcpTarget != "" {
		network = "tcp"
		var err error
		target, err = normalizeWaitTCPTarget(*tcpTarget)
		if err != nil {
			fmt.Fprintln(stderr, "midnight-doctor wait: invalid TCP target")
			return &exitError{code: 3}
		}
	} else if !filepath.IsAbs(target) {
		fmt.Fprintln(stderr, "midnight-doctor wait: Unix socket path must be absolute")
		return &exitError{code: 3}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()
	for {
		conn, err := waitDialContext(ctx, network, target)
		if err == nil {
			_ = conn.Close()
			fmt.Fprintf(stdout, "%s listener ready\n", network)
			return nil
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(stderr, "midnight-doctor wait: %s listener did not become ready before timeout\n", network)
			return &exitError{code: 2}
		case <-ticker.C:
		}
	}
}

func normalizeWaitTCPTarget(value string) (string, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid port")
	}
	switch strings.TrimSpace(host) {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}
