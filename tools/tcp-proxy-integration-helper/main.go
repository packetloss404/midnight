//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
// This command is built only by the real-Docker integration test. It places
// the production controller inside the same container/network namespace used
// by the deploy worker without starting the worker's unrelated job loops.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/tcpproxy"
)

func main() {
	dsn := strings.TrimSpace(os.Getenv("MIDNIGHT_DB_DSN"))
	containerName := strings.TrimSpace(os.Getenv("MIDNIGHT_TCP_PROXY_CONTAINER")) // not-a-failure-code
	if dsn == "" || containerName == "" {
		fmt.Fprintln(os.Stderr, "MIDNIGHT_DB_DSN and MIDNIGHT_TCP_PROXY_CONTAINER are required") // not-a-failure-code
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 2, ConnectTimeout: 10 * time.Second})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open postgres: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ping postgres: %v\n", err)
		os.Exit(1)
	}
	dockerClient, err := tcpproxy.NewDockerNetworkClient(strings.TrimSpace(os.Getenv("DOCKER_HOST")))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open Docker client: %v\n", err)
		os.Exit(1)
	}
	defer dockerClient.Close()
	controller := &tcpproxy.Controller{
		Store:        tcpproxy.NewPostgresStore(pool),
		Resolver:     tcpproxy.PostgresBackendResolver{Pool: pool},
		Enroller:     tcpproxy.DockerNetworkEnroller{Client: dockerClient, Container: containerName},
		BindHost:     "0.0.0.0",
		ControllerID: "docker-integration-helper",
		ObservePeer: func(address net.Addr) {
			fmt.Printf("tcp proxy observed peer=%s\n", address.String())
		},
	}
	fmt.Println("tcp proxy integration helper started")
	tcpproxy.RunController(ctx, controller, 100*time.Millisecond, func(err error) {
		fmt.Fprintf(os.Stderr, "tcp proxy reconcile: %v\n", err)
	})
}
