// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// DefaultCommitStatusContext is the status context Midnight publishes under
// when the caller does not supply one.
const DefaultCommitStatusContext = "midnight/deploy"

const (
	commitStatusContextMaxLength     = 100
	commitStatusDescriptionMaxLength = 140
	commitStatusTargetURLMaxLength   = 2048
	providerHostMaxLength            = 255
	repoFullNameMaxLength            = 255
)

var (
	providerHostPattern = regexp.MustCompile(`\A[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?\z`)
	repoFullNamePattern = regexp.MustCompile(`\A[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\z`)
	commitSHAPattern    = regexp.MustCompile(`\A(?:[a-f0-9]{40}|[a-f0-9]{64})\z`)
)

// CommitStatusInput is the caller-supplied, unvalidated shape used to enqueue a
// commit status. NewCommitStatusMetadata validates and normalizes it.
type CommitStatusInput struct {
	ProviderHost   string
	InstallationID int64
	RepositoryID   int64
	RepoFullName   string
	SHA            string
	State          string
	Context        string
	TargetURL      string
	Description    string
}

// CommitStatusMetadata is the validated/normalized commit-status record. This
// is the shape the enqueue layer persists and the drain worker replays.
type CommitStatusMetadata struct {
	ProviderHost   string `json:"provider_host"`
	InstallationID int64  `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
	RepoFullName   string `json:"repo_full_name"`
	SHA            string `json:"sha"`
	State          string `json:"state"`
	Context        string `json:"context"`
	TargetURL      string `json:"target_url"`
	Description    string `json:"description,omitempty"`
}

// NewCommitStatusMetadata validates and normalizes a CommitStatusInput. It
// enforces the same guards as the light jobs layer: a valid state-machine
// value, a single owner/repo, a 40/64-char lowercase-hex SHA, a positive
// installation/repository id, a context <= 100 chars, a description <= 140
// chars, and an http(s) target URL with no embedded credentials.
func NewCommitStatusMetadata(input CommitStatusInput) (CommitStatusMetadata, error) {
	providerHost, err := normalizeProviderHost(input.ProviderHost)
	if err != nil {
		return CommitStatusMetadata{}, err
	}
	repoFullName, err := normalizeRepoFullName(input.RepoFullName)
	if err != nil {
		return CommitStatusMetadata{}, err
	}
	sha, err := normalizeCommitSHA(input.SHA)
	if err != nil {
		return CommitStatusMetadata{}, err
	}
	if input.InstallationID <= 0 {
		return CommitStatusMetadata{}, fmt.Errorf("%w: installation_id must be positive", ErrCommitStatusInvalid)
	}
	if input.RepositoryID <= 0 {
		return CommitStatusMetadata{}, fmt.Errorf("%w: repository_id must be positive", ErrCommitStatusInvalid)
	}
	state := strings.TrimSpace(input.State)
	if !CommitStatusState(state).Valid() {
		return CommitStatusMetadata{}, fmt.Errorf("%w: unsupported commit status state %q", ErrCommitStatusInvalid, input.State)
	}
	context := strings.TrimSpace(input.Context)
	if context == "" {
		context = DefaultCommitStatusContext
	}
	if hasControlCharacter(context) || len(context) > commitStatusContextMaxLength {
		return CommitStatusMetadata{}, fmt.Errorf("%w: commit status context is invalid", ErrCommitStatusInvalid)
	}
	targetURL, err := normalizeTargetURL(input.TargetURL)
	if err != nil {
		return CommitStatusMetadata{}, err
	}
	description := strings.TrimSpace(input.Description)
	if hasControlCharacter(description) || len(description) > commitStatusDescriptionMaxLength {
		return CommitStatusMetadata{}, fmt.Errorf("%w: commit status description is invalid", ErrCommitStatusInvalid)
	}

	return CommitStatusMetadata{
		ProviderHost:   providerHost,
		InstallationID: input.InstallationID,
		RepositoryID:   input.RepositoryID,
		RepoFullName:   repoFullName,
		SHA:            sha,
		State:          state,
		Context:        context,
		TargetURL:      targetURL,
		Description:    description,
	}, nil
}

func normalizeProviderHost(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "github.com"
	}
	if len(value) > providerHostMaxLength ||
		strings.Contains(value, "..") ||
		strings.Contains(value, "/") ||
		strings.Contains(value, ":") ||
		hasControlCharacter(value) {
		return "", fmt.Errorf("%w: provider_host is invalid", ErrCommitStatusInvalid)
	}
	if !providerHostPattern.MatchString(value) {
		return "", fmt.Errorf("%w: provider_host is invalid", ErrCommitStatusInvalid)
	}
	return value, nil
}

func normalizeRepoFullName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || len(value) > repoFullNameMaxLength || hasControlCharacter(value) || strings.ContainsAny(value, " \t\r\n") {
		return "", fmt.Errorf("%w: repo_full_name is invalid", ErrCommitStatusInvalid)
	}
	if strings.HasSuffix(strings.ToLower(value), ".git") || !repoFullNamePattern.MatchString(value) {
		return "", fmt.Errorf("%w: repo_full_name must be owner/repo", ErrCommitStatusInvalid)
	}
	return value, nil
}

func normalizeCommitSHA(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || !commitSHAPattern.MatchString(value) {
		return "", fmt.Errorf("%w: sha must be a 40 or 64 character lowercase hex commit id", ErrCommitStatusInvalid)
	}
	return value, nil
}

func normalizeTargetURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > commitStatusTargetURLMaxLength || hasControlCharacter(value) {
		return "", fmt.Errorf("%w: target_url is invalid", ErrCommitStatusInvalid)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%w: target_url is invalid", ErrCommitStatusInvalid)
	}
	if parsed.User != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("%w: target_url must be an http(s) URL without credentials", ErrCommitStatusInvalid)
	}
	return value, nil
}

func hasControlCharacter(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
