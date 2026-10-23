// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/runtime"
	templatepkg "github.com/midnight/midnight/internal/templates"
)

func TestPostgresTemplateVerifierReplacesCandidateBeforePersistenceRead(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	fake := &templateVerificationRuntimeFake{
		target: runtime.RoutingTarget{
			ContainerID: "original-postgres", ContainerIP: "127.0.0.1",
			InternalPort: port, Ready: true,
		},
	}
	candidate := runtime.ScheduleRequest{
		DeploymentID: "dpl-template", InstanceID: "dpl-template-r0",
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
		Image: databaseTemplateVerificationSpecs[officialPostgresTemplateRef].ExpectedImage,
		Env:   []string{"POSTGRES_PASSWORD=secret", "POSTGRES_USER=postgres", "POSTGRES_DB=midnight"},
		Mounts: []runtime.ScheduleMount{{
			VolumeID: "vol-data", Source: "midnight-vol-data",
			Target: "/var/lib/postgresql/data", IsVolume: true,
		}},
	}
	verifier := &PostgresTemplateVerifier{
		Runtime: fake,
		Readiness: ReadinessConfig{
			Timeout: time.Second, Interval: time.Millisecond,
		},
	}
	evidence, err := verifier.Verify(context.Background(), TemplateVerificationRequest{
		TemplateRef: officialPostgresTemplateRef,
		Candidate:   candidate,
		Target:      fake.target,
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.WriteProbeContainerID != "probe-write" ||
		evidence.ReadProbeContainerID != "probe-read" ||
		len(evidence.MarkerSHA256) != 64 {
		t.Fatalf("evidence=%+v", evidence)
	}
	wantPrefix := []string{"probe-write", "stop", "remove", "schedule", "start"}
	if len(fake.calls) < len(wantPrefix) || !reflect.DeepEqual(fake.calls[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("calls=%v, want replacement sequence prefix %v", fake.calls, wantPrefix)
	}
	if fake.calls[len(fake.calls)-1] != "probe-read" {
		t.Fatalf("calls=%v, want persistence read after replacement", fake.calls)
	}
	if !reflect.DeepEqual(fake.scheduled.Mounts, candidate.Mounts) {
		t.Fatalf("replacement mounts=%+v, want %+v", fake.scheduled.Mounts, candidate.Mounts)
	}
}

func TestOfficialDatabaseTemplateVerifierSelectsEngineSpecificPersistenceGate(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	target := runtime.RoutingTarget{
		ContainerID:  "original",
		ContainerIP:  "127.0.0.1",
		InternalPort: listener.Addr().(*net.TCPAddr).Port,
		Ready:        true,
	}
	cases := []struct {
		name, templateRef, image, mountPath string
		env                                 []string
	}{
		{
			name: "redis", templateRef: officialRedisTemplateRef,
			image: databaseTemplateVerificationSpecs[officialRedisTemplateRef].ExpectedImage, mountPath: "/data",
			env: []string{"REDIS_PASSWORD=secret"},
		},
		{
			name: "mysql", templateRef: officialMySQLTemplateRef,
			image: databaseTemplateVerificationSpecs[officialMySQLTemplateRef].ExpectedImage, mountPath: "/var/lib/mysql",
			env: []string{"MYSQL_ROOT_PASSWORD=secret", "MYSQL_DATABASE=midnight"},
		},
		{
			name: "mongodb", templateRef: officialMongoDBTemplateRef,
			image: databaseTemplateVerificationSpecs[officialMongoDBTemplateRef].ExpectedImage, mountPath: "/data/db",
			env: []string{
				"MONGO_INITDB_ROOT_USERNAME=midnight",
				"MONGO_INITDB_ROOT_PASSWORD=secret",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &templateVerificationRuntimeFake{target: target}
			candidate := runtime.ScheduleRequest{
				DeploymentID: "dpl-" + testCase.name,
				InstanceID:   "dpl-" + testCase.name + "-r0",
				ProjectID:    "prj", EnvironmentID: "env", ServiceID: "svc",
				Image: testCase.image, Env: testCase.env,
				Mounts: []runtime.ScheduleMount{{
					VolumeID: "vol-data", Source: "midnight-vol-data",
					Target: testCase.mountPath, IsVolume: true,
				}},
			}
			verifier := &OfficialDatabaseTemplateVerifier{
				Runtime: fake,
				Readiness: ReadinessConfig{
					Timeout: time.Second, Interval: time.Millisecond,
				},
			}
			evidence, err := verifier.Verify(context.Background(), TemplateVerificationRequest{
				TemplateRef: testCase.templateRef,
				Candidate:   candidate,
				Target:      target,
			})
			if err != nil {
				t.Fatal(err)
			}
			if evidence.WriteProbeContainerID != "probe-write" ||
				evidence.ReadProbeContainerID != "probe-read" ||
				len(evidence.MarkerSHA256) != 64 {
				t.Fatalf("evidence=%+v calls=%v", evidence, fake.calls)
			}
			if len(fake.calls) < 6 || fake.calls[0] != "probe-write" ||
				fake.calls[len(fake.calls)-1] != "probe-read" {
				t.Fatalf("calls=%v, want write, replacement, read", fake.calls)
			}
		})
	}
}

func TestOfficialDatabaseTemplateVerifierRejectsImageMismatchBeforeProbes(t *testing.T) {
	spec := databaseTemplateVerificationSpecs[officialRedisTemplateRef]
	fake := &templateVerificationRuntimeFake{target: runtime.RoutingTarget{
		ContainerID: "original", ContainerIP: "127.0.0.1", InternalPort: spec.Port,
	}}
	verifier := &OfficialDatabaseTemplateVerifier{Runtime: fake}
	_, err := verifier.Verify(context.Background(), TemplateVerificationRequest{
		TemplateRef: officialRedisTemplateRef,
		Candidate: runtime.ScheduleRequest{
			DeploymentID: "dpl-mismatch", InstanceID: "dpl-mismatch-r0",
			ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
			Image: "redis:7.2.5@sha256:" + strings.Repeat("f", 64),
			Mounts: []runtime.ScheduleMount{{
				VolumeID: "vol-data", Source: "midnight-vol-data",
				Target: spec.MountPath, IsVolume: true,
			}},
		},
		Target: fake.target,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match the exact official image") {
		t.Fatalf("err=%v calls=%v", err, fake.calls)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("runtime calls=%v, want fail before probes", fake.calls)
	}
}

func TestOfficialDatabaseTemplateVerifierImagesMatchCatalog(t *testing.T) {
	slugs := map[string]string{
		officialPostgresTemplateRef: "postgres",
		officialRedisTemplateRef:    "redis",
		officialMySQLTemplateRef:    "mysql",
		officialMongoDBTemplateRef:  "mongodb",
	}
	for ref, slug := range slugs {
		tmpl, err := templatepkg.Lookup(slug)
		if err != nil || len(tmpl.Services) != 1 {
			t.Fatalf("%s catalog template=%+v err=%v", slug, tmpl, err)
		}
		if got := databaseTemplateVerificationSpecs[ref].ExpectedImage; got != tmpl.Services[0].Image {
			t.Fatalf("%s verifier image=%q catalog image=%q", slug, got, tmpl.Services[0].Image)
		}
	}
}

func TestOfficialDatabaseSmokeCommandsKeepCredentialsOutOfArguments(t *testing.T) {
	commands := map[string][]string{
		"redis":   {redisWriteSmokeCommand("marker"), redisReadSmokeCommand("marker")},
		"mysql":   {mysqlWriteSmokeCommand("marker"), mysqlReadSmokeCommand("marker")},
		"mongodb": {mongoDBWriteSmokeCommand("marker"), mongoDBReadSmokeCommand("marker")},
	}
	for engine, engineCommands := range commands {
		for _, command := range engineCommands {
			for _, forbidden := range []string{
				` -a "$REDIS_PASSWORD"`,
				`--password="$MYSQL_ROOT_PASSWORD"`,
				`--password "$MONGO_INITDB_ROOT_PASSWORD"`,
				`${MONGODB_URL}`,
			} {
				if strings.Contains(command, forbidden) {
					t.Fatalf("%s command exposes a credential argument: %s", engine, command)
				}
			}
		}
	}
}

func TestOfficialDatabaseTemplateVerifierBindsReadProbeToEachReplacement(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	spec := databaseTemplateVerificationSpecs[officialRedisTemplateRef]
	fake := &templateVerificationRuntimeFake{target: runtime.RoutingTarget{
		ContainerID: "original", ContainerIP: "127.0.0.1",
		InternalPort: listener.Addr().(*net.TCPAddr).Port,
	}}
	candidate := runtime.ScheduleRequest{
		DeploymentID: "dpl-retry", InstanceID: "dpl-retry-r0",
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
		Image: spec.ExpectedImage, Env: []string{"REDIS_PASSWORD=secret"},
		Mounts: []runtime.ScheduleMount{{
			VolumeID: "vol-data", Source: "midnight-vol-data",
			Target: spec.MountPath, IsVolume: true,
		}},
	}
	verifier := &OfficialDatabaseTemplateVerifier{
		Runtime: fake,
		Readiness: ReadinessConfig{
			Timeout: time.Second, Interval: time.Millisecond,
		},
	}
	request := TemplateVerificationRequest{
		TemplateRef: officialRedisTemplateRef, Candidate: candidate, Target: fake.target,
	}
	if _, err := verifier.Verify(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Target = fake.target
	if _, err := verifier.Verify(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(fake.readSpecs) != 2 || fake.readSpecs[0] == fake.readSpecs[1] {
		t.Fatalf("read specs=%v, want one immutable identity per replacement", fake.readSpecs)
	}
}

type templateVerificationRuntimeFake struct {
	calls              []string
	readSpecs          []string
	target             runtime.RoutingTarget
	scheduled          runtime.ScheduleRequest
	replacementCounter int
}

func (f *templateVerificationRuntimeFake) RunOneOff(_ context.Context, request runtime.OneOffRequest) (runtime.OneOffResult, error) {
	phase := "read"
	if strings.Contains(request.Command, "CREATE TABLE") ||
		strings.Contains(request.Command, " SET midnight_template_smoke ") ||
		strings.Contains(request.Command, "updateOne") {
		phase = "write"
	}
	f.calls = append(f.calls, "probe-"+phase)
	if phase == "read" {
		f.readSpecs = append(f.readSpecs, request.SpecSHA256)
	}
	return runtime.OneOffResult{ContainerID: "probe-" + phase, ExitCode: 0}, nil
}

func (f *templateVerificationRuntimeFake) Stop(context.Context, string, string, time.Duration) error {
	f.calls = append(f.calls, "stop")
	return nil
}

func (f *templateVerificationRuntimeFake) Remove(context.Context, string, string) error {
	f.calls = append(f.calls, "remove")
	return nil
}

func (f *templateVerificationRuntimeFake) Schedule(_ context.Context, request runtime.ScheduleRequest) (runtime.InstanceStatus, error) {
	f.calls = append(f.calls, "schedule")
	f.scheduled = request
	f.replacementCounter++
	f.target.ContainerID = fmt.Sprintf("replacement-%d", f.replacementCounter)
	return runtime.InstanceStatus{DeploymentID: request.DeploymentID, InstanceID: request.InstanceID}, nil
}

func (f *templateVerificationRuntimeFake) Start(context.Context, string, string) error {
	f.calls = append(f.calls, "start")
	return nil
}

func (f *templateVerificationRuntimeFake) ResolveRoutingTarget(context.Context, string, string) (runtime.RoutingTarget, error) {
	f.calls = append(f.calls, "resolve")
	return f.target, nil
}

func (f *templateVerificationRuntimeFake) CleanupTemplateSmoke(context.Context, []string) error {
	return nil
}
