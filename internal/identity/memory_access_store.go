package identity

import (
	"context"
	"sort"
	"sync"
	"time"
)

type memoryProjectScope struct {
	WorkspaceID string
}

type memoryEnvironmentScope struct {
	ProjectID  string
	Restricted bool
}

// MemoryAccessStore is the deterministic test/development implementation of
// tenant access persistence.
type MemoryAccessStore struct {
	mu               sync.Mutex
	authority        *memoryAuthority
	users            map[string]User
	workspaces       map[string]Workspace
	workspaceMembers map[string]map[string]WorkspaceMember
	projects         map[string]memoryProjectScope
	projectMembers   map[string]map[string]ProjectMember
	environments     map[string]memoryEnvironmentScope
	invitations      map[string]InvitationCredential
	invitationByMAC  map[string]string
}

func NewMemoryAccessStore() *MemoryAccessStore {
	return newMemoryAccessStore(&memoryAuthority{})
}

func newMemoryAccessStore(authority *memoryAuthority) *MemoryAccessStore {
	return &MemoryAccessStore{
		authority:        authority,
		users:            make(map[string]User),
		workspaces:       make(map[string]Workspace),
		workspaceMembers: make(map[string]map[string]WorkspaceMember),
		projects:         make(map[string]memoryProjectScope),
		projectMembers:   make(map[string]map[string]ProjectMember),
		environments:     make(map[string]memoryEnvironmentScope),
		invitations:      make(map[string]InvitationCredential),
		invitationByMAC:  make(map[string]string),
	}
}

func (s *MemoryAccessStore) SeedUser(user User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[user.ID] = user
}

func (s *MemoryAccessStore) SeedWorkspace(workspace Workspace, members ...WorkspaceMember) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces[workspace.ID] = workspace
	if s.workspaceMembers[workspace.ID] == nil {
		s.workspaceMembers[workspace.ID] = make(map[string]WorkspaceMember)
	}
	for _, member := range members {
		if member.Version < 1 {
			member.Version = 1
		}
		s.users[member.User.ID] = mergePublicUser(s.users[member.User.ID], member.User)
		member.User = s.users[member.User.ID]
		s.workspaceMembers[workspace.ID][member.User.ID] = cloneWorkspaceMember(member)
	}
}

func (s *MemoryAccessStore) SeedProject(projectID, workspaceID string, members ...ProjectMember) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects[projectID] = memoryProjectScope{WorkspaceID: workspaceID}
	if s.projectMembers[projectID] == nil {
		s.projectMembers[projectID] = make(map[string]ProjectMember)
	}
	for _, member := range members {
		if member.Version < 1 {
			member.Version = 1
		}
		s.users[member.User.ID] = mergePublicUser(s.users[member.User.ID], member.User)
		member.User = s.users[member.User.ID]
		s.projectMembers[projectID][member.User.ID] = cloneProjectMember(member)
	}
}

func (s *MemoryAccessStore) SeedEnvironment(environmentID, projectID string, restricted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.environments[environmentID] = memoryEnvironmentScope{ProjectID: projectID, Restricted: restricted}
}

// DefaultWorkspaceClaimed mirrors the ClaimDefaultWorkspace precondition so the
// in-memory stack answers the unauthenticated status probe the same way
// Postgres does.
func (s *MemoryAccessStore) DefaultWorkspaceClaimed(_ context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for userID, member := range s.workspaceMembers[DefaultWorkspaceID] {
		if userID != migrationDefaultUserID && member.Role == WorkspaceRoleAdmin && member.AcceptedAt != nil {
			return true, nil
		}
	}
	return false, nil
}

func (s *MemoryAccessStore) ClaimDefaultWorkspace(_ context.Context, request ClaimDefaultWorkspaceRequest) (ClaimDefaultWorkspaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for userID, member := range s.workspaceMembers[DefaultWorkspaceID] {
		if userID != migrationDefaultUserID && member.Role == WorkspaceRoleAdmin && member.AcceptedAt != nil {
			return ClaimDefaultWorkspaceResult{}, ErrWorkspaceClaimed
		}
	}
	if _, exists := s.users[request.UserID]; exists {
		return ClaimDefaultWorkspaceResult{}, ErrIdentityConflict
	}
	for _, user := range s.users {
		if user.Email == request.Email {
			return ClaimDefaultWorkspaceResult{}, ErrIdentityConflict
		}
	}
	user := User{
		ID: request.UserID, Email: request.Email, DisplayName: request.DisplayName,
		InstanceAdmin: true, MFAEnabled: !request.TOTPEnrolledAt.IsZero(),
	}
	createdAt := request.Now
	if existing, ok := s.workspaces[DefaultWorkspaceID]; ok && !existing.CreatedAt.IsZero() {
		createdAt = existing.CreatedAt
	}
	workspace := Workspace{
		ID: DefaultWorkspaceID, Slug: request.WorkspaceSlug,
		DisplayName: request.WorkspaceDisplayName, Role: WorkspaceRoleAdmin,
		CreatedAt: createdAt, UpdatedAt: request.Now,
	}
	s.users[user.ID] = user
	s.workspaces[workspace.ID] = workspace
	accepted := request.Now
	if s.workspaceMembers[workspace.ID] == nil {
		s.workspaceMembers[workspace.ID] = make(map[string]WorkspaceMember)
	}
	s.workspaceMembers[workspace.ID][user.ID] = WorkspaceMember{
		WorkspaceID: workspace.ID, User: user, Role: WorkspaceRoleAdmin,
		InvitedAt: request.Now, AcceptedAt: &accepted, CreatedAt: request.Now,
		UpdatedAt: request.Now, Version: 1,
	}
	delete(s.workspaceMembers[workspace.ID], migrationDefaultUserID)
	for projectID, project := range s.projects {
		if project.WorkspaceID != workspace.ID {
			continue
		}
		if s.projectMembers[projectID] == nil {
			s.projectMembers[projectID] = make(map[string]ProjectMember)
		}
		s.projectMembers[projectID][user.ID] = ProjectMember{
			ProjectID: projectID, User: user, Role: ProjectRoleOwner,
			InvitedAt: request.Now, AcceptedAt: &accepted, CreatedAt: request.Now,
			UpdatedAt: request.Now, Version: 1,
		}
		delete(s.projectMembers[projectID], migrationDefaultUserID)
	}
	return ClaimDefaultWorkspaceResult{User: user, Workspace: workspace}, nil
}

func (s *MemoryAccessStore) ListWorkspaces(_ context.Context, userID string) ([]Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Workspace, 0)
	for workspaceID, members := range s.workspaceMembers {
		member, ok := members[userID]
		if !ok || member.AcceptedAt == nil {
			continue
		}
		workspace, ok := s.workspaces[workspaceID]
		if !ok {
			continue
		}
		workspace.Role = member.Role
		out = append(out, workspace)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *MemoryAccessStore) ListWorkspaceMembers(_ context.Context, workspaceID string) ([]WorkspaceMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workspaces[workspaceID]; !ok {
		return nil, ErrAccessNotFound
	}
	out := make([]WorkspaceMember, 0, len(s.workspaceMembers[workspaceID]))
	for _, member := range s.workspaceMembers[workspaceID] {
		out = append(out, cloneWorkspaceMember(member))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User.ID < out[j].User.ID })
	return out, nil
}

func (s *MemoryAccessStore) GetWorkspaceMember(_ context.Context, workspaceID, userID string) (WorkspaceMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workspaces[workspaceID]; !ok {
		return WorkspaceMember{}, ErrAccessNotFound
	}
	member, ok := s.workspaceMembers[workspaceID][userID]
	if !ok {
		return WorkspaceMember{}, ErrMembershipNotFound
	}
	return cloneWorkspaceMember(member), nil
}

func (s *MemoryAccessStore) PutWorkspaceMember(_ context.Context, workspaceID, userID string, role WorkspaceRole, actorID string, now time.Time) (WorkspaceMember, error) {
	return s.putWorkspaceMember(workspaceID, userID, role, actorID, 0, false, now)
}

func (s *MemoryAccessStore) PutWorkspaceMemberCAS(_ context.Context, workspaceID, userID string, role WorkspaceRole, actorID string, expectedVersion int64, now time.Time) (WorkspaceMember, error) {
	return s.putWorkspaceMember(workspaceID, userID, role, actorID, expectedVersion, true, now)
}

func (s *MemoryAccessStore) putWorkspaceMember(workspaceID, userID string, role WorkspaceRole, actorID string, expectedVersion int64, conditional bool, now time.Time) (WorkspaceMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorizeWorkspaceMemberMutation(workspaceID, actorID); err != nil {
		return WorkspaceMember{}, err
	}
	if _, ok := s.users[userID]; !ok {
		return WorkspaceMember{}, ErrIdentityNotFound
	}
	if _, ok := s.workspaces[workspaceID]; !ok {
		return WorkspaceMember{}, ErrAccessNotFound
	}
	current, exists := s.workspaceMembers[workspaceID][userID]
	if !exists {
		return WorkspaceMember{}, ErrMembershipNotFound
	}
	if conditional && (expectedVersion < 1 || current.Version != expectedVersion) {
		return WorkspaceMember{}, ErrMembershipVersionConflict
	}
	if exists && current.Role == WorkspaceRoleAdmin && role != WorkspaceRoleAdmin && s.workspaceAdminCount(workspaceID, userID) == 0 {
		return WorkspaceMember{}, ErrLastWorkspaceAdmin
	}
	current.Version++
	current.Role = role
	current.InvitedBy = actorID
	current.UpdatedAt = now
	s.workspaceMembers[workspaceID][userID] = current
	return cloneWorkspaceMember(current), nil
}

func (s *MemoryAccessStore) RemoveWorkspaceMember(_ context.Context, workspaceID, userID, actorID string, _ time.Time) error {
	return s.removeWorkspaceMember(workspaceID, userID, actorID, 0, false)
}

func (s *MemoryAccessStore) RemoveWorkspaceMemberCAS(_ context.Context, workspaceID, userID, actorID string, expectedVersion int64, _ time.Time) error {
	return s.removeWorkspaceMember(workspaceID, userID, actorID, expectedVersion, true)
}

func (s *MemoryAccessStore) removeWorkspaceMember(workspaceID, userID, actorID string, expectedVersion int64, conditional bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorizeWorkspaceMemberMutation(workspaceID, actorID); err != nil {
		return err
	}
	member, ok := s.workspaceMembers[workspaceID][userID]
	if !ok {
		return ErrMembershipNotFound
	}
	if conditional && (expectedVersion < 1 || member.Version != expectedVersion) {
		return ErrMembershipVersionConflict
	}
	if member.Role == WorkspaceRoleAdmin && s.workspaceAdminCount(workspaceID, userID) == 0 {
		return ErrLastWorkspaceAdmin
	}
	for projectID, project := range s.projects {
		if project.WorkspaceID != workspaceID {
			continue
		}
		projectMember, ok := s.projectMembers[projectID][userID]
		if ok && projectMember.AcceptedAt != nil && projectMember.Role == ProjectRoleOwner &&
			s.projectOwnerCount(projectID, userID) == 0 {
			return ErrLastProjectOwner
		}
	}
	delete(s.workspaceMembers[workspaceID], userID)
	for projectID, project := range s.projects {
		if project.WorkspaceID == workspaceID {
			delete(s.projectMembers[projectID], userID)
		}
	}
	return nil
}

func (s *MemoryAccessStore) ListProjectMembers(_ context.Context, projectID string) ([]ProjectMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return nil, ErrAccessNotFound
	}
	out := make([]ProjectMember, 0, len(s.projectMembers[projectID]))
	for _, member := range s.projectMembers[projectID] {
		out = append(out, cloneProjectMember(member))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User.ID < out[j].User.ID })
	return out, nil
}

func (s *MemoryAccessStore) GetProjectMember(_ context.Context, projectID, userID string) (ProjectMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return ProjectMember{}, ErrAccessNotFound
	}
	member, ok := s.projectMembers[projectID][userID]
	if !ok {
		return ProjectMember{}, ErrMembershipNotFound
	}
	return cloneProjectMember(member), nil
}

func (s *MemoryAccessStore) PutProjectMember(_ context.Context, projectID, userID string, role ProjectRole, actorID string, now time.Time) (ProjectMember, error) {
	return s.putProjectMember(projectID, userID, role, actorID, 0, false, now)
}

func (s *MemoryAccessStore) PutProjectMemberCAS(_ context.Context, projectID, userID string, role ProjectRole, actorID string, expectedVersion int64, now time.Time) (ProjectMember, error) {
	return s.putProjectMember(projectID, userID, role, actorID, expectedVersion, true, now)
}

func (s *MemoryAccessStore) putProjectMember(projectID, userID string, role ProjectRole, actorID string, expectedVersion int64, conditional bool, now time.Time) (ProjectMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	editorOnly, err := s.authorizeProjectMemberMutation(projectID, actorID)
	if err != nil {
		return ProjectMember{}, err
	}
	if _, ok := s.users[userID]; !ok {
		return ProjectMember{}, ErrIdentityNotFound
	}
	if _, ok := s.projects[projectID]; !ok {
		return ProjectMember{}, ErrAccessNotFound
	}
	current, exists := s.projectMembers[projectID][userID]
	if !exists {
		return ProjectMember{}, ErrMembershipNotFound
	}
	if conditional && (expectedVersion < 1 || current.Version != expectedVersion) {
		return ProjectMember{}, ErrMembershipVersionConflict
	}
	if editorOnly && (current.Role == ProjectRoleOwner || role == ProjectRoleOwner) {
		return ProjectMember{}, ErrAuthorizationDenied
	}
	if exists && current.Role == ProjectRoleOwner && role != ProjectRoleOwner && s.projectOwnerCount(projectID, userID) == 0 {
		return ProjectMember{}, ErrLastProjectOwner
	}
	current.Version++
	current.Role = role
	current.InvitedBy = actorID
	current.UpdatedAt = now
	s.projectMembers[projectID][userID] = current
	return cloneProjectMember(current), nil
}

func (s *MemoryAccessStore) RemoveProjectMember(_ context.Context, projectID, userID, actorID string, _ time.Time) error {
	return s.removeProjectMember(projectID, userID, actorID, 0, false)
}

func (s *MemoryAccessStore) RemoveProjectMemberCAS(_ context.Context, projectID, userID, actorID string, expectedVersion int64, _ time.Time) error {
	return s.removeProjectMember(projectID, userID, actorID, expectedVersion, true)
}

func (s *MemoryAccessStore) removeProjectMember(projectID, userID, actorID string, expectedVersion int64, conditional bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	editorOnly, err := s.authorizeProjectMemberMutation(projectID, actorID)
	if err != nil {
		return err
	}
	member, ok := s.projectMembers[projectID][userID]
	if !ok {
		return ErrMembershipNotFound
	}
	if conditional && (expectedVersion < 1 || member.Version != expectedVersion) {
		return ErrMembershipVersionConflict
	}
	if editorOnly && member.Role == ProjectRoleOwner {
		return ErrAuthorizationDenied
	}
	if member.Role == ProjectRoleOwner && s.projectOwnerCount(projectID, userID) == 0 {
		return ErrLastProjectOwner
	}
	delete(s.projectMembers[projectID], userID)
	return nil
}

func (s *MemoryAccessStore) ResolveAccess(_ context.Context, userID string, target AccessTarget) (AccessGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if target.ProjectID == "" {
		member, ok := s.workspaceMembers[target.WorkspaceID][userID]
		if !ok || member.AcceptedAt == nil {
			return AccessGrant{}, ErrAccessNotFound
		}
		return AccessGrant{Target: target, WorkspaceRole: member.Role}, nil
	}
	project, ok := s.projects[target.ProjectID]
	if !ok || (target.WorkspaceID != "" && target.WorkspaceID != project.WorkspaceID) {
		return AccessGrant{}, ErrAccessNotFound
	}
	if target.EnvironmentID != "" {
		environment, ok := s.environments[target.EnvironmentID]
		if !ok || environment.ProjectID != target.ProjectID {
			return AccessGrant{}, ErrAccessNotFound
		}
	}
	target.WorkspaceID = project.WorkspaceID
	grant := AccessGrant{Target: target}
	if member, ok := s.workspaceMembers[project.WorkspaceID][userID]; ok && member.AcceptedAt != nil {
		grant.WorkspaceRole = member.Role
	}
	if member, ok := s.projectMembers[target.ProjectID][userID]; ok && member.AcceptedAt != nil {
		grant.ProjectRole = member.Role
	}
	if target.EnvironmentID != "" {
		grant.EnvironmentRestricted = s.environments[target.EnvironmentID].Restricted
	}
	if grant.WorkspaceRole == "" && grant.ProjectRole == "" {
		return AccessGrant{}, ErrAccessNotFound
	}
	return grant, nil
}

func (s *MemoryAccessStore) workspaceAdminCount(workspaceID, excludedUserID string) int {
	count := 0
	for userID, member := range s.workspaceMembers[workspaceID] {
		if userID != excludedUserID && member.Role == WorkspaceRoleAdmin && member.AcceptedAt != nil {
			count++
		}
	}
	return count
}

func (s *MemoryAccessStore) projectOwnerCount(projectID, excludedUserID string) int {
	count := 0
	for userID, member := range s.projectMembers[projectID] {
		if userID != excludedUserID && member.Role == ProjectRoleOwner && member.AcceptedAt != nil {
			count++
		}
	}
	return count
}

func (s *MemoryAccessStore) authorizeWorkspaceMemberMutation(workspaceID, actorID string) error {
	if _, ok := s.workspaces[workspaceID]; !ok {
		return ErrAccessNotFound
	}
	member, ok := s.workspaceMembers[workspaceID][actorID]
	if !ok || member.AcceptedAt == nil {
		return ErrAccessNotFound
	}
	if member.Role != WorkspaceRoleAdmin {
		return ErrAuthorizationDenied
	}
	return nil
}

// authorizeProjectMemberMutation returns whether the actor is authorized only
// through the editor role. Editors may manage non-owner memberships, but may
// neither grant nor change owner access.
func (s *MemoryAccessStore) authorizeProjectMemberMutation(projectID, actorID string) (bool, error) {
	project, ok := s.projects[projectID]
	if !ok {
		return false, ErrAccessNotFound
	}
	workspaceRole := WorkspaceRole("")
	if member, ok := s.workspaceMembers[project.WorkspaceID][actorID]; ok && member.AcceptedAt != nil {
		workspaceRole = member.Role
	}
	projectRole := ProjectRole("")
	if member, ok := s.projectMembers[projectID][actorID]; ok && member.AcceptedAt != nil {
		projectRole = member.Role
	}
	decision := EvaluateAccess(AccessRequest{
		WorkspaceRole: workspaceRole,
		ProjectRole:   projectRole,
		Capability:    CapabilityProjectMembersManage,
	})
	if !decision.Allowed {
		if workspaceRole == "" && projectRole == "" {
			return false, ErrAccessNotFound
		}
		return false, ErrAuthorizationDenied
	}
	return workspaceRole != WorkspaceRoleAdmin && projectRole == ProjectRoleEditor, nil
}

func cloneWorkspaceMember(member WorkspaceMember) WorkspaceMember {
	if member.AcceptedAt != nil {
		value := *member.AcceptedAt
		member.AcceptedAt = &value
	}
	return member
}

func cloneProjectMember(member ProjectMember) ProjectMember {
	if member.AcceptedAt != nil {
		value := *member.AcceptedAt
		member.AcceptedAt = &value
	}
	return member
}

func mergePublicUser(current, update User) User {
	if current.ID == "" {
		current.ID = update.ID
	}
	if update.Email != "" {
		current.Email = update.Email
	}
	if update.DisplayName != "" {
		current.DisplayName = update.DisplayName
	}
	if update.MFAEnabled {
		current.MFAEnabled = true
	}
	if !update.LastLoginAt.IsZero() {
		current.LastLoginAt = update.LastLoginAt
	}
	return current
}
