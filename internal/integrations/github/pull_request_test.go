// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParsePullRequestEventNormalizesTrustedAndForkSources(t *testing.T) {
	trusted, err := ParsePullRequestEvent(pullRequestPayload("opened", 4242, "acme/web", 4242, "acme/web"))
	if err != nil {
		t.Fatalf("trusted parse: %v", err)
	}
	if trusted.Action != PullRequestOpened || trusted.Number != 42 || trusted.FromFork ||
		trusted.BaseRepository != "acme/web" || trusted.BaseBranch != "main" ||
		trusted.HeadSHA != strings.Repeat("a", 40) {
		t.Fatalf("trusted event = %#v", trusted)
	}

	forked, err := ParsePullRequestEvent(pullRequestPayload("synchronize", 4242, "acme/web", 9001, "contributor/web"))
	if err != nil {
		t.Fatalf("fork parse: %v", err)
	}
	if !forked.FromFork || forked.HeadRepositoryID != 9001 || forked.HeadRepository != "contributor/web" {
		t.Fatalf("fork event = %#v", forked)
	}
}

func TestParsePullRequestEventRejectsIdentityConfusionAndUnsupportedActions(t *testing.T) {
	valid := string(pullRequestPayload("opened", 4242, "acme/web", 4242, "acme/web"))
	cases := map[string]string{
		"unsupported action": strings.Replace(valid, `"action":"opened"`, `"action":"labeled"`, 1),
		"outer repo id":      strings.Replace(valid, `"repository":{"id":4242`, `"repository":{"id":12`, 1),
		"outer repo name":    strings.Replace(valid, `"repository":{"id":4242,"full_name":"acme/web"`, `"repository":{"id":4242,"full_name":"evil/web"`, 1),
		"clone authority":    strings.Replace(valid, `https://github.com/acme/web.git`, `https://github.com/evil/web.git`, 1),
		"uppercase sha":      strings.Replace(valid, strings.Repeat("a", 40), strings.Repeat("A", 40), 1),
		"number mismatch":    strings.Replace(valid, `"pull_request":{`+"\n"+`    "number":42`, `"pull_request":{`+"\n"+`    "number":43`, 1),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePullRequestEvent([]byte(payload)); !errors.Is(err, ErrPullRequestEventInvalid) {
				t.Fatalf("error = %v, want ErrPullRequestEventInvalid", err)
			}
		})
	}
}

func TestParsePullRequestEventAllowsMissingForkRepositoryOnlyOnClose(t *testing.T) {
	closed := string(pullRequestPayload("closed", 4242, "acme/web", 9001, "contributor/web"))
	closed = strings.Replace(closed, `"repo":{"id":9001,"full_name":"contributor/web","clone_url":"https://github.com/contributor/web.git"}`, `"repo":null`, 1)
	event, err := ParsePullRequestEvent([]byte(closed))
	if err != nil || !event.FromFork {
		t.Fatalf("closed deleted fork = %#v, %v", event, err)
	}
	opened := strings.Replace(closed, `"action":"closed"`, `"action":"opened"`, 1)
	if _, err := ParsePullRequestEvent([]byte(opened)); !errors.Is(err, ErrPullRequestEventInvalid) {
		t.Fatalf("opened deleted fork error = %v", err)
	}
}

func pullRequestPayload(action string, baseID int64, baseName string, headID int64, headName string) []byte {
	return []byte(fmt.Sprintf(`{
  "action":%q,
  "number":42,
  "installation":{"id":77},
  "repository":{"id":%d,"full_name":%q,"clone_url":%q},
  "pull_request":{
    "number":42,
    "base":{"ref":"main","repo":{"id":%d,"full_name":%q,"clone_url":%q}},
    "head":{"ref":"feature/cart","sha":%q,"repo":{"id":%d,"full_name":%q,"clone_url":%q}}
  },
  "sender":{"login":"octocat"}
}`,
		action,
		baseID, baseName, "https://github.com/"+baseName+".git",
		baseID, baseName, "https://github.com/"+baseName+".git",
		strings.Repeat("a", 40), headID, headName, "https://github.com/"+headName+".git",
	))
}
