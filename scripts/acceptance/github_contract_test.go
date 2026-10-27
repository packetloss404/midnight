package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func httpJSON(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}
}

// A protocol double checks credential separation and cleanup. It does not
// claim an actual installation, webhook delivery, build, or external status.
func TestGitHubDriverAndReadScopedProductAppContract(t *testing.T) {
	for _, mode := range []string{"success", "runtime-success", "runtime-engine-changed", "runtime-container-remains", "runtime-positive-missing", "unbound-app-slug", "lost-pr-ack", "lost-head-ack", "lost-marker-ack", "lost-preview-marker-ack"} {
		t.Run(mode, func(t *testing.T) { runGitHubContractFixture(t, mode) })
	}
}
func runGitHubContractFixture(t *testing.T, mode string) {
	if runtime.GOOS == "windows" {
		t.Skip("private App and driver files require Unix permissions")
	}
	dir := privateTempDir(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "app.pem"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600)
	_ = os.WriteFile(filepath.Join(dir, "driver.token"), []byte("fixture-driver-secret"), 0600)
	sha := strings.Repeat("a", 40)
	headSHA := strings.Repeat("b", 40)
	cleanupSHA := strings.Repeat("c", 40)
	blob := strings.Repeat("d", 40)
	cfg := config{directory: dir, deadline: time.Minute, values: map[string]string{"QA_API_BASE": "https://qa.example.invalid", "QA_PROJECT_ID": "prj_qa", "QA_SERVICE_ID": "svc_qa", "QA_ENVIRONMENT_ID": "env_qa", "GITHUB_APP_ID": "11", "GITHUB_INSTALLATION_ID": "22", "GITHUB_PRIVATE_KEY_FILE": "app.pem", "GITHUB_APP_SLUG": "product-example", "GITHUB_QA_DRIVER_TOKEN_FILE": "driver.token", "GITHUB_QA_DRIVER_LOGIN": "qa-driver", "GITHUB_REPOSITORY_ID": "33", "GITHUB_REPOSITORY": "qa-owner/disposable-qa", "GITHUB_QA_BRANCH": "qa-midnight-main", "GITHUB_EXPECTED_HEAD": strings.Repeat("0", 40)}}
	result := receipt{RunID: strings.Repeat("f", 24)}
	r := newRunner(cfg, &result)
	r.token = "midn_user_fixture"
	observerCalls := 0
	if strings.HasPrefix(mode, "runtime-") {
		r.runtimeSSH = &runtimeSSHConfig{target: "qa@runtime.example.invalid", port: "22", keyFile: filepath.Join(dir, "app.pem"), knownHostsFile: filepath.Join(dir, "driver.token"), exec: func(ctx context.Context, name string, args []string) ([]byte, error) {
			observerCalls++
			engine := "fixture-engine-one"
			containers := ""
			if observerCalls == 1 && mode != "runtime-positive-missing" {
				containers = strings.Repeat("e", 64) + " running\n"
			}
			if observerCalls > 1 && mode == "runtime-engine-changed" {
				engine = "fixture-engine-two"
			}
			if observerCalls > 1 && mode == "runtime-container-remains" {
				containers = strings.Repeat("e", 64) + " exited\n"
			}
			return []byte(runtimeEnginePrefix + engine + "\n" + containers), nil
		}}
	}
	marker := false
	head := false
	closed := false
	revoked := false
	pr := false
	currentSHA := sha
	r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		path := req.URL.Path
		token := req.Header.Get("Authorization")
		var body map[string]any
		if req.Body != nil {
			_ = json.NewDecoder(req.Body).Decode(&body)
		}
		if req.URL.Host == "api.github.com" {
			if path == "/app" {
				slug := "product-example"
				if mode == "unbound-app-slug" {
					slug = "unrelated-app"
				}
				return httpJSON(200, map[string]any{"id": 11, "slug": slug}), nil
			}
			if path == "/user" {
				if token != "Bearer fixture-driver-secret" {
					t.Error("driver identity used wrong credential")
				}
				return httpJSON(200, map[string]string{"login": "qa-driver"}), nil
			}
			if path == "/repos/qa-owner/disposable-qa" {
				return httpJSON(200, map[string]any{"id": 33, "full_name": "qa-owner/disposable-qa", "private": true}), nil
			}
			if path == "/app/installations/22" {
				if token == "Bearer fixture-driver-secret" {
					t.Error("driver bypassed App identity")
				}
				return httpJSON(200, map[string]any{"id": 22, "app_id": 11, "suspended_at": nil}), nil
			}
			if path == "/app/installations/22/access_tokens" {
				permissions, _ := body["permissions"].(map[string]any)
				if permissions["contents"] != "read" || permissions["statuses"] != "read" || permissions["pull_requests"] != nil {
					t.Error("product App token requested test-driver write authority")
				}
				ids, _ := body["repository_ids"].([]any)
				if len(ids) != 1 || ids[0] != float64(33) {
					t.Error("App token not restricted to exact repository")
				}
				return httpJSON(201, map[string]any{"token": "fixture-product-app-secret", "expires_at": time.Now().Add(time.Hour)}), nil
			}
			if path == "/installation/repositories" {
				if token != "Bearer fixture-product-app-secret" {
					t.Error("App inventory used driver credential")
				}
				return httpJSON(200, map[string]any{"total_count": 1, "repositories": []any{map[string]any{"id": 33, "full_name": "qa-owner/disposable-qa", "private": true}}}), nil
			}
			if path == "/installation/token" {
				if req.Method != "DELETE" || token != "Bearer fixture-product-app-secret" {
					t.Error("wrong token revocation")
				}
				revoked = true
				return httpJSON(204, nil), nil
			}
			if strings.HasSuffix(path, "/statuses") {
				if token != "Bearer fixture-product-app-secret" {
					t.Error("status proof used QA driver")
				}
				return httpJSON(200, []any{map[string]any{"state": "success", "context": "midnight/deploy", "creator": map[string]string{"login": "product-example[bot]"}}, map[string]any{"state": "pending", "context": "midnight/deploy", "creator": map[string]string{"login": "product-example[bot]"}}}), nil
			}
			if token != "Bearer fixture-driver-secret" {
				t.Error("content or PR operation used read-scoped product App token")
			}
			if strings.Contains(path, "/git/commits/") {
				return httpJSON(200, map[string]any{"parents": []any{map[string]string{"sha": cfg.values["GITHUB_EXPECTED_HEAD"]}}}), nil
			}
			if strings.HasSuffix(path, "/git/ref/heads/qa-midnight-main") {
				return httpJSON(200, map[string]any{"object": map[string]string{"sha": cfg.values["GITHUB_EXPECTED_HEAD"]}}), nil
			}
			if strings.Contains(path, "/git/ref/heads/") {
				if head {
					return httpJSON(200, map[string]any{}), nil
				}
				return httpJSON(404, nil), nil
			}
			if strings.HasSuffix(path, "/git/refs") {
				if mode == "lost-head-ack" {
					return httpJSON(502, nil), nil
				}
				head = true
				return httpJSON(201, map[string]any{}), nil
			}
			if strings.Contains(path, "/git/refs/heads/") {
				if mode == "lost-head-ack" && !head {
					return httpJSON(404, nil), nil
				}
				head = false
				return httpJSON(204, nil), nil
			}
			if strings.Contains(path, "/contents/") {
				if req.Method == "PUT" {
					if strings.HasSuffix(path, "-preview") {
						if mode == "lost-preview-marker-ack" {
							return httpJSON(502, nil), nil
						}
						return httpJSON(201, map[string]any{"commit": map[string]string{"sha": headSHA}}), nil
					}
					if mode == "lost-marker-ack" {
						return httpJSON(502, nil), nil
					}
					marker = true
					return httpJSON(201, map[string]any{"commit": map[string]string{"sha": sha}}), nil
				}
				if req.Method == "DELETE" {
					if body["sha"] != blob || body["branch"] != "qa-midnight-main" {
						t.Error("marker delete lacked exact blob/branch")
					}
					marker = false
					currentSHA = cleanupSHA
					return httpJSON(200, map[string]any{"commit": map[string]string{"sha": cleanupSHA}}), nil
				}
				if !marker {
					return httpJSON(404, nil), nil
				}
				if strings.HasSuffix(path, "-preview") && mode == "lost-preview-marker-ack" {
					return httpJSON(404, nil), nil
				}
				return httpJSON(200, map[string]string{"sha": blob, "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("Disposable Midnight acceptance " + result.RunID + "\n"))}), nil
			}
			if strings.HasSuffix(path, "/pulls") {
				if req.Method == "GET" {
					return httpJSON(200, []any{}), nil
				}
				if mode == "lost-pr-ack" {
					return httpJSON(502, nil), nil
				}
				pr = true
				return httpJSON(201, map[string]any{"number": 7, "head": map[string]string{"sha": headSHA}}), nil
			}
			if strings.HasSuffix(path, "/pulls/7") {
				if req.Method == "GET" {
					return httpJSON(200, map[string]any{"number": 7, "head": map[string]any{"ref": "qa-midnight-" + result.RunID, "sha": headSHA, "repo": map[string]int{"id": 33}}, "base": map[string]any{"ref": "qa-midnight-main", "repo": map[string]int{"id": 33}}}), nil
				}
				closed = true
				return httpJSON(200, map[string]string{"state": "closed"}), nil
			}
		} else if req.URL.Host == "qa.example.invalid" {
			if token != "Bearer midn_user_fixture" {
				t.Error("external credential sent to product")
			}
			if strings.HasSuffix(path, "/latest-active") {
				return httpJSON(200, map[string]any{"commit_sha": currentSHA, "source_repository": "https://github.com/qa-owner/disposable-qa.git", "source_type": "github", "trigger_actor_type": "provider_automation", "project_id": "prj_qa", "service_id": "svc_qa", "environment_id": "env_qa", "active": true, "status": "ACTIVE"}), nil
			}
			if strings.HasSuffix(path, "/preview-environments") {
				state := "active"
				if closed {
					return httpJSON(200, map[string]any{"service_id": "svc_qa", "project_id": "prj_qa", "items": []any{}}), nil
				}
				return httpJSON(200, map[string]any{"service_id": "svc_qa", "project_id": "prj_qa", "items": []any{map[string]any{"pull_request": 7, "head_sha": headSHA, "environment_id": "env_preview", "deployment_id": "dep_preview", "state": state}}}), nil
			}
			if strings.HasSuffix(path, "/dep_preview") {
				if closed {
					return httpJSON(404, nil), nil
				}
				return httpJSON(200, map[string]any{"commit_sha": headSHA, "active": true, "status": "ACTIVE", "environment_id": "env_preview"}), nil
			}
			if path == "/api/v1alpha/environments/env_preview" && closed {
				return httpJSON(404, nil), nil
			}
		}
		t.Errorf("unexpected method/path %s %s", req.Method, path)
		return httpJSON(500, nil), nil
	})
	err = r.github(context.Background())
	if mode != "success" && mode != "runtime-success" {
		if err == nil || result.Cleanup.Verified {
			t.Fatal("unbound App or unknown write outcome reported verified cleanup")
		}
		if mode == "unbound-app-slug" {
			if err.Error() != "GITHUB_APP_ID_OR_SLUG_MISMATCH" || pr || head || marker {
				t.Fatal("unbound App identity was allowed to mutate")
			}
			return
		}
		if err.Error() != "CLEANUP_UNVERIFIED" {
			t.Fatal("unknown write outcome did not remain cleanup-unverified")
		}
		data, err := readPrivate(filepath.Join(dir, "acceptance-github-"+result.RunID+".json"), 8192)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"fixture-driver-secret", "fixture-product-app-secret", "PRIVATE KEY"} {
			if strings.Contains(string(data), secret) {
				t.Fatal("residual journal retained a credential")
			}
		}
		if !strings.Contains(string(data), "qa-midnight-"+result.RunID) || !revoked {
			t.Fatal("scoped ownership or token cleanup missing")
		}
		if mode == "lost-pr-ack" {
			pr = true
			if closed {
				t.Fatal("delayed PR fixture was unexpectedly closed")
			}
		}
		if strings.HasPrefix(mode, "runtime-") && observerCalls == 0 {
			t.Fatal("configured observer was silently skipped")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	expectedChecks := 3
	if mode == "runtime-success" {
		expectedChecks = 5
		if observerCalls != 2 {
			t.Fatal("physical cleanup lacked presence/absence observer pair")
		}
	}
	if !pr || !closed || !revoked || head || marker || !result.Cleanup.Verified || len(result.Checks) != expectedChecks {
		t.Fatal("external contract workflow or cleanup not verified")
	}
	encoded, _ := json.Marshal(result)
	for _, private := range []string{"fixture-engine-one", strings.Repeat("e", 64), "runtime.example.invalid"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("runtime private identity leaked into receipt")
		}
	}
}

func TestGitHubStatusCannotBeProvedByAnotherActor(t *testing.T) {
	r := newRunner(config{values: map[string]string{"GITHUB_REPOSITORY": "qa-owner/disposable-qa", "GITHUB_APP_SLUG": "expected-product"}}, &receipt{})
	r.githubStatusBot = "expected-product[bot]"
	r.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return httpJSON(200, []any{map[string]any{"state": "success", "context": "midnight/deploy", "creator": map[string]string{"login": "wrong-driver"}}, map[string]any{"state": "pending", "context": "midnight/deploy", "creator": map[string]string{"login": "wrong-driver"}}}), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := r.waitGitHubDeployment(ctx, "fixture", strings.Repeat("a", 40), "env_qa"); err == nil {
		t.Fatal("another actor's status accepted as product App proof")
	}
}
