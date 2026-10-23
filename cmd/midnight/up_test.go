// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/cliconfig"
	"github.com/midnight/midnight/internal/sourceartifact"
)

type capturedUpRequest struct {
	Method        string
	EscapedPath   string
	Header        http.Header
	ContentLength int64
	Body          []byte
	ReadErr       error
}

func captureUpRequest(r *http.Request) capturedUpRequest {
	body, err := io.ReadAll(r.Body)
	return capturedUpRequest{
		Method:        r.Method,
		EscapedPath:   r.URL.EscapedPath(),
		Header:        r.Header.Clone(),
		ContentLength: r.ContentLength,
		Body:          body,
		ReadErr:       err,
	}
}

func runUpWithResponse(
	t *testing.T,
	status int,
	body []byte,
	token string,
) (int, string, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != cliUserAgent() {
			t.Errorf("User-Agent = %q, want %q", got, cliUserAgent())
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, token)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runUp([]string{"--detach"}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestLoadIgnorePatternsSupportsNestedGlobsAndNegation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, ".gitignore"),
		[]byte("*.env\ndist/**\n!dist/keep.txt\n!.git\n!.git/**\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".midnightignore"),
		[]byte("*.pem\n!.midnight\n!.midnight/**\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	ignores, err := loadIgnorePatterns(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		".git/config",
		"nested/.git/config",
		".midnight/project.json",
		"nested/.midnight/project.json",
		"node_modules/pkg/index.js",
		"nested/node_modules/pkg/index.js",
		"prod.env",
		"config/prod.env",
		"tls/private.pem",
		"dist/generated.js",
	} {
		if !ignores.matches(path, false) {
			t.Errorf("expected %q to be ignored", path)
		}
	}
	if ignores.matches("dist/keep.txt", false) {
		t.Error("expected later negation to re-include dist/keep.txt")
	}
	if ignores.matches("src/main.go", false) {
		t.Error("expected src/main.go to remain in the upload")
	}
}

func TestWriteGzippedTarballEnforcesServerArchiveLimitsLocally(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(strings.Repeat("a", 800)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte(strings.Repeat("b", 800)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		limits sourceartifact.Limits
	}{
		{
			name: "compressed",
			limits: sourceartifact.Limits{
				MaxCompressedBytes:   10,
				MaxEntries:           10,
				MaxUncompressedBytes: 1 << 20,
			},
		},
		{
			name: "entries",
			limits: sourceartifact.Limits{
				MaxCompressedBytes:   1 << 20,
				MaxEntries:           1,
				MaxUncompressedBytes: 1 << 20,
			},
		},
		{
			name: "expanded stream",
			limits: sourceartifact.Limits{
				MaxCompressedBytes:   1 << 20,
				MaxEntries:           10,
				MaxUncompressedBytes: 512,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			if _, err := writeGzippedTarballWithLimits(
				root,
				ignoreSet{},
				&output,
				tc.limits,
			); err == nil {
				t.Fatal("archive limit was not enforced")
			}
		})
	}
}

func TestWriteGzippedTarballEnforcesServerArchivePathContractLocally(t *testing.T) {
	root := t.TempDir()
	segments := make([]string, sourceartifact.MaxArchivePathDepth+1)
	for index := range segments {
		segments[index] = "d"
	}
	deep := filepath.Join(append([]string{root}, segments...)...)
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := writeGzippedTarball(root, ignoreSet{}, &output); !errors.Is(err, sourceartifact.ErrLimitExceeded) {
		t.Fatalf("archive path error=%v, want ErrLimitExceeded", err)
	}
	if output.Len() > int(sourceartifact.DefaultLimits.MaxCompressedBytes) {
		t.Fatalf("failed local archive escaped compressed limit: bytes=%d", output.Len())
	}
}

func TestSupportedUpArchiveModeRejectsSpecialInodes(t *testing.T) {
	for _, mode := range []os.FileMode{0, os.ModeDir | 0o750} {
		if !supportedUpArchiveMode(mode) {
			t.Errorf("ordinary mode %v was rejected", mode)
		}
	}
	for _, mode := range []os.FileMode{
		os.ModeNamedPipe,
		os.ModeSocket,
		os.ModeDevice,
		os.ModeCharDevice,
		os.ModeIrregular,
		os.ModeSymlink,
	} {
		if supportedUpArchiveMode(mode) {
			t.Errorf("special mode %v was accepted", mode)
		}
	}
}

func TestUpClientWaitsForServerPreflightBeforeReadingArchive(t *testing.T) {
	handlerArrived := make(chan struct{})
	allowBodyRead := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(handlerArrived)
		<-allowBodyRead
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read upload body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	bodyRead := make(chan struct{}, 1)
	payload := []byte("archive body")
	body := &observedUpBody{
		Reader:       bytes.NewReader(payload),
		ReadObserved: bodyRead,
	}
	request, err := http.NewRequest(http.MethodPost, server.URL, io.NopCloser(body))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = int64(len(payload))
	request.Header.Set("Expect", "100-continue")

	responseCh := make(chan error, 1)
	go func() {
		response, err := upNoRedirectHTTPClient(3 * time.Second).Do(request)
		if err == nil {
			_ = response.Body.Close()
		}
		responseCh <- err
	}()
	select {
	case <-handlerArrived:
	case <-time.After(time.Second):
		t.Fatal("server did not receive upload headers")
	}
	select {
	case <-bodyRead:
		t.Fatal("client sent archive before the server completed preflight")
	case <-time.After(1200 * time.Millisecond):
		// The stock transport would have started the body after one second.
	}
	close(allowBodyRead)
	select {
	case err := <-responseCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not complete after server body acceptance")
	}
	select {
	case <-bodyRead:
	default:
		t.Fatal("server acceptance did not release the archive body")
	}
}

type observedUpBody struct {
	*bytes.Reader
	ReadObserved chan<- struct{}
}

func (b *observedUpBody) Read(payload []byte) (int, error) {
	select {
	case b.ReadObserved <- struct{}{}:
	default:
	}
	return b.Reader.Read(payload)
}

func TestOpenStableUpArchiveFileRejectsSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(source, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	walked, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("must not be packaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, source); err != nil {
		t.Fatal(err)
	}
	archiveRoot, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveRoot.Close()
	if file, _, err := openStableUpArchiveFile(archiveRoot, "source.txt", walked); err == nil {
		_ = file.Close()
		t.Fatal("symlink replacement was accepted")
	}
}

func TestWriteGzippedTarballRejectsParentSwapToOutsideSymlink(t *testing.T) {
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "src")
	if err := os.Mkdir(sourceDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sourceDirectory, "main.go"),
		[]byte("safe source"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(outside, "main.go"),
		[]byte("outside root"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	swapped := false
	hooks := upArchiveWalkHooks{
		afterEntryLstat: func(relativePath string) error {
			// At this point the walker has opened and retained a Root for src.
			// Replacing the parent path must not redirect the subsequent file
			// open through the newly installed symlink.
			if relativePath != "src/main.go" || swapped {
				return nil
			}
			swapped = true
			if err := os.Rename(sourceDirectory, filepath.Join(root, "src-original")); err != nil {
				return err
			}
			return os.Symlink(outside, sourceDirectory)
		},
	}
	var output bytes.Buffer
	stats, err := writeGzippedTarballWithLimitsAndHooks(
		root,
		ignoreSet{},
		&output,
		sourceartifact.DefaultLimits,
		hooks,
	)
	if !swapped {
		t.Fatal("parent-directory swap seam did not run")
	}
	if err != nil {
		t.Fatalf("descriptor-rooted archive failed after parent swap: %v", err)
	}
	if stats.Files != 1 {
		t.Fatalf("outside-root swap packaged %d files, want the one retained source", stats.Files)
	}
	files := readGzippedUpArchiveFiles(t, output.Bytes())
	if got := files["src/main.go"]; got != "safe source" {
		t.Fatalf("src/main.go=%q, want retained safe source", got)
	}
	for name, body := range files {
		if strings.Contains(name, "secret") || strings.Contains(body, "outside root") {
			t.Fatalf("outside-root content leaked into archive: %q=%q", name, body)
		}
	}
}

func TestWriteGzippedTarballRejectsParentSwapToProtectedInRootSymlink(t *testing.T) {
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "src")
	if err := os.Mkdir(sourceDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sourceDirectory, "main.go"),
		[]byte("safe source"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	protectedDirectory := filepath.Join(root, ".git")
	if err := os.Mkdir(protectedDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(protectedDirectory, "main.go"),
		[]byte("protected git credential"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	ignores, err := loadIgnorePatterns(root)
	if err != nil {
		t.Fatal(err)
	}

	swapped := false
	hooks := upArchiveWalkHooks{
		afterEntryLstat: func(relativePath string) error {
			// src is already represented by a retained Root; changing its
			// parent path must not make the next open resolve through .git.
			if relativePath != "src/main.go" || swapped {
				return nil
			}
			swapped = true
			if err := os.Rename(sourceDirectory, filepath.Join(root, "src-original")); err != nil {
				return err
			}
			return os.Symlink(".git", sourceDirectory)
		},
	}
	var output bytes.Buffer
	stats, err := writeGzippedTarballWithLimitsAndHooks(
		root,
		ignores,
		&output,
		sourceartifact.DefaultLimits,
		hooks,
	)
	if !swapped {
		t.Fatal("protected-directory swap seam did not run")
	}
	if err != nil {
		t.Fatalf("descriptor-rooted archive failed after parent swap: %v", err)
	}
	if stats.Files != 1 {
		t.Fatalf("protected-directory swap packaged %d files, want the one retained source", stats.Files)
	}
	files := readGzippedUpArchiveFiles(t, output.Bytes())
	if got := files["src/main.go"]; got != "safe source" {
		t.Fatalf("src/main.go=%q, want retained safe source", got)
	}
	for name, body := range files {
		if strings.Contains(name, ".git") || strings.Contains(body, "protected git credential") {
			t.Fatalf("protected content leaked into archive: %q=%q", name, body)
		}
	}
}

func readGzippedUpArchiveFiles(t *testing.T, archive []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	files := make(map[string]string)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = string(body)
	}
}

func TestWriteGzippedTarballUsesDeterministicLexicalTraversal(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"z.txt": "z",
		"a.txt": "a",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "y.txt"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if _, err := writeGzippedTarball(root, ignoreSet{}, &output); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	const want = "a.txt,dir/,dir/y.txt,z.txt"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("archive order=%q, want %q", got, want)
	}
}

func TestLoadIgnorePatternsRejectsInvalidPattern(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".midnightignore"), []byte("[\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadIgnorePatterns(root); err == nil {
		t.Fatal("expected an invalid ignore pattern to fail closed")
	}
}

func TestStreamOperationEventsPrematureEOFFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"state\":\"RUNNING\",\"message\":\"building\"}\n\n"))
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := streamOperationEvents(server.URL, "", "op_1", &stdout, &stderr); code == 0 {
		t.Fatalf("streamOperationEvents returned success after premature EOF; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "before a terminal state") {
		t.Fatalf("stderr = %q, want premature-close explanation", stderr.String())
	}
}

func TestStreamOperationEventsSurvivesBeyondRESTTimeout(t *testing.T) {
	originalREST, originalIdle := cliRESTTimeout, cliStreamIdleTimeout
	cliRESTTimeout = 20 * time.Millisecond
	cliStreamIdleTimeout = 500 * time.Millisecond
	t.Cleanup(func() {
		cliRESTTimeout = originalREST
		cliStreamIdleTimeout = originalIdle
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != cliUserAgent() {
			t.Errorf("User-Agent = %q, want %q", got, cliUserAgent())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(75 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"state\":\"SUCCEEDED\",\"message\":\"done\"}\n\n"))
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := streamOperationEvents(server.URL, "", "op_1", &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestStreamOperationEventsSanitizesBoundedErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "visible\x1b]52;c;hidden\a"+strings.Repeat("x", 80<<10))
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := streamOperationEvents(server.URL, "", "op_1", &stdout, &stderr); code == 0 {
		t.Fatal("stream succeeded for oversized error response")
	}
	if strings.ContainsRune(stderr.String(), '\x1b') || strings.Contains(stderr.String(), "hidden") ||
		len(stderr.String()) > 1024 {
		t.Fatalf("unsafe or unbounded stderr=%q", stderr.String())
	}
}

func TestRunLinkDoesNotFabricateEnvironmentOnAPIFailure(t *testing.T) {
	var serviceRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1alpha/projects":
			_, _ = w.Write([]byte(`[{"id":"prj_1","slug":"demo","display_name":"Demo"}]`))
		case "/api/v1alpha/environments":
			http.Error(w, "environment store unavailable", http.StatusServiceUnavailable)
		case "/api/v1alpha/services":
			serviceRequests++
			_, _ = w.Write([]byte(`[{"id":"svc_1","project_id":"prj_1","slug":"web"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("MIDNIGHT_TOKEN", "test-token")
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runLink([]string{"--api-base", server.URL, "--project", "prj_1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("runLink succeeded with an unavailable environment API; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if serviceRequests != 0 {
		t.Fatalf("service requests = %d, want link to stop before selecting a service", serviceRequests)
	}
	if !strings.Contains(stderr.String(), "list environments") {
		t.Fatalf("stderr = %q, want environment lookup failure", stderr.String())
	}
}

func TestRunUpExplicitCredentialsOverrideProjectAndEnvironment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1alpha/services/svc_1/up" {
			t.Fatalf("path = %q, want source upload endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer explicit-token" {
			t.Fatalf("Authorization = %q, want explicit token", got)
		}
		if got := r.Header.Get("X-Midnight-Environment"); got != "env_prod" {
			t.Fatalf("X-Midnight-Environment = %q, want linked environment id", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		wantDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
		if got := r.Header.Get("X-Midnight-Archive-Digest"); got != wantDigest {
			t.Fatalf("X-Midnight-Archive-Digest = %q, want %q", got, wantDigest)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"operation_id":"op_1","deployment_id":"dpl_1","operation":{"id":"op_1","state":"PENDING"}}`))
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       "http://127.0.0.1:1",
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "environment-token")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runUp([]string{
		"--api-base", server.URL,
		"--token", "explicit-token",
		"--detach",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "queued operation op_1") {
		t.Fatalf("stdout = %q, want queued operation", stdout.String())
	}
}

func TestRunUpHumanSessionSendsExactTenantUploadRequest(t *testing.T) {
	requests := make(chan capturedUpRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureUpRequest(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{
			"operation_id":"op_human",
			"deployment_id":"dpl_human",
			"operation":{"id":"op_human","kind":"build","state":"PENDING"}
		}`))
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env/prod",
		ServiceID:     "svc/one",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	const token = "midn_user_human-session-secret"
	t.Setenv(cliconfig.EnvBearerToken, token)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runUp([]string{
		"--message", "feat: tenant upload",
		"--idempotency-key", "up-human-explicit-key",
		"--detach",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	request := <-requests
	if request.ReadErr != nil {
		t.Fatal(request.ReadErr)
	}
	if request.Method != http.MethodPost {
		t.Fatalf("method=%q, want POST", request.Method)
	}
	if request.EscapedPath != "/api/v1alpha/services/svc%2Fone/up" {
		t.Fatalf("escaped path=%q", request.EscapedPath)
	}
	for name, want := range map[string]string{
		"Authorization":             "Bearer " + token,
		"Content-Type":              "application/gzip",
		"Idempotency-Key":           "up-human-explicit-key",
		"X-Midnight-Commit-Message": "feat: tenant upload",
		"X-Midnight-Environment":    "env/prod",
	} {
		if got := request.Header.Values(name); len(got) != 1 || got[0] != want {
			t.Fatalf("%s=%q, want singleton %q", name, got, want)
		}
	}
	if request.ContentLength <= 0 || request.ContentLength != int64(len(request.Body)) {
		t.Fatalf(
			"Content-Length=%d body=%d, want exact non-empty length",
			request.ContentLength,
			len(request.Body),
		)
	}
	gz, err := gzip.NewReader(bytes.NewReader(request.Body))
	if err != nil {
		t.Fatalf("upload is not gzip: %v", err)
	}
	tarBytes, err := io.ReadAll(gz)
	if closeErr := gz.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if len(tarBytes) == 0 {
		t.Fatal("gzip upload contained an empty tar stream")
	}
	if !strings.Contains(stdout.String(), "queued operation op_human") ||
		!strings.Contains(stdout.String(), "tenant-safe events") {
		t.Fatalf("stdout=%q, want accepted response envelope", stdout.String())
	}
	if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatalf("credential leaked to output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunUpGeneratedIdempotencyKeyBindsStableArchiveAndIntent(t *testing.T) {
	requests := make(chan capturedUpRequest, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureUpRequest(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"operation_id":"op_stable","deployment_id":"dpl_stable","operation":{"id":"op_stable","state":"PENDING"}}`))
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	sourcePath := filepath.Join(workdir, "main.go")
	if err := os.WriteFile(sourcePath, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "midn_user_stable-session")

	invoke := func(args ...string) capturedUpRequest {
		t.Helper()
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		args = append(args, "--detach")
		if code := runUp(args, &stdout, &stderr); code != 0 {
			t.Fatalf("exit=%d args=%q stdout=%q stderr=%q", code, args, stdout.String(), stderr.String())
		}
		return <-requests
	}

	baseline := invoke()
	repeated := invoke()
	changedEnvironment := invoke("--env", "env_staging")
	changedMessage := invoke("--message", "release candidate")
	if err := os.WriteFile(sourcePath, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changedArchive := invoke()

	baselineKey := baseline.Header.Get("Idempotency-Key")
	if !strings.HasPrefix(baselineKey, "midnight-up-v1-") {
		t.Fatalf("generated key=%q, want versioned deterministic key", baselineKey)
	}
	if got := baseline.Header.Get("Expect"); got != "100-continue" {
		t.Fatalf("Expect=%q, want 100-continue preflight", got)
	}
	if got := repeated.Header.Get("Idempotency-Key"); got != baselineKey {
		t.Fatalf("identical archive key=%q, want %q", got, baselineKey)
	}
	if !bytes.Equal(baseline.Body, repeated.Body) {
		t.Fatal("separate invocations over unchanged input produced different archives")
	}
	for name, request := range map[string]capturedUpRequest{
		"environment": changedEnvironment,
		"message":     changedMessage,
		"archive":     changedArchive,
	} {
		if got := request.Header.Get("Idempotency-Key"); got == baselineKey {
			t.Fatalf("changed %s retained generated key %q", name, got)
		}
	}
	if !bytes.Equal(baseline.Body, changedEnvironment.Body) {
		t.Fatal("environment-only change unexpectedly changed archive bytes")
	}
	if !bytes.Equal(baseline.Body, changedMessage.Body) {
		t.Fatal("message-only change unexpectedly changed archive bytes")
	}
	if bytes.Equal(baseline.Body, changedArchive.Body) {
		t.Fatal("source change did not change archive bytes")
	}
}

func TestRunUpPreservesAdmittedCredentialCompatibility(t *testing.T) {
	tests := []struct {
		name  string
		token string
		setup func(*testing.T, string, string) []string
	}{
		{
			name:  "project-automation",
			token: "midn_sa_ci-deploy-secret",
			setup: func(t *testing.T, apiBase, token string) []string {
				t.Setenv(cliconfig.EnvAPIBase, apiBase)
				t.Setenv(cliconfig.EnvBearerToken, token)
				return nil
			},
		},
		{
			name:  "operator-recovery",
			token: "operator-recovery-secret",
			setup: func(t *testing.T, apiBase, token string) []string {
				if err := cliconfig.SetOperatorRecoveryToken(apiBase, token); err != nil {
					t.Fatal(err)
				}
				return nil
			},
		},
		{
			name:  "explicit-developer",
			token: "developer-explicit-secret",
			setup: func(_ *testing.T, _, token string) []string {
				return []string{"--token", token}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan capturedUpRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- captureUpRequest(r)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"operation_id":"op_compat","deployment_id":"dpl_compat","operation":{"id":"op_compat","state":"PENDING"}}`))
			}))
			t.Cleanup(server.Close)

			workdir := t.TempDir()
			t.Chdir(workdir)
			if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
				APIBase:       server.URL,
				ProjectID:     "prj_1",
				EnvironmentID: "env_prod",
				ServiceID:     "svc_1",
			}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
			t.Setenv(cliconfig.EnvBearerToken, "")
			t.Setenv(cliconfig.EnvAPIToken, "")
			args := append(test.setup(t, server.URL, test.token), "--detach")

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := runUp(args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			request := <-requests
			if got := request.Header.Values("Authorization"); len(got) != 1 ||
				got[0] != "Bearer "+test.token {
				t.Fatalf("Authorization=%q", got)
			}
			if strings.Contains(stdout.String(), test.token) ||
				strings.Contains(stderr.String(), test.token) {
				t.Fatalf("credential leaked to output: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunUpRefusesCrossOriginRedirectWithoutForwardingCredential(t *testing.T) {
	var destinationRequests atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationRequests.Add(1)
		http.Error(w, "credential must not arrive", http.StatusInternalServerError)
	}))
	t.Cleanup(destination.Close)

	var sourceRequests atomic.Int64
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceRequests.Add(1)
		http.Redirect(w, r, destination.URL+"/capture", http.StatusFound)
	}))
	t.Cleanup(source.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       source.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, source.URL)
	const token = "midn_user_redirect-secret"
	t.Setenv(cliconfig.EnvBearerToken, token)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runUp([]string{"--detach"}, &stdout, &stderr); code == 0 {
		t.Fatalf("redirected upload succeeded: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if sourceRequests.Load() != 1 {
		t.Fatalf("source requests=%d, want 1", sourceRequests.Load())
	}
	if destinationRequests.Load() != 0 {
		t.Fatalf("redirect destination requests=%d, want zero", destinationRequests.Load())
	}
	if !strings.Contains(stderr.String(), errUpRedirect.Error()) {
		t.Fatalf("stderr=%q, want redirect refusal", stderr.String())
	}
	if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatalf("credential leaked to output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunUpRejectsUnsafeInputsBeforePackagingOrRequest(t *testing.T) {
	var requestCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		http.Error(w, "must not be requested", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	const token = "midn_user_unsafe-input-secret"
	t.Setenv(cliconfig.EnvBearerToken, token)

	tests := []struct {
		name string
		args []string
	}{
		{name: "empty explicit idempotency key", args: []string{"--idempotency-key", ""}},
		{name: "whitespace idempotency key", args: []string{"--idempotency-key", "has space"}},
		{name: "oversized idempotency key", args: []string{"--idempotency-key", strings.Repeat("k", 256)}},
		{name: "empty environment override", args: []string{"--env", ""}},
		{name: "environment control character", args: []string{"--env", "env_prod\r\nX-Evil: yes"}},
		{name: "empty service override", args: []string{"--service", ""}},
		{name: "service control character", args: []string{"--service", "svc_1\nX-Evil: yes"}},
		{name: "message control character", args: []string{"--message", "release\nX-Evil: yes"}},
		{name: "oversized message", args: []string{"--message", strings.Repeat("m", maxUpCommitMessageLen+1)}},
		{name: "invalid UTF-8 message", args: []string{"--message", string([]byte{0xff})}},
		{name: "unexpected positional argument", args: []string{"unexpected"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := requestCount.Load()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := runUp(test.args, &stdout, &stderr); code == 0 {
				t.Fatalf("exit=0 stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if got := requestCount.Load(); got != before {
				t.Fatalf("HTTP requests=%d, want zero", got-before)
			}
			if strings.Contains(stdout.String(), "packaging") {
				t.Fatalf("packaging started for unsafe input: stdout=%q", stdout.String())
			}
			if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
				t.Fatalf("credential leaked to output: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunUpRejectsInvalidResponseEnvelopeWithoutLeakingCredential(t *testing.T) {
	const token = "midn_user_response-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"deployment_id":"dpl_1","echo":"` + token + `"}`))
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, token)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runUp([]string{"--detach"}, &stdout, &stderr); code == 0 {
		t.Fatalf("invalid response succeeded: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "operation_id") {
		t.Fatalf("stderr=%q, want response-envelope error", stderr.String())
	}
	if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatalf("credential leaked to output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunUpCapsSuccessAndErrorResponsesWithoutLeakingBody(t *testing.T) {
	const (
		token  = "midn_user_oversized-response-secret"
		marker = "oversized-private-response-marker"
	)
	body := append([]byte(token+marker), bytes.Repeat(
		[]byte("x"),
		int(maxUpResponseBytes)+1-len(token)-len(marker),
	)...)
	if int64(len(body)) != maxUpResponseBytes+1 {
		t.Fatalf("fixture bytes=%d", len(body))
	}

	for _, test := range []struct {
		name   string
		status int
	}{
		{name: "accepted", status: http.StatusAccepted},
		{name: "error", status: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runUpWithResponse(t, test.status, body, token)
			if code == 0 {
				t.Fatalf("oversized response succeeded: stdout=%q stderr=%q", stdout, stderr)
			}
			if !strings.Contains(stderr, "server response exceeds") {
				t.Fatalf("stderr=%q, want response cap error", stderr)
			}
			for _, secret := range []string{token, marker} {
				if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
					t.Fatalf("response content leaked: stdout=%q stderr=%q", stdout, stderr)
				}
			}
		})
	}
}

func TestRunUpRejectsIncompleteOrInconsistentAcceptedEnvelope(t *testing.T) {
	const (
		token  = "midn_user_inconsistent-envelope-secret"
		marker = "private-envelope-marker"
	)
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing deployment id",
			body: `{"operation_id":"op_1","operation":{"id":"op_1"},"echo":"` + marker + `"}`,
		},
		{
			name: "non-printable deployment id",
			body: `{"operation_id":"op_1","deployment_id":"dpl_1\nbad","operation":{"id":"op_1"},"echo":"` + marker + `"}`,
		},
		{
			name: "missing operation object",
			body: `{"operation_id":"op_1","deployment_id":"dpl_1","echo":"` + marker + `"}`,
		},
		{
			name: "null operation object",
			body: `{"operation_id":"op_1","deployment_id":"dpl_1","operation":null,"echo":"` + marker + `"}`,
		},
		{
			name: "operation id mismatch",
			body: `{"operation_id":"op_1","deployment_id":"dpl_1","operation":{"id":"op_other"},"echo":"` + marker + `"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runUpWithResponse(
				t,
				http.StatusAccepted,
				[]byte(test.body),
				token,
			)
			if code == 0 {
				t.Fatalf("invalid envelope succeeded: stdout=%q stderr=%q", stdout, stderr)
			}
			if !strings.Contains(stderr, "invalid accepted response envelope") {
				t.Fatalf("stderr=%q, want accepted-envelope error", stderr)
			}
			if strings.Contains(stdout, "queued operation") {
				t.Fatalf("invalid envelope was printed as queued: stdout=%q", stdout)
			}
			for _, secret := range []string{token, marker} {
				if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
					t.Fatalf("response content leaked: stdout=%q stderr=%q", stdout, stderr)
				}
			}
		})
	}
}

func TestRunUpHumanSessionFollowsTenantSafeOperationEvents(t *testing.T) {
	requests := make(chan capturedUpRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureUpRequest(r)
		switch {
		case r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operation_id":"op/follow","deployment_id":"dpl_follow","operation":{"id":"op/follow","state":"PENDING"}}`))
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: operation\ndata: {\"state\":\"SUCCEEDED\",\"message\":\"build complete\"}\n\n"))
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	const token = "midn_user_follow-session"
	t.Setenv(cliconfig.EnvBearerToken, token)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runUp(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	upload := <-requests
	follow := <-requests
	if upload.Method != http.MethodPost ||
		upload.EscapedPath != "/api/v1alpha/services/svc_1/up" {
		t.Fatalf("upload request=%#v", upload)
	}
	if follow.Method != http.MethodGet ||
		follow.EscapedPath != "/api/v1alpha/operations/op%2Ffollow/events" {
		t.Fatalf("follow request=%#v", follow)
	}
	if got := follow.Header.Values("Authorization"); len(got) != 1 ||
		got[0] != "Bearer "+token {
		t.Fatalf("follow Authorization=%q", got)
	}
	if got := follow.Header.Values("Accept"); len(got) != 1 ||
		got[0] != "text/event-stream" {
		t.Fatalf("follow Accept=%q", got)
	}
	if !strings.Contains(stdout.String(), "build complete") ||
		!strings.Contains(stdout.String(), "midnight up: deploy succeeded") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunUpMissingEnvironmentFailsBeforePackagingOrUpload(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:   server.URL,
		ProjectID: "prj_1",
		ServiceID: "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runUp([]string{
		"--api-base", server.URL,
		"--token", "explicit-token",
		"--detach",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("runUp succeeded without an environment; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if requestCount != 0 {
		t.Fatalf("requests = %d, want no upload or lookup", requestCount)
	}
	if strings.Contains(stdout.String(), "packaging") {
		t.Fatalf("stdout = %q, packaging started before environment preflight", stdout.String())
	}
	if !strings.Contains(stderr.String(), "environment_id") {
		t.Fatalf("stderr = %q, want environment preflight error", stderr.String())
	}
}

func TestDomainListDoesNotReportSuccessWhenInventoryEndpointFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "inventory unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:   server.URL,
		ProjectID: "prj_1",
		ServiceID: "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "test-token")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runDomainList(nil, &stdout, &stderr); code == 0 {
		t.Fatalf("runDomainList reported success; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "domain list") {
		t.Fatalf("stderr = %q, want endpoint failure", stderr.String())
	}
}

func setStreamFollowTestTimings(t *testing.T) {
	t.Helper()
	originalREST, originalIdle := cliRESTTimeout, cliStreamIdleTimeout
	originalWindow, originalDelay := cliStreamFollowMaxDuration, cliStreamReconnectDelay
	cliRESTTimeout = 2 * time.Second
	cliStreamIdleTimeout = 150 * time.Millisecond
	cliStreamFollowMaxDuration = 10 * time.Second
	cliStreamReconnectDelay = 10 * time.Millisecond
	t.Cleanup(func() {
		cliRESTTimeout = originalREST
		cliStreamIdleTimeout = originalIdle
		cliStreamFollowMaxDuration = originalWindow
		cliStreamReconnectDelay = originalDelay
	})
}

func TestStreamOperationEventsIdleReconnectsWhileOperationRuns(t *testing.T) {
	setStreamFollowTestTimings(t)
	var mu sync.Mutex
	streams := 0
	statusPolls := 0
	var afters []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1alpha/operations/op_1":
			mu.Lock()
			statusPolls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"op_1","kind":"deploy","state":"RUNNING"}`))
		case "/api/v1alpha/operations/op_1/events":
			mu.Lock()
			streams++
			attempt := streams
			afters = append(afters, r.URL.Query().Get("after"))
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			if attempt == 1 {
				_, _ = w.Write([]byte("id: 3\nevent: operation\ndata: {\"seq\":3,\"state\":\"RUNNING\",\"message\":\"building\"}\n\n"))
				flusher.Flush()
				// Go quiet for longer than the idle timeout while the build runs.
				time.Sleep(400 * time.Millisecond)
				return
			}
			_, _ = w.Write([]byte("data: {\"seq\":4,\"state\":\"SUCCEEDED\",\"message\":\"done\"}\n\n"))
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	if code := streamOperationEvents(server.URL, "", "op_1", &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if streams != 2 || statusPolls != 1 {
		t.Fatalf("streams=%d statusPolls=%d, want one reconnect after one status confirmation", streams, statusPolls)
	}
	if afters[0] != "" || afters[1] != "3" {
		t.Fatalf("after cursors = %q, want reconnect from the last sequence", afters)
	}
	if !strings.Contains(stdout.String(), "building") || !strings.Contains(stdout.String(), "deploy succeeded") {
		t.Fatalf("stdout = %q, want events and success", stdout.String())
	}
	if !strings.Contains(stderr.String(), "still RUNNING") || !strings.Contains(stderr.String(), "midnight deploy events --operation-id op_1") {
		t.Fatalf("stderr = %q, want a reconnect hint", stderr.String())
	}
	if strings.Contains(stderr.String(), "failed") {
		t.Fatalf("stderr = %q, want no failure report for a running build", stderr.String())
	}
}

func TestStreamOperationEventsIdleReportsConfirmedFailure(t *testing.T) {
	setStreamFollowTestTimings(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1alpha/operations/op_1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"op_1","kind":"deploy","state":"FAILED"}`))
		case "/api/v1alpha/operations/op_1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"seq\":1,\"state\":\"RUNNING\",\"message\":\"building\"}\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(400 * time.Millisecond)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	if code := streamOperationEvents(server.URL, "", "op_1", &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d, want 1 for a confirmed failure; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "deploy failed") {
		t.Fatalf("stderr = %q, want failure report", stderr.String())
	}
}

func TestStreamOperationEventsStopsAtFollowWindowWithoutFailing(t *testing.T) {
	setStreamFollowTestTimings(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1alpha/operations/op_1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"op_1","kind":"deploy","state":"RUNNING"}`))
		case "/api/v1alpha/operations/op_1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			// Never emit anything; the follow window must bound the wait.
			time.Sleep(time.Second)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	execution := NewCLIExecutionContext(nil, io.Discard, io.Discard)
	execution.Options.Timeout = 500 * time.Millisecond
	var stdout, stderr bytes.Buffer
	output := &cliOutputWriter{Writer: &stdout, execution: execution}
	started := time.Now()
	code := streamOperationEvents(server.URL, "", "op_1", output, &stderr)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("follow took %s, want the --timeout window to bound it", elapsed)
	}
	if code != 0 {
		t.Fatalf("code=%d, want 0 while the operation is still running; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "still RUNNING after 500ms") || !strings.Contains(stderr.String(), "midnight deploy events --operation-id op_1") {
		t.Fatalf("stderr = %q, want a bounded-follow hint", stderr.String())
	}
}
