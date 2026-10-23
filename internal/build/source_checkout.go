// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/midnight/midnight/internal/sourceartifact"
)

// SourceCheckoutPlanner materializes a SourceRef on disk so a Builder can run
// over it. Production callers shell out to `git clone --depth 1 --branch
// <ref>`; tests pass a fake CommandRunner.
type SourceCheckoutPlanner struct {
	// Root is the directory under which clones are created. Each checkout
	// lands in <Root>/<ServiceID>-<sha-hash> for partial determinism.
	Root string
	// ArtifactRoot is the API/build-worker shared source artifact root.
	// sourceartifact:// refs are verified and materialized from this store into
	// a unique per-attempt checkout.
	ArtifactRoot string
	// GitBinary is the path to the pinned git binary. Empty means `git` on
	// PATH.
	GitBinary string
	// Runner is the seam tests use to substitute a fake `git` binary.
	Runner CommandRunner
	// SkipClone, when true, treats SourceRef.Repo as a local directory path
	// and copies it instead of running git. Used by integration tests that
	// stage a local sample repo.
	SkipClone bool
	// Token, when non-empty, authenticates the git clone via an
	// `http.extraHeader: Authorization: Bearer <token>` config injected through
	// the environment (GIT_CONFIG_COUNT / GIT_CONFIG_KEY_0 / GIT_CONFIG_VALUE_0)
	// rather than being embedded in the clone URL. Keeping the credential out of
	// the URL prevents it from surfacing in git's own error/stderr output. The
	// token value is additionally redacted from any error text this planner
	// returns. It only takes effect on the production exec path (Runner == nil).
	Token string
}

// ErrEmbeddedCredentials is returned when a repository URL carries inline
// credentials (userinfo). Such URLs are rejected because the credential would
// otherwise be echoed verbatim in git's stderr on failure.
var ErrEmbeddedCredentials = errors.New("source checkout: repository url must not embed credentials")

// LocalSourceScheme is the URL scheme reserved for `midnight up` payloads
// staged by the API. A SourceRef whose Repo starts with this scheme is
// treated as already on-disk and never cloned. The API tarball-intake
// handler rewrites raw filesystem paths to this scheme so the build worker
// can route on it without parsing platform-specific paths.
const LocalSourceScheme = "local://"

// IsLocalSourceURI reports whether the supplied repo string identifies a
// pre-extracted local checkout staged by `midnight up`.
func IsLocalSourceURI(uri string) bool {
	return strings.HasPrefix(strings.TrimSpace(uri), LocalSourceScheme)
}

// LocalSourcePath returns the on-disk path embedded in a `local://` URI, or
// the empty string when the URI is not a local source ref.
func LocalSourcePath(uri string) string {
	trimmed := strings.TrimSpace(uri)
	if !IsLocalSourceURI(trimmed) {
		return ""
	}
	return strings.TrimPrefix(trimmed, LocalSourceScheme)
}

// Checkout materializes the SourceRef under Root. The returned SourceCheckout
// is owned by the caller; Discard removes the clone directory.
//
// Three modes are supported:
//
//  1. Git clone (default) — clones SourceRef.Repo into a fresh temp dir under
//     Root and checks out SourceRef.Ref.
//  2. SkipClone — copies SourceRef.Repo (a local directory) into a fresh temp
//     dir. Used by integration tests that stage a sample repo.
//  3. `local://` URI — used by `midnight up`. The API has already extracted
//     the operator's tarball to the path embedded in the URI; the planner
//     verifies the path exists and returns it directly without copying. The
//     RepoDir is owned by the API extraction temp directory; Discard is a
//     no-op in this mode.
func (p SourceCheckoutPlanner) Checkout(ctx context.Context, serviceID string, ref SourceRef) (SourceCheckout, error) {
	if strings.HasPrefix(strings.TrimSpace(ref.Repo), "sourceartifact://") {
		return p.checkoutSourceArtifact(ctx, serviceID, ref)
	}
	if IsLocalSourceURI(ref.Repo) {
		return p.checkoutLocalURI(serviceID, ref)
	}
	if err := ref.Validate(); err != nil {
		return SourceCheckout{}, err
	}
	if strings.TrimSpace(p.Root) == "" {
		return SourceCheckout{}, errors.New("source checkout planner: root is required")
	}
	if err := os.MkdirAll(p.Root, 0o750); err != nil {
		return SourceCheckout{}, fmt.Errorf("source checkout: create root: %w", err)
	}
	dest, err := os.MkdirTemp(p.Root, sanitizeServiceID(serviceID)+"-")
	if err != nil {
		return SourceCheckout{}, fmt.Errorf("source checkout: temp dir: %w", err)
	}

	if p.SkipClone {
		if err := copyDir(ref.Repo, dest); err != nil {
			_ = os.RemoveAll(dest)
			return SourceCheckout{}, fmt.Errorf("source checkout: local copy: %w", err)
		}
	} else if strings.TrimSpace(ref.FetchRef) != "" {
		if err := p.gitFetchPinned(ctx, ref, dest); err != nil {
			_ = os.RemoveAll(dest)
			return SourceCheckout{}, err
		}
	} else {
		if err := p.gitClone(ctx, ref, dest); err != nil {
			_ = os.RemoveAll(dest)
			return SourceCheckout{}, err
		}
	}

	work := dest
	if strings.TrimSpace(ref.Subpath) != "" {
		work = filepath.Join(dest, ref.Subpath)
		info, statErr := os.Stat(work)
		if statErr != nil || !info.IsDir() {
			_ = os.RemoveAll(dest)
			return SourceCheckout{}, fmt.Errorf("source checkout: subpath %q is not a directory in checkout", ref.Subpath)
		}
	}

	commit, _ := readHeadCommit(dest)
	return SourceCheckout{Ref: ref, RepoDir: dest, WorkDir: work, CommitID: commit}, nil
}

// gitFetchPinned fetches one provider-owned remote ref, checks it out detached,
// and then verifies that it still resolves to the immutable expected Ref. This
// lets a GitHub fork PR use the base repository's refs/pull namespace without
// ever substituting a moving ref for the signed webhook SHA.
func (p SourceCheckoutPlanner) gitFetchPinned(ctx context.Context, ref SourceRef, dest string) error {
	if err := rejectEmbeddedCredentials(ref.Repo); err != nil {
		return err
	}
	binary := p.GitBinary
	if strings.TrimSpace(binary) == "" {
		binary = "git"
	}
	redactor := newGitStderrRedactor(p.Token)
	runOnce := func(args []string) error {
		stderr := &bytes.Buffer{}
		var err error
		if p.Runner != nil {
			err = p.Runner.Run(ctx, binary, args, io.Discard, stderr)
		} else {
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Stderr = stderr
			cmd.Env = append(os.Environ(), gitAuthEnv(p.Token)...)
			err = cmd.Run()
		}
		if err != nil {
			message := redactor.redact(strings.TrimSpace(stderr.String()))
			if message != "" {
				return fmt.Errorf("%w (stderr: %s)", err, message)
			}
			return err
		}
		return nil
	}
	if err := runOnce([]string{"init", dest}); err != nil {
		return fmt.Errorf("git init for pinned fetch: %w", err)
	}
	if err := runOnce([]string{"-C", dest, "remote", "add", "origin", ref.Repo}); err != nil {
		return fmt.Errorf("git remote for pinned fetch: %w", err)
	}
	if err := runOnce([]string{"-C", dest, "fetch", "--depth", "1", "origin", ref.FetchRef}); err != nil {
		return fmt.Errorf("git pinned ref fetch failed: %w", err)
	}
	if err := runOnce([]string{"-C", dest, "checkout", "--detach", "FETCH_HEAD"}); err != nil { // not-a-failure-code
		return fmt.Errorf("git pinned ref checkout failed: %w", err)
	}
	commit, err := readHeadCommit(dest)
	if err != nil {
		return fmt.Errorf("git pinned ref resolve: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(commit), strings.TrimSpace(ref.Ref)) {
		return errors.New("git pinned ref no longer matches the expected commit")
	}
	return nil
}

func (p SourceCheckoutPlanner) gitClone(ctx context.Context, ref SourceRef, dest string) error {
	if err := rejectEmbeddedCredentials(ref.Repo); err != nil {
		return err
	}
	binary := p.GitBinary
	if strings.TrimSpace(binary) == "" {
		binary = "git"
	}
	redactor := newGitStderrRedactor(p.Token)
	args := []string{"clone", "--depth", "1", "--branch", ref.Ref, "--single-branch", ref.Repo, dest}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	var err error
	if p.Runner != nil {
		err = p.Runner.Run(ctx, binary, args, stdout, stderr)
	} else {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		cmd.Env = append(os.Environ(), gitAuthEnv(p.Token)...)
		err = cmd.Run()
	}
	if err != nil {
		// Some refs (e.g. full commit SHAs) cannot be cloned with --branch;
		// fall back to a full clone + checkout for those.
		fallbackErr := p.gitCloneFallback(ctx, ref, dest)
		if fallbackErr == nil {
			return nil
		}
		// Route git's stderr through the redactor so a URL-embedded or
		// header-injected token never surfaces in the returned error.
		return fmt.Errorf("git clone failed: %w (stderr: %s)", err, redactor.redact(strings.TrimSpace(stderr.String())))
	}
	return nil
}

func (p SourceCheckoutPlanner) gitCloneFallback(ctx context.Context, ref SourceRef, dest string) error {
	if err := rejectEmbeddedCredentials(ref.Repo); err != nil {
		return err
	}
	binary := p.GitBinary
	if strings.TrimSpace(binary) == "" {
		binary = "git"
	}
	redactor := newGitStderrRedactor(p.Token)
	if entries, _ := os.ReadDir(dest); len(entries) > 0 {
		// Failed clone may have left partial state. Wipe and try again.
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		if err := os.MkdirAll(dest, 0o750); err != nil {
			return err
		}
	}
	runOnce := func(args []string) error {
		stderr := &bytes.Buffer{}
		var err error
		if p.Runner != nil {
			err = p.Runner.Run(ctx, binary, args, io.Discard, stderr)
		} else {
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Stderr = stderr
			cmd.Env = append(os.Environ(), gitAuthEnv(p.Token)...)
			err = cmd.Run()
		}
		if err != nil {
			if message := strings.TrimSpace(stderr.String()); message != "" {
				return fmt.Errorf("%w (stderr: %s)", err, redactor.redact(message))
			}
			return err
		}
		return nil
	}
	if err := runOnce([]string{"clone", ref.Repo, dest}); err != nil {
		return err
	}
	if err := runOnce([]string{"-C", dest, "checkout", ref.Ref}); err != nil {
		return err
	}
	return nil
}

// rejectEmbeddedCredentials refuses repository URLs that carry inline
// credentials (e.g. https://user:token@host/...). Such credentials would leak
// into git's stderr and into any log that records the clone URL. SCP-style
// remotes (git@host:org/repo) and local paths have no url.Userinfo and pass.
func rejectEmbeddedCredentials(repo string) error {
	parsed, err := url.Parse(strings.TrimSpace(repo))
	if err == nil && parsed.Scheme != "" && parsed.User != nil {
		return ErrEmbeddedCredentials
	}
	return nil
}

// gitAuthEnv returns the environment used to run git. It always disables
// interactive credential prompts and, when a token is supplied, injects an
// Authorization header via git's GIT_CONFIG_* mechanism so the credential never
// appears in the clone URL or command line.
func gitAuthEnv(token string) []string {
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	token = strings.TrimSpace(token)
	if token == "" {
		return env
	}
	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Bearer "+token,
	)
}

// secretValue is a string whose textual renderings all collapse to the
// redaction placeholder, so a token cannot leak through accidental formatting
// (%v/%s/%#v/log/print).
type secretValue string

func (s secretValue) raw() string { return string(s) }

func (s secretValue) String() string { return redactedValue }

func (s secretValue) GoString() string { return redactedValue }

func (s secretValue) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(redactedValue))
}

// gitStderrRedactor scrubs the raw token (and its bare bearer form) out of git
// stderr/error text before it is surfaced to callers or logs.
type gitStderrRedactor struct {
	secrets []secretValue
}

func newGitStderrRedactor(token string) gitStderrRedactor {
	token = strings.TrimSpace(token)
	if token == "" {
		return gitStderrRedactor{}
	}
	return gitStderrRedactor{secrets: []secretValue{
		secretValue("Authorization: Bearer " + token),
		secretValue(token),
	}}
}

func (r gitStderrRedactor) redact(text string) string {
	redacted := text
	for _, secret := range r.secrets {
		raw := secret.raw()
		if raw == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, raw, redactedValue)
	}
	return redacted
}

func readHeadCommit(repo string) (string, error) {
	headPath := filepath.Join(repo, ".git", "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(data))
	if strings.HasPrefix(head, "ref: ") {
		refPath := filepath.Join(repo, ".git", strings.TrimPrefix(head, "ref: "))
		refData, err := os.ReadFile(refPath)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(refData)), nil
	}
	return head, nil
}

// Discard removes the on-disk checkout. Safe to call with a zero value.
// Checkouts backed by a `local://` source ref leave the path on disk
// because the API extraction step owns it; Discard is a no-op for those.
func (c SourceCheckout) Discard() error {
	if c.release != nil {
		return c.release()
	}
	if strings.TrimSpace(c.RepoDir) == "" {
		return nil
	}
	if IsLocalSourceURI(c.Ref.Repo) {
		return nil
	}
	return os.RemoveAll(c.RepoDir)
}

func (p SourceCheckoutPlanner) checkoutSourceArtifact(
	ctx context.Context,
	serviceID string,
	ref SourceRef,
) (SourceCheckout, error) {
	if err := ref.Validate(); err != nil {
		return SourceCheckout{}, err
	}
	digest, err := sourceartifact.ParseURI(ref.Repo)
	if err != nil {
		return SourceCheckout{}, err
	}
	if strings.TrimSpace(ref.Ref) != "" && strings.TrimSpace(ref.Ref) != digest {
		return SourceCheckout{}, errors.New("source checkout: artifact ref does not match URI digest")
	}
	if strings.TrimSpace(p.ArtifactRoot) == "" {
		return SourceCheckout{}, errors.New("source checkout: artifact root is required")
	}
	store, err := sourceartifact.NewFSStore(p.ArtifactRoot)
	if err != nil {
		return SourceCheckout{}, fmt.Errorf("source checkout: artifact store: %w", err)
	}
	materialized, err := store.Materialize(ctx, digest, sanitizeServiceID(serviceID))
	if err != nil {
		return SourceCheckout{}, fmt.Errorf("source checkout: materialize artifact: %w", err)
	}
	work := materialized.Path
	if strings.TrimSpace(ref.Subpath) != "" {
		work = filepath.Join(materialized.Path, filepath.FromSlash(ref.Subpath))
		info, statErr := os.Stat(work)
		if statErr != nil || !info.IsDir() {
			_ = materialized.Release()
			return SourceCheckout{}, fmt.Errorf("source checkout: subpath %q is not a directory in artifact checkout", ref.Subpath)
		}
	}
	return SourceCheckout{
		Ref:      ref,
		RepoDir:  materialized.Path,
		WorkDir:  work,
		CommitID: strings.TrimPrefix(digest, "sha256:"),
		release:  materialized.Release,
	}, nil
}

// checkoutLocalURI implements the `local://` SourceRef path. The API
// extraction step has already exploded the tarball at the embedded path; the
// planner only verifies the directory exists and applies Subpath.
func (p SourceCheckoutPlanner) checkoutLocalURI(serviceID string, ref SourceRef) (SourceCheckout, error) {
	path := LocalSourcePath(ref.Repo)
	if strings.TrimSpace(path) == "" {
		return SourceCheckout{}, errors.New("source checkout: local:// uri is missing a path")
	}
	cleaned := filepath.Clean(path)
	info, err := os.Stat(cleaned)
	if err != nil {
		return SourceCheckout{}, fmt.Errorf("source checkout: local path: %w", err)
	}
	if !info.IsDir() {
		return SourceCheckout{}, fmt.Errorf("source checkout: local path %q is not a directory", cleaned)
	}
	work := cleaned
	if strings.TrimSpace(ref.Subpath) != "" {
		work = filepath.Join(cleaned, ref.Subpath)
		subInfo, subErr := os.Stat(work)
		if subErr != nil || !subInfo.IsDir() {
			return SourceCheckout{}, fmt.Errorf("source checkout: subpath %q is not a directory in local checkout", ref.Subpath)
		}
	}
	_ = serviceID
	return SourceCheckout{Ref: ref, RepoDir: cleaned, WorkDir: work, CommitID: ""}, nil
}

func copyDir(src, dst string) error {
	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("copyDir: %s is not a directory", src)
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		return copyFile(path, target)
	})
}
