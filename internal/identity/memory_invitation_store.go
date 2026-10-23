// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package identity

import (
	"context"
	"crypto/sha256"
	"sort"
	"strings"
	"time"
)

func (s *MemoryAccessStore) CreateInvitation(ctx context.Context, credential InvitationCredential) (Invitation, error) {
	if err := ctx.Err(); err != nil {
		return Invitation{}, err
	}
	if err := validateInvitationCredential(credential); err != nil {
		return Invitation{}, err
	}
	if credential.ReplacesInvitationID != "" {
		return Invitation{}, ErrInvalidInvitation
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.authorizeInvitationMutation(credential.Invitation); err != nil {
		return Invitation{}, err
	}
	if s.invitationTargetAlreadyMember(credential.Invitation) {
		return Invitation{}, ErrInvitationMemberExists
	}
	if _, exists := s.invitations[credential.ID]; exists {
		return Invitation{}, ErrInvitationPending
	}
	macKey := string(credential.Verifier)
	if _, exists := s.invitationByMAC[macKey]; exists {
		return Invitation{}, ErrInvitationPending
	}
	for _, existing := range s.invitations {
		if existing.Email == credential.Email &&
			existing.WorkspaceID == credential.WorkspaceID &&
			existing.ProjectID == credential.ProjectID &&
			existing.AcceptedAt == nil && existing.RevokedAt == nil &&
			credential.CreatedAt.Before(existing.ExpiresAt) {
			return Invitation{}, ErrInvitationPending
		}
	}
	stored := cloneInvitationCredential(credential)
	s.invitations[stored.ID] = stored
	s.invitationByMAC[macKey] = stored.ID
	return cloneInvitation(stored.Invitation), nil
}

func (s *MemoryAccessStore) ListInvitations(ctx context.Context, request ListInvitationsRequest) ([]Invitation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	if request.WorkspaceID == "" || request.ActorUserID == "" {
		return nil, ErrInvalidInvitation
	}
	if request.Limit == 0 {
		request.Limit = InvitationDefaultPageLimit
	}
	if request.Limit < 1 || request.Limit > InvitationMaxPageLimit+1 ||
		request.Offset < 0 {
		return nil, ErrInvalidInvitation
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	editorOnly, err := s.authorizeInvitationScope(
		request.WorkspaceID,
		request.ProjectID,
		request.ActorUserID,
	)
	if err != nil {
		return nil, err
	}
	out := make([]Invitation, 0)
	for _, record := range s.invitations {
		if record.WorkspaceID != request.WorkspaceID ||
			record.ProjectID != request.ProjectID ||
			(editorOnly && record.ProjectRole == ProjectRoleOwner) {
			continue
		}
		out = append(out, cloneInvitation(record.Invitation))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if request.Offset >= len(out) {
		return []Invitation{}, nil
	}
	end := request.Offset + request.Limit
	if end > len(out) {
		end = len(out)
	}
	return out[request.Offset:end], nil
}

func (s *MemoryAccessStore) GetInvitation(ctx context.Context, request GetInvitationRequest) (Invitation, error) {
	if err := ctx.Err(); err != nil {
		return Invitation{}, err
	}
	request.InvitationID = strings.TrimSpace(request.InvitationID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	if request.InvitationID == "" || request.WorkspaceID == "" || request.ActorUserID == "" {
		return Invitation{}, ErrInvalidInvitation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.authorizeInvitationScope(request.WorkspaceID, request.ProjectID, request.ActorUserID); err != nil {
		return Invitation{}, err
	}
	record, ok := s.invitations[request.InvitationID]
	if !ok || record.WorkspaceID != request.WorkspaceID || record.ProjectID != request.ProjectID {
		return Invitation{}, ErrInvitationNotFound
	}
	authorityProbe := record.Invitation
	authorityProbe.InvitedByUserID = request.ActorUserID
	if err := s.authorizeInvitationMutation(authorityProbe); err != nil {
		return Invitation{}, err
	}
	return cloneInvitation(record.Invitation), nil
}

func (s *MemoryAccessStore) ReissueInvitation(
	ctx context.Context,
	replacement InvitationReplacement,
) (ReissueInvitationMetadata, error) {
	if err := ctx.Err(); err != nil {
		return ReissueInvitationMetadata{}, err
	}
	if err := validateInvitationReplacement(replacement); err != nil {
		return ReissueInvitationMetadata{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.invitations[replacement.InvitationID]
	if !ok || record.WorkspaceID != replacement.WorkspaceID ||
		record.ProjectID != replacement.ProjectID {
		return ReissueInvitationMetadata{}, ErrInvitationNotFound
	}
	if replacement.CreatedAt.Before(record.CreatedAt) {
		return ReissueInvitationMetadata{}, ErrInvalidInvitation
	}
	authorityProbe := record.Invitation
	authorityProbe.InvitedByUserID = replacement.ReissuedByUserID
	if err := s.authorizeInvitationMutation(authorityProbe); err != nil {
		return ReissueInvitationMetadata{}, err
	}
	if record.AcceptedAt != nil {
		return ReissueInvitationMetadata{}, ErrInvitationConsumed
	}
	if s.invitationTargetAlreadyMember(record.Invitation) {
		return ReissueInvitationMetadata{}, ErrInvitationMemberExists
	}
	for _, existing := range s.invitations {
		if existing.ReplacesInvitationID == record.ID {
			return ReissueInvitationMetadata{}, ErrInvitationReplaced
		}
		if existing.ID != record.ID &&
			existing.Email == record.Email &&
			existing.WorkspaceID == record.WorkspaceID &&
			existing.ProjectID == record.ProjectID &&
			existing.AcceptedAt == nil && existing.RevokedAt == nil &&
			replacement.CreatedAt.Before(existing.ExpiresAt) {
			return ReissueInvitationMetadata{}, ErrInvitationPending
		}
	}
	if _, exists := s.invitations[replacement.ReplacementID]; exists {
		return ReissueInvitationMetadata{}, ErrInvitationPending
	}
	replacementMAC := string(replacement.ReplacementVerifier)
	if _, exists := s.invitationByMAC[replacementMAC]; exists {
		return ReissueInvitationMetadata{}, ErrInvitationPending
	}

	newCredential := InvitationCredential{
		Invitation: Invitation{
			ID:                   replacement.ReplacementID,
			Email:                record.Email,
			WorkspaceID:          record.WorkspaceID,
			ProjectID:            record.ProjectID,
			WorkspaceRole:        record.WorkspaceRole,
			ProjectRole:          record.ProjectRole,
			TokenPrefix:          replacement.ReplacementTokenPrefix,
			InvitedByUserID:      replacement.ReissuedByUserID,
			CreatedAt:            replacement.CreatedAt,
			UpdatedAt:            replacement.CreatedAt,
			ExpiresAt:            replacement.ExpiresAt,
			ReplacesInvitationID: record.ID,
			ResourceVersion:      1,
		},
		Verifier: append([]byte(nil), replacement.ReplacementVerifier...),
	}
	if err := validateInvitationCredential(newCredential); err != nil {
		return ReissueInvitationMetadata{}, err
	}

	if record.RevokedAt == nil {
		revokedAt := replacement.CreatedAt.UTC()
		record.RevokedAt = &revokedAt
		record.RevokedByUserID = replacement.ReissuedByUserID
		record.RevokedReason = invitationReissueReason
		record.UpdatedAt = revokedAt
		record.ResourceVersion++
	}
	storedReplacement := cloneInvitationCredential(newCredential)
	s.invitations[record.ID] = record
	s.invitations[storedReplacement.ID] = storedReplacement
	s.invitationByMAC[replacementMAC] = storedReplacement.ID
	return ReissueInvitationMetadata{
		ReplacedInvitation: cloneInvitation(record.Invitation),
		Invitation:         cloneInvitation(storedReplacement.Invitation),
	}, nil
}

func (s *MemoryAccessStore) AcceptInvitation(ctx context.Context, acceptance InvitationAcceptance, now time.Time) (Invitation, error) {
	if err := ctx.Err(); err != nil {
		return Invitation{}, err
	}
	acceptance.UserID = strings.TrimSpace(acceptance.UserID)
	acceptance.Email = normalizeInvitationEmail(acceptance.Email)
	if len(acceptance.Verifier) != sha256.Size || acceptance.UserID == "" ||
		!validInvitationEmail(acceptance.Email) {
		return Invitation{}, ErrInvalidInvitation
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	id, ok := s.invitationByMAC[string(acceptance.Verifier)]
	if !ok {
		return Invitation{}, ErrInvitationNotFound
	}
	record := s.invitations[id]
	if now.Before(record.CreatedAt) {
		return Invitation{}, ErrInvalidInvitation
	}
	if record.AcceptedAt != nil {
		return Invitation{}, ErrInvitationConsumed
	}
	if record.RevokedAt != nil {
		return Invitation{}, ErrInvitationRevoked
	}
	if !now.Before(record.ExpiresAt) {
		return Invitation{}, ErrInvitationExpired
	}
	user, ok := s.users[acceptance.UserID]
	if !ok {
		return Invitation{}, ErrIdentityNotFound
	}
	if normalizeInvitationEmail(user.Email) != record.Email ||
		acceptance.Email != record.Email {
		return Invitation{}, ErrInvitationEmailMismatch
	}

	acceptedAt := now.UTC()
	if record.ProjectID == "" {
		if _, ok := s.workspaces[record.WorkspaceID]; !ok {
			return Invitation{}, ErrInvitationScopeNotActive
		}
		if _, exists := s.workspaceMembers[record.WorkspaceID][acceptance.UserID]; exists {
			return Invitation{}, ErrInvitationMemberExists
		}
		if s.workspaceMembers[record.WorkspaceID] == nil {
			s.workspaceMembers[record.WorkspaceID] = make(map[string]WorkspaceMember)
		}
		s.workspaceMembers[record.WorkspaceID][acceptance.UserID] = WorkspaceMember{
			WorkspaceID: record.WorkspaceID,
			User:        user,
			Role:        record.WorkspaceRole,
			InvitedBy:   record.InvitedByUserID,
			InvitedAt:   record.CreatedAt,
			AcceptedAt:  &acceptedAt,
			CreatedAt:   acceptedAt,
			UpdatedAt:   acceptedAt,
			Version:     1,
		}
	} else {
		project, ok := s.projects[record.ProjectID]
		if !ok || project.WorkspaceID != record.WorkspaceID {
			return Invitation{}, ErrInvitationScopeNotActive
		}
		if _, exists := s.projectMembers[record.ProjectID][acceptance.UserID]; exists {
			return Invitation{}, ErrInvitationMemberExists
		}
		if s.projectMembers[record.ProjectID] == nil {
			s.projectMembers[record.ProjectID] = make(map[string]ProjectMember)
		}
		s.projectMembers[record.ProjectID][acceptance.UserID] = ProjectMember{
			ProjectID:  record.ProjectID,
			User:       user,
			Role:       record.ProjectRole,
			InvitedBy:  record.InvitedByUserID,
			InvitedAt:  record.CreatedAt,
			AcceptedAt: &acceptedAt,
			CreatedAt:  acceptedAt,
			UpdatedAt:  acceptedAt,
			Version:    1,
		}
	}

	record.AcceptedAt = &acceptedAt
	record.AcceptedByUserID = acceptance.UserID
	record.UpdatedAt = acceptedAt
	record.ResourceVersion++
	s.invitations[record.ID] = record
	return cloneInvitation(record.Invitation), nil
}

func (s *MemoryAccessStore) RevokeInvitation(ctx context.Context, request RevokeInvitationRequest, now time.Time) (Invitation, error) {
	if err := ctx.Err(); err != nil {
		return Invitation{}, err
	}
	request.InvitationID = strings.TrimSpace(request.InvitationID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.RevokedByUserID = strings.TrimSpace(request.RevokedByUserID)
	request.Reason = cleanInvitationReason(request.Reason)
	if request.InvitationID == "" || request.WorkspaceID == "" ||
		request.RevokedByUserID == "" {
		return Invitation{}, ErrInvalidInvitation
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.invitations[request.InvitationID]
	if !ok || record.WorkspaceID != request.WorkspaceID ||
		record.ProjectID != request.ProjectID {
		return Invitation{}, ErrInvitationNotFound
	}
	if now.Before(record.CreatedAt) {
		return Invitation{}, ErrInvalidInvitation
	}
	authorityProbe := record.Invitation
	authorityProbe.InvitedByUserID = request.RevokedByUserID
	if err := s.authorizeInvitationMutation(authorityProbe); err != nil {
		return Invitation{}, err
	}
	if record.AcceptedAt != nil {
		return Invitation{}, ErrInvitationConsumed
	}
	if request.ExpectedVersion != nil && record.ResourceVersion != *request.ExpectedVersion {
		return Invitation{}, ErrInvitationVersionConflict
	}
	if record.RevokedAt == nil {
		revokedAt := now.UTC()
		record.RevokedAt = &revokedAt
		record.RevokedByUserID = request.RevokedByUserID
		record.RevokedReason = request.Reason
		record.UpdatedAt = revokedAt
		record.ResourceVersion++
		s.invitations[record.ID] = record
	}
	return cloneInvitation(record.Invitation), nil
}

func (s *MemoryAccessStore) authorizeInvitationMutation(invitation Invitation) error {
	editorOnly, err := s.authorizeInvitationScope(
		invitation.WorkspaceID,
		invitation.ProjectID,
		invitation.InvitedByUserID,
	)
	if err != nil {
		return err
	}
	if editorOnly && invitation.ProjectRole == ProjectRoleOwner {
		return ErrAuthorizationDenied
	}
	return nil
}

func (s *MemoryAccessStore) authorizeInvitationScope(workspaceID, projectID, actorID string) (bool, error) {
	if projectID == "" {
		return false, s.authorizeWorkspaceMemberMutation(workspaceID, actorID)
	}
	project, ok := s.projects[projectID]
	if !ok || project.WorkspaceID != workspaceID {
		return false, ErrInvitationScopeNotActive
	}
	editorOnly, err := s.authorizeProjectMemberMutation(projectID, actorID)
	if err != nil {
		return false, err
	}
	return editorOnly, nil
}

func (s *MemoryAccessStore) invitationTargetAlreadyMember(invitation Invitation) bool {
	for userID, user := range s.users {
		if normalizeInvitationEmail(user.Email) != invitation.Email {
			continue
		}
		if invitation.ProjectID == "" {
			_, exists := s.workspaceMembers[invitation.WorkspaceID][userID]
			return exists
		}
		_, exists := s.projectMembers[invitation.ProjectID][userID]
		return exists
	}
	return false
}
