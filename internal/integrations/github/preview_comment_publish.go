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
)

var ErrPreviewCommentInvalid = errors.New("github: preview comment request invalid")

type PublishInstallationPreviewCommentRequest struct {
	InstallationID int64
	RepositoryID   int64
	RepoFullName   string
	PullRequest    int
	PreviewID      string
	CommentID      int64
	Body           string
}

type PreviewCommentResponse struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url,omitempty"`
}

// PublishInstallationPreviewComment creates the first lifecycle comment and
// updates that same comment on later states. Pull requests use GitHub's issue
// comments API; the App manifest therefore requires pull_requests:write.
func (c *AppClient) PublishInstallationPreviewComment(
	ctx context.Context,
	request PublishInstallationPreviewCommentRequest,
) (PreviewCommentResponse, error) {
	if c == nil || request.InstallationID <= 0 || request.RepositoryID <= 0 ||
		request.PullRequest <= 0 || strings.TrimSpace(request.Body) == "" ||
		len(request.Body) > 65536 {
		return PreviewCommentResponse{}, ErrPreviewCommentInvalid
	}
	owner, repo, err := splitRepoFullName(request.RepoFullName)
	if err != nil {
		return PreviewCommentResponse{}, ErrPreviewCommentInvalid
	}
	marker := previewCommentMarker(request.PreviewID)
	if strings.TrimSpace(request.PreviewID) != "" && marker == "" {
		return PreviewCommentResponse{}, ErrPreviewCommentInvalid
	}
	commentBody := strings.TrimSpace(request.Body)
	if marker != "" {
		commentBody += "\n\n" + marker
	}
	if len(commentBody) > 65536 {
		return PreviewCommentResponse{}, ErrPreviewCommentInvalid
	}
	token, err := c.mintInstallationToken(
		ctx,
		request.InstallationID,
		[]int64{request.RepositoryID},
		map[string]string{"pull_requests": "write"},
	)
	if err != nil {
		return PreviewCommentResponse{}, err
	}
	if request.CommentID == 0 && marker != "" {
		existing, findErr := c.findInstallationPreviewComment(
			ctx, token, owner, repo, request.PullRequest, marker,
		)
		if findErr != nil {
			return PreviewCommentResponse{}, findErr
		}
		request.CommentID = existing
	}
	method := http.MethodPost
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", c.cfg.APIBase(), owner, repo, request.PullRequest)
	if request.CommentID > 0 {
		method = http.MethodPatch
		endpoint = fmt.Sprintf("%s/repos/%s/%s/issues/comments/%d", c.cfg.APIBase(), owner, repo, request.CommentID)
	}
	payload, _ := json.Marshal(struct {
		Body string `json:"body"`
	}{Body: commentBody})
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return PreviewCommentResponse{}, err
	}
	req.Header.Set("Authorization", "token "+token.Token.Value())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.doer.Do(req)
	if err != nil {
		return PreviewCommentResponse{}, errors.New(scrubSecret(err.Error(), token.Token))
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return PreviewCommentResponse{}, fmt.Errorf("%w: status=%d body=%s", ErrAppHTTP, resp.StatusCode, scrubSecret(truncate(body, 256), token.Token))
	}
	var decoded PreviewCommentResponse
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.ID <= 0 {
		return PreviewCommentResponse{}, fmt.Errorf("%w: invalid comment response", ErrAppHTTP)
	}
	return decoded, nil
}

func previewCommentMarker(previewID string) string {
	previewID = strings.TrimSpace(previewID)
	if len(previewID) < 5 || len(previewID) > 128 || !strings.HasPrefix(previewID, "prv_") {
		return ""
	}
	for _, char := range previewID {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-') {
			return ""
		}
	}
	return "<!-- midnight-preview:" + previewID + " -->"
}

func (c *AppClient) findInstallationPreviewComment(
	ctx context.Context,
	token InstallationToken,
	owner, repo string,
	pullRequest int,
	marker string,
) (int64, error) {
	type comment struct {
		ID                    int64  `json:"id"`
		Body                  string `json:"body"`
		PerformedViaGitHubApp *struct {
			ID int64 `json:"id"`
		} `json:"performed_via_github_app"`
	}
	for page := 1; page <= 10; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=100&page=%d",
			c.cfg.APIBase(), owner, repo, pullRequest, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "token "+token.Token.Value())
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		resp, err := c.doer.Do(req)
		if err != nil {
			return 0, errors.New(scrubSecret(err.Error(), token.Token))
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return 0, readErr
		}
		if resp.StatusCode/100 != 2 {
			return 0, fmt.Errorf("%w: status=%d body=%s", ErrAppHTTP, resp.StatusCode, scrubSecret(truncate(body, 256), token.Token))
		}
		var comments []comment
		if err := json.Unmarshal(body, &comments); err != nil {
			return 0, fmt.Errorf("%w: invalid comment list response", ErrAppHTTP)
		}
		for _, candidate := range comments {
			if candidate.ID > 0 && candidate.PerformedViaGitHubApp != nil &&
				candidate.PerformedViaGitHubApp.ID == c.cfg.AppID && strings.Contains(candidate.Body, marker) {
				return candidate.ID, nil
			}
		}
		if len(comments) < 100 {
			break
		}
	}
	return 0, nil
}
