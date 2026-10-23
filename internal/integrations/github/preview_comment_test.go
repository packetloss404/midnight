// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublishInstallationPreviewCommentCreatesThenUpdates(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	const token = "ghs_preview_comment_token"
	var methods, paths []string
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/77/access_tokens" {
			_, _ = w.Write([]byte(`{"token":"` + token + `","expires_at":"2030-01-01T00:00:00Z"}`))
			return
		}
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		if strings.Contains(r.Header.Get("Authorization"), token) == false {
			t.Fatalf("comment request did not use installation token")
		}
		if r.Method == http.MethodGet {
			if !created {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":901,"body":"Preview ready\n\n<!-- midnight-preview:prv_preview -->","performed_via_github_app":{"id":7}}]`))
			return
		}
		var body map[string]string
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil ||
			body["body"] != "Preview ready\n\n<!-- midnight-preview:prv_preview -->" {
			t.Fatalf("comment body=%s err=%v", raw, err)
		}
		if r.Method == http.MethodPost {
			created = true
		}
		_, _ = w.Write([]byte(`{"id":901,"html_url":"https://github.com/acme/web/pull/42#issuecomment-901"}`))
	}))
	defer server.Close()
	client, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, APIBaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	request := PublishInstallationPreviewCommentRequest{InstallationID: 77, RepositoryID: 7700,
		RepoFullName: "acme/web", PullRequest: 42, PreviewID: "prv_preview", Body: "Preview ready"}
	if _, err := client.PublishInstallationPreviewComment(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PublishInstallationPreviewComment(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "GET,POST,GET,PATCH" ||
		strings.Join(paths, ",") != "/repos/acme/web/issues/42/comments,/repos/acme/web/issues/42/comments,/repos/acme/web/issues/42/comments,/repos/acme/web/issues/comments/901" {
		t.Fatalf("comment calls methods=%v paths=%v", methods, paths)
	}
}

func TestPreviewCommentBodyIsStateSpecificAndValidated(t *testing.T) {
	base := PendingPreviewComment{HeadSHA: strings.Repeat("a", 40), PreviewURL: "https://preview.example.test"}
	for _, state := range []string{"deploying", "refreshing", "ready", "failed", "deleting", "deleted"} {
		base.State = state
		body, err := previewCommentBody(base)
		if err != nil || !strings.Contains(body, "Midnight preview") {
			t.Fatalf("state=%s body=%q err=%v", state, body, err)
		}
	}
	base.State = "invented"
	if _, err := previewCommentBody(base); err == nil {
		t.Fatal("previewCommentBody accepted an invented lifecycle state")
	}
}
