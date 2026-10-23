// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// PullRequestAction is the subset of GitHub pull_request actions that changes
// preview lifecycle state. Every other action remains an acknowledged no-op.
type PullRequestAction string

const (
	PullRequestOpened      PullRequestAction = "opened"
	PullRequestReopened    PullRequestAction = "reopened"
	PullRequestSynchronize PullRequestAction = "synchronize"
	PullRequestClosed      PullRequestAction = "closed"
)

var (
	ErrPullRequestEventInvalid = errors.New("github: pull request event invalid")
	pullRequestSHA             = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// PullRequestEvent is the normalized, security-relevant subset of GitHub's
// pull_request webhook. BaseRepository is the installed repository and is the
// only source-authority boundary. HeadRepository is metadata used to classify
// forks; checkout still uses the installed base repository's pull ref/SHA.
type PullRequestEvent struct {
	Action           PullRequestAction
	Number           int
	InstallationID   int64
	BaseRepositoryID int64
	BaseRepository   string
	BaseCloneURL     string
	BaseBranch       string
	HeadRepositoryID int64
	HeadRepository   string
	HeadBranch       string
	HeadSHA          string
	SenderLogin      string
	FromFork         bool
}

func (event PullRequestEvent) IsLifecycleAction() bool {
	switch event.Action {
	case PullRequestOpened, PullRequestReopened, PullRequestSynchronize, PullRequestClosed:
		return true
	default:
		return false
	}
}

func (event PullRequestEvent) IsClose() bool { return event.Action == PullRequestClosed }

// PullRequestEventAction reads only the routing discriminator. Callers use it
// to acknowledge non-lifecycle GitHub actions without relaxing validation for
// the four actions that can mutate preview state.
func PullRequestEventAction(body []byte) (PullRequestAction, error) {
	var payload struct {
		Action PullRequestAction `json:"action"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(string(payload.Action)) == "" {
		return "", ErrPullRequestEventInvalid
	}
	return payload.Action, nil
}

func IsPullRequestLifecycleAction(action PullRequestAction) bool {
	return validPullRequestAction(action)
}

// PullRequestEventInstallationID extracts only the installation fence key for
// persist-before-ack admission. Full payload validation remains in the durable
// planner.
func PullRequestEventInstallationID(body []byte) int64 {
	var payload struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Installation.ID <= 0 {
		return 0
	}
	return payload.Installation.ID
}

// ParsePullRequestEvent rejects provider payloads whose duplicated repository
// identities disagree. Repository names alone are never accepted as authority.
func ParsePullRequestEvent(body []byte) (PullRequestEvent, error) {
	var payload struct {
		Action       PullRequestAction `json:"action"`
		Number       int               `json:"number"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository  pullRequestRepository `json:"repository"`
		PullRequest struct {
			Number int `json:"number"`
			Base   struct {
				Ref  string                `json:"ref"`
				Repo pullRequestRepository `json:"repo"`
			} `json:"base"`
			Head struct {
				Ref  string                `json:"ref"`
				SHA  string                `json:"sha"`
				Repo pullRequestRepository `json:"repo"`
			} `json:"head"`
		} `json:"pull_request"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return PullRequestEvent{}, fmt.Errorf("%w: decode: %v", ErrPullRequestEventInvalid, err)
	}
	baseName := strings.ToLower(strings.TrimSpace(payload.PullRequest.Base.Repo.FullName))
	repositoryName := strings.ToLower(strings.TrimSpace(payload.Repository.FullName))
	baseCloneURL := strings.TrimSpace(payload.PullRequest.Base.Repo.CloneURL)
	if !validPullRequestAction(payload.Action) || payload.Number <= 0 ||
		payload.PullRequest.Number != payload.Number || payload.Installation.ID <= 0 ||
		payload.Repository.ID <= 0 || payload.PullRequest.Base.Repo.ID != payload.Repository.ID ||
		baseName == "" || repositoryName != baseName ||
		!strings.EqualFold(strings.TrimSpace(payload.Repository.CloneURL), baseCloneURL) ||
		!validCanonicalCloneURL(baseCloneURL, baseName) ||
		strings.TrimSpace(payload.PullRequest.Base.Ref) == "" ||
		strings.TrimSpace(payload.PullRequest.Head.Ref) == "" ||
		!pullRequestSHA.MatchString(strings.TrimSpace(payload.PullRequest.Head.SHA)) {
		return PullRequestEvent{}, ErrPullRequestEventInvalid
	}
	headName := strings.ToLower(strings.TrimSpace(payload.PullRequest.Head.Repo.FullName))
	headID := payload.PullRequest.Head.Repo.ID
	// GitHub can send a null head repository after a fork is deleted. Only a
	// close event may use that shape, and it remains classified as a fork.
	missingHeadRepository := headID <= 0 || headName == ""
	if missingHeadRepository && payload.Action != PullRequestClosed {
		return PullRequestEvent{}, ErrPullRequestEventInvalid
	}
	fromFork := missingHeadRepository || headID != payload.Repository.ID || !strings.EqualFold(headName, baseName)
	return PullRequestEvent{
		Action:           payload.Action,
		Number:           payload.Number,
		InstallationID:   payload.Installation.ID,
		BaseRepositoryID: payload.Repository.ID,
		BaseRepository:   baseName,
		BaseCloneURL:     baseCloneURL,
		BaseBranch:       strings.TrimSpace(payload.PullRequest.Base.Ref),
		HeadRepositoryID: headID,
		HeadRepository:   headName,
		HeadBranch:       strings.TrimSpace(payload.PullRequest.Head.Ref),
		HeadSHA:          strings.TrimSpace(payload.PullRequest.Head.SHA),
		SenderLogin:      strings.TrimSpace(payload.Sender.Login),
		FromFork:         fromFork,
	}, nil
}

type pullRequestRepository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
}

func validPullRequestAction(action PullRequestAction) bool {
	switch action {
	case PullRequestOpened, PullRequestReopened, PullRequestSynchronize, PullRequestClosed:
		return true
	default:
		return false
	}
}

func validCanonicalCloneURL(cloneURL, repository string) bool {
	want := "https://github.com/" + strings.ToLower(strings.TrimSpace(repository)) + ".git"
	return strings.EqualFold(strings.TrimSpace(cloneURL), want)
}
