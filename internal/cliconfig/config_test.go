// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package cliconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func clearCredentialEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvAPIBase, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvBearerToken, "")
	t.Setenv(EnvAPIToken, "")
}

func TestSaveAndLoadGlobalRoundTrip(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	if err := SetToken("https://api.midnight.test", "midn_user_alpha"); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	tok, err := Token("https://api.midnight.test/")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "midn_user_alpha" {
		t.Fatalf("Token = %q, want midn_user_alpha", tok)
	}
}

func TestV1CredentialMigrationClassifiesHumanAndOperatorTokens(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	legacy := []byte(`{
		"version": 1,
		"servers": {
			"https://human.test": {"api_base":"https://human.test","token":"midn_user_alpha"},
			"https://operator.test": {"api_base":"https://operator.test","token":"legacy-operator-token"}
		}
	}`)
	if err := os.WriteFile(filepath.Join(configHome, GlobalConfigFileName), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Servers["https://human.test"].CredentialKind != CredentialKindHumanSession {
		t.Fatalf("human kind = %q", cfg.Servers["https://human.test"].CredentialKind)
	}
	if cfg.Servers["https://operator.test"].CredentialKind != CredentialKindOperatorRecovery {
		t.Fatalf("operator kind = %q", cfg.Servers["https://operator.test"].CredentialKind)
	}
	human, err := ResolveCredentials(ResolveOptions{APIBase: "https://human.test"})
	if err != nil || human.Kind != CredentialKindHumanSession || human.Source != CredentialSourceStoredSession {
		t.Fatalf("human credentials = %#v err=%v", human, err)
	}
	operator, err := ResolveCredentials(ResolveOptions{APIBase: "https://operator.test", OperatorRecovery: true})
	if err != nil || operator.Token != "legacy-operator-token" ||
		operator.Kind != CredentialKindOperatorRecovery || operator.Source != CredentialSourceStoredRecovery {
		t.Fatalf("operator credentials = %#v err=%v", operator, err)
	}
}

func TestHumanSessionCoexistsWithRecoveryWithoutSilentFallback(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	const base = "https://api.midnight.test"
	if err := SetOperatorRecoveryToken(base, "operator-recovery-token"); err != nil {
		t.Fatal(err)
	}
	if err := SetHumanSession(base, HumanSessionCredential{
		Token: "midn_user_alpha",
		HumanSessionMetadata: HumanSessionMetadata{
			SessionID: "ses_cli", UserID: "usr_alice", Email: "alice@example.com",
		},
	}); err != nil {
		t.Fatal(err)
	}
	defaultCredentials, err := ResolveCredentials(ResolveOptions{APIBase: base})
	if err != nil || defaultCredentials.Token != "midn_user_alpha" ||
		defaultCredentials.Kind != CredentialKindHumanSession {
		t.Fatalf("default credentials = %#v err=%v", defaultCredentials, err)
	}
	operatorCredentials, err := ResolveCredentials(ResolveOptions{APIBase: base, OperatorRecovery: true})
	if err != nil || operatorCredentials.Token != "operator-recovery-token" {
		t.Fatalf("operator credentials = %#v err=%v", operatorCredentials, err)
	}
	if err := DeleteHumanSession(base); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCredentials(ResolveOptions{APIBase: base}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("default resolver silently fell back to recovery: %v", err)
	}
	operatorCredentials, err = ResolveCredentials(ResolveOptions{APIBase: base, OperatorRecovery: true})
	if err != nil || operatorCredentials.Token != "operator-recovery-token" {
		t.Fatalf("recovery was not preserved: %#v err=%v", operatorCredentials, err)
	}
}

func TestSetOperatorRecoveryDoesNotReplaceActiveHumanSession(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	const base = "https://api.midnight.test"
	if err := SetHumanSession(base, HumanSessionCredential{
		Token: "midn_user_alpha",
		HumanSessionMetadata: HumanSessionMetadata{
			SessionID: "ses_cli", UserID: "usr_alice",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := SetOperatorRecoveryToken(base, "operator-recovery-token"); err != nil {
		t.Fatal(err)
	}
	session, err := HumanSession(base)
	if err != nil || session.Token != "midn_user_alpha" {
		t.Fatalf("human session = %#v err=%v", session, err)
	}
	recovery, err := OperatorRecoveryToken(base)
	if err != nil || recovery != "operator-recovery-token" {
		t.Fatalf("operator recovery = %q err=%v", recovery, err)
	}
}

func TestLegacySetTokenPreservesSplitRecoveryAndOnlyMatchingHumanMetadata(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	const base = "https://api.midnight.test"
	if err := SetOperatorRecoveryToken(base, "operator-recovery-token"); err != nil {
		t.Fatal(err)
	}
	if err := SetHumanSession(base, HumanSessionCredential{
		Token: "midn_user_alpha",
		HumanSessionMetadata: HumanSessionMetadata{
			SessionID: "ses_alpha", UserID: "usr_alice", Email: "alice@example.com",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := SetToken(base, "midn_user_alpha"); err != nil {
		t.Fatal(err)
	}
	session, err := HumanSession(base)
	if err != nil || session.SessionID != "ses_alpha" {
		t.Fatalf("matching legacy write lost metadata: session=%#v err=%v", session, err)
	}
	recovery, err := OperatorRecoveryToken(base)
	if err != nil || recovery != "operator-recovery-token" {
		t.Fatalf("matching legacy write lost recovery: recovery=%q err=%v", recovery, err)
	}

	if err := SetToken(base, "midn_user_bravo"); err != nil {
		t.Fatal(err)
	}
	session, err = HumanSession(base)
	if err != nil || session.Token != "midn_user_bravo" {
		t.Fatalf("replacement session=%#v err=%v", session, err)
	}
	if session.HumanSessionMetadata != (HumanSessionMetadata{}) {
		t.Fatalf("replacement inherited stale metadata: %#v", session.HumanSessionMetadata)
	}
	recovery, err = OperatorRecoveryToken(base)
	if err != nil || recovery != "operator-recovery-token" {
		t.Fatalf("replacement legacy write lost recovery: recovery=%q err=%v", recovery, err)
	}
}

func TestFutureGlobalConfigVersionIsRefusedWithoutRewrite(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	path := filepath.Join(configHome, GlobalConfigFileName)
	future := []byte(`{
		"version": 4,
		"servers": {
			"https://api.midnight.test": {
				"api_base": "https://api.midnight.test",
				"token": "midn_user_alpha",
				"future_credential": "must-survive"
			}
		},
		"future_root": {"secret": "must-also-survive"}
	}`)
	if err := os.WriteFile(path, future, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadGlobal(); !errors.Is(err, ErrUnsupportedGlobalConfigVersion) {
		t.Fatalf("LoadGlobal err=%v, want ErrUnsupportedGlobalConfigVersion", err)
	}
	if err := SetToken("https://api.midnight.test", "replacement"); !errors.Is(err, ErrUnsupportedGlobalConfigVersion) {
		t.Fatalf("SetToken err=%v, want ErrUnsupportedGlobalConfigVersion", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(future) {
		t.Fatalf("future config was rewritten:\n%s", after)
	}
	if err := SaveGlobal(Global{Version: CurrentVersion + 1}); !errors.Is(err, ErrUnsupportedGlobalConfigVersion) {
		t.Fatalf("SaveGlobal err=%v, want ErrUnsupportedGlobalConfigVersion", err)
	}
}

func TestPerAPIBaseTokenIsolation(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	if err := SetToken("https://prod.midnight.test", "prod-token"); err != nil {
		t.Fatalf("SetToken prod: %v", err)
	}
	if err := SetToken("https://staging.midnight.test", "staging-token"); err != nil {
		t.Fatalf("SetToken staging: %v", err)
	}
	prod, err := Token("https://prod.midnight.test")
	if err != nil || prod != "prod-token" {
		t.Fatalf("prod token = %q err=%v", prod, err)
	}
	staging, err := Token("https://staging.midnight.test")
	if err != nil || staging != "staging-token" {
		t.Fatalf("staging token = %q err=%v", staging, err)
	}
	if _, err := Token("https://unknown.midnight.test"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("unknown token err = %v, want ErrNoToken", err)
	}
}

func TestEnvTokenShortCircuits(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(EnvBearerToken, "env-token")
	got, err := Token("https://any.midnight.test")
	if err != nil || got != "env-token" {
		t.Fatalf("Token = %q err=%v, want env-token", got, err)
	}
}

func TestResolveAPIBasePrecedence(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(EnvAPIBase, "https://env.midnight.test/")
	t.Setenv(EnvBaseURL, "https://legacy.midnight.test/")
	if err := SetToken("https://stored.midnight.test", "stored-token"); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveAPIBase(
		"https://explicit.midnight.test/",
		"https://project.midnight.test/",
	)
	if err != nil || got != "https://explicit.midnight.test" {
		t.Fatalf("explicit API base = %q err=%v", got, err)
	}

	got, err = ResolveAPIBase("", "https://project.midnight.test/")
	if err != nil || got != "https://env.midnight.test" {
		t.Fatalf("environment API base = %q err=%v", got, err)
	}

	t.Setenv(EnvAPIBase, "")
	got, err = ResolveAPIBase("", "https://project.midnight.test/")
	if err != nil || got != "https://legacy.midnight.test" {
		t.Fatalf("legacy environment API base = %q err=%v", got, err)
	}

	t.Setenv(EnvBaseURL, "")
	got, err = ResolveAPIBase("", "https://project.midnight.test/")
	if err != nil || got != "https://project.midnight.test" {
		t.Fatalf("project API base = %q err=%v", got, err)
	}

	got, err = ResolveAPIBase("", "")
	if err != nil || got != "https://stored.midnight.test" {
		t.Fatalf("stored API base = %q err=%v", got, err)
	}
}

func TestValidateAPIBase(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "https origin", input: "  HTTPS://API.MIDNIGHT.TEST/  ", want: "https://api.midnight.test"},
		{name: "https custom port", input: "https://api.midnight.test:8443", want: "https://api.midnight.test:8443"},
		{name: "ipv4 loopback http", input: "http://127.0.0.1:8080/", want: "http://127.0.0.1:8080"},
		{name: "ipv6 loopback http", input: "http://[::1]:8080", want: "http://[::1]:8080"},
		{name: "localhost http", input: "http://LOCALHOST:8080", want: "http://localhost:8080"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ValidateAPIBase(test.input)
			if err != nil || got != test.want {
				t.Fatalf("ValidateAPIBase() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestValidateAPIBaseRejectsHostileAndAmbiguousURLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "relative", input: "api.midnight.test"},
		{name: "scheme relative", input: "//api.midnight.test"},
		{name: "opaque", input: "https:api.midnight.test"},
		{name: "missing host", input: "https:///api"},
		{name: "unsupported scheme", input: "file:///tmp/midnight.sock"},
		{name: "userinfo", input: "https://alice:super-secret@api.midnight.test"},
		{name: "path prefix", input: "https://api.midnight.test/control-plane"},
		{name: "encoded path", input: "https://api.midnight.test/%2e%2e/evil"},
		{name: "query", input: "https://api.midnight.test?redirect=evil.test"},
		{name: "empty query", input: "https://api.midnight.test?"},
		{name: "fragment", input: "https://api.midnight.test/#token"},
		{name: "empty port", input: "https://api.midnight.test:"},
		{name: "invalid port", input: "https://api.midnight.test:not-a-port"},
		{name: "remote cleartext", input: "http://api.midnight.test"},
		{name: "unspecified ipv4 cleartext", input: "http://0.0.0.0:8080"},
		{name: "unspecified ipv6 cleartext", input: "http://[::]:8080"},
		{name: "control character", input: "https://api.midnight.test\n.evil.test"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ValidateAPIBase(test.input)
			if got != "" || !errors.Is(err, ErrInvalidAPIBase) {
				t.Fatalf("ValidateAPIBase(%q) = %q, %v; want ErrInvalidAPIBase", test.input, got, err)
			}
			if strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("validation error disclosed URL userinfo: %q", err)
			}
		})
	}
}

func TestStrictAPIBaseValidationAppliesToCredentialStorageAndLookup(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	for name, operation := range map[string]func() error{
		"set token": func() error { return SetToken("http://attacker.test", "secret") },
		"set human session": func() error {
			return SetHumanSession("https://attacker.test/path", HumanSessionCredential{Token: "midn_user_secret"})
		},
		"set recovery": func() error { return SetOperatorRecoveryToken("ftp://attacker.test", "secret") },
		"resolve token": func() error {
			_, err := ResolveToken("https://alice:secret@attacker.test", "explicit-secret")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, ErrInvalidAPIBase) {
				t.Fatalf("error = %v, want ErrInvalidAPIBase", err)
			}
		})
	}
}

func TestResolveCredentialsDoesNotPairEnvironmentTokenWithProjectEndpoint(t *testing.T) {
	for _, envName := range []string{EnvBearerToken, EnvAPIToken} {
		t.Run(envName, func(t *testing.T) {
			t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
			clearCredentialEnv(t)
			t.Setenv(envName, "ambient-ci-secret")
			_, err := ResolveCredentials(ResolveOptions{
				ProjectAPIBase: "https://repository-controlled.test",
			})
			if !errors.Is(err, ErrUnsafeCredentialMix) {
				t.Fatalf("ResolveCredentials error = %v, want ErrUnsafeCredentialMix", err)
			}
			if strings.Contains(err.Error(), "ambient-ci-secret") {
				t.Fatalf("resolution error disclosed token: %q", err)
			}
		})
	}
}

func TestResolveCredentialsPreservesTrustedProvenancePairings(t *testing.T) {
	tests := []struct {
		name            string
		configure       func(*testing.T)
		options         ResolveOptions
		wantBase        string
		wantBaseSource  APIBaseSource
		wantToken       string
		wantTokenSource CredentialSource
	}{
		{
			name:      "explicit endpoint with environment token",
			configure: func(t *testing.T) { t.Setenv(EnvBearerToken, "ambient-token") },
			options: ResolveOptions{
				APIBase: "https://explicit.test", ProjectAPIBase: "https://project.test",
			},
			wantBase: "https://explicit.test", wantBaseSource: APIBaseSourceExplicit,
			wantToken: "ambient-token", wantTokenSource: CredentialSourceEnvironment,
		},
		{
			name: "environment endpoint with environment token",
			configure: func(t *testing.T) {
				t.Setenv(EnvAPIBase, "https://environment.test")
				t.Setenv(EnvBearerToken, "ambient-token")
			},
			options:  ResolveOptions{ProjectAPIBase: "https://project.test"},
			wantBase: "https://environment.test", wantBaseSource: APIBaseSourceEnvironment,
			wantToken: "ambient-token", wantTokenSource: CredentialSourceEnvironment,
		},
		{
			name:      "project endpoint with explicit token",
			configure: func(*testing.T) {},
			options: ResolveOptions{
				ProjectAPIBase: "https://project.test", Token: "explicit-token",
			},
			wantBase: "https://project.test", wantBaseSource: APIBaseSourceProject,
			wantToken: "explicit-token", wantTokenSource: CredentialSourceExplicit,
		},
		{
			name: "project endpoint with matching stored token",
			configure: func(t *testing.T) {
				if err := SetHumanSession("https://project.test", HumanSessionCredential{Token: "midn_user_stored"}); err != nil {
					t.Fatal(err)
				}
			},
			options:  ResolveOptions{ProjectAPIBase: "https://project.test"},
			wantBase: "https://project.test", wantBaseSource: APIBaseSourceProject,
			wantToken: "midn_user_stored", wantTokenSource: CredentialSourceStoredSession,
		},
		{
			name: "stored endpoint with environment token",
			configure: func(t *testing.T) {
				if err := SetHumanSession("https://stored.test", HumanSessionCredential{Token: "midn_user_stored"}); err != nil {
					t.Fatal(err)
				}
				t.Setenv(EnvBearerToken, "ambient-token")
			},
			options:  ResolveOptions{},
			wantBase: "https://stored.test", wantBaseSource: APIBaseSourceStored,
			wantToken: "ambient-token", wantTokenSource: CredentialSourceEnvironment,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
			clearCredentialEnv(t)
			test.configure(t)
			got, err := ResolveCredentials(test.options)
			if err != nil {
				t.Fatal(err)
			}
			if got.APIBase != test.wantBase || got.APIBaseSource != test.wantBaseSource ||
				got.Token != test.wantToken || got.Source != test.wantTokenSource {
				t.Fatalf("credentials = %#v", got)
			}
		})
	}
}

func TestEnvironmentTokenIsNotSilentlyPreferredOrIgnoredForProjectEndpoint(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	if err := SetHumanSession("https://project.test", HumanSessionCredential{Token: "midn_user_stored"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvBearerToken, "ambient-token")
	_, err := ResolveCredentials(ResolveOptions{ProjectAPIBase: "https://project.test"})
	if !errors.Is(err, ErrUnsafeCredentialMix) {
		t.Fatalf("ResolveCredentials error = %v, want ErrUnsafeCredentialMix", err)
	}
}

func TestInvalidHigherPrecedenceAPIBaseDoesNotFallBack(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	t.Setenv(EnvAPIBase, "http://attacker.test")
	_, err := ResolveAPIBase("", "https://safe-project.test")
	if !errors.Is(err, ErrInvalidAPIBase) {
		t.Fatalf("ResolveAPIBase error = %v, want ErrInvalidAPIBase", err)
	}
}

func TestResolveTokenPrecedence(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(EnvBearerToken, "project-token")
	t.Setenv(EnvAPIToken, "legacy-token")
	const base = "https://api.midnight.test"
	if err := SetToken(base, "stored-token"); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveToken(base, "explicit-token")
	if err != nil || got != "explicit-token" {
		t.Fatalf("explicit token = %q err=%v", got, err)
	}

	got, err = ResolveToken(base, "")
	if err != nil || got != "project-token" {
		t.Fatalf("project token = %q err=%v", got, err)
	}

	t.Setenv(EnvBearerToken, "")
	got, err = ResolveToken(base, "")
	if err != nil || got != "legacy-token" {
		t.Fatalf("legacy token = %q err=%v", got, err)
	}

	t.Setenv(EnvAPIToken, "")
	got, err = ResolveToken(base, "")
	if err != nil || got != "stored-token" {
		t.Fatalf("stored token = %q err=%v", got, err)
	}
}

func TestResolveCredentialsReturnsTypedMissingErrors(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(EnvAPIBase, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvBearerToken, "")
	t.Setenv(EnvAPIToken, "")

	if _, err := ResolveCredentials(ResolveOptions{}); !errors.Is(err, ErrNoAPIBase) {
		t.Fatalf("missing API base err = %v, want ErrNoAPIBase", err)
	}
	if _, err := ResolveCredentials(ResolveOptions{
		APIBase: "https://api.midnight.test",
	}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("missing token err = %v, want ErrNoToken", err)
	}
}

func TestResolveAPIBaseRejectsAmbiguousStoredServers(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(EnvAPIBase, "")
	t.Setenv(EnvBaseURL, "")
	if err := SetToken("https://one.midnight.test", "one-token"); err != nil {
		t.Fatal(err)
	}
	if err := SetToken("https://two.midnight.test", "two-token"); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveAPIBase("", "")
	if !errors.Is(err, ErrNoAPIBase) {
		t.Fatalf("ambiguous API base err = %v, want ErrNoAPIBase", err)
	}
	var missing *MissingCredentialError
	if !errors.As(err, &missing) || missing.Reason == "" {
		t.Fatalf("ambiguous API base err = %#v, want contextual typed error", err)
	}
}

func TestDeleteTokenIsIdempotent(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	if err := DeleteToken("https://nope.midnight.test"); err != nil {
		t.Fatalf("DeleteToken missing: %v", err)
	}
	if err := SetToken("https://a.midnight.test", "a-token"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteToken("https://a.midnight.test"); err != nil {
		t.Fatalf("DeleteToken existing: %v", err)
	}
	if _, err := Token("https://a.midnight.test"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("Token after delete err=%v", err)
	}
}

func TestProjectLinkRoundTrip(t *testing.T) {
	workdir := t.TempDir()
	link := ProjectLink{
		APIBase:         "https://api.midnight.test",
		ProjectID:       "prj_alpha",
		ProjectSlug:     "alpha",
		EnvironmentID:   "env_prod",
		EnvironmentName: "prod",
		ServiceID:       "svc_web",
		ServiceSlug:     "web",
	}
	if err := SaveProjectLink(workdir, link); err != nil {
		t.Fatalf("SaveProjectLink: %v", err)
	}
	got, err := LoadProjectLink(workdir)
	if err != nil {
		t.Fatalf("LoadProjectLink: %v", err)
	}
	if got.APIBase != link.APIBase || got.ProjectID != link.ProjectID || got.EnvironmentID != link.EnvironmentID || got.ServiceID != link.ServiceID {
		t.Fatalf("project link = %#v, want round-trip", got)
	}
	if got.Version != ProjectLinkCurrentVersion {
		t.Fatalf("version = %d, want %d", got.Version, ProjectLinkCurrentVersion)
	}
}

func TestProjectLinkRejectsUnsafeEndpoint(t *testing.T) {
	workdir := t.TempDir()
	path, err := ProjectLinkPath(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":1,"api_base":"https://alice:secret@attacker.test","project_id":"prj_1"}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProjectLink(workdir); !errors.Is(err, ErrInvalidAPIBase) {
		t.Fatalf("LoadProjectLink error = %v, want ErrInvalidAPIBase", err)
	} else if strings.Contains(err.Error(), "secret") {
		t.Fatalf("LoadProjectLink error disclosed URL userinfo: %q", err)
	}
}

func TestFutureProjectLinkVersionIsRefusedWithoutRewrite(t *testing.T) {
	workdir := t.TempDir()
	path, err := ProjectLinkPath(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	future := []byte(`{"version":2,"api_base":"https://api.midnight.test","project_id":"prj_1","future_field":"must-survive"}`)
	if err := os.WriteFile(path, future, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProjectLink(workdir); !errors.Is(err, ErrUnsupportedProjectLinkVersion) {
		t.Fatalf("LoadProjectLink error = %v, want ErrUnsupportedProjectLinkVersion", err)
	}
	if err := SaveProjectLink(workdir, ProjectLink{
		Version: 2, APIBase: "https://api.midnight.test", ProjectID: "replacement",
	}); !errors.Is(err, ErrUnsupportedProjectLinkVersion) {
		t.Fatalf("SaveProjectLink error = %v, want ErrUnsupportedProjectLinkVersion", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(future) {
		t.Fatalf("future project link was rewritten: %s", after)
	}
}

func TestGlobalConfigRejectsUnsafeStoredEndpoint(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	data := []byte(`{"version":2,"servers":{"http://attacker.test":{"token":"secret"}}}`)
	if err := os.WriteFile(filepath.Join(configHome, GlobalConfigFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGlobal(); !errors.Is(err, ErrInvalidAPIBase) {
		t.Fatalf("LoadGlobal error = %v, want ErrInvalidAPIBase", err)
	}
}

func TestProjectLinkMissingReturnsErrNotLinked(t *testing.T) {
	workdir := t.TempDir()
	if _, err := LoadProjectLink(workdir); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v, want ErrNotLinked", err)
	}
}

func TestProjectLinkDelete(t *testing.T) {
	workdir := t.TempDir()
	if err := SaveProjectLink(workdir, ProjectLink{ProjectID: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteProjectLink(workdir); err != nil {
		t.Fatalf("DeleteProjectLink: %v", err)
	}
	if _, err := LoadProjectLink(workdir); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestAtomicWriteSurvivesCrash(t *testing.T) {
	// Write a value, then write a second value, then verify the second
	// wins. The intermediate tmp file must NOT remain.
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	clearCredentialEnv(t)
	if err := SetToken("https://api.test", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := SetToken("https://api.test", "v2"); err != nil {
		t.Fatal(err)
	}
	path, err := GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), filepath.Base(path)+".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("tmp files left behind: %v", matches)
	}
	got, err := Token("https://api.test")
	if err != nil || got != "v2" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("mode = %v, want owner-only", info.Mode())
		}
	}
}

func TestUserFacingErrorsDoNotLeakPackagePrefixAndGuideSetup(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(EnvAPIBase, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvBearerToken, "")
	t.Setenv(EnvAPIToken, "")

	_, err := ResolveCredentials(ResolveOptions{})
	if !errors.Is(err, ErrNoAPIBase) {
		t.Fatalf("err = %v, want ErrNoAPIBase", err)
	}
	message := err.Error()
	if strings.Contains(message, "cliconfig") {
		t.Fatalf("message leaks the package name: %q", message)
	}
	for _, want := range []string{"No Midnight server configured", "midnight login --api-base", EnvAPIBase} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want it to contain %q", message, want)
		}
	}

	_, err = LoadProjectLink(t.TempDir())
	if !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v, want ErrNotLinked", err)
	}
	message = err.Error()
	if strings.Contains(message, "cliconfig") || !strings.Contains(message, "`midnight link`") || !strings.Contains(message, "`midnight login`") {
		t.Fatalf("not-linked message = %q, want a guiding link/login hint", message)
	}

	for _, sentinel := range []error{
		ErrNoHumanSession, ErrNoOperatorRecovery, ErrUnsupportedGlobalConfigVersion,
		ErrUnsupportedProjectLinkVersion, ErrInvalidAPIBase, ErrUnsafeCredentialMix,
		ErrCredentialStore, ErrPlaintextCredentialInCurrentConfig, ErrNoToken,
	} {
		if strings.Contains(sentinel.Error(), "cliconfig") {
			t.Errorf("sentinel %q leaks the package name", sentinel.Error())
		}
	}
}
