//go:build ignore

// Controlled failed-deployment fixture plus the real diagnosis Runner, HTTP
// provider adapter, conversation adapter and PostgreSQL event stores. The local
// HTTP model is a fixture, not external provider or full worker/runtime proof.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/agent/claude"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/agent/diagnosis"
	"github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type input struct {
	Mode          string `json:"mode"`
	DSN           string `json:"dsn"`
	ProjectID     string `json:"project_id"`
	ServiceID     string `json:"service_id"`
	EnvironmentID string `json:"environment_id"`
	OperationID   string `json:"operation_id"`
	DeploymentID  string `json:"deployment_id"`
}
type result struct {
	OperationID    string `json:"operation_id"`
	DeploymentID   string `json:"deployment_id"`
	ConversationID string `json:"conversation_id,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "disposable browser diagnosis fixture failed")
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 1 {
		return errors.New("stdin configuration required")
	}
	var in input
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil || len(data) > 4096 {
		return errors.New("bounded stdin configuration required")
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("one configuration required")
	}
	u, err := url.Parse(in.DSN)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.Path != "/midnight_browser_e2e" || u.Fragment != "" {
		return errors.New("disposable loopback database required")
	}
	for name, values := range u.Query() {
		if len(values) != 1 || (name != "sslmode" && name != "connect_timeout") || (name == "sslmode" && values[0] != "disable") {
			return errors.New("connection override refused")
		}
		if name == "connect_timeout" {
			seconds, err := strconv.Atoi(values[0])
			if err != nil || seconds < 1 || seconds > 60 {
				return errors.New("connection timeout refused")
			}
		}
	}
	idPattern := regexp.MustCompile(`^[A-Za-z0-9_-]{3,160}$`)
	for _, id := range []string{in.ProjectID, in.ServiceID, in.EnvironmentID} {
		if !idPattern.MatchString(id) {
			return errors.New("exact fixture scope required")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: in.DSN, MaxConns: 3})
	if err != nil {
		return err
	}
	defer pool.Close()
	var scopeExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects p JOIN services s ON s.project_id=p.id AND s.id=$2 AND s.deleted_at IS NULL JOIN environments e ON e.project_id=p.id AND e.id=$3 AND e.deleted_at IS NULL WHERE p.id=$1 AND p.deleted_at IS NULL)`, in.ProjectID, in.ServiceID, in.EnvironmentID).Scan(&scopeExists); err != nil || !scopeExists {
		return errors.New("fixture lineage unavailable")
	}
	operations := api.NewPostgresOperationStore(pool)
	deployments := api.NewPostgresDeploymentStore(pool)
	var out result
	switch in.Mode {
	case "prepare":
		if in.OperationID != "" || in.DeploymentID != "" {
			return errors.New("prepare cannot overwrite a fixture")
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		suffix := hex.EncodeToString(nonce[:])
		out = result{OperationID: "op_browser_diagnosis_" + suffix, DeploymentID: "dep_browser_diagnosis_" + suffix}
		op, err := operations.Create(ctx, orailtypes.Operation{ID: orailtypes.ID(out.OperationID), Kind: "deploy", State: orailtypes.OperationRunning})
		if err != nil {
			return err
		}
		if _, err := deployments.CreateDeployment(ctx, api.DeploymentRecord{ID: out.DeploymentID, ProjectID: in.ProjectID, ServiceID: in.ServiceID, EnvironmentID: in.EnvironmentID, OperationID: string(op.ID), Action: deploy.ActionDeployImage, ImageRef: "browser-controlled-diagnosis-fixture", State: deploy.DeploymentQueued}); err != nil {
			return err
		}
		if err := deployments.FailDeployment(ctx, out.DeploymentID, deploy.DeploymentQueued, "RECONCILE_STEP_FAILED", "Controlled browser failure: listener port mismatch"); err != nil {
			return err
		}
		if _, err := operations.Fail(ctx, op.ID, "RECONCILE_STEP_FAILED", "Controlled browser failure: listener port mismatch"); err != nil {
			return err
		}
	case "run":
		fixtureID := regexp.MustCompile(`^(?:op|dep)_browser_diagnosis_[a-f0-9]{32}$`)
		if !fixtureID.MatchString(in.OperationID) || !fixtureID.MatchString(in.DeploymentID) {
			return errors.New("owned fixture IDs required")
		}
		var matches bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE id=$1 AND operation_id=$2 AND project_id=$3 AND service_id=$4 AND environment_id=$5 AND state='failed')`, in.DeploymentID, in.OperationID, in.ProjectID, in.ServiceID, in.EnvironmentID).Scan(&matches); err != nil || !matches {
			return errors.New("owned failed fixture required")
		}
		op, err := operations.Get(ctx, orailtypes.ID(in.OperationID))
		if err != nil || op.State != orailtypes.OperationFailed {
			return errors.New("failed operation required")
		}
		const model = "browser-local-diagnosis-model"
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Model string            `json:"model"`
				Tools []json.RawMessage `json:"tools"`
			}
			if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "browser-local-provider" || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request) != nil || request.Model != model || len(request.Tools) != 0 {
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"browser-report","type":"message","role":"assistant","model":"browser-local-diagnosis-model","content":[{"type":"text","text":"Controlled browser diagnosis: set PORT to the application listener."}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":10}}`)
		}))
		defer provider.Close()
		caller, err := claude.NewCaller(claude.Options{APIKey: "browser-local-provider", BaseURL: provider.URL, Registry: tools.New(), DefaultModel: model, Logger: log.New(io.Discard, "", 0)})
		if err != nil {
			return err
		}
		adapted, err := diagnosis.NewAgentCaller(caller, diagnosis.AgentCallerOptions{})
		if err != nil {
			return err
		}
		service, err := conversation.NewService(conversation.Config{Store: conversation.NewPostgresStore(pool)})
		if err != nil {
			return err
		}
		runner := diagnosis.NewRunner(diagnosis.Options{Enabled: true, Model: model, Caller: adapted, Conversations: diagnosis.NewConversationStore(service, "agent:diagnosis", model), Services: fixtureContext{pool}, Events: operations, Timeout: 30 * time.Second, AsyncRun: func(fn func()) { fn() }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		events, err := operations.ListEvents(ctx, op.ID)
		if err != nil {
			return err
		}
		runner.OnFailed(ctx, op, events)
		events, err = operations.ListEvents(ctx, op.ID)
		if err != nil {
			return err
		}
		out = result{OperationID: in.OperationID, DeploymentID: in.DeploymentID}
		for _, event := range events {
			if strings.HasPrefix(event.Message, diagnosis.EventNameDiagnosisAttached+" diagnosis_id=") {
				out.ConversationID = strings.TrimPrefix(event.Message, diagnosis.EventNameDiagnosisAttached+" diagnosis_id=")
			}
		}
		if out.ConversationID == "" {
			return errors.New("real runner did not attach report")
		}
	default:
		return errors.New("unknown fixture mode")
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

type fixtureContext struct{ pool *pg.Pool }

func (s fixtureContext) Resolve(ctx context.Context, op orailtypes.Operation) (diagnosis.ServiceContext, error) {
	var result diagnosis.ServiceContext
	var spec []byte
	err := s.pool.QueryRow(ctx, `SELECT p.workspace_id,d.project_id,d.environment_id,d.service_id,s.slug,d.id,s.spec FROM deployments d JOIN services s ON s.id=d.service_id AND s.project_id=d.project_id JOIN projects p ON p.id=d.project_id JOIN environments e ON e.id=d.environment_id AND e.project_id=p.id WHERE d.operation_id=$1`, string(op.ID)).Scan(&result.WorkspaceID, &result.ProjectID, &result.EnvironmentID, &result.ServiceID, &result.ServiceSlug, &result.DeploymentID, &spec)
	result.SpecDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(spec))
	return result, err
}
