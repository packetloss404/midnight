// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Request is the JSON shape the /graphql/v2 HTTP endpoint accepts. It
// matches the GraphQL-over-HTTP spec.
type Request struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName,omitempty"`
	Variables     map[string]interface{} `json:"variables,omitempty"`
}

// Response is the JSON shape the endpoint returns.
type Response struct {
	Data   map[string]interface{} `json:"data,omitempty"`
	Errors []ResponseError        `json:"errors,omitempty"`
}

// ResponseError carries the GraphQL-spec error shape.
type ResponseError struct {
	Message    string                 `json:"message"`
	Path       []interface{}          `json:"path,omitempty"`
	Locations  []errorLocation        `json:"locations,omitempty"`
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

type errorLocation struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// Executor runs validated GraphQL queries against the typed resolver
// layer. It is intentionally narrow: it only supports the operations
// declared in the schema, validates inputs through gqlparser, and
// dispatches into ServicesAPI via the resolver methods in resolver.go.
type Executor struct {
	schema   *ast.Schema
	services *api.ServicesAPI
	// Deployments is the canonical deployment store used to mirror
	// redeploy/rollback/restart mutations. Nil-safe (skipped when unset).
	Deployments api.DeploymentStore
	// DeploymentActions is intentionally separate from DeploymentStore: runtime
	// control must atomically persist an operation, action ledger row and
	// staged-work intent, which only the Postgres action intake can provide.
	DeploymentActions api.DeploymentActionIntake
}

type requestIdentity struct {
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

// NewExecutor builds an executor backed by the supplied service layer.
func NewExecutor(services *api.ServicesAPI) (*Executor, error) {
	schema, err := LoadSchema()
	if err != nil {
		return nil, err
	}
	return &Executor{schema: schema, services: services}, nil
}

// Execute parses, validates, and runs the supplied GraphQL request.
func (e *Executor) Execute(ctx context.Context, req Request) Response {
	if strings.TrimSpace(req.Query) == "" {
		return errResponse("query is required", nil)
	}
	doc, gerr := gqlparser.LoadQuery(e.schema, req.Query)
	if gerr != nil {
		return errsResponse(gerr)
	}
	if doc == nil || len(doc.Operations) == 0 {
		return errResponse("no operation in document", nil)
	}
	op := selectOperation(doc, req.OperationName)
	if op == nil {
		return errResponse("operation not found in document", nil)
	}

	// Bind variables: gqlparser leaves them as JSON; we coerce per the
	// declared variable types.
	vars, err := coerceVariables(e.schema, op, req.Variables)
	if err != nil {
		return errResponse(err.Error(), nil)
	}

	identityBody, err := json.Marshal(struct {
		OperationName string                 `json:"operation_name"`
		Query         string                 `json:"query"`
		Variables     map[string]interface{} `json:"variables"`
	}{OperationName: req.OperationName, Query: req.Query, Variables: req.Variables})
	if err != nil {
		return errResponse("request identity encoding failed", nil)
	}
	transport, _ := ctx.Value(requestTransportContextKey{}).(requestTransport)
	if transport.Method == "" {
		transport.Method = "POST"
	}
	resolver := &resolver{
		services: e.services, deployments: e.Deployments, actions: e.DeploymentActions, schema: e.schema, vars: vars,
		requestIdentity: requestIdentity{
			Fingerprint: idempotency.RequestFingerprint{
				Method: transport.Method, Path: api.GraphQLDeployFingerprintPath, BodySHA256: idempotency.BodyHashSHA256(identityBody),
			},
			RequestID: transport.RequestID, SourceIP: transport.SourceIP, UserAgent: transport.UserAgent,
		},
	}

	switch op.Operation {
	case ast.Query:
		data := map[string]interface{}{}
		errs := executeSelectionSet(ctx, resolver, op.SelectionSet, resolver.resolveQuery, nil, data)
		if len(errs) > 0 {
			return Response{Data: data, Errors: errs}
		}
		return Response{Data: data}
	case ast.Mutation:
		if e.services.GraphQLDeployIntake != nil && !durableDeployIsSoleMutation(op.SelectionSet) {
			return errResponse(
				"serviceInstanceDeployV2 must be the sole top-level mutation when durable intake is enabled",
				map[string]interface{}{"code": string(failurecodes.GraphQLDeployMustBeSoleMutation)},
			)
		}
		data := map[string]interface{}{}
		// Mutations run sequentially per GraphQL spec.
		errs := executeSelectionSet(ctx, resolver, op.SelectionSet, resolver.resolveMutation, nil, data)
		if len(errs) > 0 {
			return Response{Data: data, Errors: errs}
		}
		return Response{Data: data}
	default:
		return errResponse("operation type not supported (subscriptions are not enabled in v1)", nil)
	}
}

// durableDeployIsSoleMutation keeps the idempotent unit identical to the
// GraphQL response unit. A durable deploy cannot be combined with a sibling
// mutation whose side effects would otherwise run again on replay.
func durableDeployIsSoleMutation(set ast.SelectionSet) bool {
	fields := make([]*ast.Field, 0, len(set))
	var collect func(ast.SelectionSet)
	collect = func(current ast.SelectionSet) {
		for _, selection := range current {
			switch value := selection.(type) {
			case *ast.Field:
				fields = append(fields, value)
			case *ast.InlineFragment:
				collect(value.SelectionSet)
			case *ast.FragmentSpread:
				if value.Definition != nil {
					collect(value.Definition.SelectionSet)
				}
			}
		}
	}
	collect(set)
	containsDeploy := false
	for _, field := range fields {
		if field.Name == "serviceInstanceDeployV2" {
			containsDeploy = true
			break
		}
	}
	return !containsDeploy || len(fields) == 1
}

func selectOperation(doc *ast.QueryDocument, name string) *ast.OperationDefinition {
	if name == "" {
		return doc.Operations[0]
	}
	for _, op := range doc.Operations {
		if op.Name == name {
			return op
		}
	}
	return nil
}

func coerceVariables(schema *ast.Schema, op *ast.OperationDefinition, raw map[string]interface{}) (map[string]interface{}, error) {
	if raw == nil {
		raw = map[string]interface{}{}
	}
	out := map[string]interface{}{}
	for _, vd := range op.VariableDefinitions {
		v, ok := raw[vd.Variable]
		if !ok {
			if vd.DefaultValue != nil {
				dv, err := evalValue(vd.DefaultValue, nil)
				if err != nil {
					return nil, fmt.Errorf("variable %q default: %w", vd.Variable, err)
				}
				out[vd.Variable] = dv
				continue
			}
			if vd.Type.NonNull {
				return nil, fmt.Errorf("variable %q is required", vd.Variable)
			}
			continue
		}
		out[vd.Variable] = v
	}
	return out, nil
}

// selectionResolver is the per-field dispatcher used by both Query and
// Mutation execution paths. It returns the resolved value, or a list of
// GraphQL errors.
type selectionResolver func(ctx context.Context, field *ast.Field, parent interface{}) (interface{}, []ResponseError)

// executeSelectionSet walks every field in `set`, calls the resolver,
// and writes the result into `out` keyed by alias-or-name.
func executeSelectionSet(ctx context.Context, r *resolver, set ast.SelectionSet, fn selectionResolver, parent interface{}, out map[string]interface{}) []ResponseError {
	var errs []ResponseError
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			key := s.Alias
			if key == "" {
				key = s.Name
			}
			if s.Name == "__typename" {
				out[key] = typenameFor(parent, r)
				continue
			}
			val, ferrs := fn(ctx, s, parent)
			if len(ferrs) > 0 {
				for i := range ferrs {
					ferrs[i].Path = append([]interface{}{key}, ferrs[i].Path...)
				}
				errs = append(errs, ferrs...)
			}
			out[key] = val
		case *ast.InlineFragment:
			ierrs := executeSelectionSet(ctx, r, s.SelectionSet, fn, parent, out)
			errs = append(errs, ierrs...)
		case *ast.FragmentSpread:
			if s.Definition != nil {
				ferrs := executeSelectionSet(ctx, r, s.Definition.SelectionSet, fn, parent, out)
				errs = append(errs, ferrs...)
			}
		}
	}
	return errs
}

func typenameFor(parent interface{}, _ *resolver) string {
	if parent == nil {
		return ""
	}
	switch parent.(type) {
	case *gqlProject:
		return "Project"
	case *gqlService:
		return "Service"
	case *gqlEnvironment:
		return "Environment"
	case *gqlDeployment:
		return "Deployment"
	case *gqlVariable:
		return "Variable"
	case *gqlVolume:
		return "Volume"
	case *gqlServiceInstance:
		return "ServiceInstance"
	default:
		return ""
	}
}

func errResponse(msg string, extensions map[string]interface{}) Response {
	return Response{Errors: []ResponseError{{Message: msg, Extensions: extensions}}}
}

func errsResponse(g gqlerror.List) Response {
	out := make([]ResponseError, 0, len(g))
	for _, e := range g {
		out = append(out, ResponseError{Message: e.Message, Extensions: map[string]interface{}{"code": "GRAPHQL_VALIDATION_FAILED"}})
	}
	return Response{Errors: out}
}

func wrapServiceError(err *api.ServiceError) ResponseError {
	if err == nil {
		return ResponseError{Message: "unknown service error"}
	}
	ext := map[string]interface{}{}
	if err.Code != "" {
		ext["code"] = err.Code
	}
	if err.Status != 0 {
		ext["status"] = err.Status
	}
	if err.Details != nil {
		ext["details"] = err.Details
	}
	return ResponseError{Message: err.Message, Extensions: ext}
}

// evalValue resolves an AST value into a concrete Go value. Variables
// are looked up in `vars`.
func evalValue(v *ast.Value, vars map[string]interface{}) (interface{}, error) {
	if v == nil {
		return nil, nil
	}
	switch v.Kind {
	case ast.Variable:
		if vars == nil {
			return nil, nil
		}
		return vars[v.Raw], nil
	case ast.IntValue, ast.FloatValue, ast.StringValue, ast.BlockValue, ast.EnumValue:
		return v.Raw, nil
	case ast.BooleanValue:
		return v.Raw == "true", nil
	case ast.NullValue:
		return nil, nil
	case ast.ListValue:
		out := make([]interface{}, 0, len(v.Children))
		for _, c := range v.Children {
			cv, err := evalValue(c.Value, vars)
			if err != nil {
				return nil, err
			}
			out = append(out, cv)
		}
		return out, nil
	case ast.ObjectValue:
		out := map[string]interface{}{}
		for _, c := range v.Children {
			cv, err := evalValue(c.Value, vars)
			if err != nil {
				return nil, err
			}
			out[c.Name] = cv
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported value kind: %v", v.Kind)
	}
}

// argString resolves a field argument by name as a string.
func argString(field *ast.Field, name string, vars map[string]interface{}) (string, bool) {
	v, ok := argValue(field, name, vars)
	if !ok {
		return "", false
	}
	switch s := v.(type) {
	case string:
		return s, true
	case json.Number:
		return s.String(), true
	case nil:
		return "", false
	default:
		return fmt.Sprintf("%v", s), true
	}
}

// argInt resolves a field argument by name as an int.
func argInt(field *ast.Field, name string, vars map[string]interface{}) (int, bool) {
	v, ok := argValue(field, name, vars)
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case string:
		var i int
		_, err := fmt.Sscanf(n, "%d", &i)
		if err != nil {
			return 0, false
		}
		return i, true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

// argObject resolves a field argument by name as a JSON object.
func argObject(field *ast.Field, name string, vars map[string]interface{}) (map[string]interface{}, bool) {
	v, ok := argValue(field, name, vars)
	if !ok {
		return nil, false
	}
	switch m := v.(type) {
	case map[string]interface{}:
		return m, true
	case nil:
		return nil, false
	default:
		return nil, false
	}
}

func argValue(field *ast.Field, name string, vars map[string]interface{}) (interface{}, bool) {
	for _, a := range field.Arguments {
		if a.Name == name {
			v, err := evalValue(a.Value, vars)
			if err != nil {
				return nil, false
			}
			return v, true
		}
	}
	return nil, false
}

// Compile-time guard: keep the imports honest if the executor ever
// stops using errors.
var _ = errors.New
