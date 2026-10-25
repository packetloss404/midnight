// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/midnight/midnight/internal/cliconfig"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

const (
	upUploadTimeout       = 5 * time.Minute
	maxUpResourceIDBytes  = 1024
	maxUpEnvironmentBytes = 255
	maxUpCommitMessageLen = 256
	maxUpResponseBytes    = 1 << 20
)

// Compatibility alias for the former upload-only redirect sentinel. Uploads
// and streams now share one credential-safe redirect policy.
var errUpRedirect = errCLIRedirect

func runUp(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineUpFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight up [--service-id SLUG_OR_ID] [--environment-id ENV_ID] [--message TEXT] [--idempotency-key KEY] [--detach]")
		return 64
	}
	envOverrideSupplied := flagSupplied(fs, flagEnvironmentID)
	serviceOverrideSupplied := flagSupplied(fs, flagServiceID)
	idempotencyKeySupplied := flagSupplied(fs, "idempotency-key")

	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return cliConfigExitCode(err)
	}
	serviceID := strings.TrimSpace(link.ServiceID)
	if serviceID == "" {
		fmt.Fprintln(stderr, "midnight up: .midnight/project.json is incomplete; re-run `midnight link`")
		return 1
	}
	environmentID := strings.TrimSpace(link.EnvironmentID)
	if envOverrideSupplied {
		override := strings.TrimSpace(*f.envOverride)
		if override == "" {
			fmt.Fprintln(stderr, "midnight up: --environment-id requires a non-empty environment id")
			return 1
		}
		environmentID = override
	}
	if environmentID == "" {
		fmt.Fprintln(stderr, "midnight up: .midnight/project.json has no environment_id; re-run `midnight link` or pass --environment-id <environment-id>")
		return 1
	}
	commitMessage := strings.TrimSpace(*f.message)
	if err := validateUpRequestValue("service id", serviceID, true, maxUpResourceIDBytes); err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return 1
	}
	if err := validateUpRequestValue("environment id", environmentID, true, maxUpEnvironmentBytes); err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return 1
	}
	if err := validateUpRequestValue("commit message", commitMessage, false, maxUpCommitMessageLen); err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return 1
	}
	serviceSelection := strings.TrimSpace(*f.serviceOverride)
	if serviceOverrideSupplied {
		if serviceSelection == "" {
			fmt.Fprintln(stderr, "midnight up: --service-id requires a non-empty service slug or id")
			return 1
		}
		if err := validateUpRequestValue("service override", serviceSelection, true, maxUpResourceIDBytes); err != nil {
			fmt.Fprintf(stderr, "midnight up: %s\n", err)
			return 1
		}
		if strings.TrimSpace(link.ProjectID) == "" {
			fmt.Fprintln(stderr, "midnight up: --service-id requires a linked project id; re-run `midnight link`")
			return 1
		}
	}
	if idempotencyKeySupplied {
		if err := idempotency.ValidateKey(idempotency.Key(*f.explicitIdempotencyKey)); err != nil {
			fmt.Fprintln(stderr, "midnight up: --idempotency-key must be 1..255 visible ASCII characters without whitespace")
			return 1
		}
	}

	credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{
		APIBase:        *f.apiBase,
		Token:          *f.explicitToken,
		ProjectAPIBase: link.APIBase,
	})
	if err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return cliConfigExitCode(err)
	}
	if err := validateUpCredentialAuthority(credentials); err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return 1
	}
	base := credentials.APIBase
	token := credentials.Token

	if serviceOverrideSupplied {
		// If the override looks like a slug, resolve it.
		client := &devAPIClient{
			base:   base,
			token:  token,
			client: upNoRedirectHTTPClient(upUploadTimeout),
		}
		services, err := client.listServices(link.ProjectID)
		if err != nil {
			fmt.Fprintf(
				stderr,
				"midnight up: resolve override service: %s\n",
				redactUpCredential(err.Error(), token),
			)
			return 1
		}
		match, err := pickService(services, serviceSelection, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "midnight up: %s\n", err)
			return 1
		}
		serviceID = strings.TrimSpace(match.ID)
		if err := validateUpRequestValue("resolved service id", serviceID, true, maxUpResourceIDBytes); err != nil {
			fmt.Fprintf(stderr, "midnight up: %s\n", err)
			return 1
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return 1
	}
	ignores, err := loadIgnorePatterns(cwd)
	if err != nil {
		fmt.Fprintf(stderr, "midnight up: load ignore patterns: %s\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "midnight up: packaging %s\n", cwd)
	tmp, err := os.CreateTemp("", "midnight-up-*.tar.gz")
	if err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return 1
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	archiveHasher := sha256.New()
	stats, err := writeGzippedTarball(cwd, ignores, io.MultiWriter(tmp, archiveHasher))
	if err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return 1
	}
	if stats.Files == 0 {
		fmt.Fprintln(stderr, "midnight up: no regular source files remain after applying ignore rules")
		return 1
	}
	if stats.BytesGzipped <= 0 {
		fmt.Fprintln(stderr, "midnight up: prepared archive is empty")
		return 1
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "midnight up: %d files, %s gzipped, uploading...\n", stats.Files, humanBytes(stats.BytesGzipped))

	idempotencyKey := *f.explicitIdempotencyKey
	archiveDigest := fmt.Sprintf("sha256:%x", archiveHasher.Sum(nil))
	if !idempotencyKeySupplied {
		idempotencyKey = deterministicUpIdempotencyKey(
			archiveHasher.Sum(nil),
			serviceID,
			environmentID,
			commitMessage,
		)
	}

	uploadCtx, cancelUpload := context.WithTimeout(context.Background(), upUploadTimeout)
	defer cancelUpload()
	req, err := http.NewRequestWithContext(
		uploadCtx,
		http.MethodPost,
		base+"/api/v1alpha/services/"+url.PathEscape(serviceID)+"/up",
		tmp,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	req.Header.Set("X-Midnight-Archive-Digest", archiveDigest)
	// Let the server finish authentication and target preflight before the
	// transport sends a potentially large archive. Go's server emits
	// 100 Continue only when the accepted handler first reads the body.
	req.Header.Set("Expect", "100-continue")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	if commitMessage != "" {
		req.Header.Set("X-Midnight-Commit-Message", commitMessage)
	}
	req.Header.Set("X-Midnight-Environment", environmentID)
	req.ContentLength = stats.BytesGzipped

	httpClient := upNoRedirectHTTPClient(upUploadTimeout)
	res, body, err := executeCLIRequest(httpClient, req, maxUpResponseBytes)
	if err != nil {
		var tooLarge *cliResponseTooLargeError
		if errors.As(err, &tooLarge) {
			fmt.Fprintf(stderr, "midnight up: server response exceeds %d bytes\n", maxUpResponseBytes)
			return 1
		}
		fmt.Fprintf(stderr, "midnight up: %s\n", err)
		return 1
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		detail := safeHTTPText(redactUpCredential(strings.TrimSpace(string(body)), token))
		retryHint := upRetryAfterHint(res)
		if detail == "" {
			fmt.Fprintf(stderr, "midnight up: %s%s\n", res.Status, retryHint)
		} else {
			fmt.Fprintf(stderr, "midnight up: %s%s: %s\n", res.Status, retryHint, detail)
		}
		return 1
	}

	operationID, err := decodeUpAcceptedResponse(body, token)
	if err != nil {
		fmt.Fprintf(stderr, "midnight up: invalid accepted response envelope: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "midnight up: queued operation %s\n", operationID)
	fmt.Fprintf(stdout, "midnight up: follow tenant-safe events with `midnight deploy events --operation-id %s`\n", operationID)
	if *f.detach {
		return 0
	}
	if code := streamOperationEvents(base, token, operationID, stdout, stderr); code != 0 {
		return code
	}
	return 0
}

type upFlags struct {
	apiBase                *string
	explicitToken          *string
	serviceOverride        *string
	envOverride            *string
	detach                 *bool
	message                *string
	explicitIdempotencyKey *string
}

func defineUpFlags(fs *flag.FlagSet) upFlags {
	return upFlags{
		apiBase:                cliAPIBaseFlag(fs, "Midnight API base URL (overrides environment, project link, and stored server)"),
		explicitToken:          fs.String("token", "", "Midnight API bearer token (overrides environment and stored credential)"),
		serviceOverride:        cliServiceIDFlag(fs, "override the linked service slug or id"),
		envOverride:            cliEnvironmentIDFlag(fs, "override the linked environment id"),
		detach:                 fs.Bool("detach", false, "exit after the deploy is queued instead of streaming events"),
		message:                fs.String("message", "", "commit-style message attached to the deploy"),
		explicitIdempotencyKey: fs.String("idempotency-key", "", "opaque retry key (generated deterministically from the upload intent when omitted)"),
	}
}

func upRetryAfterHint(response *http.Response) string {
	if response == nil ||
		(response.StatusCode != http.StatusConflict &&
			response.StatusCode != http.StatusTooManyRequests) {
		return ""
	}
	values := response.Header.Values("Retry-After")
	if len(values) != 1 {
		return ""
	}
	seconds, err := strconv.ParseUint(strings.TrimSpace(values[0]), 10, 31)
	if err != nil || seconds == 0 {
		return ""
	}
	return fmt.Sprintf(" (retry after %ds)", seconds)
}

func decodeUpAcceptedResponse(body []byte, token string) (string, error) {
	var response struct {
		OperationID  string          `json:"operation_id"`
		Operation    json.RawMessage `json:"operation"`
		DeploymentID string          `json:"deployment_id"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	operationID := strings.TrimSpace(response.OperationID)
	if operationID == "" {
		return "", errors.New("operation_id is required")
	}
	if operationID != response.OperationID {
		return "", errors.New("operation_id must not contain surrounding whitespace")
	}
	if err := validateUpRequestValue("operation_id", operationID, true, maxUpResourceIDBytes); err != nil {
		return "", err
	}

	deploymentID := strings.TrimSpace(response.DeploymentID)
	if deploymentID == "" {
		return "", errors.New("deployment_id is required")
	}
	if deploymentID != response.DeploymentID {
		return "", errors.New("deployment_id must not contain surrounding whitespace")
	}
	if err := validateUpRequestValue("deployment_id", deploymentID, true, maxUpResourceIDBytes); err != nil {
		return "", err
	}

	rawOperation := strings.TrimSpace(string(response.Operation))
	if rawOperation == "" || !strings.HasPrefix(rawOperation, "{") {
		return "", errors.New("operation object is required")
	}
	var operation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Operation, &operation); err != nil {
		return "", errors.New("operation must be an object")
	}
	if operation.ID == "" || operation.ID != operationID {
		return "", errors.New("operation.id must equal operation_id")
	}

	if trimmedToken := strings.TrimSpace(token); trimmedToken != "" &&
		(strings.Contains(operationID, trimmedToken) ||
			strings.Contains(deploymentID, trimmedToken)) {
		return "", errors.New("response identifiers are invalid")
	}
	return operationID, nil
}

func validateUpCredentialAuthority(credentials cliconfig.Credentials) error {
	switch credentials.Kind {
	case cliconfig.CredentialKindHumanSession:
		if !strings.HasPrefix(strings.TrimSpace(credentials.Token), "midn_user_") {
			return errors.New("stored human-session credential is malformed; run `midnight login` again")
		}
		return nil
	case cliconfig.CredentialKindOperatorRecovery, cliconfig.CredentialKindExternal:
		// The operator/developer compatibility route remains available.
		// midn_sa_ credentials resolve as external (or legacy stored recovery)
		// and are explicitly admitted by the server's ci-deploy-v1 registry.
		return nil
	default:
		return fmt.Errorf("%q credentials cannot upload source; sign in with `midnight login` or use a project deploy token", credentials.Kind)
	}
}

func validateUpRequestValue(name, value string, required bool, maxBytes int) error {
	if required && value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains control characters", name)
		}
	}
	return nil
}

func deterministicUpIdempotencyKey(
	archiveDigest []byte,
	serviceID,
	environmentID,
	commitMessage string,
) string {
	hasher := sha256.New()
	for _, component := range []string{
		"midnight-up-intent-v1",
		fmt.Sprintf("%x", archiveDigest),
		serviceID,
		environmentID,
		commitMessage,
	} {
		_, _ = fmt.Fprintf(hasher, "%d:", len(component))
		_, _ = io.WriteString(hasher, component)
		_, _ = hasher.Write([]byte{0})
	}
	return fmt.Sprintf("midnight-up-v1-%x", hasher.Sum(nil))
}

func upNoRedirectHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}
	// DefaultTransport waits only one second before sending an Expect:
	// 100-continue body. Use the request's full timeout so an authorization
	// preflight never turns into an unsolicited archive upload merely because
	// the database took slightly longer than one second.
	transport.ExpectContinueTimeout = timeout
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: rejectCLIRedirect,
	}
}

func redactUpCredential(value, token string) string {
	if trimmedToken := strings.TrimSpace(token); trimmedToken != "" {
		return strings.ReplaceAll(value, trimmedToken, "[REDACTED]")
	}
	return value
}

type tarballStats struct {
	Files        int
	BytesGzipped int64
}

func writeGzippedTarball(root string, ignores ignoreSet, w io.Writer) (tarballStats, error) {
	return writeGzippedTarballWithLimits(root, ignores, w, sourceartifact.DefaultLimits)
}

func writeGzippedTarballWithLimits(
	root string,
	ignores ignoreSet,
	w io.Writer,
	limits sourceartifact.Limits,
) (tarballStats, error) {
	return writeGzippedTarballWithLimitsAndHooks(
		root,
		ignores,
		w,
		limits,
		upArchiveWalkHooks{},
	)
}

// upArchiveWalkHooks is an injection seam for deterministic filesystem-race
// tests. Production callers always use the zero value.
type upArchiveWalkHooks struct {
	afterRootLstat  func() error
	afterEntryLstat func(relativePath string) error
}

func writeGzippedTarballWithLimitsAndHooks(
	root string,
	ignores ignoreSet,
	w io.Writer,
	limits sourceartifact.Limits,
	hooks upArchiveWalkHooks,
) (tarballStats, error) {
	stats := tarballStats{}
	if limits.MaxCompressedBytes <= 0 ||
		limits.MaxEntries <= 0 ||
		limits.MaxUncompressedBytes <= 0 {
		return stats, errors.New("source archive limits must be positive")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return stats, err
	}
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil {
		return stats, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return stats, errors.New("source root must be a directory, not a symbolic link")
	}
	if hooks.afterRootLstat != nil {
		if err := hooks.afterRootLstat(); err != nil {
			return stats, err
		}
	}
	archiveRoot, err := os.OpenRoot(rootAbs)
	if err != nil {
		return stats, err
	}
	defer archiveRoot.Close()
	openedRootInfo, err := archiveRoot.Stat(".")
	if err != nil {
		return stats, err
	}
	if !openedRootInfo.IsDir() || !os.SameFile(rootInfo, openedRootInfo) {
		return stats, errors.New("source root changed while preparing upload")
	}

	compressed := &boundedUpArchiveWriter{
		w:     w,
		limit: limits.MaxCompressedBytes,
		kind:  "compressed",
	}
	gz := gzip.NewWriter(compressed)
	expanded := &boundedUpArchiveWriter{
		w:     gz,
		limit: limits.MaxUncompressedBytes,
		kind:  "expanded",
	}
	tw := tar.NewWriter(expanded)
	walker := upArchiveWalker{
		ignores: ignores,
		limits:  limits,
		hooks:   hooks,
		writer:  tw,
		stats:   &stats,
	}
	err = walker.walkDirectory(archiveRoot, rootInfo, "")
	if err != nil {
		return stats, err
	}
	if err := tw.Close(); err != nil {
		return stats, err
	}
	if err := gz.Close(); err != nil {
		return stats, err
	}
	stats.BytesGzipped = compressed.n
	return stats, nil
}

type upArchiveWalker struct {
	ignores    ignoreSet
	limits     sourceartifact.Limits
	hooks      upArchiveWalkHooks
	writer     *tar.Writer
	stats      *tarballStats
	entryCount int
}

func (w *upArchiveWalker) walkDirectory(
	directoryRoot *os.Root,
	expectedDirectory os.FileInfo,
	relativeDirectory string,
) error {
	entries, err := readStableUpArchiveDirectory(directoryRoot, expectedDirectory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		relativePath := name
		if relativeDirectory != "" {
			relativePath = relativeDirectory + "/" + name
		}

		info, err := directoryRoot.Lstat(name)
		if err != nil {
			return fmt.Errorf("inspect source path %q: %w", relativePath, err)
		}
		if w.hooks.afterEntryLstat != nil {
			if err := w.hooks.afterEntryLstat(relativePath); err != nil {
				return err
			}
		}
		if w.ignores.matches(relativePath, info.IsDir()) {
			continue
		}
		// Lstat is deliberate: links are never archive entries and are never
		// followed. The stable-open checks below also reject a link swapped
		// into place after this check.
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := sourceartifact.ValidateArchivePath(relativePath); err != nil {
			return fmt.Errorf("invalid source path %q: %w", relativePath, err)
		}
		if !supportedUpArchiveMode(info.Mode()) {
			return fmt.Errorf("unsupported source file type at %s", relativePath)
		}

		switch {
		case info.IsDir():
			childRoot, openedInfo, err := openStableUpArchiveDirectory(
				directoryRoot,
				name,
				info,
			)
			if err != nil {
				return fmt.Errorf("open source directory %q: %w", relativePath, err)
			}
			if err := w.writeHeader(relativePath+"/", openedInfo); err != nil {
				_ = childRoot.Close()
				return err
			}
			walkErr := w.walkDirectory(childRoot, openedInfo, relativePath)
			closeErr := childRoot.Close()
			if walkErr != nil {
				return walkErr
			}
			if closeErr != nil {
				return fmt.Errorf("close source directory %q: %w", relativePath, closeErr)
			}
		case info.Mode().IsRegular():
			file, openedInfo, err := openStableUpArchiveFile(
				directoryRoot,
				name,
				info,
			)
			if err != nil {
				return fmt.Errorf("open source file %q: %w", relativePath, err)
			}
			if err := w.writeHeader(relativePath, openedInfo); err != nil {
				_ = file.Close()
				return err
			}
			_, copyErr := io.CopyN(w.writer, file, openedInfo.Size())
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("read source file %q: %w", relativePath, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close source file %q: %w", relativePath, closeErr)
			}
			w.stats.Files++
		}
	}
	return nil
}

func (w *upArchiveWalker) writeHeader(name string, info os.FileInfo) error {
	w.entryCount++
	if w.entryCount > w.limits.MaxEntries {
		return fmt.Errorf("source archive entry count exceeds %d", w.limits.MaxEntries)
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = name
	if err := w.writer.WriteHeader(header); err != nil {
		return err
	}
	return nil
}

func readStableUpArchiveDirectory(
	directoryRoot *os.Root,
	expected os.FileInfo,
) ([]os.DirEntry, error) {
	directory, err := directoryRoot.Open(".")
	if err != nil {
		return nil, err
	}
	opened, err := directory.Stat()
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	if expected == nil ||
		!expected.IsDir() ||
		!opened.IsDir() ||
		!os.SameFile(expected, opened) {
		_ = directory.Close()
		return nil, errors.New("source directory changed while preparing upload")
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	return entries, nil
}

func openStableUpArchiveDirectory(
	parent *os.Root,
	name string,
	expected os.FileInfo,
) (*os.Root, os.FileInfo, error) {
	directoryRoot, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := directoryRoot.Stat(".")
	if err != nil {
		_ = directoryRoot.Close()
		return nil, nil, err
	}
	if expected == nil ||
		!expected.IsDir() ||
		!opened.IsDir() ||
		!os.SameFile(expected, opened) {
		_ = directoryRoot.Close()
		return nil, nil, errors.New("source directory changed while preparing upload")
	}
	return directoryRoot, opened, nil
}

func supportedUpArchiveMode(mode os.FileMode) bool {
	return mode.IsDir() || mode.IsRegular()
}

// openStableUpArchiveFile prevents a file that was accepted by Lstat from
// being replaced with a symlink or different inode before its bytes are read.
// The parent Root and opened descriptor, rather than a mutable filesystem
// path, remain authoritative after this check.
func openStableUpArchiveFile(
	parent *os.Root,
	name string,
	expected os.FileInfo,
) (*os.File, os.FileInfo, error) {
	file, err := parent.Open(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if expected == nil ||
		!opened.Mode().IsRegular() ||
		!os.SameFile(expected, opened) {
		_ = file.Close()
		return nil, nil, errors.New("source file changed while preparing upload")
	}
	return file, opened, nil
}

type boundedUpArchiveWriter struct {
	w     io.Writer
	limit int64
	n     int64
	kind  string
}

func (w *boundedUpArchiveWriter) Write(payload []byte) (int, error) {
	if int64(len(payload)) > w.limit-w.n {
		return 0, fmt.Errorf("source archive %s bytes exceed %d", w.kind, w.limit)
	}
	n, err := w.w.Write(payload)
	w.n += int64(n)
	return n, err
}

type ignoreSet struct {
	matcher       *patternmatcher.PatternMatcher
	safetyMatcher *patternmatcher.PatternMatcher
}

func loadIgnorePatterns(root string) (ignoreSet, error) {
	safetyPatterns := []string{
		".git",
		".git/**",
		"**/.git",
		"**/.git/**",
		".midnight",
		".midnight/**",
		"**/.midnight",
		"**/.midnight/**",
	}
	patterns := append([]string(nil), safetyPatterns...)
	patterns = append(patterns,
		"node_modules",
		"node_modules/**",
		"**/node_modules",
		"**/node_modules/**",
		".DS_Store",
		"**/.DS_Store",
	)
	for _, file := range []string{".gitignore", ".midnightignore"} {
		path := filepath.Join(root, file)
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return ignoreSet{}, err
		}
		filePatterns, readErr := ignorefile.ReadAll(f)
		closeErr := f.Close()
		if readErr != nil {
			return ignoreSet{}, fmt.Errorf("parse %s: %w", file, readErr)
		}
		if closeErr != nil {
			return ignoreSet{}, fmt.Errorf("close %s: %w", file, closeErr)
		}
		patterns = append(patterns, expandBasenameIgnorePatterns(filePatterns)...)
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		return ignoreSet{}, fmt.Errorf("compile ignore patterns: %w", err)
	}
	safetyMatcher, err := patternmatcher.New(safetyPatterns)
	if err != nil {
		return ignoreSet{}, fmt.Errorf("compile protected ignore patterns: %w", err)
	}
	return ignoreSet{matcher: matcher, safetyMatcher: safetyMatcher}, nil
}

// expandBasenameIgnorePatterns preserves Git's "match in any directory"
// behavior for patterns without a slash. patternmatcher intentionally follows
// Docker-context semantics, where a bare glob only matches the context root.
func expandBasenameIgnorePatterns(patterns []string) []string {
	expanded := make([]string, 0, len(patterns)*2)
	for _, pattern := range patterns {
		expanded = append(expanded, pattern)
		body := strings.TrimPrefix(pattern, "!")
		if strings.Contains(body, "/") || body == "" {
			continue
		}
		prefix := ""
		if strings.HasPrefix(pattern, "!") {
			prefix = "!"
		}
		expanded = append(expanded, prefix+"**/"+body)
	}
	return expanded
}

func (i ignoreSet) matches(slashedPath string, isDir bool) bool {
	if i.safetyMatcher != nil {
		protected, err := i.safetyMatcher.MatchesOrParentMatches(filepath.FromSlash(slashedPath))
		if err != nil {
			return true
		}
		if protected {
			return true
		}
	}
	if i.matcher == nil {
		return false
	}
	matched, err := i.matcher.MatchesOrParentMatches(filepath.FromSlash(slashedPath))
	return err == nil && matched
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func streamOperationEvents(apiBase, token, operationID string, stdout, stderr io.Writer) int {
	return streamOperationEventsForCommand(
		apiBase,
		token,
		operationID,
		"midnight up",
		"deploy",
		stdout,
		stderr,
	)
}

// Streaming a long build can legitimately go quiet for longer than the SSE
// idle timeout. When the stream idles or closes without a terminal state the
// CLI confirms the operation's state over REST and reconnects from the last
// cursor instead of reporting a failure. Both values are variables so tests
// can shorten them.
var (
	cliStreamFollowMaxDuration = 30 * time.Minute
	cliStreamReconnectDelay    = 2 * time.Second
)

type operationStreamResult struct {
	terminal     bool
	code         int
	lastState    string
	lastSequence int64
	reason       string
}

func streamOperationEventsForCommand(
	apiBase,
	token,
	operationID,
	command,
	subject string,
	stdout,
	stderr io.Writer,
) int {
	operationID = strings.TrimSpace(operationID)
	if err := validateUpRequestValue("operation id", operationID, true, maxUpResourceIDBytes); err != nil {
		fmt.Fprintf(stderr, "%s: %s\n", command, err)
		return 1
	}
	window := cliStreamFollowMaxDuration
	if wrapped, ok := stdout.(*cliOutputWriter); ok && wrapped.execution != nil && wrapped.execution.Options.Timeout > 0 {
		window = wrapped.execution.Options.Timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	followHint := fmt.Sprintf("follow with `midnight deploy events --operation-id %s`", operationID)

	var after int64
	var lastHint string
	for {
		result := streamOperationEventsOnce(ctx, apiBase, token, operationID, after, command, subject, stdout, stderr)
		if result.terminal {
			return result.code
		}
		if result.lastSequence > after {
			after = result.lastSequence
		}
		state, err := fetchOperationState(apiBase, token, operationID)
		if err != nil {
			detail := result.reason
			if result.lastState != "" {
				detail += fmt.Sprintf(" (last state=%s)", result.lastState)
			}
			fmt.Fprintf(stderr, "%s: %s; could not confirm the operation state: %s\n", command, detail, err)
			return 1
		}
		switch state {
		case "SUCCEEDED":
			fmt.Fprintf(stdout, "%s: %s succeeded\n", command, subject)
			return 0
		case "FAILED":
			fmt.Fprintf(stderr, "%s: %s failed\n", command, subject)
			return 1
		case "CANCELED":
			fmt.Fprintf(stderr, "%s: %s canceled\n", command, subject)
			return 1
		}
		if ctx.Err() != nil {
			fmt.Fprintf(stderr, "%s: operation %s is still %s after %s; %s\n", command, operationID, state, window, followHint)
			return 0
		}
		if hint := result.reason + state; hint != lastHint {
			// Print once per distinct interruption so a server that closes
			// idle streams quickly does not flood stderr while reconnecting.
			lastHint = hint
			fmt.Fprintf(stderr, "%s: %s; operation %s is still %s, reconnecting (%s)\n", command, result.reason, operationID, state, followHint)
		}
		select {
		case <-ctx.Done():
		case <-time.After(cliStreamReconnectDelay):
		}
	}
}

// streamOperationEventsOnce follows one SSE connection until a terminal state
// (terminal=true) or an interruption such as an idle timeout, a closed
// stream, or the follow window elapsing (terminal=false with reason set).
func streamOperationEventsOnce(
	ctx context.Context,
	apiBase,
	token,
	operationID string,
	after int64,
	command,
	subject string,
	stdout,
	stderr io.Writer,
) operationStreamResult {
	failed := func(code int) operationStreamResult { return operationStreamResult{terminal: true, code: code} }
	target := apiBase + "/api/v1alpha/operations/" + url.PathEscape(operationID) + "/events?stream=sse"
	if after > 0 {
		target += "&after=" + strconv.FormatInt(after, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %s\n", command, err)
		return failed(1)
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	req.Header.Set("Accept", "text/event-stream")
	res, err := executeCLIStream(authHTTPClient, req)
	if err != nil {
		// The follow window can expire while reconnecting or waiting for
		// response headers, before a scanner exists. Confirm the operation
		// over REST just as we do when the deadline interrupts its body.
		if ctx.Err() != nil {
			return operationStreamResult{lastSequence: after, reason: "follow window elapsed"}
		}
		fmt.Fprintf(stderr, "%s: %s\n", command, err)
		return failed(1)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, readErr := readBoundedCLIResponse(http.MethodGet, req.URL.RequestURI(), res.Body, maxCLIErrorResponseBytes)
		if readErr != nil {
			fmt.Fprintf(stderr, "%s: stream events: %s\n", command, readErr)
			return failed(1)
		}
		fmt.Fprintf(stderr, "%s: stream events: %s\n", command,
			apiStatusError(http.MethodGet, req.URL.RequestURI(), res.Status, body))
		return failed(1)
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	result := operationStreamResult{lastSequence: after}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		for _, key := range []string{"seq", "next_sequence"} {
			if sequence, ok := event[key].(float64); ok && int64(sequence) > result.lastSequence {
				result.lastSequence = int64(sequence)
			}
		}
		if msg, ok := event["message"].(string); ok && strings.TrimSpace(msg) != "" {
			fmt.Fprintln(stdout, msg)
		}
		if state, ok := event["state"].(string); ok {
			result.lastState = state
			switch state {
			case "SUCCEEDED":
				fmt.Fprintf(stdout, "%s: %s succeeded\n", command, subject)
				return failed(0)
			case "FAILED":
				fmt.Fprintf(stderr, "%s: %s failed\n", command, subject)
				return failed(1)
			case "CANCELED":
				fmt.Fprintf(stderr, "%s: %s canceled\n", command, subject)
				return failed(1)
			}
		}
	}
	var idle *cliStreamIdleError
	switch err := scanner.Err(); {
	case err == nil:
		result.reason = "event stream closed before a terminal state"
	case errors.As(err, &idle):
		result.reason = fmt.Sprintf("no events for %s", idle.Timeout)
	case ctx.Err() != nil:
		result.reason = "follow window elapsed"
	default:
		fmt.Fprintf(stderr, "%s: %s\n", command, err)
		return failed(1)
	}
	return result
}

// fetchOperationState reads the operation's current state over REST so a
// quiet or interrupted event stream is never mistaken for a failed deploy.
func fetchOperationState(apiBase, token, operationID string) (string, error) {
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		apiBase+"/api/v1alpha/operations/"+url.PathEscape(operationID),
		nil,
	)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	req.Header.Set("Accept", "application/json")
	res, body, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", apiStatusError(http.MethodGet, req.URL.RequestURI(), res.Status, body)
	}
	var operation struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &operation); err != nil {
		return "", fmt.Errorf("decode operation: %w", err)
	}
	state := strings.ToUpper(strings.TrimSpace(operation.State))
	if state == "" {
		return "", errors.New("operation response has no state")
	}
	return state, nil
}
