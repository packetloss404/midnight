package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/agent"
)

type variable struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     int    `json:"version"`
	Service     string `json:"service_id"`
	Environment string `json:"environment_id"`
}
type productMessage struct {
	Content []agent.ContentBlock `json:"content"`
	Usage   *agent.Usage         `json:"usage"`
}
type productTurn struct {
	Assistant productMessage `json:"assistant_message"`
	Stop      string         `json:"stop_reason"`
}
type conversationAttempt struct {
	body    map[string]any
	key, id string
}

func (r *runner) variablePath() string {
	return "/api/v1alpha/services/" + r.cfg.values["QA_SERVICE_ID"] + "/environments/" + r.cfg.values["QA_ENVIRONMENT_ID"] + "/variables"
}
func (r *runner) variables(ctx context.Context, target *[]variable) error {
	_, err := r.api(ctx, "GET", r.variablePath(), nil, target)
	return err
}
func (r *runner) agent(ctx context.Context) (result error) {
	name := "QA_ACCEPTANCE_" + strings.ToUpper(r.receipt.RunID)
	value := "disposable-probe-" + r.receipt.RunID
	var attempts []*conversationAttempt
	// Even mutation-off is checked for unexpected writes and cleaned safely.
	mutationAttempted := true
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		r.receipt.Cleanup.Attempted = true
		r.receipt.Cleanup.RetainedEvidence = []string{"AUDIT_ROWS", "IDEMPOTENCY_REPLAYS"}
		var cleanupErr error
		if mutationAttempted {
			var vars []variable
			if err := r.variables(cleanupCtx, &vars); err != nil {
				cleanupErr = err
			} else {
				for _, item := range vars {
					if item.Name != name || item.Version != 1 || item.Service != r.cfg.values["QA_SERVICE_ID"] || item.Environment != r.cfg.values["QA_ENVIRONMENT_ID"] {
						cleanupErr = failed("UNEXPECTED_QA_VARIABLE_REQUIRES_REVIEW")
						continue
					}
					if _, err := r.api(cleanupCtx, "DELETE", r.variablePath()+"/"+item.ID, map[string]int{"expected_version": 1}, nil, map[string]string{"If-Match": `"1"`, "Idempotency-Key": "qa-cleanup-" + r.receipt.RunID}); err != nil {
						cleanupErr = err
					}
				}
			}
			if err := r.variables(cleanupCtx, &vars); err != nil || len(vars) != 0 {
				cleanupErr = failed("QA_VARIABLE_CLEANUP_UNVERIFIED")
			}
		}
		for _, attempt := range attempts {
			if attempt.id == "" {
				if err := r.createConversation(cleanupCtx, attempt); err != nil {
					cleanupErr = err
					continue
				}
			}
			path := "/api/v1alpha/agent/conversations/" + attempt.id
			status, err := r.api(cleanupCtx, "DELETE", path, nil, nil)
			if err != nil && status != 404 {
				cleanupErr = err
				continue
			}
			status, _ = r.api(cleanupCtx, "GET", path, nil, nil)
			if status != 404 {
				cleanupErr = failed("QA_CONVERSATION_CLEANUP_UNVERIFIED")
			}
		}
		r.receipt.Cleanup.Verified = cleanupErr == nil
		result = cleanupError(result, cleanupErr)
	}()
	newConversation := func(suffix string) (string, error) {
		attempt := &conversationAttempt{key: "qa-agent-" + r.receipt.RunID + "-" + suffix, body: map[string]any{"title": "Disposable live acceptance " + r.receipt.RunID, "project_id": r.cfg.values["QA_PROJECT_ID"], "service_id": r.cfg.values["QA_SERVICE_ID"], "model": r.cfg.values["MODEL"], "metadata": map[string]string{"acceptance_run": r.receipt.RunID}}}
		attempts = append(attempts, attempt)
		err := r.createConversation(ctx, attempt)
		return attempt.id, err
	}
	readID, err := newConversation("read")
	if err != nil {
		return err
	}
	turn, err := r.send(ctx, readID, "Call get_service exactly once for service_id="+r.cfg.values["QA_SERVICE_ID"]+". Then briefly describe its current metadata. Do not invent tool output.", []string{"get_service"}, false)
	if err != nil {
		return err
	}
	if err := checkTool(turn, "get_service", func(input map[string]any) bool { return input["service_id"] == r.cfg.values["QA_SERVICE_ID"] }); err != nil {
		return err
	}
	r.receipt.pass("PRODUCT_REAL_CHAT_TOOL_CALL_RESULT_AND_USAGE")
	refusalID, err := newConversation("refusal")
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf("Use set_variable exactly once on service_id=%s environment_id=%s name=%s value=%s. If the tool is unavailable, state that you cannot change it; never claim a write without a successful tool result.", r.cfg.values["QA_SERVICE_ID"], r.cfg.values["QA_ENVIRONMENT_ID"], name, value)
	turn, err = r.send(ctx, refusalID, prompt, []string{"set_variable"}, false)
	if err != nil {
		return err
	}
	for _, block := range turn.Assistant.Content {
		if block.ToolUse != nil || block.ToolResult != nil {
			return failed("MUTATION_OFF_EXPOSED_TOOLS")
		}
	}
	var vars []variable
	if err := r.variables(ctx, &vars); err != nil {
		return err
	}
	if len(vars) != 0 {
		return failed("MUTATION_OFF_CHANGED_STATE")
	}
	r.receipt.pass("PRODUCT_MUTATION_DISABLED_NO_TOOL_OR_STATE_CHANGE")
	allowedID, err := newConversation("allowed")
	if err != nil {
		return err
	}
	mutationAttempted = true
	turn, err = r.send(ctx, allowedID, prompt, []string{"set_variable"}, true)
	if err != nil {
		return err
	}
	if err := checkTool(turn, "set_variable", func(input map[string]any) bool {
		return input["service_id"] == r.cfg.values["QA_SERVICE_ID"] && input["environment_id"] == r.cfg.values["QA_ENVIRONMENT_ID"] && input["name"] == name && input["value"] == value
	}); err != nil {
		return err
	}
	if err := r.variables(ctx, &vars); err != nil {
		return err
	}
	if len(vars) != 1 || vars[0].Name != name || vars[0].Version != 1 || vars[0].Service != r.cfg.values["QA_SERVICE_ID"] || vars[0].Environment != r.cfg.values["QA_ENVIRONMENT_ID"] {
		return failed("ALLOWED_MUTATION_READBACK_MISMATCH")
	}
	var audit struct {
		Events []struct {
			Resource string `json:"resource_id"`
			Action   string `json:"action"`
			Hash     string `json:"row_hash"`
		} `json:"events"`
	}
	if _, err := r.api(ctx, "GET", "/api/v1alpha/projects/"+r.cfg.values["QA_PROJECT_ID"]+"/audit/events?limit=200", nil, &audit); err != nil {
		return err
	}
	found := false
	for _, event := range audit.Events {
		if event.Resource == vars[0].ID && strings.Contains(event.Action, "variable") && event.Hash != "" {
			found = true
		}
	}
	if !found {
		return failed("VARIABLE_MUTATION_AUDIT_MISSING")
	}
	r.receipt.pass("PRODUCT_ALLOWED_QA_MUTATION_METADATA_AND_AUDIT")
	return nil
}
func (r *runner) createConversation(ctx context.Context, attempt *conversationAttempt) error {
	var response struct{ ID, ProjectID, ServiceID, Model string }
	var raw map[string]json.RawMessage
	_, err := r.api(ctx, "POST", "/api/v1alpha/agent/conversations", attempt.body, &raw, map[string]string{"Idempotency-Key": attempt.key})
	if err != nil {
		return err
	}
	_ = json.Unmarshal(raw["id"], &response.ID)
	_ = json.Unmarshal(raw["project_id"], &response.ProjectID)
	_ = json.Unmarshal(raw["service_id"], &response.ServiceID)
	_ = json.Unmarshal(raw["model"], &response.Model)
	if !identifier.MatchString(response.ID) || response.ProjectID != r.cfg.values["QA_PROJECT_ID"] || response.ServiceID != r.cfg.values["QA_SERVICE_ID"] || response.Model != r.cfg.values["MODEL"] {
		return failed("CONVERSATION_SCOPE_MISMATCH")
	}
	attempt.id = response.ID
	return nil
}
func (r *runner) send(ctx context.Context, id, prompt string, tools []string, mutate bool) (productTurn, error) {
	var turn productTurn
	_, err := r.api(ctx, "POST", "/api/v1alpha/agent/conversations/"+id+"/messages", map[string]any{"content": prompt, "allowed_tools": tools, "allow_mutations": mutate}, &turn)
	if err != nil {
		return turn, err
	}
	if turn.Stop != "end_turn" && turn.Stop != "stop" && turn.Stop != "completed" {
		return turn, failed("AGENT_TURN_NOT_COMPLETE")
	}
	if turn.Assistant.Usage == nil || turn.Assistant.Usage.OutputTokens <= 0 || turn.Assistant.Usage.Model != r.cfg.values["MODEL"] {
		return turn, failed("AGENT_USAGE_OR_MODEL_MISSING")
	}
	text := false
	for _, block := range turn.Assistant.Content {
		if strings.TrimSpace(block.Text) != "" {
			text = true
		}
	}
	if !text {
		return turn, failed("AGENT_FINAL_TEXT_MISSING")
	}
	return turn, nil
}
func checkTool(turn productTurn, name string, validate func(map[string]any) bool) error {
	calls := map[string]bool{}
	results := map[string]bool{}
	inputs := map[string]map[string]any{}
	for _, block := range turn.Assistant.Content {
		if block.ToolUse != nil {
			var input map[string]any
			if block.ToolUse.Name != name || json.Unmarshal(block.ToolUse.Input, &input) != nil || !validate(input) || block.ToolUse.ID == "" || calls[block.ToolUse.ID] {
				return failed("TOOL_CALL_SCOPE_MISMATCH")
			}
			calls[block.ToolUse.ID] = true
			inputs[block.ToolUse.ID] = input
		}
		if block.ToolResult != nil {
			if block.ToolResult.IsError || results[block.ToolResult.ToolUseID] || !calls[block.ToolResult.ToolUseID] {
				return failed("TOOL_RESULT_FAILED")
			}
			var output map[string]any
			if json.Unmarshal(block.ToolResult.Output, &output) != nil || output == nil {
				return failed("TOOL_RESULT_INVALID")
			}
			input := inputs[block.ToolResult.ToolUseID]
			switch name {
			case "get_service":
				if output["id"] != input["service_id"] {
					return failed("TOOL_RESULT_SCOPE_MISMATCH")
				}
			case "set_variable":
				if output["ok"] != true || output["service_id"] != input["service_id"] || output["environment_id"] != input["environment_id"] || output["name"] != input["name"] || output["value"] != nil {
					return failed("TOOL_RESULT_SCOPE_MISMATCH")
				}
			}
			results[block.ToolResult.ToolUseID] = true
		}
	}
	if len(calls) != 1 || len(results) != 1 {
		return failed("REAL_TOOL_LOOP_NOT_OBSERVED")
	}
	for id := range calls {
		if !results[id] {
			return failed("TOOL_RESULT_NOT_CORRELATED")
		}
	}
	return nil
}

// Diagnosis intentionally starts a real failed private-image deployment; it
// never calls the synthetic operator /fail endpoint.
func (r *runner) diagnosis(ctx context.Context) (result error) {
	var operation struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	var deployment struct {
		ID          string `json:"id"`
		Operation   string `json:"operation_id"`
		Service     string `json:"service_id"`
		Environment string `json:"environment_id"`
		Status      string `json:"status"`
		Observed    string `json:"runtime_observed_state"`
		Image       string `json:"image"`
	}
	defer func() {
		r.receipt.Cleanup.Attempted = true
		r.receipt.Cleanup.RetainedEvidence = []string{"FAILED_DEPLOYMENT_AND_OPERATION", "DIAGNOSIS_CONVERSATION", "AUDIT_ROWS"}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if operation.ID == "" {
			// Recover an intake whose committed response was lost, using exactly
			// the original idempotent body/key, before looking for its deployment.
			_, err := r.api(cleanupCtx, "POST", "/api/v1alpha/services/"+r.cfg.values["QA_SERVICE_ID"]+"/deployments", map[string]any{"environment_id": r.cfg.values["QA_ENVIRONMENT_ID"], "image": r.cfg.values["DIAGNOSIS_IMAGE"]}, &operation, map[string]string{"Idempotency-Key": "qa-diagnosis-" + r.receipt.RunID})
			if err != nil || !identifier.MatchString(operation.ID) {
				result = failed("CLEANUP_UNVERIFIED")
				return
			}
		}
		if deployment.ID == "" {
			_ = r.findDeployment(cleanupCtx, operation.ID, &deployment)
		}
		var cleanupErr error
		if deployment.ID == "" {
			cleanupErr = failed("FAILED_DEPLOYMENT_CLEANUP_UNVERIFIED")
		} else {
			var removal struct {
				ID string `json:"id"`
			}
			_, cleanupErr = r.api(cleanupCtx, "POST", "/api/v1alpha/services/"+r.cfg.values["QA_SERVICE_ID"]+"/deployments/"+deployment.ID+"/actions/remove", map[string]any{}, &removal, map[string]string{"Idempotency-Key": "qa-diagnosis-remove-" + r.receipt.RunID})
			if cleanupErr == nil {
				cleanupErr = r.waitOperation(cleanupCtx, removal.ID, "SUCCEEDED")
			}
			if cleanupErr == nil {
				var removed struct {
					ID          string `json:"id"`
					Service     string `json:"service_id"`
					Environment string `json:"environment_id"`
					Observed    string `json:"runtime_observed_state"`
					Active      bool   `json:"active"`
				}
				_, cleanupErr = r.api(cleanupCtx, "GET", "/api/v1alpha/services/"+r.cfg.values["QA_SERVICE_ID"]+"/deployments/"+deployment.ID+"?environment_id="+r.cfg.values["QA_ENVIRONMENT_ID"], nil, &removed)
				if cleanupErr == nil && (removed.ID != deployment.ID || removed.Service != r.cfg.values["QA_SERVICE_ID"] || removed.Environment != r.cfg.values["QA_ENVIRONMENT_ID"] || removed.Observed != "removed" || removed.Active) {
					cleanupErr = failed("FAILED_DEPLOYMENT_REMOVAL_READBACK_MISMATCH")
				}
			}
		}
		r.receipt.Cleanup.Verified = cleanupErr == nil
		result = cleanupError(result, cleanupErr)
	}()
	_, err := r.api(ctx, "POST", "/api/v1alpha/services/"+r.cfg.values["QA_SERVICE_ID"]+"/deployments", map[string]any{"environment_id": r.cfg.values["QA_ENVIRONMENT_ID"], "image": r.cfg.values["DIAGNOSIS_IMAGE"]}, &operation, map[string]string{"Idempotency-Key": "qa-diagnosis-" + r.receipt.RunID})
	if err != nil {
		return err
	}
	if !identifier.MatchString(operation.ID) {
		return failed("DIAGNOSIS_INTAKE_INVALID")
	}
	if err := r.waitOperation(ctx, operation.ID, "FAILED"); err != nil {
		return err
	}
	if err := r.findDeployment(ctx, operation.ID, &deployment); err != nil {
		return err
	}
	if deployment.Service != r.cfg.values["QA_SERVICE_ID"] || deployment.Environment != r.cfg.values["QA_ENVIRONMENT_ID"] || deployment.Image != r.cfg.values["DIAGNOSIS_IMAGE"] || deployment.Status != "FAILED" {
		return failed("FAILED_DEPLOYMENT_SCOPE_MISMATCH")
	}
	r.receipt.pass("REAL_WORKER_FAILURE_OBSERVED")
	var conversationID string
	err = wait(ctx, 2*time.Second, func() (bool, error) {
		var raw []byte
		if _, err := r.api(ctx, "GET", "/api/v1alpha/operations/"+operation.ID+"/events", nil, &raw); err != nil {
			return false, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var event struct {
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
				continue
			}
			if !strings.Contains(event.Message, "deploy:diagnosis_attached") {
				continue
			}
			_, id, ok := strings.Cut(event.Message, "diagnosis_id=")
			if ok && identifier.MatchString(strings.TrimSpace(id)) {
				conversationID = strings.TrimSpace(id)
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	var conversation struct {
		Service  string         `json:"service_id"`
		Model    string         `json:"model"`
		Metadata map[string]any `json:"metadata"`
	}
	if _, err := r.api(ctx, "GET", "/api/v1alpha/agent/conversations/"+conversationID, nil, &conversation); err != nil {
		return err
	}
	if conversation.Service != r.cfg.values["QA_SERVICE_ID"] || conversation.Model != r.cfg.values["MODEL"] || conversation.Metadata["midnight.operation_id"] != operation.ID || conversation.Metadata["midnight.deployment_id"] != deployment.ID {
		return failed("DIAGNOSIS_LINEAGE_MISMATCH")
	}
	var messages struct {
		Data []productMessage `json:"data"`
	}
	if _, err := r.api(ctx, "GET", "/api/v1alpha/agent/conversations/"+conversationID+"/messages?limit=200", nil, &messages); err != nil {
		return err
	}
	text := false
	for _, message := range messages.Data {
		for _, block := range message.Content {
			if block.ToolUse != nil && block.ToolUse.Name != "get_service" && block.ToolUse.Name != "get_logs" && block.ToolUse.Name != "get_deployment" && block.ToolUse.Name != "list_services" && block.ToolUse.Name != "get_metrics" {
				return failed("DIAGNOSIS_MUTATION_OBSERVED")
			}
			if strings.TrimSpace(block.Text) != "" {
				text = true
			}
		}
	}
	if !text {
		return failed("DIAGNOSIS_REPORT_MISSING")
	}
	r.receipt.pass("PRODUCT_DIAGNOSIS_EVENT_LINEAGE_AND_PERSISTED_REPORT")
	return nil
}
func (r *runner) waitOperation(ctx context.Context, id, want string) error {
	if !identifier.MatchString(id) {
		return failed("OPERATION_ID_INVALID")
	}
	return wait(ctx, 2*time.Second, func() (bool, error) {
		var op struct{ ID, State string }
		_, err := r.api(ctx, http.MethodGet, "/api/v1alpha/operations/"+id, nil, &op)
		if err != nil {
			return false, err
		}
		if op.ID != id {
			return false, failed("OPERATION_SCOPE_MISMATCH")
		}
		if op.State == want {
			return true, nil
		}
		switch op.State {
		case "QUEUED", "RUNNING":
			return false, nil
		default:
			return false, failed("UNEXPECTED_TERMINAL_OPERATION")
		}
	})
}
func (r *runner) findDeployment(ctx context.Context, operation string, target any) error {
	var rows struct {
		Items []json.RawMessage `json:"items"`
	}
	if _, err := r.api(ctx, "GET", "/api/v1alpha/services/"+r.cfg.values["QA_SERVICE_ID"]+"/deployments?environment_id="+r.cfg.values["QA_ENVIRONMENT_ID"]+"&limit=100", nil, &rows); err != nil {
		return err
	}
	for _, row := range rows.Items {
		var head struct {
			Operation string `json:"operation_id"`
		}
		_ = json.Unmarshal(row, &head)
		if head.Operation == operation {
			if err := json.Unmarshal(row, target); err != nil {
				return failed("DEPLOYMENT_JSON_INVALID")
			}
			return nil
		}
	}
	return failed("DEPLOYMENT_NOT_FOUND")
}
