// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/midnight/midnight/internal/auth"
	"github.com/midnight/midnight/internal/auth/oauth"
	"github.com/midnight/midnight/internal/cliconfig"
	"github.com/midnight/midnight/internal/failurecodes"
	"golang.org/x/term"
)

// browserOpener is the hook unit tests use to assert the explicitly
// operator-scoped compatibility flow drove the browser. Human login uses the
// canonical durable session endpoint instead.
var browserOpener = openBrowser

var (
	loginLinePrompt = func(label string, output io.Writer) (string, error) {
		fmt.Fprint(output, label)
		value, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimSpace(value), err
	}
	loginSecretPrompt = func(label string, output io.Writer) (string, error) {
		fd := int(os.Stdin.Fd())
		if !term.IsTerminal(fd) {
			return "", errors.New("interactive terminal required for secret input")
		}
		fmt.Fprint(output, label)
		value, err := term.ReadPassword(fd)
		fmt.Fprintln(output)
		return strings.TrimSpace(string(value)), err
	}
)

func runLogin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineLoginFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 64
	}
	base, err := cliconfig.ResolveAPIBase(*f.apiBase, "")
	if err != nil {
		if errors.Is(err, cliconfig.ErrInvalidAPIBase) {
			fmt.Fprintln(stderr, "midnight login: API base requires HTTPS except for loopback hosts")
		} else {
			fmt.Fprintf(stderr, "midnight login: %s\n", err)
		}
		if errors.Is(err, cliconfig.ErrNoAPIBase) || errors.Is(err, cliconfig.ErrInvalidAPIBase) {
			return 64
		}
		return 1
	}
	if err := validateHumanLoginBase(base); err != nil {
		fmt.Fprintf(stderr, "midnight login: %s\n", err)
		return 64
	}
	email := strings.TrimSpace(*f.emailFlag)
	if email == "" {
		email, err = loginLinePrompt("Email: ", stderr)
		if err != nil {
			fmt.Fprintf(stderr, "midnight login: read email: %s\n", err)
			return 1
		}
	}
	if email == "" {
		fmt.Fprintln(stderr, "midnight login: email is required")
		return 64
	}
	password, err := loginSecretPrompt("Password: ", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "midnight login: read password: %s\n", err)
		return 1
	}
	if password == "" {
		fmt.Fprintln(stderr, "midnight login: password is required")
		return 64
	}
	result, err := createHumanCLISession(base, email, password, "")
	var authErr *humanAuthError
	if errors.As(err, &authErr) && authErr.Code == failurecodes.MFARequired {
		totpCode, promptErr := loginSecretPrompt("TOTP code: ", stderr)
		if promptErr != nil {
			fmt.Fprintf(stderr, "midnight login: read TOTP code: %s\n", promptErr)
			return 1
		}
		if totpCode == "" {
			fmt.Fprintln(stderr, "midnight login: TOTP code is required")
			return 64
		}
		result, err = createHumanCLISession(base, email, password, totpCode)
	}
	if err != nil {
		fmt.Fprintf(stderr, "midnight login: %s\n", err)
		return 1
	}
	if err := cliconfig.SetHumanSession(base, cliconfig.HumanSessionCredential{
		Token: result.AccessToken,
		HumanSessionMetadata: cliconfig.HumanSessionMetadata{
			SessionID:   result.Session.ID,
			UserID:      result.User.ID,
			Email:       result.User.Email,
			DisplayName: result.User.DisplayName,
			ExpiresAt:   result.Session.ExpiresAt,
		},
	}); err != nil {
		fmt.Fprintf(stderr, "midnight login: save human session: %s\n", err)
		return 1
	}
	displayEmail := terminalSafeText(result.User.Email)
	identity := displayEmail
	if displayName := terminalSafeText(result.User.DisplayName); displayName != "" {
		identity = displayName + " <" + displayEmail + ">"
	}
	fmt.Fprintf(stdout, "midnight: logged in as %s to %s (human session)\n", identity, terminalSafeText(base))
	return 0
}

// runAdminAuthorizeCLI retains the original PKCE shim under an explicitly
// operator-scoped name. It returns the installation-wide recovery credential;
// it is not a human login.

type loginFlags struct {
	apiBase   *string
	emailFlag *string
}

func defineLoginFlags(fs *flag.FlagSet) loginFlags {
	return loginFlags{
		apiBase:   cliAPIBaseFlag(fs, "Midnight API base URL (overrides environment and stored server)"),
		emailFlag: fs.String("email", "", "human account email (prompted when omitted)"),
	}
}

type adminAuthorizeCLIFlags struct {
	apiBase     *string
	browserless *bool
	timeout     *time.Duration
}

func defineAdminAuthorizeCLIFlags(fs *flag.FlagSet) adminAuthorizeCLIFlags {
	return adminAuthorizeCLIFlags{
		apiBase:     cliAPIBaseFlag(fs, "Midnight API base URL (overrides environment and stored server)"),
		browserless: fs.Bool("browserless", false, "print the authorize URL instead of opening the browser"),
		timeout:     fs.Duration("timeout", 5*time.Minute, "how long to wait for the browser callback"),
	}
}

func runAdminAuthorizeCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight admin authorize-cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineAdminAuthorizeCLIFlags(fs)
	apiBase, browserless, timeout := f.apiBase, f.browserless, f.timeout
	if err := fs.Parse(args); err != nil {
		return 64
	}
	base, err := cliconfig.ResolveAPIBase(*apiBase, "")
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin authorize-cli: %s\n", err)
		if errors.Is(err, cliconfig.ErrNoAPIBase) {
			return 64
		}
		return 1
	}

	verifier, err := oauth.NewCodeVerifier()
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin authorize-cli: %s\n", err)
		return 1
	}
	state, err := oauth.NewState()
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin authorize-cli: %s\n", err)
		return 1
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin authorize-cli: open local listener: %s\n", err)
		return 1
	}
	defer listener.Close()
	callbackURL := fmt.Sprintf("http://%s/callback", listener.Addr().String())

	authorizeURL, err := buildAuthorizeURL(base, callbackURL, verifier, state)
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin authorize-cli: %s\n", err)
		return 1
	}

	type callbackResult struct {
		Code  string
		State string
		Err   error
	}
	resultCh := make(chan callbackResult, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		code := query.Get("code")
		gotState := query.Get("state")
		errParam := query.Get("error")
		if errParam != "" {
			safeError := terminalSafeText(errParam)
			http.Error(w, "authorization failed: "+safeError, http.StatusBadRequest)
			resultCh <- callbackResult{Err: fmt.Errorf("authorization failed: %s", safeError)}
			return
		}
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resultCh <- callbackResult{Err: errors.New("authorization missing code parameter")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body style="font-family:system-ui;text-align:center;margin-top:60px;">
<h2>Midnight operator authorization complete</h2>
<p>You can close this tab and return to the terminal.</p>
</body></html>`))
		resultCh <- callbackResult{Code: code, State: gotState}
	})}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Fprintln(stdout, "midnight: opening operator recovery authorization in a browser:")
	fmt.Fprintln(stdout, "  "+authorizeURL)
	if *browserless {
		fmt.Fprintln(stdout, "midnight: paste the authorize URL into a browser and enter the operator recovery token.")
	} else {
		if err := browserOpener(authorizeURL); err != nil {
			fmt.Fprintf(stderr, "midnight admin authorize-cli: open browser: %s\n", err)
			fmt.Fprintln(stdout, "midnight: paste the URL above into a browser and enter the operator recovery token.")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var got callbackResult
	select {
	case <-ctx.Done():
		fmt.Fprintln(stderr, "midnight admin authorize-cli: timed out waiting for browser callback")
		return 1
	case got = <-resultCh:
	}
	if got.Err != nil {
		fmt.Fprintf(stderr, "midnight admin authorize-cli: %s\n", got.Err)
		return 1
	}
	if got.State != state {
		fmt.Fprintln(stderr, "midnight admin authorize-cli: state mismatch; possible CSRF — refusing")
		return 1
	}

	token, err := exchangeCodeForToken(ctx, base, got.Code, verifier, callbackURL)
	if err != nil {
		fmt.Fprintf(stderr, "midnight admin authorize-cli: token exchange: %s\n", err)
		return 1
	}
	if err := cliconfig.SetOperatorRecoveryToken(base, token); err != nil {
		fmt.Fprintf(stderr, "midnight admin authorize-cli: save operator recovery credential: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "midnight: stored operator recovery credential for %s; this is not a human login\n", base)
	return 0
}

type logoutFlags struct {
	apiBase   *string
	localOnly *bool
}

func defineLogoutFlags(fs *flag.FlagSet) logoutFlags {
	return logoutFlags{
		apiBase:   cliAPIBaseFlag(fs, "Midnight API base URL (overrides environment and stored server)"),
		localOnly: fs.Bool("local-only", false, "forget the local human session without revoking it on the server"),
	}
}

func runLogout(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight logout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineLogoutFlags(fs)
	apiBase, localOnly := f.apiBase, f.localOnly
	if err := fs.Parse(args); err != nil {
		return 64
	}
	base, err := cliconfig.ResolveAPIBase(*apiBase, "")
	if err != nil {
		fmt.Fprintf(stderr, "midnight logout: %s\n", err)
		if errors.Is(err, cliconfig.ErrNoAPIBase) {
			return 64
		}
		return 1
	}
	session, err := cliconfig.HumanSession(base)
	if errors.Is(err, cliconfig.ErrNoHumanSession) {
		fmt.Fprintf(stdout, "midnight: no stored human session for %s; operator recovery credentials were not changed\n", base)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "midnight logout: load human session: %s\n", err)
		return 1
	}
	if *localOnly {
		if err := cliconfig.DeleteHumanSession(base); err != nil {
			fmt.Fprintf(stderr, "midnight logout: %s\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "midnight: forgot local human session for %s without server revocation\n", base)
		return 0
	}
	// Resolve the session ID from the authenticated server response even when
	// metadata is stored locally. Trusting stale/tampered local metadata could
	// revoke a different session and then forget the still-active current one.
	me, identityStatus, err := fetchHumanIdentity(base, session.Token)
	if err != nil {
		if identityStatus == http.StatusUnauthorized {
			if deleteErr := cliconfig.DeleteHumanSession(base); deleteErr != nil {
				fmt.Fprintf(stderr, "midnight logout: clear rejected local session: %s\n", deleteErr)
				return 1
			}
			fmt.Fprintf(stdout, "midnight: cleared expired or revoked human session for %s\n", base)
			return 0
		}
		fmt.Fprintf(stderr, "midnight logout: could not identify the remote session; local credential preserved: %s\n", err)
		return 1
	}
	status, err := revokeHumanSession(base, session.Token, me.Session.ID)
	if err != nil {
		fmt.Fprintf(stderr, "midnight logout: remote revocation failed; local credential preserved: %s\n", err)
		return 1
	}
	switch status {
	case http.StatusNoContent, http.StatusUnauthorized, http.StatusNotFound:
	default:
		fmt.Fprintf(stderr, "midnight logout: remote revocation returned status %d; local credential preserved\n", status)
		return 1
	}
	if err := cliconfig.DeleteHumanSession(base); err != nil {
		fmt.Fprintf(stderr, "midnight logout: remote session revoked but local cleanup failed: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "midnight: logged out human session for %s; operator recovery credentials were not changed\n", base)
	return 0
}

type whoamiFlags struct {
	apiBase       *string
	explicitToken *string
}

func defineWhoamiFlags(fs *flag.FlagSet) whoamiFlags {
	return whoamiFlags{
		apiBase:       cliAPIBaseFlag(fs, "Midnight API base URL (overrides environment and stored server)"),
		explicitToken: fs.String("token", "", "Midnight API bearer token (overrides environment and stored credential)"),
	}
}

func runWhoami(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("midnight whoami", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := defineWhoamiFlags(fs)
	apiBase, explicitToken := f.apiBase, f.explicitToken
	if err := fs.Parse(args); err != nil {
		return 64
	}
	credentials, err := cliconfig.ResolveCredentials(cliconfig.ResolveOptions{
		APIBase: *apiBase,
		Token:   *explicitToken,
	})
	if err != nil {
		fmt.Fprintf(stderr, "midnight whoami: %s\n", err)
		if errors.Is(err, cliconfig.ErrNoAPIBase) {
			return 64
		}
		return 1
	}
	base := credentials.APIBase
	token := credentials.Token
	if credentials.Kind != cliconfig.CredentialKindHumanSession {
		classification := "non-human explicit or environment credential"
		if credentials.Kind == cliconfig.CredentialKindOperatorRecovery {
			classification = "stored operator recovery credential"
		}
		// Non-human credentials carry no identity to display, but the caller
		// still needs to know whether the server accepts them. Verify against
		// a read-only tenant endpoint rather than the human-only /auth/me.
		if err := verifyBearerCredential(base, token); err != nil {
			outcome := "could not be verified"
			var statusFailure *cliAPIStatusFailure
			if errors.As(err, &statusFailure) {
				outcome = "was rejected"
			}
			fmt.Fprintf(stderr, "midnight whoami: %s configured for %s %s: %s\n", classification, base, outcome, err)
			return 1
		}
		fmt.Fprintf(stdout, "midnight: %s configured for %s; accepted by the server (no human identity is claimed; the credential was not sent to /auth/me)\n",
			classification, base)
		return 0
	}
	me, _, err := fetchHumanIdentity(base, token)
	if err != nil {
		fmt.Fprintf(stderr, "midnight whoami: %s\n", err)
		return 1
	}
	name := terminalSafeText(me.Principal.DisplayName)
	if name == "" {
		name = terminalSafeText(me.Principal.Email)
	} else if email := terminalSafeText(me.Principal.Email); email != "" {
		name += " <" + email + ">"
	}
	fmt.Fprintf(stdout, "midnight: %s\n", name)
	fmt.Fprintf(stdout, "  user_id: %s\n  session_id: %s\n  api_base: %s\n  expires_at: %s\n",
		terminalSafeText(me.Principal.UserID),
		terminalSafeText(me.Session.ID),
		terminalSafeText(base),
		me.Session.ExpiresAt.UTC().Format(time.RFC3339),
	)
	return 0
}

type humanLoginResult struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	User        struct {
		ID          string `json:"id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	} `json:"user"`
	Session struct {
		ID        string    `json:"id"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"session"`
}

type humanIdentityResponse struct {
	Principal struct {
		UserID      string `json:"user_id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	} `json:"principal"`
	Session struct {
		ID        string    `json:"id"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"session"`
}

type humanAuthError struct {
	Status  int
	Code    failurecodes.Code
	Message string
}

func (e *humanAuthError) Error() string {
	code := terminalSafeText(string(e.Code))
	message := terminalSafeText(e.Message)
	if code != "" {
		return fmt.Sprintf("%s: %s", code, message)
	}
	if message != "" {
		return message
	}
	return fmt.Sprintf("server returned status %d", e.Status)
}

func createHumanCLISession(apiBase, email, password, totpCode string) (humanLoginResult, error) {
	if err := validateHumanLoginBase(apiBase); err != nil {
		return humanLoginResult{}, err
	}
	body, err := json.Marshal(map[string]string{
		"email": email, "password": password, "totp_code": totpCode, "client_kind": "cli",
	})
	if err != nil {
		return humanLoginResult{}, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, apiBase+"/api/v1alpha/auth/sessions", strings.NewReader(string(body)))
	if err != nil {
		return humanLoginResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, responseBody, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		return humanLoginResult{}, err
	}
	if res.StatusCode != http.StatusCreated {
		return humanLoginResult{}, decodeHumanAuthError(res.StatusCode, responseBody)
	}
	var result humanLoginResult
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return humanLoginResult{}, fmt.Errorf("decode human session: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(result.TokenType), "Bearer") {
		return humanLoginResult{}, errors.New("server returned a human session with an invalid token_type")
	}
	token, err := auth.ParseBearerToken("Bearer " + strings.TrimSpace(result.AccessToken))
	if err != nil || token.Class != auth.TokenClassUser {
		return humanLoginResult{}, errors.New("server returned a non-human credential from the human session endpoint")
	}
	if result.Session.ID == "" || result.User.ID == "" || strings.TrimSpace(result.User.Email) == "" {
		return humanLoginResult{}, errors.New("server returned an incomplete human session")
	}
	if result.Session.ExpiresAt.IsZero() || !result.Session.ExpiresAt.After(time.Now().UTC()) {
		return humanLoginResult{}, errors.New("server returned an already-expired human session")
	}
	return result, nil
}

// verifyBearerCredential sends the credential to GET /api/v1alpha/projects,
// a read-only endpoint every accepted tenant credential can call, and reports
// the server's decision. The response body is discarded.
func verifyBearerCredential(apiBase, token string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, apiBase+"/api/v1alpha/projects", nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	req.Header.Set("Accept", "application/json")
	res, body, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiStatusError(http.MethodGet, req.URL.RequestURI(), res.Status, body)
	}
	return nil
}

func fetchHumanIdentity(apiBase, token string) (humanIdentityResponse, int, error) {
	if err := validateHumanLoginBase(apiBase); err != nil {
		return humanIdentityResponse{}, 0, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, apiBase+"/api/v1alpha/auth/me", nil)
	if err != nil {
		return humanIdentityResponse{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
	res, body, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		return humanIdentityResponse{}, status, err
	}
	if res.StatusCode != http.StatusOK {
		return humanIdentityResponse{}, res.StatusCode, decodeHumanAuthError(res.StatusCode, body)
	}
	var response humanIdentityResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return humanIdentityResponse{}, res.StatusCode, fmt.Errorf("decode human identity: %w", err)
	}
	if response.Principal.UserID == "" || response.Session.ID == "" {
		return humanIdentityResponse{}, res.StatusCode, errors.New("server returned an incomplete human identity")
	}
	return response, res.StatusCode, nil
}

func revokeHumanSession(apiBase, token, sessionID string) (int, error) {
	if err := validateHumanLoginBase(apiBase); err != nil {
		return 0, err
	}
	target := apiBase + "/api/v1alpha/auth/sessions/" + url.PathEscape(strings.TrimSpace(sessionID))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	res, _, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		return 0, err
	}
	return res.StatusCode, nil
}

func decodeHumanAuthError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Code    failurecodes.Code `json:"code"`
			Message string            `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && (envelope.Error.Code != "" || envelope.Error.Message != "") {
		code := failurecodes.Code(terminalSafeText(string(envelope.Error.Code)))
		if len(code) > 128 {
			code = ""
		}
		return &humanAuthError{Status: status, Code: code, Message: boundedHumanAuthMessage(envelope.Error.Message)}
	}
	message := boundedHumanAuthMessage(string(body))
	if message == "" {
		message = http.StatusText(status)
	}
	return &humanAuthError{Status: status, Message: message}
}

func boundedHumanAuthMessage(message string) string {
	const maxBytes = 4 << 10
	message = terminalSafeText(message)
	if len(message) <= maxBytes {
		return message
	}
	return strings.TrimSpace(strings.ToValidUTF8(message[:maxBytes], "\uFFFD")) + "…"
}

// terminalSafeText converts an untrusted server or identity value into one
// printable line. CSI/OSC/control-string payloads are removed as units so an
// escape cannot reach the terminal and its hidden payload does not become
// misleading visible text. Remaining C0/C1 controls become ordinary spacing.
// Call this only at the rendering boundary; credential metadata stays raw.
func terminalSafeText(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	var output strings.Builder
	output.Grow(len(value))
	for index := 0; index < len(value); {
		current, size := utf8.DecodeRuneInString(value[index:])
		switch current {
		case '\x1b':
			index += size
			if index >= len(value) {
				continue
			}
			next, nextSize := utf8.DecodeRuneInString(value[index:])
			index += nextSize
			switch next {
			case '[':
				index = consumeTerminalCSI(value, index)
			case ']', 'P', 'X', '^', '_':
				index = consumeTerminalControlString(value, index)
			}
			continue
		case '\u009b':
			index = consumeTerminalCSI(value, index+size)
			continue
		case '\u0090', '\u0098', '\u009d', '\u009e', '\u009f':
			index = consumeTerminalControlString(value, index+size)
			continue
		}
		if unicode.IsControl(current) {
			output.WriteByte(' ')
		} else {
			output.WriteRune(current)
		}
		index += size
	}
	return strings.Join(strings.Fields(output.String()), " ")
}

func consumeTerminalCSI(value string, index int) int {
	for index < len(value) {
		current, size := utf8.DecodeRuneInString(value[index:])
		index += size
		// ANSI CSI final bytes end the sequence. If the input is malformed or
		// unterminated, consuming through EOF is the fail-closed rendering.
		if current >= 0x40 && current <= 0x7e {
			return index
		}
	}
	return index
}

func consumeTerminalControlString(value string, index int) int {
	for index < len(value) {
		current, size := utf8.DecodeRuneInString(value[index:])
		index += size
		switch current {
		case '\a', '\u009c':
			return index
		case '\x1b':
			if index < len(value) && value[index] == '\\' {
				return index + 1
			}
		}
	}
	return index
}

func validateHumanLoginBase(apiBase string) error {
	parsed, err := url.ParseRequestURI(apiBase)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid API base URL %q", apiBase)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("API base URL must not contain a path")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		ip := net.ParseIP(parsed.Hostname())
		if ip != nil && ip.IsLoopback() {
			return nil
		}
		return errors.New("human login requires HTTPS; plain HTTP is allowed only for a loopback IP")
	default:
		return errors.New("human login API base must use https:// or loopback http://")
	}
}

func buildAuthorizeURL(apiBase, redirectURI, verifier, state string) (string, error) {
	u, err := url.Parse(apiBase + "/auth/oauth/authorize")
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", oauth.DefaultClientID)
	q.Set("code_challenge", oauth.ComputeS256Challenge(verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("redirect_uri", redirectURI)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func exchangeCodeForToken(ctx context.Context, apiBase, code, verifier, redirectURI string) (string, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"code_verifier": verifier,
		"redirect_uri":  redirectURI,
		"client_id":     oauth.DefaultClientID,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/auth/oauth/token", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, responseBody, err := executeCLIRequest(authHTTPClient, req, maxCLIResponseBytes)
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		message := boundedHumanAuthMessage(string(responseBody))
		if message == "" {
			message = http.StatusText(res.StatusCode)
		}
		return "", fmt.Errorf("status %s: %s", res.Status, message)
	}
	var envelope struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return "", err
	}
	if strings.TrimSpace(envelope.AccessToken) == "" {
		return "", errors.New("token endpoint returned an empty access_token")
	}
	return envelope.AccessToken, nil
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}
