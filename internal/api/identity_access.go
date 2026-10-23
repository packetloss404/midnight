package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
)

const identityWorkspacesPath = v1alphaPrefix + "/workspaces"

type putIdentityMemberRequest struct {
	Role string `json:"role"`
}

func registerIdentityAccessRoutes(mux handlerRegistrar, manager *identity.Manager, store identity.AccessStore, clock func() time.Time, emitter *auditEmitter) {
	if manager == nil || store == nil {
		return
	}
	authenticated := identitySessionMiddleware(manager)
	mux.Handle(identityWorkspacesPath, authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		principal, _ := identityPrincipalFromContext(r.Context())
		workspaces, err := store.ListWorkspaces(r.Context(), principal.UserID)
		if err != nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "workspace list failed", nil)
			return
		}
		WriteJSON(w, http.StatusOK, workspaces)
	})))
	mux.Handle(identityWorkspacesPath+"/{workspace_id}/members", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listIdentityWorkspaceMembers(w, r, store)
	})))
	mux.Handle(identityWorkspacesPath+"/{workspace_id}/members/{user_id}", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutateIdentityWorkspaceMember(w, r, store, clock, emitter)
	})))
	mux.Handle(v1alphaPrefix+"/projects/{project_id}/members", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listIdentityProjectMembers(w, r, store)
	})))
	mux.Handle(v1alphaPrefix+"/projects/{project_id}/members/{user_id}", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutateIdentityProjectMember(w, r, store, clock, emitter)
	})))
}

func listIdentityWorkspaceMembers(w http.ResponseWriter, r *http.Request, store identity.AccessStore) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	workspaceID := strings.TrimSpace(r.PathValue("workspace_id"))
	principal, _ := identityPrincipalFromContext(r.Context())
	if _, _, err := identity.Authorize(r.Context(), store, principal.UserID, identity.AccessTarget{WorkspaceID: workspaceID}, identity.CapabilityWorkspaceMembersManage, false); err != nil {
		writeIdentityAuthorizationError(w, r, err)
		return
	}
	members, err := store.ListWorkspaceMembers(r.Context(), workspaceID)
	if err != nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "workspace member list failed", nil)
		return
	}
	WriteJSON(w, http.StatusOK, members)
}

func mutateIdentityWorkspaceMember(w http.ResponseWriter, r *http.Request, store identity.AccessStore, clock func() time.Time, emitter *auditEmitter) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	workspaceID := strings.TrimSpace(r.PathValue("workspace_id"))
	userID := strings.TrimSpace(r.PathValue("user_id"))
	principal, _ := identityPrincipalFromContext(r.Context())
	if _, _, err := identity.Authorize(r.Context(), store, principal.UserID, identity.AccessTarget{WorkspaceID: workspaceID}, identity.CapabilityWorkspaceMembersManage, false); err != nil {
		emitIdentityMembershipAudit(emitter, r, principal, workspaceID, "", userID, "", err)
		writeIdentityAuthorizationError(w, r, err)
		return
	}
	if r.Method == http.MethodGet {
		member, err := store.GetWorkspaceMember(r.Context(), workspaceID, userID)
		if err != nil {
			writeIdentityMembershipMutationResult(w, r, err)
			return
		}
		w.Header().Set("ETag", strongVersionETag(member.Version))
		WriteJSON(w, http.StatusOK, member)
		return
	}
	now := clock().UTC()
	if r.Method == http.MethodDelete {
		expectedVersion, ok := requireIfMatchVersion(w, r)
		if !ok {
			return
		}
		err := store.RemoveWorkspaceMemberCAS(r.Context(), workspaceID, userID, principal.UserID, expectedVersion, now)
		emitIdentityMembershipAudit(emitter, r, principal, workspaceID, "", userID, "", err)
		writeIdentityMembershipMutationResult(w, r, err)
		return
	}
	var request putIdentityMemberRequest
	if !decodeIdentityMemberRequest(w, r, &request) {
		return
	}
	role, ok := identity.ParseWorkspaceRole(request.Role)
	if !ok {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "workspace role is invalid", nil)
		return
	}
	expectedVersion, ok := requireIfMatchVersion(w, r)
	if !ok {
		return
	}
	member, err := store.PutWorkspaceMemberCAS(r.Context(), workspaceID, userID, role, principal.UserID, expectedVersion, now)
	if err != nil {
		emitIdentityMembershipAudit(emitter, r, principal, workspaceID, "", userID, string(role), err)
		writeIdentityMembershipMutationResult(w, r, err)
		return
	}
	emitIdentityMembershipAudit(emitter, r, principal, workspaceID, "", userID, string(role), nil)
	w.Header().Set("ETag", strongVersionETag(member.Version))
	WriteJSON(w, http.StatusOK, member)
}

func listIdentityProjectMembers(w http.ResponseWriter, r *http.Request, store identity.AccessStore) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	projectID := strings.TrimSpace(r.PathValue("project_id"))
	principal, _ := identityPrincipalFromContext(r.Context())
	if _, _, err := identity.Authorize(r.Context(), store, principal.UserID, identity.AccessTarget{ProjectID: projectID}, identity.CapabilityProjectMembersManage, false); err != nil {
		writeIdentityAuthorizationError(w, r, err)
		return
	}
	members, err := store.ListProjectMembers(r.Context(), projectID)
	if err != nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project member list failed", nil)
		return
	}
	WriteJSON(w, http.StatusOK, members)
}

func mutateIdentityProjectMember(w http.ResponseWriter, r *http.Request, store identity.AccessStore, clock func() time.Time, emitter *auditEmitter) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	projectID := strings.TrimSpace(r.PathValue("project_id"))
	userID := strings.TrimSpace(r.PathValue("user_id"))
	principal, _ := identityPrincipalFromContext(r.Context())
	grant, _, err := identity.Authorize(r.Context(), store, principal.UserID, identity.AccessTarget{ProjectID: projectID}, identity.CapabilityProjectMembersManage, false)
	if err != nil {
		emitIdentityMembershipAudit(emitter, r, principal, "", projectID, userID, "", err)
		writeIdentityAuthorizationError(w, r, err)
		return
	}
	memberBefore, memberErr := store.GetProjectMember(r.Context(), projectID, userID)
	if memberErr != nil {
		emitIdentityMembershipAudit(emitter, r, principal, grant.Target.WorkspaceID, projectID, userID, "", memberErr)
		writeIdentityMembershipMutationResult(w, r, memberErr)
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("ETag", strongVersionETag(memberBefore.Version))
		WriteJSON(w, http.StatusOK, memberBefore)
		return
	}
	editorOnly := grant.WorkspaceRole != identity.WorkspaceRoleAdmin &&
		grant.ProjectRole == identity.ProjectRoleEditor
	if editorOnly && memberBefore.Role == identity.ProjectRoleOwner {
		emitIdentityMembershipAudit(emitter, r, principal, grant.Target.WorkspaceID, projectID, userID, "", identity.ErrAuthorizationDenied)
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "project editors cannot change owners", nil)
		return
	}
	now := clock().UTC()
	if r.Method == http.MethodDelete {
		expectedVersion, ok := requireIfMatchVersion(w, r)
		if !ok {
			return
		}
		err := store.RemoveProjectMemberCAS(r.Context(), projectID, userID, principal.UserID, expectedVersion, now)
		emitIdentityMembershipAudit(emitter, r, principal, grant.Target.WorkspaceID, projectID, userID, "", err)
		writeIdentityMembershipMutationResult(w, r, err)
		return
	}
	var request putIdentityMemberRequest
	if !decodeIdentityMemberRequest(w, r, &request) {
		return
	}
	role, ok := identity.ParseProjectRole(request.Role)
	if !ok {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project role is invalid", nil)
		return
	}
	if role == identity.ProjectRoleOwner && grant.WorkspaceRole != identity.WorkspaceRoleAdmin && grant.ProjectRole != identity.ProjectRoleOwner {
		emitIdentityMembershipAudit(emitter, r, principal, grant.Target.WorkspaceID, projectID, userID, string(role), identity.ErrAuthorizationDenied)
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "project editors cannot grant owner", nil)
		return
	}
	expectedVersion, ok := requireIfMatchVersion(w, r)
	if !ok {
		return
	}
	member, err := store.PutProjectMemberCAS(r.Context(), projectID, userID, role, principal.UserID, expectedVersion, now)
	if err != nil {
		emitIdentityMembershipAudit(emitter, r, principal, grant.Target.WorkspaceID, projectID, userID, string(role), err)
		writeIdentityMembershipMutationResult(w, r, err)
		return
	}
	emitIdentityMembershipAudit(emitter, r, principal, grant.Target.WorkspaceID, projectID, userID, string(role), nil)
	w.Header().Set("ETag", strongVersionETag(member.Version))
	WriteJSON(w, http.StatusOK, member)
}

func decodeIdentityMemberRequest(w http.ResponseWriter, r *http.Request, request *putIdentityMemberRequest) bool {
	return decodeStrictJSONRequest(w, r, request, "invalid member request")
}

func writeIdentityAuthorizationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, identity.ErrAccessNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "resource not found", nil)
		return
	}
	if errors.Is(err, identity.ErrAuthorizationDenied) {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to perform this action", nil)
		return
	}
	writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "authorization service is unavailable", nil)
}

func writeIdentityMembershipMutationResult(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, identity.ErrLastWorkspaceAdmin):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "workspace must retain an active administrator", nil)
	case errors.Is(err, identity.ErrLastProjectOwner):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "project must retain an active owner", nil)
	case errors.Is(err, identity.ErrMembershipNotFound), errors.Is(err, identity.ErrIdentityNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "member not found", nil)
	case errors.Is(err, identity.ErrAccessNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "resource not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to perform this action", nil)
	case errors.Is(err, identity.ErrMembershipVersionConflict):
		writeIfMatchFailed(w, r, "membership version changed", nil)
	default:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "membership mutation failed", nil)
	}
}

func emitIdentityMembershipAudit(
	emitter *auditEmitter,
	r *http.Request,
	principal identity.Principal,
	workspaceID, projectID, targetUserID, role string,
	mutationErr error,
) {
	scope := "workspace"
	resourceKind := "workspace_member"
	if projectID != "" {
		scope = "project"
		resourceKind = "project_member"
	}
	verb := "update"
	if r.Method == http.MethodDelete {
		verb = "remove"
	}
	row := audit.Row{
		ActorID: principal.UserID, ActorType: "user", AuthMethod: principal.AuthMethod,
		WorkspaceID: workspaceID, ProjectID: projectID,
		ResourceKind: resourceKind, ResourceID: targetUserID,
		Action:         "identity." + scope + "-member." + verb,
		PolicyDecision: "allowed",
	}
	if role != "" && mutationErr == nil {
		row.After = map[string]any{"role": role}
	}
	if mutationErr != nil {
		row.Action += ".denied"
		row.PolicyDecision = "denied"
		switch {
		case errors.Is(mutationErr, identity.ErrAuthorizationDenied):
			row.ReasonCode = string(failurecodes.AuthorizationDenied)
		case errors.Is(mutationErr, identity.ErrLastWorkspaceAdmin), errors.Is(mutationErr, identity.ErrLastProjectOwner):
			row.ReasonCode = string(failurecodes.ResourceConflict)
		case errors.Is(mutationErr, identity.ErrMembershipVersionConflict):
			row.ReasonCode = string(failurecodes.PreconditionFailed)
		case errors.Is(mutationErr, identity.ErrAccessNotFound),
			errors.Is(mutationErr, identity.ErrMembershipNotFound),
			errors.Is(mutationErr, identity.ErrIdentityNotFound):
			row.ReasonCode = string(failurecodes.ResourceNotFound)
		default:
			row.ReasonCode = string(failurecodes.InternalError)
		}
	}
	emitter.emit(r.Context(), row)
}
