//go:build integration

package deploy

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"

	runtimedocker "github.com/midnight/midnight/internal/runtime/docker"
)

func TestOfficialDatabaseSmokeCommandsSurviveRealContainerReplacement(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	const password = "MidnightTemplateProof1234567890"
	cases := []struct {
		name, image, mountPath string
		env                    map[string]string
		entrypoint, command    []string
		port                   string
		waitFor                wait.Strategy
		write, read            func(string) string
		expectPID1NonRoot      bool
	}{
		{
			name:      "postgres",
			image:     "postgres:16.14@sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20",
			mountPath: "/var/lib/postgresql/data",
			env: map[string]string{
				"POSTGRES_PASSWORD": password,
				"POSTGRES_USER":     "postgres",
				"POSTGRES_DB":       "midnight",
			},
			port: "5432/tcp",
			waitFor: wait.ForListeningPort("5432/tcp").
				WithStartupTimeout(5 * time.Minute),
			write: postgresWriteSmokeCommand, read: postgresReadSmokeCommand,
		},
		{
			name:       "redis",
			image:      "redis:7.2.5@sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e",
			mountPath:  "/data",
			env:        map[string]string{"REDIS_PASSWORD": password},
			entrypoint: []string{"/bin/sh", "-lc"},
			command: []string{
				`case "$REDIS_PASSWORD" in ""|*[!A-Za-z0-9]*) echo "REDIS_PASSWORD must be non-empty alphanumeric" >&2; exit 64;; esac; mkdir -p /run/midnight; chown root:redis /run/midnight; chmod 0710 /run/midnight; rm -f /run/midnight/redis.conf; (umask 027; set -C; printf 'appendonly yes\nrequirepass %s\n' "$REDIS_PASSWORD" > /run/midnight/redis.conf) || exit 1; chown root:redis /run/midnight/redis.conf; chmod 0640 /run/midnight/redis.conf; exec /usr/local/bin/docker-entrypoint.sh redis-server /run/midnight/redis.conf`,
			},
			port: "6379/tcp",
			waitFor: wait.ForListeningPort("6379/tcp").
				WithStartupTimeout(3 * time.Minute),
			write: redisWriteSmokeCommand, read: redisReadSmokeCommand,
			expectPID1NonRoot: true,
		},
		{
			name:      "mysql",
			image:     "mysql:8.4.0@sha256:dab7049abafe3a0e12cbe5e49050cf149881c0cd9665c289e5808b9dad39c9e0",
			mountPath: "/var/lib/mysql",
			env: map[string]string{
				"MYSQL_ROOT_PASSWORD": password,
				"MYSQL_PASSWORD":      password,
				"MYSQL_DATABASE":      "midnight",
				"MYSQL_USER":          "midnight",
			},
			port: "3306/tcp",
			waitFor: wait.ForListeningPort("3306/tcp").
				WithStartupTimeout(5 * time.Minute),
			write: mysqlWriteSmokeCommand, read: mysqlReadSmokeCommand,
		},
		{
			name:      "mongodb",
			image:     "mongo:7.0.9@sha256:97aac78a80553735b3d9b9b7212803468781b4859645f892a3d04e6b621a7b77",
			mountPath: "/data/db",
			env: map[string]string{
				"MONGO_INITDB_ROOT_USERNAME": "midnight",
				"MONGO_INITDB_ROOT_PASSWORD": password,
				"MONGO_INITDB_DATABASE":      "midnight",
			},
			port: "27017/tcp",
			waitFor: wait.ForListeningPort("27017/tcp").
				WithStartupTimeout(5 * time.Minute),
			write: mongoDBWriteSmokeCommand, read: mongoDBReadSmokeCommand,
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			volumeName := fmt.Sprintf("midnight-template-%s-%d", testCase.name, time.Now().UnixNano())
			engine, err := runtimedocker.NewEngineClient("")
			if err != nil {
				t.Skipf("Docker unavailable: %v", err)
			}
			t.Cleanup(func() {
				if volumes, ok := engine.(runtimedocker.VolumeEngineClient); ok {
					_ = volumes.VolumeRemove(context.Background(), volumeName, true)
				}
				_ = engine.Close()
			})
			if err := engine.Ping(ctx); err != nil {
				t.Skipf("Docker unavailable: %v", err)
			}

			request := tc.ContainerRequest{
				Image:        testCase.image,
				Env:          testCase.env,
				Entrypoint:   testCase.entrypoint,
				Cmd:          testCase.command,
				ExposedPorts: []string{testCase.port},
				Mounts: tc.ContainerMounts{{
					Source: tc.GenericVolumeMountSource{Name: volumeName},
					Target: tc.ContainerMountTarget(testCase.mountPath),
				}},
				WaitingFor: testCase.waitFor,
			}
			first := startTemplateDatabaseContainer(t, ctx, request)
			if testCase.expectPID1NonRoot {
				assertTemplateDatabasePID1IsNonRoot(t, ctx, first)
			}
			marker := templateMarker("real-" + testCase.name + "-" + volumeName)
			runTemplateDatabaseCommand(t, ctx, first, testCase.env, marker, testCase.write(marker))
			if err := first.Terminate(ctx); err != nil {
				t.Fatalf("terminate write container: %v", err)
			}

			replacement := startTemplateDatabaseContainer(t, ctx, request)
			t.Cleanup(func() { _ = replacement.Terminate(context.Background()) })
			if testCase.expectPID1NonRoot {
				assertTemplateDatabasePID1IsNonRoot(t, ctx, replacement)
			}
			runTemplateDatabaseCommand(t, ctx, replacement, testCase.env, marker, testCase.read(marker))
		})
	}
}

func assertTemplateDatabasePID1IsNonRoot(t *testing.T, ctx context.Context, container tc.Container) {
	t.Helper()
	exitCode, output, err := container.Exec(
		ctx,
		[]string{"/bin/sh", "-lc", `test "$(awk '/^Uid:/{print $2}' /proc/1/status)" != "0"`},
	)
	if err != nil {
		t.Fatalf("inspect database process user: %v", err)
	}
	body, readErr := io.ReadAll(output)
	if readErr != nil {
		t.Fatalf("read process-user output: %v", readErr)
	}
	if exitCode != 0 {
		t.Fatalf("database PID 1 remained root: %s", strings.TrimSpace(string(body)))
	}
}

func startTemplateDatabaseContainer(t *testing.T, ctx context.Context, request tc.ContainerRequest) tc.Container {
	t.Helper()
	container, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
		ContainerRequest: request,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start database container: %v", err)
	}
	return container
}

func runTemplateDatabaseCommand(
	t *testing.T,
	ctx context.Context,
	container tc.Container,
	baseEnv map[string]string,
	marker, command string,
) {
	t.Helper()
	env := []string{
		"MIDNIGHT_TEMPLATE_HOST=127.0.0.1",
		"MIDNIGHT_TEMPLATE_MARKER=" + marker,
	}
	for key, value := range baseEnv {
		env = append(env, key+"="+value)
	}
	exitCode, output, err := container.Exec(
		ctx,
		[]string{"/bin/sh", "-lc", command},
		tcexec.WithEnv(env),
	)
	if err != nil {
		t.Fatalf("exec smoke command: %v", err)
	}
	body, readErr := io.ReadAll(output)
	if readErr != nil {
		t.Fatalf("read smoke output: %v", readErr)
	}
	if exitCode != 0 {
		redacted := string(body)
		for _, key := range []string{"REDIS_PASSWORD", "MYSQL_ROOT_PASSWORD", "MYSQL_PASSWORD", "MONGO_INITDB_ROOT_PASSWORD"} {
			if secret := baseEnv[key]; secret != "" {
				redacted = strings.ReplaceAll(redacted, secret, "[REDACTED]")
			}
		}
		t.Fatalf("smoke command exit=%d output=%s", exitCode, redacted)
	}
}
