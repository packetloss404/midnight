// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrCommitStatusInvalid is returned when a commit-status request fails
// validation before any HTTP call is made.
var ErrCommitStatusInvalid = errors.New("github: commit status request invalid")

// CommitStatusState is the validated GitHub commit-status state machine. Only
// the four states GitHub accepts on the statuses API are valid.
type CommitStatusState string

const (
	StatusPending CommitStatusState = "pending"
	StatusSuccess CommitStatusState = "success"
	StatusError   CommitStatusState = "error"
	StatusFailure CommitStatusState = "failure"
)

// Valid reports whether s is one of the four accepted states.
func (s CommitStatusState) Valid() bool {
	switch s {
	case StatusPending, StatusSuccess, StatusError, StatusFailure:
		return true
	default:
		return false
	}
}

// Validate returns ErrCommitStatusInvalid when s is not an accepted state.
func (s CommitStatusState) Validate() error {
	if s.Valid() {
		return nil
	}
	return fmt.Errorf("%w: state must be one of pending, success, error, or failure", ErrCommitStatusInvalid)
}

// PublishCommitStatusRequest is a fully-resolved commit-status write: it already
// carries the installation Token (a redacting Secret) and owner/repo/sha split.
type PublishCommitStatusRequest struct {
	Owner       string
	Repo        string
	SHA         string
	State       CommitStatusState
	TargetURL   string
	Description string
	Context     string
	Token       Secret
}

// CommitStatusResponse is the subset of GitHub's status response we retain.
type CommitStatusResponse struct {
	ID          int64             `json:"id"`
	State       CommitStatusState `json:"state"`
	TargetURL   string            `json:"target_url,omitempty"`
	Description string            `json:"description,omitempty"`
	Context     string            `json:"context,omitempty"`
	URL         string            `json:"url,omitempty"`
	CreatedAt   time.Time         `json:"created_at,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at,omitempty"`
}

type commitStatusRequestBody struct {
	State       CommitStatusState `json:"state"`
	TargetURL   string            `json:"target_url,omitempty"`
	Description string            `json:"description,omitempty"`
	Context     string            `json:"context,omitempty"`
}

// Validate checks the request is well-formed before an HTTP call.
func (r PublishCommitStatusRequest) Validate() error {
	if err := validateStatusPathSegment("owner", r.Owner); err != nil {
		return err
	}
	if err := validateStatusPathSegment("repo", r.Repo); err != nil {
		return err
	}
	if err := validateStatusPathSegment("sha", r.SHA); err != nil {
		return err
	}
	if err := r.State.Validate(); err != nil {
		return err
	}
	if r.Token.IsZero() {
		return fmt.Errorf("%w: token is required", ErrCommitStatusInvalid)
	}
	return nil
}

func (r PublishCommitStatusRequest) body() commitStatusRequestBody {
	return commitStatusRequestBody{
		State:       r.State,
		TargetURL:   strings.TrimSpace(r.TargetURL),
		Description: strings.TrimSpace(r.Description),
		Context:     strings.TrimSpace(r.Context),
	}
}

// PublishCommitStatus POSTs a commit status to
// /repos/{owner}/{repo}/statuses/{sha} using the supplied installation token.
// The token is scrubbed from any error/response text before it is surfaced.
func (c *AppClient) PublishCommitStatus(ctx context.Context, request PublishCommitStatusRequest) (CommitStatusResponse, error) {
	if c == nil {
		return CommitStatusResponse{}, fmt.Errorf("%w: client is required", ErrCommitStatusInvalid)
	}
	if err := request.Validate(); err != nil {
		return CommitStatusResponse{}, err
	}

	url := fmt.Sprintf("%s/repos/%s/%s/statuses/%s",
		c.cfg.APIBase(),
		strings.TrimSpace(request.Owner),
		strings.TrimSpace(request.Repo),
		strings.TrimSpace(request.SHA),
	)
	payload, err := json.Marshal(request.body())
	if err != nil {
		return CommitStatusResponse{}, fmt.Errorf("%w: body must be JSON encodable", ErrCommitStatusInvalid)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return CommitStatusResponse{}, err
	}
	req.Header.Set("Authorization", "token "+request.Token.Value())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.doer.Do(req)
	if err != nil {
		return CommitStatusResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return CommitStatusResponse{}, fmt.Errorf("%w: status=%d body=%s", ErrAppHTTP, resp.StatusCode, scrubSecret(truncate(body, 256), request.Token))
	}
	var decoded CommitStatusResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return CommitStatusResponse{}, fmt.Errorf("%w: decode: %v", ErrAppHTTP, err)
	}
	return decoded, nil
}

// PublishInstallationCommitStatusRequest describes a status write in terms of an
// installation + repository; the installation token is minted internally.
type PublishInstallationCommitStatusRequest struct {
	InstallationID int64
	RepositoryID   int64
	RepoFullName   string
	SHA            string
	State          CommitStatusState
	TargetURL      string
	Description    string
	Context        string
}

// PublishInstallationCommitStatus mints a short-lived installation token (which
// must carry the App's `statuses:write` permission) and posts the commit
// status. It reuses AppClient.MintInstallationToken — no JWT/token duplication.
func (c *AppClient) PublishInstallationCommitStatus(ctx context.Context, request PublishInstallationCommitStatusRequest) (CommitStatusResponse, error) {
	if c == nil {
		return CommitStatusResponse{}, fmt.Errorf("%w: client is required", ErrCommitStatusInvalid)
	}
	owner, repo, err := splitRepoFullName(request.RepoFullName)
	if err != nil {
		return CommitStatusResponse{}, err
	}
	if request.InstallationID <= 0 {
		return CommitStatusResponse{}, fmt.Errorf("%w: installation_id must be positive", ErrCommitStatusInvalid)
	}
	if err := request.State.Validate(); err != nil {
		return CommitStatusResponse{}, err
	}

	tok, err := c.MintInstallationToken(ctx, request.InstallationID)
	if err != nil {
		return CommitStatusResponse{}, err
	}
	return c.PublishCommitStatus(ctx, PublishCommitStatusRequest{
		Owner:       owner,
		Repo:        repo,
		SHA:         request.SHA,
		State:       request.State,
		TargetURL:   request.TargetURL,
		Description: request.Description,
		Context:     request.Context,
		Token:       tok.Token,
	})
}

func splitRepoFullName(value string) (string, string, error) {
	owner, repo, ok := strings.Cut(strings.TrimSpace(value), "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("%w: repo_full_name must be owner/repo", ErrCommitStatusInvalid)
	}
	return strings.TrimSpace(owner), strings.TrimSpace(repo), nil
}

func validateStatusPathSegment(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrCommitStatusInvalid, field)
	}
	if strings.Contains(value, "/") || strings.Contains(value, "\\") {
		return fmt.Errorf("%w: %s must be a single path segment", ErrCommitStatusInvalid, field)
	}
	return nil
}
