// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
)

const identityClaimAPIPath = "/api/v1alpha/auth/claim"

// adminCreateUserClock is a package hook so tests can pin the TOTP window.
var adminCreateUserClock = func() time.Time { return time.Now().UTC() }

type adminCreateUserRequest struct {
	Email                string `json:"email"`
	DisplayName          string `json:"display_name"`
	Password             string `json:"password"`
	TOTPSecret           string `json:"totp_secret"`
	TOTPCode             string `json:"totp_code"`
	WorkspaceSlug        string `json:"workspace_slug"`
	WorkspaceDisplayName string `json:"workspace_display_name"`
}

// adminCreateUserResult is what `--output json` prints. The TOTP secret and
// enrollment URI are included because this is the only moment they exist in
// plaintext; the password never appears in the output.
type adminCreateUserResult struct {
	User        identity.User      `json:"user"`
	Workspace   identity.Workspace `json:"workspace"`
	TOTPSecret  string             `json:"totp_secret"`
	OTPAuthURI  string             `json:"otpauth_uri"`
	TOTPWarning string             `json:"totp_warning"`
}

type adminCreateUserFlags struct {
	email         *string
	displayName   *string
	workspaceSlug *string
	workspaceName *string
	password      *string
	issuer        *string
}

func defineAdminCreateUserFlags(fs *flag.FlagSet) adminCreateUserFlags {
	return adminCreateUserFlags{
		email:         fs.String("email", "", "administrator email address"),
		displayName:   fs.String("display-name", "", "administrator display name"),
		workspaceSlug: fs.String("workspace-slug", "", "workspace slug (lowercase letters, digits, and hyphens)"),
		workspaceName: fs.String("workspace-name", "", "workspace display name"),
		password: fs.String(
			"password",
			"",
			fmt.Sprintf("administrator password (at least %d characters; prompted securely when omitted)", bootstrap.MinAdminPasswordLength),
		),
		issuer: fs.String("issuer", "Midnight", "issuer label recorded in the otpauth:// enrollment URI"),
	}
}

// runAdminCreateUser creates the first human administrator by driving the
// one-shot POST /auth/claim with an operator recovery credential. It is the
// only supported way to turn a fresh installation into one a person can sign
// in to, so it also generates and reports the TOTP enrollment material.
func runAdminCreateUser(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight admin create-user", stderr)
	f := defineAdminCreateUserFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight admin create-user --email EMAIL --display-name NAME --workspace-slug SLUG --workspace-name NAME [--password VALUE] [--api-base URL] [--token TOKEN]")
		return 64
	}
	email := strings.ToLower(strings.TrimSpace(*f.email))
	displayName := strings.TrimSpace(*f.displayName)
	workspaceSlug := strings.ToLower(strings.TrimSpace(*f.workspaceSlug))
	workspaceName := strings.TrimSpace(*f.workspaceName)
	for _, required := range []struct {
		flag  string
		value string
	}{
		{"--email", email},
		{"--display-name", displayName},
		{"--workspace-slug", workspaceSlug},
		{"--workspace-name", workspaceName},
	} {
		if required.value == "" {
			fmt.Fprintf(stderr, "midnight admin create-user: %s is required\n", required.flag)
			return 64
		}
	}

	password := *f.password
	if password == "" {
		prompted, err := loginSecretPrompt("Administrator password: ", stderr)
		if err != nil {
			fmt.Fprintf(stderr, "midnight admin create-user: read password: %s\n", err)
			return 1
		}
		confirmation, err := loginSecretPrompt("Confirm password: ", stderr)
		if err != nil {
			fmt.Fprintf(stderr, "midnight admin create-user: read password confirmation: %s\n", err)
			return 1
		}
		if prompted != confirmation {
			fmt.Fprintln(stderr, "midnight admin create-user: passwords did not match")
			return 64
		}
		password = prompted
	}
	if len(password) < bootstrap.MinAdminPasswordLength {
		fmt.Fprintf(
			stderr,
			"midnight admin create-user: password must be at least %d characters\n",
			bootstrap.MinAdminPasswordLength,
		)
		return 64
	}

	secretBase32, err := bootstrap.GenerateTOTPSecret()
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin create-user: generate TOTP secret: %s\n", err)
		return 1
	}
	rawSecret, err := bootstrap.DecodeTOTPSecret(secretBase32)
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin create-user: decode TOTP secret: %s\n", err)
		return 1
	}
	totpCode, err := bootstrap.GenerateTOTP(rawSecret, adminCreateUserClock())
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin create-user: derive TOTP code: %s\n", err)
		return 1
	}
	idempotencyKey, err := newAdminCreateUserIdempotencyKey()
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin create-user: generate idempotency key: %s\n", err)
		return 1
	}

	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin create-user: %s\n", err)
		return 64
	}
	body := adminCreateUserRequest{
		Email:                email,
		DisplayName:          displayName,
		Password:             password,
		TOTPSecret:           secretBase32,
		TOTPCode:             totpCode,
		WorkspaceSlug:        workspaceSlug,
		WorkspaceDisplayName: workspaceName,
	}
	var claim identity.ClaimDefaultWorkspaceResult
	_, err = client.doJSON(http.MethodPost, identityClaimAPIPath, body, idempotencyKey, &claim)
	if err != nil {
		writeAdminCreateUserFailure(stderr, err)
		return 1
	}

	result := adminCreateUserResult{
		User:        claim.User,
		Workspace:   claim.Workspace,
		TOTPSecret:  secretBase32,
		OTPAuthURI:  bootstrap.OTPAuthURI(strings.TrimSpace(*f.issuer), email, secretBase32),
		TOTPWarning: "Save the TOTP secret now. It is not stored in plaintext and cannot be shown again.",
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		fmt.Fprintf(stderr, "midnight admin create-user: format result: %s\n", marshalErr)
		return 1
	}
	return renderCLISingle(stdout, stderr, cfg.JSON, encoded, result,
		func(value adminCreateUserResult) string { return value.User.ID },
		writeAdminCreateUserSummary,
	)
}

func writeAdminCreateUserSummary(output io.Writer, value adminCreateUserResult) {
	fmt.Fprintf(output, "administrator created: %s\n", terminalSafeText(value.User.Email))
	fmt.Fprintf(output, "user id:      %s\n", terminalSafeText(value.User.ID))
	fmt.Fprintf(output, "workspace:    %s (%s)\n",
		terminalSafeText(value.Workspace.DisplayName), terminalSafeText(value.Workspace.Slug))
	fmt.Fprintln(output)
	fmt.Fprintf(output, "TOTP enrollment URL: %s\n", terminalSafeText(value.OTPAuthURI))
	fmt.Fprintf(output, "TOTP secret (Base32): %s\n", terminalSafeText(value.TOTPSecret))
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Save this now: the TOTP secret is not shown again and cannot be recovered.")
	fmt.Fprintln(output, "Add it to an authenticator app, then sign in with `midnight login`.")
}

// writeAdminCreateUserFailure turns the claim's typed conflict into the one
// answer an operator actually needs: this installation already has an admin.
func writeAdminCreateUserFailure(stderr io.Writer, err error) {
	var status *cliAPIStatusFailure
	if errors.As(err, &status) && status.Code == string(failurecodes.ResourceConflict) {
		fmt.Fprintln(stderr, "midnight admin create-user: this installation already has an administrator; the first-user claim is one-shot")
		fmt.Fprintln(stderr, "Sign in with `midnight login`, or invite additional people with `midnight invitation create`.")
		return
	}
	fmt.Fprintf(stderr, "midnight admin create-user: %s\n", err)
}

func newAdminCreateUserIdempotencyKey() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "admin-create-user-" + base64.RawURLEncoding.EncodeToString(buf), nil
}
