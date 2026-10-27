package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	githubapp "github.com/midnight/midnight/internal/integrations/github"
)

type githubLink struct {
	Installation int64  `json:"installation_id"`
	Repository   int64  `json:"repository_id"`
	Name         string `json:"repository_full_name"`
	Branch       string `json:"branch"`
	State        string `json:"binding_state"`
}
type preview struct {
	Number      int    `json:"pull_request"`
	Environment string `json:"environment_id"`
	Deployment  string `json:"deployment_id"`
	State       string `json:"state"`
	Head        string `json:"head_sha"`
}

func (r *runner) githubPreflight(ctx context.Context) error {
	if _, err := r.cfg.secret("GITHUB_QA_DRIVER_TOKEN_FILE"); err != nil {
		return err
	}
	key, err := readPrivate(r.cfg.path("GITHUB_PRIVATE_KEY_FILE"), 1<<20)
	if err != nil {
		return err
	}
	_, err = githubapp.NewAppClient(githubapp.AppConfig{AppID: r.integer("GITHUB_APP_ID"), PrivateKeyPEM: key}, githubapp.WithHTTPDoer(r.client))
	if err != nil {
		return blocked("GITHUB_APP_KEY_INVALID")
	}
	var link githubLink
	if _, err := r.api(ctx, "GET", "/api/v1alpha/services/"+r.cfg.values["QA_SERVICE_ID"]+"/github", nil, &link); err != nil {
		return err
	}
	if link.Installation != r.integer("GITHUB_INSTALLATION_ID") || link.Repository != r.integer("GITHUB_REPOSITORY_ID") || link.Name != r.cfg.values["GITHUB_REPOSITORY"] || link.Branch != r.cfg.values["GITHUB_QA_BRANCH"] || link.State != "bound" {
		return blocked("PREPROVISIONED_APP_SOURCE_LINK_MISMATCH")
	}
	var previews struct {
		Items      []preview `json:"items"`
		Capability struct {
			Enabled bool `json:"automatic_lifecycle"`
		} `json:"capability"`
	}
	if _, err := r.api(ctx, "GET", r.previewsPath(), nil, &previews); err != nil {
		return err
	}
	if !previews.Capability.Enabled {
		return blocked("GITHUB_PREVIEW_LIFECYCLE_UNAVAILABLE")
	}
	for _, item := range previews.Items {
		if item.State != "deleted" {
			return blocked("QA_PREVIEW_SCOPE_NOT_EMPTY")
		}
	}
	r.receipt.pass("PREPROVISIONED_EXACT_APP_REPOSITORY_BRANCH_BINDING")
	r.receipt.incomplete("INTERACTIVE_OAUTH_INSTALL_AND_WORKSPACE_GRANT_HANDSHAKE")
	observer, enabled, err := runtimeSSHConfigFrom(r.cfg)
	if err != nil {
		return err
	}
	if enabled {
		r.runtimeSSH = &observer
		r.receipt.pass("PINNED_READ_ONLY_RUNTIME_OBSERVER_PREFLIGHT")
	} else {
		r.receipt.incomplete("PHYSICAL_PREVIEW_RUNTIME_REMOVAL_AFTER_ENVIRONMENT_RETIREMENT")
	}
	return nil
}
func (r *runner) integer(key string) int64 {
	n, _ := strconv.ParseInt(r.cfg.values[key], 10, 64)
	return n
}
func (r *runner) previewsPath() string {
	return "/api/v1alpha/services/" + r.cfg.values["QA_SERVICE_ID"] + "/preview-environments"
}
func (r *runner) gh(ctx context.Context, method, path, token string, body, target any) (int, error) {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return 0, failed("GITHUB_PATH_INVALID")
	}
	return r.request(ctx, method, "https://api.github.com"+path, map[string]string{"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}, body, target)
}
func (r *runner) github(ctx context.Context) (result error) {
	driver, err := r.cfg.secret("GITHUB_QA_DRIVER_TOKEN_FILE")
	if err != nil {
		return err
	}
	var driverIdentity struct {
		Login string `json:"login"`
	}
	if _, err := r.gh(ctx, "GET", "/user", driver, nil, &driverIdentity); err != nil {
		return err
	}
	if driverIdentity.Login != r.cfg.values["GITHUB_QA_DRIVER_LOGIN"] {
		return blocked("QA_DRIVER_IDENTITY_MISMATCH")
	}
	var driverRepo struct {
		ID      int64  `json:"id"`
		Name    string `json:"full_name"`
		Private bool   `json:"private"`
	}
	if _, err := r.gh(ctx, "GET", "/repos/"+r.cfg.values["GITHUB_REPOSITORY"], driver, nil, &driverRepo); err != nil {
		return err
	}
	if driverRepo.ID != r.integer("GITHUB_REPOSITORY_ID") || driverRepo.Name != r.cfg.values["GITHUB_REPOSITORY"] || !driverRepo.Private {
		return blocked("QA_DRIVER_REPOSITORY_MISMATCH")
	}
	key, err := readPrivate(r.cfg.path("GITHUB_PRIVATE_KEY_FILE"), 1<<20)
	if err != nil {
		return err
	}
	app, err := githubapp.NewAppClient(githubapp.AppConfig{AppID: r.integer("GITHUB_APP_ID"), PrivateKeyPEM: key}, githubapp.WithHTTPDoer(r.client))
	if err != nil {
		return blocked("GITHUB_APP_KEY_INVALID")
	}
	jwt, err := app.MintJWT()
	if err != nil {
		return blocked("GITHUB_APP_KEY_INVALID")
	}
	var appIdentity struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if _, err := r.gh(ctx, "GET", "/app", jwt, nil, &appIdentity); err != nil {
		return err
	}
	if appIdentity.ID != r.integer("GITHUB_APP_ID") || appIdentity.Slug != r.cfg.values["GITHUB_APP_SLUG"] {
		return blocked("GITHUB_APP_ID_OR_SLUG_MISMATCH")
	}
	// Bind status attribution to the JWT-authenticated App identity, rather
	// than trusting a separately configured bot name.
	r.githubStatusBot = appIdentity.Slug + "[bot]"
	var installation struct {
		ID        int64 `json:"id"`
		App       int64 `json:"app_id"`
		Suspended any   `json:"suspended_at"`
	}
	if _, err := r.gh(ctx, "GET", fmt.Sprintf("/app/installations/%d", r.integer("GITHUB_INSTALLATION_ID")), jwt, nil, &installation); err != nil {
		return err
	}
	if installation.ID != r.integer("GITHUB_INSTALLATION_ID") || installation.App != r.integer("GITHUB_APP_ID") || installation.Suspended != nil {
		return blocked("GITHUB_INSTALLATION_IDENTITY_MISMATCH")
	}
	var auth struct {
		Token   string    `json:"token"`
		Expires time.Time `json:"expires_at"`
	}
	_, err = r.gh(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", installation.ID), jwt, map[string]any{"repository_ids": []int64{r.integer("GITHUB_REPOSITORY_ID")}, "permissions": map[string]string{"contents": "read", "statuses": "read", "metadata": "read"}}, &auth)
	if err != nil {
		return err
	}
	if auth.Token == "" {
		return blocked("GITHUB_INSTALLATION_TOKEN_MISSING")
	}
	repositoryPath := "/repos/" + r.cfg.values["GITHUB_REPOSITORY"]
	branch := r.cfg.values["GITHUB_QA_BRANCH"]
	headBranch := "qa-midnight-" + r.receipt.RunID
	marker := ".midnight-acceptance-" + r.receipt.RunID + ".txt"
	markerText := "Disposable Midnight acceptance " + r.receipt.RunID + "\n"
	markerAttempted := false
	headAttempted := false
	prAttempted := false
	previewMarkerAttempted := false
	previewMarkerConfirmed := false
	headConfirmed := false
	prNumber := 0
	var headSHA string
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		r.receipt.Cleanup.Attempted = true
		var cleanupErr error
		if prAttempted && prNumber == 0 {
			var pulls []struct {
				Number int `json:"number"`
			}
			_, err := r.gh(cleanupCtx, "GET", repositoryPath+"/pulls?state=all&head="+url.QueryEscape(strings.Split(r.cfg.values["GITHUB_REPOSITORY"], "/")[0]+":"+headBranch), driver, nil, &pulls)
			if err != nil || len(pulls) > 1 {
				cleanupErr = failed("QA_PULL_REQUEST_DISCOVERY_FAILED")
			} else if len(pulls) == 1 {
				prNumber = pulls[0].Number
			} else {
				// A timed-out POST may still commit or become indexed later.
				cleanupErr = failed("QA_PULL_REQUEST_CREATE_OUTCOME_UNKNOWN")
			}
		}
		if prNumber > 0 {
			var owned struct {
				Number int `json:"number"`
				Head   struct {
					Ref        string `json:"ref"`
					SHA        string `json:"sha"`
					Repository struct {
						ID int64 `json:"id"`
					} `json:"repo"`
				} `json:"head"`
				Base struct {
					Ref        string `json:"ref"`
					Repository struct {
						ID int64 `json:"id"`
					} `json:"repo"`
				} `json:"base"`
			}
			_, ownershipErr := r.gh(cleanupCtx, "GET", fmt.Sprintf("%s/pulls/%d", repositoryPath, prNumber), driver, nil, &owned)
			if ownershipErr != nil || owned.Number != prNumber || owned.Head.Ref != headBranch || owned.Head.SHA != headSHA || owned.Head.Repository.ID != r.integer("GITHUB_REPOSITORY_ID") || owned.Base.Ref != branch || owned.Base.Repository.ID != r.integer("GITHUB_REPOSITORY_ID") {
				cleanupErr = failed("QA_PULL_REQUEST_OWNERSHIP_UNVERIFIED")
			} else {
				var response struct {
					State string `json:"state"`
				}
				if _, err := r.gh(cleanupCtx, "PATCH", fmt.Sprintf("%s/pulls/%d", repositoryPath, prNumber), driver, map[string]string{"state": "closed"}, &response); err != nil || response.State != "closed" {
					cleanupErr = failed("QA_PULL_REQUEST_CLOSE_FAILED")
				} else if err := r.waitPreview(cleanupCtx, prNumber, "", true); err != nil {
					cleanupErr = err
				}
			}
		}
		if headAttempted {
			if previewMarkerAttempted && !previewMarkerConfirmed {
				var file struct {
					SHA      string `json:"sha"`
					Content  string `json:"content"`
					Encoding string `json:"encoding"`
				}
				_, err := r.gh(cleanupCtx, "GET", repositoryPath+"/contents/"+marker+"-preview?ref="+url.QueryEscape(headBranch), driver, nil, &file)
				decoded, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
				if err != nil || !commitSHA.MatchString(file.SHA) || file.Encoding != "base64" || string(decoded) != markerText {
					cleanupErr = failed("QA_PREVIEW_MARKER_CREATE_OUTCOME_UNKNOWN")
				}
			}
			status, err := r.gh(cleanupCtx, "DELETE", repositoryPath+"/git/refs/heads/"+headBranch, driver, nil, nil)
			if err != nil && status != 404 {
				cleanupErr = err
			}
			if status == 404 && !headConfirmed {
				cleanupErr = failed("QA_BRANCH_CREATE_OUTCOME_UNKNOWN")
			}
			status, _ = r.gh(cleanupCtx, "GET", repositoryPath+"/git/ref/heads/"+headBranch, driver, nil, nil)
			if status != 404 {
				cleanupErr = failed("QA_BRANCH_CLEANUP_UNVERIFIED")
			}
		}
		if markerAttempted {
			var file struct {
				SHA      string `json:"sha"`
				Content  string `json:"content"`
				Encoding string `json:"encoding"`
			}
			path := repositoryPath + "/contents/" + marker
			status, err := r.gh(cleanupCtx, "GET", path+"?ref="+url.QueryEscape(branch), driver, nil, &file)
			if status != 404 {
				decoded, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
				if err != nil || file.Encoding != "base64" || string(decoded) != markerText || !commitSHA.MatchString(file.SHA) {
					cleanupErr = failed("QA_MARKER_OWNERSHIP_UNVERIFIED")
				} else {
					var removed struct {
						Commit struct {
							SHA string `json:"sha"`
						} `json:"commit"`
					}
					_, err := r.gh(cleanupCtx, "DELETE", path, driver, map[string]string{"message": "Remove disposable Midnight acceptance marker", "branch": branch, "sha": file.SHA}, &removed)
					if err != nil {
						cleanupErr = err
					} else if err := r.waitGitHubDeployment(cleanupCtx, auth.Token, removed.Commit.SHA, r.cfg.values["QA_ENVIRONMENT_ID"]); err != nil {
						cleanupErr = err
					}
				}
			} else {
				// No file observed means no positive cleanup of an attempted
				// create, even if this list read happened before a late commit.
				cleanupErr = failed("QA_MARKER_CREATE_OUTCOME_UNKNOWN")
			}
			status, _ = r.gh(cleanupCtx, "GET", path+"?ref="+url.QueryEscape(branch), driver, nil, nil)
			if status != 404 {
				cleanupErr = failed("QA_MARKER_CLEANUP_UNVERIFIED")
			}
		}
		if _, err := r.gh(cleanupCtx, "DELETE", "/installation/token", auth.Token, nil, nil); err != nil {
			cleanupErr = failed("GITHUB_TOKEN_REVOCATION_UNVERIFIED")
		}
		r.receipt.Cleanup.Verified = cleanupErr == nil
		r.receipt.Cleanup.RetainedEvidence = []string{"COMMITS", "CLOSED_PULL_REQUEST", "DEPLOYMENT_AND_OPERATION_HISTORY", "AUDIT_ROWS"}
		if markerAttempted {
			r.receipt.Cleanup.RetainedResources = []string{"QA_BASE_SERVICE_ON_CLEANUP_COMMIT"}
		}
		if cleanupErr != nil {
			journalID := "acceptance-github-" + r.receipt.RunID + ".json"
			snapshot := struct {
				Schema                 string `json:"schema_version"`
				RunID                  string `json:"run_id"`
				RepositoryID           string `json:"repository_id"`
				Repository             string `json:"repository"`
				BaseBranch             string `json:"base_branch"`
				ExpectedBaseSHA        string `json:"expected_base_sha"`
				HeadBranch             string `json:"head_branch"`
				Marker                 string `json:"marker_path"`
				PR                     int    `json:"pull_request,omitempty"`
				MarkerAttempted        bool   `json:"marker_create_attempted"`
				HeadAttempted          bool   `json:"head_create_attempted"`
				PRPending              bool   `json:"pull_request_create_attempted"`
				PreviewMarkerAttempted bool   `json:"preview_marker_create_attempted"`
				PreviewMarkerSHA       string `json:"preview_marker_commit_sha,omitempty"`
			}{"midnight.github-residual/v1", r.receipt.RunID, r.cfg.values["GITHUB_REPOSITORY_ID"], r.cfg.values["GITHUB_REPOSITORY"], branch, r.cfg.values["GITHUB_EXPECTED_HEAD"], headBranch, marker, prNumber, markerAttempted, headAttempted, prAttempted, previewMarkerAttempted, headSHA}
			r.receipt.Cleanup.ResidualJournalID = journalID
			if err := writeExclusivePrivateJSON(filepath.Join(r.cfg.directory, journalID), snapshot); err == nil {
				r.receipt.Cleanup.ResidualJournalLocation = "CONFIG_DIRECTORY"
			} else {
				r.receipt.Cleanup.ResidualJournalLocation = "PRESERVATION_FAILED"
			}
		}
		result = cleanupError(result, cleanupErr)
	}()
	if time.Until(auth.Expires) < r.cfg.deadline+4*time.Minute {
		return blocked("GITHUB_TOKEN_LIFETIME_TOO_SHORT")
	}
	var inventory struct {
		Count        int `json:"total_count"`
		Repositories []struct {
			ID      int64  `json:"id"`
			Name    string `json:"full_name"`
			Private bool   `json:"private"`
		} `json:"repositories"`
	}
	if _, err := r.gh(ctx, "GET", "/installation/repositories?per_page=100", auth.Token, nil, &inventory); err != nil {
		return err
	}
	if inventory.Count != 1 || len(inventory.Repositories) != 1 || inventory.Repositories[0].ID != r.integer("GITHUB_REPOSITORY_ID") || inventory.Repositories[0].Name != r.cfg.values["GITHUB_REPOSITORY"] || !inventory.Repositories[0].Private {
		return blocked("PRIVATE_REPOSITORY_TOKEN_SCOPE_MISMATCH")
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if _, err := r.gh(ctx, "GET", repositoryPath+"/git/ref/heads/"+branch, driver, nil, &ref); err != nil {
		return err
	}
	if ref.Object.SHA != r.cfg.values["GITHUB_EXPECTED_HEAD"] {
		return blocked("QA_BRANCH_HEAD_MOVED")
	}
	status, _ := r.gh(ctx, "GET", repositoryPath+"/contents/"+marker+"?ref="+branch, driver, nil, nil)
	if status != 404 {
		return blocked("QA_MARKER_ALREADY_EXISTS")
	}
	r.receipt.pass("REAL_APP_INSTALLATION_PRIVATE_REPOSITORY_AND_PINNED_BRANCH")
	markerAttempted = true
	sha, err := r.putMarker(ctx, driver, repositoryPath, branch, marker, markerText)
	if err != nil {
		return err
	}
	var pushed struct {
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if _, err := r.gh(ctx, "GET", repositoryPath+"/git/commits/"+sha, driver, nil, &pushed); err != nil {
		return err
	}
	if len(pushed.Parents) != 1 || pushed.Parents[0].SHA != r.cfg.values["GITHUB_EXPECTED_HEAD"] {
		return failed("QA_BRANCH_CHANGED_DURING_PUSH")
	}
	if err := r.waitGitHubDeployment(ctx, auth.Token, sha, r.cfg.values["QA_ENVIRONMENT_ID"]); err != nil {
		return err
	}
	r.receipt.pass("REAL_PUSH_GITHUB_PENDING_SUCCESS_AND_CANONICAL_ACTIVE_DEPLOYMENT")
	status, _ = r.gh(ctx, "GET", repositoryPath+"/git/ref/heads/"+headBranch, driver, nil, nil)
	if status != 404 {
		return failed("QA_PREVIEW_BRANCH_ALREADY_EXISTS")
	}
	headAttempted = true
	if _, err := r.gh(ctx, "POST", repositoryPath+"/git/refs", driver, map[string]string{"ref": "refs/heads/" + headBranch, "sha": sha}, nil); err != nil {
		return err
	}
	headConfirmed = true
	previewMarkerAttempted = true
	headSHA, err = r.putMarker(ctx, driver, repositoryPath, headBranch, marker+"-preview", markerText)
	if err != nil {
		return err
	}
	previewMarkerConfirmed = true
	prAttempted = true
	var pull struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if _, err := r.gh(ctx, "POST", repositoryPath+"/pulls", driver, map[string]any{"title": "Disposable Midnight preview acceptance", "body": "Automated QA scope; this pull request is closed by its acceptance run.", "head": headBranch, "base": branch, "draft": true}, &pull); err != nil {
		return err
	}
	if pull.Number < 1 || pull.Head.SHA != headSHA {
		return failed("QA_PULL_REQUEST_HEAD_MISMATCH")
	}
	prNumber = pull.Number
	if err := r.waitPreview(ctx, pull.Number, headSHA, false); err != nil {
		return err
	}
	r.receipt.pass("REAL_PULL_REQUEST_PREVIEW_CANONICAL_ACTIVATION")
	return nil
}
func (r *runner) putMarker(ctx context.Context, token, repository, branch, path, content string) (string, error) {
	var response struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	_, err := r.gh(ctx, "PUT", repository+"/contents/"+path, token, map[string]string{"message": "Add disposable Midnight acceptance marker", "content": base64.StdEncoding.EncodeToString([]byte(content)), "branch": branch}, &response)
	if err != nil {
		return "", err
	}
	if !commitSHA.MatchString(response.Commit.SHA) {
		return "", failed("GITHUB_COMMIT_RESPONSE_INVALID")
	}
	return response.Commit.SHA, nil
}
func (r *runner) waitGitHubDeployment(ctx context.Context, token, sha, environment string) error {
	if !commitSHA.MatchString(sha) {
		return failed("GITHUB_COMMIT_SHA_INVALID")
	}
	if r.githubStatusBot == "" {
		return failed("GITHUB_APP_IDENTITY_UNVERIFIED")
	}
	return wait(ctx, 3*time.Second, func() (bool, error) {
		var statuses []struct {
			State   string `json:"state"`
			Context string `json:"context"`
			Creator struct {
				Login string `json:"login"`
			} `json:"creator"`
		}
		if _, err := r.gh(ctx, "GET", "/repos/"+r.cfg.values["GITHUB_REPOSITORY"]+"/commits/"+sha+"/statuses?per_page=100", token, nil, &statuses); err != nil {
			return false, err
		}
		pending, success := false, false
		contextName := r.cfg.values["GITHUB_STATUS_CONTEXT"]
		if contextName == "" {
			contextName = "midnight/deploy"
		}
		for _, status := range statuses {
			if status.Context != contextName || status.Creator.Login != r.githubStatusBot {
				continue
			}
			switch status.State {
			case "pending":
				pending = true
			case "success":
				success = true
			case "failure", "error":
				return false, failed("GITHUB_REPORTED_DEPLOYMENT_FAILURE")
			}
		}
		if !pending || !success {
			return false, nil
		}
		var active struct {
			Commit      string `json:"commit_sha"`
			Repository  string `json:"source_repository"`
			Source      string `json:"source_type"`
			Actor       string `json:"trigger_actor_type"`
			Project     string `json:"project_id"`
			Service     string `json:"service_id"`
			Environment string `json:"environment_id"`
			Active      bool   `json:"active"`
			Status      string `json:"status"`
		}
		status, err := r.api(ctx, "GET", "/api/v1alpha/services/"+r.cfg.values["QA_SERVICE_ID"]+"/deployments/latest-active?environment_id="+environment, nil, &active)
		if status == 404 {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if active.Commit != sha {
			return false, nil
		}
		repository := strings.TrimSuffix(strings.TrimPrefix(active.Repository, "https://github.com/"), ".git")
		return active.Active && active.Status == "ACTIVE" && active.Source == "github" && repository == r.cfg.values["GITHUB_REPOSITORY"] && active.Actor == "provider_automation" && active.Project == r.cfg.values["QA_PROJECT_ID"] && active.Service == r.cfg.values["QA_SERVICE_ID"] && active.Environment == environment, nil
	})
}
func (r *runner) waitPreview(ctx context.Context, number int, sha string, deleted bool) error {
	return wait(ctx, 3*time.Second, func() (bool, error) {
		var rows struct {
			Items   []preview `json:"items"`
			Service string    `json:"service_id"`
			Project string    `json:"project_id"`
		}
		if _, err := r.api(ctx, "GET", r.previewsPath(), nil, &rows); err != nil {
			return false, err
		}
		if rows.Service != r.cfg.values["QA_SERVICE_ID"] || rows.Project != r.cfg.values["QA_PROJECT_ID"] {
			return false, failed("PREVIEW_SCOPE_MISMATCH")
		}
		for _, item := range rows.Items {
			if item.Number != number {
				continue
			}
			if deleted {
				return false, nil
			}
			if item.Head != sha || item.Deployment == "" || !identifier.MatchString(item.Deployment) || !identifier.MatchString(item.Environment) {
				return false, nil
			}
			var deployment struct {
				Commit      string `json:"commit_sha"`
				Active      bool   `json:"active"`
				Status      string `json:"status"`
				Environment string `json:"environment_id"`
			}
			if _, err := r.api(ctx, "GET", "/api/v1alpha/services/"+r.cfg.values["QA_SERVICE_ID"]+"/deployments/"+item.Deployment+"?environment_id="+item.Environment, nil, &deployment); err != nil {
				return false, err
			}
			if deployment.Status == "FAILED" {
				return false, failed("PREVIEW_DEPLOYMENT_FAILED")
			}
			if deployment.Commit == sha && deployment.Active && deployment.Status == "ACTIVE" && deployment.Environment == item.Environment {
				if r.previewScopes == nil {
					r.previewScopes = map[int]preview{}
				}
				r.previewScopes[number] = item
				if r.runtimeSSH != nil {
					scope := runtimeScope{ProjectID: r.cfg.values["QA_PROJECT_ID"], ServiceID: r.cfg.values["QA_SERVICE_ID"], EnvironmentID: item.Environment, DeploymentID: item.Deployment}
					observed, err := probePreviewRuntime(ctx, *r.runtimeSSH, scope)
					if err != nil {
						return false, err
					}
					if r.runtimeBaselines == nil {
						r.runtimeBaselines = map[int]runtimeObservation{}
					}
					r.runtimeBaselines[number] = observed
					r.receipt.pass("EXACT_PREVIEW_CONTAINER_RUNNING_ON_PINNED_DOCKER_ENGINE")
				}
				return true, nil
			}
			return false, nil
		}
		if deleted {
			// The product collection omits deleted previews. Absence alone
			// could also mean a delayed webhook, so require the exact preview
			// observed by this run to have its environment retired too. Human
			// deployment reads hide retired environments, so their 404 is not
			// independent evidence that the underlying container was removed.
			owned, ok := r.previewScopes[number]
			if !ok {
				return false, failed("PREVIEW_CLEANUP_LINEAGE_UNOBSERVED")
			}
			status, err := r.api(ctx, "GET", "/api/v1alpha/environments/"+owned.Environment, nil, nil)
			if status != 404 {
				if err != nil {
					return false, err
				}
				return false, nil
			}
			var removed struct {
				ID          string `json:"id"`
				Environment string `json:"environment_id"`
				Active      bool   `json:"active"`
				Observed    string `json:"runtime_observed_state"`
			}
			status, err = r.api(ctx, "GET", "/api/v1alpha/services/"+r.cfg.values["QA_SERVICE_ID"]+"/deployments/"+owned.Deployment+"?environment_id="+owned.Environment, nil, &removed)
			if status == 404 {
				return r.previewRuntimeCleanup(ctx, number, owned)
			}
			if err != nil {
				return false, err
			}
			if removed.ID == owned.Deployment && removed.Environment == owned.Environment && !removed.Active && removed.Observed == "removed" {
				return r.previewRuntimeCleanup(ctx, number, owned)
			}
			return false, nil
		}
		return false, nil
	})
}

func (r *runner) previewRuntimeCleanup(ctx context.Context, number int, owned preview) (bool, error) {
	if r.runtimeSSH == nil {
		return true, nil
	}
	baseline, ok := r.runtimeBaselines[number]
	if !ok {
		return false, failed("RUNTIME_POSITIVE_CONTROL_MISSING")
	}
	scope := runtimeScope{ProjectID: r.cfg.values["QA_PROJECT_ID"], ServiceID: r.cfg.values["QA_SERVICE_ID"], EnvironmentID: owned.Environment, DeploymentID: owned.Deployment}
	if err := confirmPreviewRuntimeRemoved(ctx, *r.runtimeSSH, scope, baseline); err != nil {
		return false, err
	}
	r.receipt.pass("EXACT_PREVIEW_CONTAINERS_REMOVED_ON_SAME_DOCKER_ENGINE")
	return true, nil
}
