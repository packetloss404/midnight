//go:build integration

package router

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTraefikDynamicPublicationServesYAML writes a route group artifact to
// a temporary directory and (when MIDNIGHT_TRAEFIK_BIN is set) spawns
// Traefik with isolated loopback entrypoints. Its LiveHTTP subtest verifies
// generated-host routing and a watched backend replacement with real requests.
//
// To run locally:
//
//	# 1. Install pinned Traefik:
//	#       curl -L https://github.com/traefik/traefik/releases/download/v3.1.5/traefik_v3.1.5_linux_amd64.tar.gz | tar -xz
//	# 2. Run the integration suite:
//	#       MIDNIGHT_TRAEFIK_BIN=$(pwd)/traefik \
//	#       go test -tags=integration ./internal/router/...
//
// When MIDNIGHT_TRAEFIK_BIN is not set, the test still validates the
// directory contract (atomic write, route artifact present), and LiveHTTP is
// explicitly skipped. This test does not establish public DNS or TLS evidence.
func TestTraefikDynamicPublicationServesYAML(t *testing.T) {
	tmp := t.TempDir()
	pubdir, err := NewTraefikDynamicPublicationDirectory(tmp)
	if err != nil {
		t.Fatal(err)
	}
	routes := []RouteInput{{
		Name:       "demo",
		Host:       "demo.example.test",
		ServiceURL: "http://127.0.0.1:65535", // unreachable upstream is fine; we only assert routing
	}}
	evidence, err := pubdir.PublishRouteGroup("demo-group", routes, "")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if evidence.Status != RouteArtifactPublicationApplyStatusReady {
		t.Fatalf("status = %s, want ready", evidence.Status)
	}
	stem, err := SanitizeRouteFilename("demo")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename: %v", err)
	}
	written := filepath.Join(tmp, "demo-group."+stem+".traefik.dynamic.yaml")
	body, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("read written: %v", err)
	}
	if !strings.Contains(string(body), "demo.example.test") {
		t.Fatalf("artifact missing host: %s", body)
	}

	t.Run("LiveHTTP", func(t *testing.T) {
		bin := os.Getenv("MIDNIGHT_TRAEFIK_BIN")
		if bin == "" {
			t.Skip("MIDNIGHT_TRAEFIK_BIN is required for real HTTP ingress")
		}
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("configured traefik binary: %v", err)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		_ = listener.Close()
		logs, err := os.Create(filepath.Join(t.TempDir(), "traefik.log"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = logs.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		cmd := exec.CommandContext(ctx, bin,
			"--global.checknewversion=false", "--global.sendanonymoususage=false",
			"--providers.file.directory="+tmp, "--providers.providersthrottleduration=100ms",
			"--entrypoints.web.address="+address, "--entrypoints.websecure.address=127.0.0.1:0",
		)
		cmd.Stdout, cmd.Stderr = logs, logs
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatalf("traefik start: %v", err)
		}
		done := make(chan struct{})
		var processError error
		go func() { processError = cmd.Wait(); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		client := &http.Client{Timeout: 500 * time.Millisecond}
		probe := func(host, wantBody string, wantStatus int) {
			t.Helper()
			deadline := time.Now().Add(15 * time.Second)
			var last string
			for time.Now().Before(deadline) {
				select {
				case <-done:
					logBody, _ := os.ReadFile(logs.Name())
					t.Fatalf("traefik exited: %v: %s", processError, logBody)
				default:
				}
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/proof", nil)
				request.Host = host
				response, err := client.Do(request)
				if err == nil {
					body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
					_ = response.Body.Close()
					if readErr == nil && response.StatusCode == wantStatus && (wantBody == "" || string(body) == wantBody) {
						return
					}
					last = fmt.Sprintf("status=%d body=%q read=%v", response.StatusCode, body, readErr)
				} else {
					last = err.Error()
				}
				time.Sleep(50 * time.Millisecond)
			}
			logBody, _ := os.ReadFile(logs.Name())
			t.Fatalf("host %s did not serve status=%d body=%q: %s; logs=%s", host, wantStatus, wantBody, last, logBody)
		}
		for _, marker := range []string{"first-backend", "replacement-backend"} {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, marker)
			}))
			t.Cleanup(backend.Close)
			routes[0].ServiceURL = backend.URL
			if evidence, err := pubdir.PublishRouteGroup("demo-group", routes, ""); err != nil || evidence.Status != RouteArtifactPublicationApplyStatusReady {
				t.Fatalf("publish live route: evidence=%+v error=%v", evidence, err)
			}
			probe("demo.example.test", marker, http.StatusOK)
		}
		probe("unknown.example.test", "", http.StatusNotFound)
	})
}
