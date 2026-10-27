package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type runner struct {
	cfg              config
	receipt          *receipt
	client           *http.Client
	token            string
	previewScopes    map[int]preview
	githubStatusBot  string
	runtimeSSH       *runtimeSSHConfig
	runtimeBaselines map[int]runtimeObservation
}

func newRunner(cfg config, result *receipt) *runner {
	return &runner{cfg: cfg, receipt: result, client: &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (r *runner) api(ctx context.Context, method, path string, body any, target any, extra ...map[string]string) (int, error) {
	if !strings.HasPrefix(path, "/api/v1alpha/") || strings.Contains(path, "..") {
		return 0, failed("API_PATH_OUTSIDE_SCOPE")
	}
	headers := map[string]string{"Authorization": "Bearer " + r.token}
	for _, h := range extra {
		for key, value := range h {
			headers[key] = value
		}
	}
	return r.request(ctx, method, strings.TrimRight(r.cfg.values["QA_API_BASE"], "/")+path, headers, body, target)
}
func (r *runner) request(ctx context.Context, method, endpoint string, headers map[string]string, body any, target any) (int, error) {
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, failed("REQUEST_ENCODING")
		}
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, input)
	if err != nil {
		return 0, failed("REQUEST_INVALID")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "Midnight-live-acceptance")
	response, err := r.client.Do(req)
	if err != nil {
		return 0, failed("REMOTE_REQUEST_FAILED")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return response.StatusCode, failed("REMOTE_RESPONSE_LIMIT")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Only fixed public codes may influence receipts. Never forward an
		// upstream message, URL, token, or arbitrary provider error string.
		var problem struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		if response.StatusCode == http.StatusUnauthorized {
			return response.StatusCode, blocked("CREDENTIAL_REJECTED")
		}
		if response.StatusCode == http.StatusServiceUnavailable {
			switch problem.Error.Code {
			case "AGENT_NOT_CONFIGURED", "CAPABILITY_UNAVAILABLE":
				return response.StatusCode, blocked(problem.Error.Code)
			}
		}
		return response.StatusCode, failed("REMOTE_HTTP_REJECTED")
	}
	if target != nil {
		if raw, ok := target.(*[]byte); ok {
			*raw = data
		} else if err := json.Unmarshal(data, target); err != nil {
			return response.StatusCode, failed("REMOTE_JSON_INVALID")
		}
	}
	return response.StatusCode, nil
}
func (r *runner) preflight(ctx context.Context, lane string) error {
	if lane == "dns-acme" {
		return r.dnsPreflight(ctx)
	}
	token, err := r.cfg.secret("QA_HUMAN_TOKEN_FILE")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(token, "midn_user_") {
		return blocked("HUMAN_SESSION_REQUIRED")
	}
	r.token = token
	var compat struct {
		Commit string `json:"build_commit"`
	}
	if _, err := r.api(ctx, "GET", "/api/v1alpha/compat", nil, &compat); err != nil {
		return err
	}
	if compat.Commit != r.cfg.values["QA_EXPECTED_COMMIT"] {
		return blocked("QA_COMMIT_MISMATCH")
	}
	var me struct {
		Principal struct {
			User string `json:"user_id"`
		} `json:"principal"`
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if _, err := r.api(ctx, "GET", "/api/v1alpha/auth/me", nil, &me); err != nil {
		return err
	}
	if me.Principal.User == "" || me.Session.ID == "" {
		return blocked("HUMAN_IDENTITY_UNVERIFIED")
	}
	var projectRows []map[string]any
	if _, err := r.api(ctx, "GET", "/api/v1alpha/projects", nil, &projectRows); err != nil {
		return err
	}
	if len(projectRows) != 1 || projectRows[0]["id"] != r.cfg.values["QA_PROJECT_ID"] || projectRows[0]["workspace_id"] != r.cfg.values["QA_WORKSPACE_ID"] {
		return blocked("QA_PROJECT_NOT_EXCLUSIVE")
	}
	var services []map[string]any
	if _, err := r.api(ctx, "GET", "/api/v1alpha/services?project_id="+r.cfg.values["QA_PROJECT_ID"], nil, &services); err != nil {
		return err
	}
	if len(services) != 1 || services[0]["id"] != r.cfg.values["QA_SERVICE_ID"] || services[0]["project_id"] != r.cfg.values["QA_PROJECT_ID"] {
		return blocked("QA_SERVICE_NOT_EXCLUSIVE")
	}
	var environments []map[string]any
	if _, err := r.api(ctx, "GET", "/api/v1alpha/environments?project_id="+r.cfg.values["QA_PROJECT_ID"], nil, &environments); err != nil {
		return err
	}
	if len(environments) != 1 || environments[0]["id"] != r.cfg.values["QA_ENVIRONMENT_ID"] || environments[0]["project_id"] != r.cfg.values["QA_PROJECT_ID"] {
		return blocked("QA_ENVIRONMENT_NOT_EXCLUSIVE")
	}
	r.receipt.pass("PINNED_COMMIT_HUMAN_AND_EXCLUSIVE_QA_SCOPE")
	if lane == "github" {
		return r.githubPreflight(ctx)
	}
	if lane == "agent" {
		var vars []variable
		if err := r.variables(ctx, &vars); err != nil {
			return err
		}
		if len(vars) != 0 {
			return blocked("EMPTY_QA_VARIABLE_SCOPE_REQUIRED")
		}
		r.receipt.pass("EMPTY_QA_VARIABLE_SCOPE")
	}
	if lane == "diagnosis" {
		base := "/api/v1alpha/services/" + r.cfg.values["QA_SERVICE_ID"] + "/environments/" + r.cfg.values["QA_ENVIRONMENT_ID"]
		for _, suffix := range []string{"domains", "custom-domains"} {
			var domains []json.RawMessage
			if _, err := r.api(ctx, "GET", base+"/"+suffix, nil, &domains); err != nil {
				return err
			}
			if len(domains) != 0 {
				return blocked("PRIVATE_DIAGNOSIS_SCOPE_HAS_DOMAIN_BINDINGS")
			}
		}
		var active json.RawMessage
		status, err := r.api(ctx, "GET", "/api/v1alpha/services/"+r.cfg.values["QA_SERVICE_ID"]+"/deployments/latest-active?environment_id="+r.cfg.values["QA_ENVIRONMENT_ID"], nil, &active)
		if status != 404 {
			if err != nil {
				return err
			}
			return blocked("EMPTY_DIAGNOSIS_RUNTIME_SCOPE_REQUIRED")
		}
		r.receipt.pass("PRIVATE_EMPTY_DIAGNOSIS_RUNTIME_SCOPE")
	}
	r.receipt.incomplete("PROVIDER_DISPATCH_REQUIRES_REVIEWED_QA_SERVER_CONFIGURATION")
	return nil
}
func wait(ctx context.Context, interval time.Duration, probe func() (bool, error)) error {
	for {
		done, err := probe()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return failed("DEADLINE_EXCEEDED")
		case <-timer.C:
		}
	}
}
