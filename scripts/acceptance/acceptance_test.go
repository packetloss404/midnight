package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/dns"
)

// These are harness contract doubles. They do not run an external provider,
// GitHub App, real DNS, or ACME and must never be presented as live acceptance.
func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLiteralConfigRejectsAmbiguousEntries(t *testing.T) {
	for _, input := range []string{"export TARGET_CLASS=disposable-qa", "TARGET_CLASS=x\nTARGET_CLASS=y", "TARGET_CLASS=\"disposable-qa\"", "UNKNOWN_SECRET=do-not-echo", "TARGET_CLASS=x\nTARGET_CLASS=", "NO_EQUALS"} {
		if _, err := parseEnv(strings.NewReader(input)); err == nil {
			t.Errorf("accepted ambiguous configuration")
		}
	}
	values, err := parseEnv(strings.NewReader("# literal data, never shell\nMODEL=$(this-is-not-executed)\nTARGET_CLASS=disposable-qa\n"))
	if err != nil || values["MODEL"] != "$(this-is-not-executed)" {
		t.Fatal("configuration must be literal, not shell code")
	}
}

func TestScopeValidationAndExplicitAuthorization(t *testing.T) {
	for _, endpoint := range []string{"http://qa.example.invalid", "https://user:secret@qa.example.invalid", "https://qa.example.invalid/api", "https://qa.example.invalid?key=secret", "file:///tmp/config"} {
		if safeAPIBase(endpoint) {
			t.Errorf("accepted unsafe API base")
		}
	}
	for _, endpoint := range []string{"https://qa.example.invalid", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if !safeAPIBase(endpoint) {
			t.Errorf("rejected scoped endpoint")
		}
	}
	if validQASuffix("example.invalid", "example.invalid") || validQASuffix("example.invalid", "other.invalid") || validQASuffix("example.invalid", "bad..example.invalid") {
		t.Fatal("zone apex or other zone accepted")
	}
	if !validQASuffix("example.invalid", "qa.example.invalid") {
		t.Fatal("disposable child suffix rejected")
	}
	c := config{values: map[string]string{}}
	if c.authorize("agent") == nil {
		t.Fatal("missing external mutation acknowledgement accepted")
	}
	c.values["ACK_EXTERNAL_MUTATIONS"] = "disposable-scopes-only"
	if c.authorize("agent") == nil {
		t.Fatal("missing paid call acknowledgement accepted")
	}
	c.values["ACK_PAID_CALLS"] = "allow-bounded-calls"
	if c.authorize("agent") != nil || c.authorize("diagnosis") != nil {
		t.Fatal("explicit authorization rejected")
	}
	if c.authorize("dns-acme") == nil {
		t.Fatal("DNS mutation acknowledgement not required")
	}
}

func TestPrivateFilesRequireRegularOwnerOnlyBoundedInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix owner-only permissions; the command itself blocks Windows")
	}
	dir := privateTempDir(t)
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("never-echo-this"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(path, 64); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(path, 2); err == nil {
		t.Fatal("oversized private input accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(path, 64); err == nil {
		t.Fatal("world readable secret accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(link, 64); err == nil {
		t.Fatal("symlink secret accepted")
	}
}

func TestMissingPrerequisiteAndBadArgumentsNeverLookGreen(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"--lane", "agent"}, &out); code != 78 {
		t.Fatalf("missing credential exit=%d", code)
	}
	var result receipt
	if json.Unmarshal(out.Bytes(), &result) != nil || result.State != "BLOCKED" || result.Cleanup.Verified {
		t.Fatalf("invalid blocked receipt: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"--secret-token=NEVER_PRINT_THIS"}, &out); code != 64 {
		t.Fatal("unknown secret argument accepted")
	}
	if strings.Contains(out.String(), "NEVER_PRINT_THIS") {
		t.Fatal("argument leaked into receipt")
	}
	out.Reset()
	if code := run([]string{"--lane", "NEVER_PRINT_PRIVATE_SCOPE"}, &out); code != 78 || strings.Contains(out.String(), "NEVER_PRINT_PRIVATE_SCOPE") {
		t.Fatal("unknown lane leaked or looked green")
	}
}

func TestReadyPreflightIsNonzeroAndReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("full file-mode preflight runs on Linux; Windows command blocks")
	}
	writes := 0
	administrator := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" {
			writes++
			w.WriteHeader(500)
			return
		}
		if req.Header.Get("Authorization") != "Bearer midn_user_private_fixture" {
			t.Error("human session header missing")
		}
		var value any
		switch req.URL.Path {
		case "/api/v1alpha/compat":
			value = map[string]any{"build_commit": strings.Repeat("a", 40)}
		case "/api/v1alpha/auth/me":
			value = map[string]any{"principal": map[string]any{"user_id": "usr_qa", "instance_admin": administrator}, "session": map[string]string{"id": "session_qa"}}
		case "/api/v1alpha/agent/conversations":
			value = map[string]any{"data": []any{}, "page": map[string]any{}}
		case "/api/v1alpha/projects":
			value = []any{map[string]string{"id": "prj_qa", "workspace_id": "ws_qa"}}
		case "/api/v1alpha/services":
			value = []any{map[string]string{"id": "svc_qa", "project_id": "prj_qa"}}
		case "/api/v1alpha/environments":
			value = []any{map[string]string{"id": "env_qa", "project_id": "prj_qa"}}
		case "/api/v1alpha/services/svc_qa/environments/env_qa/variables":
			value = []any{}
		default:
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	defer server.Close()
	dir := privateTempDir(t)
	_ = os.WriteFile(filepath.Join(dir, "human.token"), []byte("midn_user_private_fixture"), 0600)
	env := "TARGET_CLASS=disposable-qa\nQA_API_BASE=" + server.URL + "\nQA_EXPECTED_COMMIT=" + strings.Repeat("a", 40) + "\nQA_WORKSPACE_ID=ws_qa\nQA_PROJECT_ID=prj_qa\nQA_SERVICE_ID=svc_qa\nQA_ENVIRONMENT_ID=env_qa\nQA_HUMAN_TOKEN_FILE=human.token\nPROVIDER=openai\nMODEL=fixture-model\n"
	path := filepath.Join(dir, "qa.env")
	_ = os.WriteFile(path, []byte(env), 0600)
	var out bytes.Buffer
	if code := run([]string{"--lane", "agent", "--env-file", path}, &out); code != 3 {
		t.Fatalf("preflight exit=%d receipt=%s", code, out.String())
	}
	var result receipt
	_ = json.Unmarshal(out.Bytes(), &result)
	if result.State != "READY_NOT_EXECUTED" || result.Mode != "preflight" || writes != 0 || len(result.Checks) != 3 {
		t.Fatal("preflight must be read-only and cannot pass")
	}
	for _, raw := range []string{server.URL, "midn_user_private_fixture", "prj_qa", "svc_qa", "env_qa"} {
		if strings.Contains(out.String(), raw) {
			t.Fatal("private scope or credential leaked")
		}
	}
	administrator = false
	if err := os.WriteFile(path, []byte(env+"DIAGNOSIS_IMAGE=registry.example.invalid/qa/failure@sha256:"+strings.Repeat("a", 64)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, lane := range []string{"agent", "diagnosis"} {
		out.Reset()
		if code := run([]string{"--lane", lane, "--env-file", path, "--execute"}, &out); code != 78 {
			t.Fatal("ordinary human session was not blocked before execution")
		}
		_ = json.Unmarshal(out.Bytes(), &result)
		if result.Reason != "HUMAN_INSTANCE_ADMIN_SESSION_REQUIRED" || writes != 0 || result.Cleanup.Attempted {
			t.Fatal("ordinary tenant authority reached a write/cleanup path")
		}
	}
}

func TestHTTPRefusesRedirectAndRedactsProviderErrors(t *testing.T) {
	leaked := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, destination.URL, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"CAPABILITY_UNAVAILABLE","message":"secret-provider-token and private host"}}`)
	}))
	defer server.Close()
	r := newRunner(config{}, &receipt{})
	_, err := r.request(context.Background(), "GET", server.URL+"/redirect", map[string]string{"Authorization": "Bearer secret"}, nil, nil)
	if err == nil || leaked {
		t.Fatal("redirect followed or accepted")
	}
	_, err = r.request(context.Background(), "GET", server.URL+"/unavailable", nil, nil, nil)
	var problem *outcomeError
	if !errors.As(err, &problem) || !problem.blocked || problem.code != "CAPABILITY_UNAVAILABLE" {
		t.Fatal("unavailable capability must block with an allowlisted code")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("provider body leaked")
	}
}

func fixtureTurn(name string, input map[string]any) productTurn {
	body, _ := json.Marshal(input)
	output := map[string]any{"ok": true}
	if name == "get_service" {
		output = map[string]any{"id": input["service_id"], "project_id": "prj_qa"}
	}
	if name == "set_variable" {
		output = map[string]any{"ok": true, "service_id": input["service_id"], "environment_id": input["environment_id"], "name": input["name"]}
	}
	resultBody, _ := json.Marshal(output)
	blocks := []agent.ContentBlock{{Kind: agent.BlockText, Text: "Observed result."}}
	if name != "" {
		blocks = append([]agent.ContentBlock{{Kind: agent.BlockToolUse, ToolUse: &agent.ToolCall{ID: "call_1", Name: name, Input: body}}, {Kind: agent.BlockToolResult, ToolResult: &agent.ToolResult{ToolUseID: "call_1", Output: resultBody}}}, blocks...)
	}
	return productTurn{Stop: "end_turn", Assistant: productMessage{Content: blocks, Usage: &agent.Usage{Model: "fixture-model", OutputTokens: 4, IterationCount: 2}}}
}

func TestToolLoopRequiresCorrelatedSuccessfulExactScope(t *testing.T) {
	valid := fixtureTurn("get_service", map[string]any{"service_id": "svc_qa"})
	validate := func(input map[string]any) bool { return input["service_id"] == "svc_qa" }
	if checkTool(valid, "get_service", validate) != nil {
		t.Fatal("faithful persisted assistant tool blocks rejected")
	}
	for _, kind := range []string{"missing-result", "wrong-scope", "wrong-result-scope", "unmatched-result", "tool-error", "extra-call", "result-before-call"} {
		t.Run(kind, func(t *testing.T) {
			turn := fixtureTurn("get_service", map[string]any{"service_id": "svc_qa"})
			switch kind {
			case "missing-result":
				turn.Assistant.Content = append(turn.Assistant.Content[:1], turn.Assistant.Content[2:]...)
			case "wrong-scope":
				turn.Assistant.Content[0].ToolUse.Input = json.RawMessage(`{"service_id":"svc_other"}`)
			case "wrong-result-scope":
				turn.Assistant.Content[1].ToolResult.Output = json.RawMessage(`{"id":"svc_other"}`)
			case "unmatched-result":
				turn.Assistant.Content[1].ToolResult.ToolUseID = "other"
			case "tool-error":
				turn.Assistant.Content[1].ToolResult.IsError = true
			case "extra-call":
				turn.Assistant.Content = append(turn.Assistant.Content, turn.Assistant.Content[0])
			case "result-before-call":
				turn.Assistant.Content[0], turn.Assistant.Content[1] = turn.Assistant.Content[1], turn.Assistant.Content[0]
			}
			if checkTool(turn, "get_service", validate) == nil {
				t.Fatal("unproven tool loop accepted")
			}
		})
	}
}

func TestProductAgentJourneyAndVersionedCleanup(t *testing.T) {
	for _, mode := range []string{"success", "refusal-writes", "cleanup-fails", "lost-create-response", "cancel-during-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config{values: map[string]string{"QA_PROJECT_ID": "prj_qa", "QA_SERVICE_ID": "svc_qa", "QA_ENVIRONMENT_ID": "env_qa", "MODEL": "fixture-model"}}
			result := receipt{RunID: strings.Repeat("a", 24)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			name := "QA_ACCEPTANCE_" + strings.ToUpper(result.RunID)
			value := "disposable-probe-" + result.RunID
			conversations := map[string]bool{}
			idempotency := map[string]string{}
			messageCalls := 0
			hasVariable := false
			lost := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("Authorization") != "Bearer midn_user_fixture" {
					t.Error("not using real human-session wire header")
				}
				path := req.URL.Path
				if path == "/api/v1alpha/agent/conversations" && req.Method == "POST" {
					var body map[string]any
					_ = json.NewDecoder(req.Body).Decode(&body)
					if body["service_id"] != "svc_qa" || body["project_id"] != "prj_qa" || body["model"] != "fixture-model" {
						t.Error("wrong conversation lineage")
					}
					key := req.Header.Get("Idempotency-Key")
					if key == "" {
						t.Error("no idempotent creation recovery")
					}
					id := idempotency[key]
					if id == "" {
						id = "conv_" + strings.TrimPrefix(key, "qa-agent-")
						idempotency[key] = id
						conversations[id] = true
					}
					if mode == "lost-create-response" && !lost {
						lost = true
						w.WriteHeader(502)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "project_id": "prj_qa", "service_id": "svc_qa", "model": "fixture-model"})
					return
				}
				if strings.HasPrefix(path, "/api/v1alpha/agent/conversations/") {
					id := strings.TrimPrefix(path, "/api/v1alpha/agent/conversations/")
					if strings.HasSuffix(id, "/messages") {
						messageCalls++
						var body struct {
							Allowed []string `json:"allowed_tools"`
							Mutate  bool     `json:"allow_mutations"`
						}
						_ = json.NewDecoder(req.Body).Decode(&body)
						var turn productTurn
						switch messageCalls {
						case 1:
							if body.Mutate || len(body.Allowed) != 1 || body.Allowed[0] != "get_service" {
								t.Error("read tool contract mismatch")
							}
							turn = fixtureTurn("get_service", map[string]any{"service_id": "svc_qa"})
						case 2:
							if body.Mutate || len(body.Allowed) != 1 || body.Allowed[0] != "set_variable" {
								t.Error("mutation-off wire contract mismatch")
							}
							turn = fixtureTurn("", nil)
							if mode == "refusal-writes" {
								hasVariable = true
							}
						case 3:
							if !body.Mutate {
								t.Error("allowed mutation not requested")
							}
							hasVariable = true
							turn = fixtureTurn("set_variable", map[string]any{"service_id": "svc_qa", "environment_id": "env_qa", "name": name, "value": value})
						default:
							t.Error("extra paid turn")
						}
						_ = json.NewEncoder(w).Encode(turn)
						return
					}
					if req.Method == "DELETE" {
						delete(conversations, id)
						w.WriteHeader(204)
						return
					}
					if !conversations[id] {
						w.WriteHeader(404)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
					return
				}
				if strings.HasSuffix(path, "/variables/var_qa") && req.Method == "DELETE" {
					var body struct {
						Version int `json:"expected_version"`
					}
					_ = json.NewDecoder(req.Body).Decode(&body)
					if body.Version != 1 || req.Header.Get("If-Match") != `"1"` || req.Header.Get("Idempotency-Key") == "" {
						t.Error("cleanup lacks current version and If-Match contract")
					}
					if mode == "cleanup-fails" {
						w.WriteHeader(500)
						return
					}
					hasVariable = false
					if mode == "cancel-during-cleanup" {
						cancel()
					}
					w.WriteHeader(204)
					return
				}
				if strings.HasSuffix(path, "/variables") {
					vars := []variable{}
					if hasVariable {
						vars = append(vars, variable{ID: "var_qa", Name: name, Service: "svc_qa", Environment: "env_qa", Version: 1})
					}
					_ = json.NewEncoder(w).Encode(vars)
					return
				}
				if strings.HasSuffix(path, "/audit/events") {
					_ = json.NewEncoder(w).Encode(map[string]any{"events": []any{map[string]string{"resource_id": "var_qa", "action": "variable.create", "row_hash": "digest"}}})
					return
				}
				t.Error("unexpected product path " + path)
				w.WriteHeader(404)
			}))
			defer server.Close()
			cfg.values["QA_API_BASE"] = server.URL
			r := newRunner(cfg, &result)
			r.token = "midn_user_fixture"
			err := executionResult(ctx, r.agent(ctx))
			if mode == "success" {
				if err != nil || len(result.Checks) != 3 || !result.Cleanup.Verified || messageCalls != 3 {
					t.Fatalf("product contract journey failed: %v", err)
				}
			} else if err == nil {
				t.Fatal("failure reported success")
			}
			if mode == "cancel-during-cleanup" && err.Error() != "EXECUTION_CANCELED" {
				t.Fatal("cancellation during successful independent cleanup was not retained")
			}
			if mode == "cleanup-fails" {
				if result.Cleanup.Verified || err.Error() != "CLEANUP_UNVERIFIED" {
					t.Fatal("failed cleanup reported green")
				}
			} else if hasVariable || len(conversations) != 0 || !result.Cleanup.Verified {
				t.Fatal("owned fixture state left after cleanup")
			}
		})
	}
}

type dnsDouble struct {
	records    []dns.Record
	createLost bool
	deletes    int
}

func (d *dnsDouble) Name() string { return "contract-double" }
func (d *dnsDouble) ListRecords(context.Context, string) ([]dns.Record, error) {
	return append([]dns.Record(nil), d.records...), nil
}
func (d *dnsDouble) CreateRecord(_ context.Context, zone string, want dns.DesiredRecord) (dns.Record, error) {
	record := dns.Record{ID: "record_qa", ZoneID: zone, Type: want.Type, Name: want.Name, Content: want.Content}
	d.records = append(d.records, record)
	if d.createLost {
		return dns.Record{}, errors.New("response lost")
	}
	return record, nil
}
func (d *dnsDouble) UpdateRecord(context.Context, string, string, dns.DesiredRecord) (dns.Record, error) {
	return dns.Record{}, errors.New("unexpected update")
}
func (d *dnsDouble) DeleteRecord(_ context.Context, zone, id string) error {
	for i, item := range d.records {
		if item.ID == id {
			d.records = append(d.records[:i], d.records[i+1:]...)
			d.deletes++
			return nil
		}
	}
	return errors.New("not found")
}

func TestDNSChallengeScopeAndLostAcknowledgementCleanup(t *testing.T) {
	t.Setenv("LEGO_DISABLE_CNAME_SUPPORT", "true")
	if runtime.GOOS == "windows" {
		t.Skip("private journal permissions are enforced and tested on Linux")
	}
	for _, lost := range []bool{false, true} {
		dir := privateTempDir(t)
		provider := &dnsDouble{createLost: lost, records: []dns.Record{{ID: "unmanaged", Type: dns.TypeTXT, Name: "keep.example.invalid", Content: "unmanaged"}}}
		p := scopedChallenge{ctx: context.Background(), provider: provider, zone: "zone_qa", domain: "qa.example.invalid", stateDir: dir}
		if p.Present("other.example.invalid", "token", "key.auth") == nil {
			t.Fatal("challenge escaped pinned leaf")
		}
		err := p.Present(p.domain, "token", "key.auth")
		if (err != nil) != lost {
			t.Fatalf("unexpected create outcome %v", err)
		}
		journal, err := readPrivate(filepath.Join(dir, "journal.json"), 4096)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(journal), "key.auth") {
			t.Fatal("raw challenge persisted in journal")
		}
		if err := cleanupDNS(context.Background(), provider, p.zone, "_acme-challenge."+p.domain, dir); err != nil {
			t.Fatal(err)
		}
		if len(provider.records) != 1 || provider.records[0].ID != "unmanaged" || provider.deletes != 1 {
			t.Fatal("cleanup changed unmanaged DNS or missed own lost-ack record")
		}
	}
}

func TestStagingRequiresExistingValidStagingAccount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private native account permissions are enforced on Linux")
	}
	dir := privateTempDir(t)
	_ = os.Chmod(dir, 0700)
	c := config{directory: dir, values: map[string]string{"ACME_STAGING_ACCOUNT_DIR": dir}}
	if _, _, err := stagingAccount(c); err == nil {
		t.Fatal("missing account could trigger registration")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "account.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0600)
	for _, uri := range []string{"https://acme-v02.api.letsencrypt.org/acme/acct/1", "https://other.invalid/acme/acct/1", "https://acme-staging-v02.api.letsencrypt.org/acme/acct/1"} {
		data, _ := json.Marshal(map[string]any{"uri": uri, "body": map[string]any{"status": "valid"}})
		_ = os.WriteFile(filepath.Join(dir, "registration.json"), data, 0600)
		_, _, err := stagingAccount(c)
		if (err == nil) != (strings.Contains(uri, "acme-staging-v02.api.letsencrypt.org")) {
			t.Fatal("staging account authority gate mismatch")
		}
	}
}

func TestDNSCleanupRefusesChangedOwnership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private journal permissions are enforced and tested on Linux")
	}
	dir := privateTempDir(t)
	name := "_acme-challenge.qa.example.invalid"
	if err := writePrivateJSON(filepath.Join(dir, "journal.json"), []dnsIntent{{Name: name, Digest: digest("original"), ID: "owned"}}); err != nil {
		t.Fatal(err)
	}
	p := &dnsDouble{records: []dns.Record{{ID: "owned", Type: dns.TypeTXT, Name: name, Content: "operator-replaced"}}}
	if cleanupDNS(context.Background(), p, "zone_qa", name, dir) == nil || p.deletes != 0 {
		t.Fatal("changed provider record was deleted")
	}
}

func TestBoundedWaitAndCleanupFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := wait(ctx, time.Second, func() (bool, error) { return false, nil }); err == nil {
		t.Fatal("unbounded wait accepted")
	}
	if err := cleanupError(nil, errors.New("secret details")); err == nil || err.Error() != "CLEANUP_UNVERIFIED" {
		t.Fatal("cleanup failure was green or leaked")
	}
	deadline, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	if err := executionResult(deadline, nil); err == nil || err.Error() != "EXECUTION_DEADLINE_EXCEEDED" {
		t.Fatal("expired execution was allowed to pass")
	}
	for _, previous := range []error{failed("EXISTING_FAILURE"), blocked("EXISTING_BLOCKER")} {
		if got := executionResult(deadline, previous); got != previous {
			t.Fatal("existing failure/blocker was replaced")
		}
	}
}
