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

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
)

const identityPasswordResetAPIPath = "/api/v1alpha/auth/password-reset"

type adminResetPasswordRequest struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	TOTPSecret   string `json:"totp_secret,omitempty"`
	TOTPCode     string `json:"totp_code,omitempty"`
	PreserveTOTP bool   `json:"preserve_totp,omitempty"`
}

// adminResetPasswordResult is what `--output json` prints. A replacement TOTP
// secret and its enrollment URI appear only when this run generated them, and
// only here: this is the single moment they exist in plaintext. The password
// never appears in the output.
type adminResetPasswordResult struct {
	User            identity.User `json:"user"`
	TOTPReenrolled  bool          `json:"totp_reenrolled"`
	SessionsRevoked int           `json:"sessions_revoked"`
	TOTPSecret      string        `json:"totp_secret,omitempty"`
	OTPAuthURI      string        `json:"otpauth_uri,omitempty"`
	TOTPWarning     string        `json:"totp_warning,omitempty"`
}

type adminResetPasswordFlags struct {
	email    *string
	password *string
	keepTOTP *bool
	issuer   *string
}

func defineAdminResetPasswordFlags(fs *flag.FlagSet) adminResetPasswordFlags {
	return adminResetPasswordFlags{
		email: fs.String("email", "", "administrator email address to recover"),
		password: fs.String(
			"password",
			"",
			fmt.Sprintf("new password (at least %d characters; prompted securely when omitted)", bootstrap.MinAdminPasswordLength),
		),
		keepTOTP: fs.Bool(
			"keep-totp",
			false,
			"keep the account's existing authenticator instead of enrolling a replacement",
		),
		issuer: fs.String("issuer", "Midnight", "issuer label recorded in the otpauth:// enrollment URI"),
	}
}

// runAdminResetPassword recovers a locked-out installation administrator by
// driving POST /auth/password-reset with the operator recovery credential.
// `admin create-user` is one-shot, so this is the supported way back into an
// installation whose first-run password was lost or mistyped.
//
// The second factor is never silently dropped: the command enrolls a
// replacement authenticator and reports it once, or --keep-totp preserves the
// one the account already has.
func runAdminResetPassword(args []string, stdout, stderr io.Writer) int {
	fs, cfg := newAPIFlagSet("midnight admin reset-password", stderr)
	f := defineAdminResetPasswordFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: midnight admin reset-password --email EMAIL [--password VALUE] [--keep-totp] [--api-base URL] [--token TOKEN]")
		return 64
	}
	email := strings.ToLower(strings.TrimSpace(*f.email))
	if email == "" {
		fmt.Fprintln(stderr, "midnight admin reset-password: --email is required")
		return 64
	}

	password := *f.password
	if password == "" {
		prompted, err := loginSecretPrompt("New administrator password: ", stderr)
		if err != nil {
			fmt.Fprintf(stderr, "midnight admin reset-password: read password: %s\n", err)
			return 1
		}
		confirmation, err := loginSecretPrompt("Confirm password: ", stderr)
		if err != nil {
			fmt.Fprintf(stderr, "midnight admin reset-password: read password confirmation: %s\n", err)
			return 1
		}
		if prompted != confirmation {
			fmt.Fprintln(stderr, "midnight admin reset-password: passwords did not match")
			return 64
		}
		password = prompted
	}
	if len(password) < bootstrap.MinAdminPasswordLength {
		fmt.Fprintf(
			stderr,
			"midnight admin reset-password: password must be at least %d characters\n",
			bootstrap.MinAdminPasswordLength,
		)
		return 64
	}

	body := adminResetPasswordRequest{Email: email, Password: password}
	secretBase32 := ""
	if *f.keepTOTP {
		body.PreserveTOTP = true
	} else {
		generated, err := bootstrap.GenerateTOTPSecret()
		if err != nil {
			fmt.Fprintf(stderr, "midnight admin reset-password: generate TOTP secret: %s\n", err)
			return 1
		}
		rawSecret, err := bootstrap.DecodeTOTPSecret(generated)
		if err != nil {
			fmt.Fprintf(stderr, "midnight admin reset-password: decode TOTP secret: %s\n", err)
			return 1
		}
		totpCode, err := bootstrap.GenerateTOTP(rawSecret, adminCreateUserClock())
		if err != nil {
			fmt.Fprintf(stderr, "midnight admin reset-password: derive TOTP code: %s\n", err)
			return 1
		}
		secretBase32 = generated
		body.TOTPSecret = generated
		body.TOTPCode = totpCode
	}

	idempotencyKey, err := newAdminResetPasswordIdempotencyKey()
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin reset-password: generate idempotency key: %s\n", err)
		return 1
	}
	client, err := newCLIAPIClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin reset-password: %s\n", err)
		return 64
	}
	var reset identity.ResetAdministratorCredentialsResult
	if _, err := client.doJSON(http.MethodPost, identityPasswordResetAPIPath, body, idempotencyKey, &reset); err != nil {
		writeAdminResetPasswordFailure(stderr, err)
		return 1
	}

	result := adminResetPasswordResult{
		User:            reset.User,
		TOTPReenrolled:  reset.TOTPReenrolled,
		SessionsRevoked: reset.SessionsRevoked,
	}
	if reset.TOTPReenrolled && secretBase32 != "" {
		result.TOTPSecret = secretBase32
		result.OTPAuthURI = bootstrap.OTPAuthURI(strings.TrimSpace(*f.issuer), email, secretBase32)
		result.TOTPWarning = "Save the TOTP secret now. It is not stored in plaintext and cannot be shown again."
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		fmt.Fprintf(stderr, "midnight admin reset-password: format result: %s\n", marshalErr)
		return 1
	}
	return renderCLISingle(stdout, stderr, cfg.JSON, encoded, result,
		func(value adminResetPasswordResult) string { return value.User.ID },
		writeAdminResetPasswordSummary,
	)
}

func writeAdminResetPasswordSummary(output io.Writer, value adminResetPasswordResult) {
	fmt.Fprintf(output, "administrator password reset: %s\n", terminalSafeText(value.User.Email))
	fmt.Fprintf(output, "user id:           %s\n", terminalSafeText(value.User.ID))
	fmt.Fprintf(output, "sessions revoked:  %d\n", value.SessionsRevoked)
	if !value.TOTPReenrolled {
		fmt.Fprintln(output, "authenticator:     unchanged (--keep-totp)")
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Sign in with `midnight login` using the new password and the existing authenticator.")
		return
	}
	fmt.Fprintln(output, "authenticator:     re-enrolled; the previous secret no longer works")
	fmt.Fprintln(output)
	fmt.Fprintf(output, "TOTP enrollment URL: %s\n", terminalSafeText(value.OTPAuthURI))
	fmt.Fprintf(output, "TOTP secret (Base32): %s\n", terminalSafeText(value.TOTPSecret))
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Save this now: the TOTP secret is not shown again and cannot be recovered.")
	fmt.Fprintln(output, "Replace the old entry in an authenticator app, then sign in with `midnight login`.")
}

// writeAdminResetPasswordFailure turns the route's typed refusals into the
// answer an operator can act on, rather than a bare status line.
func writeAdminResetPasswordFailure(stderr io.Writer, err error) {
	var status *cliAPIStatusFailure
	if errors.As(err, &status) {
		switch status.Code {
		case string(failurecodes.AuthOperatorRecoveryRequired):
			fmt.Fprintln(stderr, "midnight admin reset-password: this command requires the installation operator recovery credential")
			fmt.Fprintln(stderr, "Run it on the Midnight host with MIDNIGHT_API_TOKEN set to $MIDNIGHT_API_BEARER_TOKEN; a signed-in session cannot reset an administrator credential.")
			return
		case string(failurecodes.ResourceNotFound):
			fmt.Fprintln(stderr, "midnight admin reset-password: no enabled administrator account matches that address")
			fmt.Fprintln(stderr, "Check the address, and note that disabled or deleted accounts are not recoverable this way.")
			return
		}
	}
	fmt.Fprintf(stderr, "midnight admin reset-password: %s\n", err)
}

func newAdminResetPasswordIdempotencyKey() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "admin-reset-password-" + base64.RawURLEncoding.EncodeToString(buf), nil
}
