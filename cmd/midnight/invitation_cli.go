// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/midnight/midnight/internal/cliconfig"
	"github.com/midnight/midnight/internal/identity"
)

const invitationPath = "/api/v1alpha/invitations"

type cliInvitationList struct {
	Items      []identity.Invitation `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type cliInvitationCreateRequest struct {
	Email         string                 `json:"email"`
	WorkspaceID   string                 `json:"workspace_id"`
	ProjectID     string                 `json:"project_id,omitempty"`
	WorkspaceRole identity.WorkspaceRole `json:"workspace_role,omitempty"`
	ProjectRole   identity.ProjectRole   `json:"project_role,omitempty"`
	ExpiresAt     time.Time              `json:"expires_at"`
}

type cliInvitationScopeRequest struct {
	WorkspaceID string    `json:"workspace_id"`
	ProjectID   string    `json:"project_id,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type cliInvitationAcceptRequest struct {
	InvitationToken string `json:"invitation_token"`
}

type cliInvitationCredentialResult struct {
	InvitationToken string              `json:"invitation_token"`
	Invitation      identity.Invitation `json:"invitation"`
}

func runInvitation(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printInvitationUsage(stderr)
		return 64
	}
	switch args[0] {
	case "list":
		return runInvitationList(args[1:], stdout, stderr)
	case "create":
		return runInvitationCreate(args[1:], stdout, stderr)
	case "revoke":
		return runInvitationRevoke(args[1:], stdout, stderr)
	case "reissue":
		return runInvitationReissue(args[1:], stdout, stderr)
	case "accept":
		return runInvitationAccept(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "midnight invitation: unknown subcommand %q\n", args[0])
		printInvitationUsage(stderr)
		return 64
	}
}

func runInvitationList(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight invitation list", stderr)
	f := defineInvitationListFlags(fs)
	workspaceID, projectID, limit, cursor := f.workspaceID, f.projectID, f.limit, f.cursor
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 || strings.TrimSpace(*workspaceID) == "" {
		fmt.Fprintln(stderr, "usage: midnight invitation list --workspace-id ID [--project-id ID] [--limit N] [--cursor TOKEN] [--api-base URL] [--json]")
		return 64
	}
	if *limit < 1 || *limit > identity.InvitationMaxPageLimit {
		fmt.Fprintf(
			stderr,
			"midnight invitation list: --limit must be between 1 and %d\n",
			identity.InvitationMaxPageLimit,
		)
		return 64
	}
	client, err := newHumanInvitationClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation list: %s\n", err)
		return 1
	}
	path := invitationCollectionPath(*workspaceID, *projectID, *limit, *cursor)
	var result cliInvitationList
	raw, err := client.doJSON(http.MethodGet, path, nil, "", &result)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation list: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	writeInvitationTable(stdout, result.Items)
	if strings.TrimSpace(result.NextCursor) != "" {
		fmt.Fprintf(stdout, "next page: midnight invitation list --workspace-id %s",
			terminalSafeText(strings.TrimSpace(*workspaceID)))
		if value := strings.TrimSpace(*projectID); value != "" {
			fmt.Fprintf(stdout, " --project-id %s", terminalSafeText(value))
		}
		fmt.Fprintf(
			stdout,
			" --limit %d --cursor %s\n",
			*limit,
			terminalSafeText(result.NextCursor),
		)
	}
	return 0
}

func runInvitationCreate(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight invitation create", stderr)
	f := defineInvitationCreateFlags(fs)
	email, workspaceID, projectID, workspaceRole := f.email, f.workspaceID, f.projectID, f.workspaceRole
	projectRole, expiresAt, idempotencyKey := f.projectRole, f.expiresAt, f.idempotencyKey
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "midnight invitation create: unexpected positional arguments")
		return 64
	}
	roleScope, err := validateInvitationRoleScope(
		*workspaceID,
		*projectID,
		*workspaceRole,
		*projectRole,
	)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation create: %s\n", err)
		return 64
	}
	if strings.TrimSpace(*email) == "" {
		fmt.Fprintln(stderr, "midnight invitation create: --email is required")
		return 64
	}
	expiry, err := fixedInvitationExpiry(*expiresAt)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation create: %s\n", err)
		return 64
	}
	key, err := requiredInvitationIdempotencyKey(*idempotencyKey)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation create: %s\n", err)
		return 64
	}
	client, err := newHumanInvitationClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation create: %s\n", err)
		return 1
	}
	request := cliInvitationCreateRequest{
		Email:         strings.ToLower(strings.TrimSpace(*email)),
		WorkspaceID:   roleScope.workspaceID,
		ProjectID:     roleScope.projectID,
		WorkspaceRole: roleScope.workspaceRole,
		ProjectRole:   roleScope.projectRole,
		ExpiresAt:     expiry,
	}
	var result cliInvitationCredentialResult
	raw, err := client.doJSON(http.MethodPost, invitationPath, request, key, &result)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation create: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	writeInvitationCredential(stdout, "Created", result)
	return 0
}

func runInvitationRevoke(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight invitation revoke", stderr)
	f := defineInvitationRevokeFlags(fs)
	workspaceID, projectID, idempotencyKey := f.workspaceID, f.projectID, f.idempotencyKey
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*workspaceID) == "" {
		fmt.Fprintln(stderr, "usage: midnight invitation revoke --workspace-id ID [--project-id ID] --idempotency-key KEY <invitation-id>")
		return 64
	}
	key, err := requiredInvitationIdempotencyKey(*idempotencyKey)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation revoke: %s\n", err)
		return 64
	}
	client, err := newHumanInvitationClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation revoke: %s\n", err)
		return 1
	}
	path := invitationResourcePath(fs.Arg(0)) + invitationScopeQuery(*workspaceID, *projectID)
	raw, err := client.doJSON(http.MethodDelete, path, nil, key, nil)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation revoke: %s\n", err)
		return 1
	}
	document := raw
	if len(bytes.TrimSpace(document)) == 0 {
		document = cliRevokedDocument("id", strings.TrimSpace(fs.Arg(0)))
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, document); done {
		return code
	}
	fmt.Fprintf(stdout, "Revoked invitation %s.\n", terminalSafeText(fs.Arg(0)))
	return 0
}

func runInvitationReissue(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight invitation reissue", stderr)
	f := defineInvitationReissueFlags(fs)
	workspaceID, projectID, expiresAt, idempotencyKey := f.workspaceID, f.projectID, f.expiresAt, f.idempotencyKey
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 || strings.TrimSpace(*workspaceID) == "" {
		fmt.Fprintln(stderr, "usage: midnight invitation reissue --workspace-id ID [--project-id ID] --expires-at RFC3339 --idempotency-key KEY <invitation-id>")
		return 64
	}
	expiry, err := fixedInvitationExpiry(*expiresAt)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation reissue: %s\n", err)
		return 64
	}
	key, err := requiredInvitationIdempotencyKey(*idempotencyKey)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation reissue: %s\n", err)
		return 64
	}
	client, err := newHumanInvitationClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation reissue: %s\n", err)
		return 1
	}
	request := cliInvitationScopeRequest{
		WorkspaceID: strings.TrimSpace(*workspaceID),
		ProjectID:   strings.TrimSpace(*projectID),
		ExpiresAt:   expiry,
	}
	path := invitationResourcePath(fs.Arg(0)) + "/reissue"
	var result cliInvitationCredentialResult
	raw, err := client.doJSON(http.MethodPost, path, request, key, &result)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation reissue: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	writeInvitationCredential(stdout, "Reissued", result)
	return 0
}

func runInvitationAccept(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight invitation accept", stderr)
	f := defineInvitationAcceptFlags(fs)
	invitationToken, idempotencyKey := f.invitationToken, f.idempotencyKey
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 || strings.TrimSpace(*invitationToken) == "" {
		fmt.Fprintln(stderr, "usage: midnight invitation accept --invitation-token TOKEN --idempotency-key KEY [--api-base URL]")
		return 64
	}
	key, err := requiredInvitationIdempotencyKey(*idempotencyKey)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation accept: %s\n", err)
		return 64
	}
	client, err := newHumanInvitationClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation accept: %s\n", err)
		return 1
	}
	request := cliInvitationAcceptRequest{
		InvitationToken: strings.TrimSpace(*invitationToken),
	}
	var invitation identity.Invitation
	raw, err := client.doJSON(http.MethodPost, invitationPath+"/accept", request, key, &invitation)
	if err != nil {
		fmt.Fprintf(stderr, "midnight invitation accept: %s\n", err)
		return 1
	}
	if code, done := renderCLIRawDocument(stdout, stderr, cfg.JSON, raw); done {
		return code
	}
	fmt.Fprintf(
		stdout,
		"Accepted invitation %s for workspace %s",
		terminalSafeText(invitation.ID),
		terminalSafeText(invitation.WorkspaceID),
	)
	if invitation.ProjectID != "" {
		fmt.Fprintf(stdout, " and project %s", terminalSafeText(invitation.ProjectID))
	}
	fmt.Fprintln(stdout, ".")
	return 0
}

type invitationRoleScope struct {
	workspaceID   string
	projectID     string
	workspaceRole identity.WorkspaceRole
	projectRole   identity.ProjectRole
}

func validateInvitationRoleScope(
	workspaceID,
	projectID,
	workspaceRole,
	projectRole string,
) (invitationRoleScope, error) {
	scope := invitationRoleScope{
		workspaceID: strings.TrimSpace(workspaceID),
		projectID:   strings.TrimSpace(projectID),
	}
	if scope.workspaceID == "" {
		return invitationRoleScope{}, fmt.Errorf("--workspace-id is required")
	}
	workspaceRole = strings.TrimSpace(workspaceRole)
	projectRole = strings.TrimSpace(projectRole)
	if (workspaceRole == "") == (projectRole == "") {
		return invitationRoleScope{}, fmt.Errorf("exactly one of --workspace-role or --project-role is required")
	}
	if scope.projectID == "" {
		if projectRole != "" {
			return invitationRoleScope{}, fmt.Errorf("--project-role requires --project-id")
		}
		role, ok := identity.ParseWorkspaceRole(workspaceRole)
		if !ok {
			return invitationRoleScope{}, fmt.Errorf("--workspace-role must be admin, member, deployer, or viewer")
		}
		scope.workspaceRole = role
		return scope, nil
	}
	if workspaceRole != "" {
		return invitationRoleScope{}, fmt.Errorf("--project-id requires --project-role and forbids --workspace-role")
	}
	role, ok := identity.ParseProjectRole(projectRole)
	if !ok {
		return invitationRoleScope{}, fmt.Errorf("--project-role must be owner, editor, or viewer")
	}
	scope.projectRole = role
	return scope, nil
}

func fixedInvitationExpiry(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("--expires-at is required so exact retries use the same request")
	}
	expiry, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("--expires-at must be RFC3339")
	}
	return expiry.UTC(), nil
}

func requiredInvitationIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("--idempotency-key is required")
	}
	return value, nil
}

func newHumanInvitationClient(cfg *apiCommandConfig) (*cliAPIClient, error) {
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		return nil, err
	}
	if client.credentialKind != cliconfig.CredentialKindHumanSession {
		return nil, fmt.Errorf("invitation management requires a human session; run `midnight login`")
	}
	return client, nil
}

func invitationCollectionPath(workspaceID, projectID string, limit int, cursor string) string {
	query := url.Values{}
	query.Set("workspace_id", strings.TrimSpace(workspaceID))
	if value := strings.TrimSpace(projectID); value != "" {
		query.Set("project_id", value)
	}
	query.Set("limit", fmt.Sprintf("%d", limit))
	if value := strings.TrimSpace(cursor); value != "" {
		query.Set("cursor", value)
	}
	return invitationPath + "?" + query.Encode()
}

func invitationResourcePath(invitationID string) string {
	return invitationPath + "/" + url.PathEscape(strings.TrimSpace(invitationID))
}

func invitationScopeQuery(workspaceID, projectID string) string {
	query := url.Values{}
	query.Set("workspace_id", strings.TrimSpace(workspaceID))
	if value := strings.TrimSpace(projectID); value != "" {
		query.Set("project_id", value)
	}
	return "?" + query.Encode()
}

func invitationRole(invitation identity.Invitation) string {
	if invitation.ProjectID != "" {
		return string(invitation.ProjectRole)
	}
	return string(invitation.WorkspaceRole)
}

func invitationScope(invitation identity.Invitation) string {
	if invitation.ProjectID != "" {
		return invitation.ProjectID
	}
	return invitation.WorkspaceID
}

func invitationState(invitation identity.Invitation, now time.Time) string {
	switch {
	case invitation.AcceptedAt != nil:
		return "accepted"
	case invitation.RevokedAt != nil:
		return "revoked"
	case !now.Before(invitation.ExpiresAt):
		return "expired"
	default:
		return "pending"
	}
}

func writeInvitationTable(output io.Writer, invitations []identity.Invitation) {
	if len(invitations) == 0 {
		fmt.Fprintln(output, "No invitations.")
		return
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tEMAIL\tSCOPE\tROLE\tPREFIX\tEXPIRES\tSTATE")
	now := time.Now().UTC()
	for _, invitation := range invitations {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			terminalSafeText(invitation.ID),
			terminalSafeText(invitation.Email),
			terminalSafeText(invitationScope(invitation)),
			terminalSafeText(invitationRole(invitation)),
			terminalSafeText(invitation.TokenPrefix),
			invitation.ExpiresAt.UTC().Format(time.RFC3339),
			invitationState(invitation, now),
		)
	}
	_ = writer.Flush()
}

func writeInvitationCredential(
	output io.Writer,
	verb string,
	result cliInvitationCredentialResult,
) {
	fmt.Fprintf(
		output,
		"%s invitation %s for %s.\n",
		verb,
		terminalSafeText(result.Invitation.ID),
		terminalSafeText(result.Invitation.Email),
	)
	fmt.Fprintln(output, "Midnight does not send invitation email. Deliver this command securely; the credential will not appear in invitation lists:")
	fmt.Fprintf(
		output,
		"midnight invitation accept --invitation-token %s --idempotency-key RECIPIENT_RETRY_KEY\n",
		terminalSafeText(result.InvitationToken),
	)
}

func printInvitationUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: midnight invitation list|create|revoke|reissue|accept [flags]")
}

type invitationListFlags struct {
	workspaceID *string
	projectID   *string
	limit       *int
	cursor      *string
}

func defineInvitationListFlags(fs *flag.FlagSet) invitationListFlags {
	return invitationListFlags{
		workspaceID: fs.String("workspace-id", "", "workspace invitation scope"),
		projectID:   cliProjectIDFlag(fs, "optional direct-project invitation scope"),
		limit:       cliLimitFlag(fs, identity.InvitationDefaultPageLimit, "maximum rows (1-200)"),
		cursor:      fs.String("cursor", "", "opaque continuation cursor returned by a previous list"),
	}
}

type invitationCreateFlags struct {
	email          *string
	workspaceID    *string
	projectID      *string
	workspaceRole  *string
	projectRole    *string
	expiresAt      *string
	idempotencyKey *string
}

func defineInvitationCreateFlags(fs *flag.FlagSet) invitationCreateFlags {
	return invitationCreateFlags{
		email:          fs.String("email", "", "invitee email address"),
		workspaceID:    fs.String("workspace-id", "", "workspace invitation scope"),
		projectID:      cliProjectIDFlag(fs, "optional direct-project invitation scope"),
		workspaceRole:  fs.String("workspace-role", "", "admin|member|deployer|viewer"),
		projectRole:    fs.String("project-role", "", "owner|editor|viewer"),
		expiresAt:      fs.String("expires-at", "", "fixed RFC3339 expiry"),
		idempotencyKey: cliIdempotencyKeyFlag(fs, "required stable retry key"),
	}
}

type invitationRevokeFlags struct {
	workspaceID    *string
	projectID      *string
	idempotencyKey *string
}

func defineInvitationRevokeFlags(fs *flag.FlagSet) invitationRevokeFlags {
	return invitationRevokeFlags{
		workspaceID:    fs.String("workspace-id", "", "workspace invitation scope"),
		projectID:      cliProjectIDFlag(fs, "optional direct-project invitation scope"),
		idempotencyKey: cliIdempotencyKeyFlag(fs, "required stable retry key"),
	}
}

type invitationReissueFlags struct {
	workspaceID    *string
	projectID      *string
	expiresAt      *string
	idempotencyKey *string
}

func defineInvitationReissueFlags(fs *flag.FlagSet) invitationReissueFlags {
	return invitationReissueFlags{
		workspaceID:    fs.String("workspace-id", "", "workspace invitation scope"),
		projectID:      cliProjectIDFlag(fs, "optional direct-project invitation scope"),
		expiresAt:      fs.String("expires-at", "", "fixed RFC3339 expiry for the replacement invitation"),
		idempotencyKey: cliIdempotencyKeyFlag(fs, "required stable retry key"),
	}
}

type invitationAcceptFlags struct {
	invitationToken *string
	idempotencyKey  *string
}

func defineInvitationAcceptFlags(fs *flag.FlagSet) invitationAcceptFlags {
	return invitationAcceptFlags{
		invitationToken: fs.String("invitation-token", "", "one-time midn_inv_* invitation credential"),
		idempotencyKey:  cliIdempotencyKeyFlag(fs, "required stable retry key"),
	}
}
