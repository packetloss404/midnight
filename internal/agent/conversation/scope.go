// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package conversation

import "context"

// Scope identifies the project / service / actor a conversation
// belongs to, plus the authenticated principal making the request. The
// fields mirror Conversation so a ProjectScopeAuthorizer can decide
// "may this caller see this conversation?" without re-fetching.
type Scope struct {
	// ConversationID is the resource being accessed. Empty on
	// list/create checks; populated on Get/Delete/Send.
	ConversationID string

	// ProjectID and ServiceID identify the conversation's parent
	// resources. Empty when the conversation is global (e.g. the
	// dashboard-level "ask a question" panel).
	ProjectID string
	ServiceID string

	// ActorID is the principal_id from the bearer token. Empty when
	// the router is running in unauthenticated dev mode.
	ActorID string

	// Action enumerates the operation being authorised: "create",
	// "read", "list", "delete", "send".
	Action string
}

// ProjectScopeAuthorizer decides whether the caller's principal may
// access conversations in the supplied scope. The conversation track
// owns the seam definition; the concurrent auth track owns the
// implementation that maps a principal to its project memberships and
// RBAC role bindings.
//
// When the DataStack wires a nil ProjectScopeAuthorizer, every
// authenticated caller sees every conversation (the developer / dev
// mode default). Production wiring MUST install an authorizer before
// shipping multi-tenant conversations.
type ProjectScopeAuthorizer interface {
	// Authorize returns nil when the principal is permitted to perform
	// scope.Action on the supplied scope. Implementations SHOULD
	// return ErrUnauthorized for denials so the service layer
	// translates them to a 403.
	Authorize(ctx context.Context, scope Scope) error
}

// AllowAllAuthorizer is the no-op authorizer used when DataStack does
// not supply one. It explicitly returns nil for every call.
type AllowAllAuthorizer struct{}

// Authorize implements ProjectScopeAuthorizer.
func (AllowAllAuthorizer) Authorize(ctx context.Context, scope Scope) error { return nil }
