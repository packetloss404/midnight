// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package cliconfig

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	keyring "github.com/zalando/go-keyring"
)

func TestNativeKeyringIsDefaultWhenNoConfigOverrideIsSet(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", "")
	t.Setenv("HOME", configHome)
	clearCredentialEnv(t)
	keyring.MockInit()
	const secret = "midn_user_native-keyring-secret"
	if err := SetHumanSession("https://keyring.test", HumanSessionCredential{Token: secret}); err != nil {
		t.Fatal(err)
	}
	path, err := GlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), secret) || !strings.Contains(string(metadata), `"credential_ref": "keyring:default:`) {
		t.Fatalf("native-keyring metadata contract violated:\n%s", metadata)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), FallbackCredentialFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default storage unexpectedly created fallback file: %v", err)
	}
	if got, err := Token("https://keyring.test"); err != nil || got != secret {
		t.Fatalf("Token = %q, %v", got, err)
	}
}

func TestCurrentConfigContainsOnlyMetadataReferences(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	const secret = "midn_user_metadata-must-not-contain-this-secret"
	if err := SetHumanSession("https://api.midnight.test", HumanSessionCredential{
		Token: secret,
		HumanSessionMetadata: HumanSessionMetadata{
			SessionID: "ses_safe", Email: "alice@example.com",
		},
	}); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(configHome, GlobalConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), secret) || strings.Contains(string(metadata), `"token"`) {
		t.Fatalf("metadata disclosed a bearer token:\n%s", metadata)
	}
	if !strings.Contains(string(metadata), `"credential_ref": "fallback:default:`) {
		t.Fatalf("metadata has no opaque fallback reference:\n%s", metadata)
	}
	fallback, err := os.ReadFile(filepath.Join(configHome, FallbackCredentialFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fallback), secret) {
		t.Fatal("explicit headless fallback did not persist the credential")
	}
	if info, err := os.Stat(filepath.Join(configHome, FallbackCredentialFileName)); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("fallback credential mode = %v, want owner-only", info.Mode())
	}
}

func TestV2PlaintextMigratesOnSuccessfulMutation(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	const legacySecret = "midn_user_legacy-secret"
	legacy := fmt.Sprintf(`{
		"version": 2,
		"servers": {
			"https://legacy.test": {
				"api_base":"https://legacy.test",
				"token":%q,
				"credential_kind":"human_session"
			}
		}
	}`, legacySecret)
	path := filepath.Join(configHome, GlobalConfigFileName)
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := Token("https://legacy.test"); err != nil || token != legacySecret {
		t.Fatalf("legacy read = %q, %v", token, err)
	}
	if err := SetOperatorRecoveryToken("https://new.test", "operator-new"); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), legacySecret) || strings.Contains(string(metadata), "operator-new") {
		t.Fatalf("migration left plaintext in metadata:\n%s", metadata)
	}
	if token, err := Token("https://legacy.test"); err != nil || token != legacySecret {
		t.Fatalf("migrated read = %q, %v", token, err)
	}
}

func TestSaveGlobalCompatibilityMigratesLegacyPlaintext(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	const secret = "legacy-save-global-secret"
	err := SaveGlobal(Global{
		Version: 2,
		Servers: map[string]ServerEntry{
			"https://legacy-save.test/": {Token: secret},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(configHome, GlobalConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), secret) {
		t.Fatalf("SaveGlobal left plaintext in metadata:\n%s", metadata)
	}
	if got, err := Token("https://legacy-save.test"); err != nil || got != secret {
		t.Fatalf("Token = %q, %v", got, err)
	}
}

func TestCurrentConfigRejectsPlaintextAndFutureVersionWithoutRewrite(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	path := filepath.Join(configHome, GlobalConfigFileName)
	plaintext := []byte(`{"version":3,"servers":{"https://api.test":{"api_base":"https://api.test","token":"do-not-disclose"}}}`)
	if err := os.WriteFile(path, plaintext, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGlobal(); !errors.Is(err, ErrPlaintextCredentialInCurrentConfig) {
		t.Fatalf("LoadGlobal error = %v", err)
	}
	if err := SetToken("https://api.test", "replacement-secret"); !errors.Is(err, ErrPlaintextCredentialInCurrentConfig) {
		t.Fatalf("SetToken error = %v", err)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(plaintext) {
		t.Fatalf("plaintext config changed: %q, %v", after, err)
	}

	future := []byte(`{"version":4,"servers":{},"future":"must-survive"}`)
	if err := os.WriteFile(path, future, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGlobal(); !errors.Is(err, ErrUnsupportedGlobalConfigVersion) {
		t.Fatalf("future LoadGlobal error = %v", err)
	}
	if err := SetToken("https://api.test", "replacement-secret"); !errors.Is(err, ErrUnsupportedGlobalConfigVersion) {
		t.Fatalf("future SetToken error = %v", err)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(future) {
		t.Fatalf("future config changed: %q, %v", after, err)
	}
}

func TestCredentialMutationRollsBackSecretWhenMetadataWriteFails(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	const base = "https://rollback.test"
	if err := SetToken(base, "original-secret"); err != nil {
		t.Fatal(err)
	}
	originalMetadata, err := os.ReadFile(filepath.Join(configHome, GlobalConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	originalWriter := globalConfigWriteJSON
	globalConfigWriteJSON = func(string, any, os.FileMode, os.FileMode) error {
		return errors.New("injected metadata failure containing replacement-secret")
	}
	t.Cleanup(func() { globalConfigWriteJSON = originalWriter })
	err = SetToken(base, "replacement-secret")
	if err == nil {
		t.Fatal("SetToken succeeded despite injected metadata failure")
	}
	if strings.Contains(err.Error(), "replacement-secret") {
		t.Fatalf("error disclosed secret: %q", err)
	}
	globalConfigWriteJSON = originalWriter
	if token, err := Token(base); err != nil || token != "original-secret" {
		t.Fatalf("rollback token = %q, %v", token, err)
	}
	if after, err := os.ReadFile(filepath.Join(configHome, GlobalConfigFileName)); err != nil || string(after) != string(originalMetadata) {
		t.Fatalf("metadata changed after rollback: %q, %v", after, err)
	}
}

func TestConcurrentCredentialMutationsDoNotLoseUpdates(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	const count = 24
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			base := fmt.Sprintf("https://api-%02d.test", i)
			errs <- SetToken(base, fmt.Sprintf("token-%02d", i))
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != count {
		t.Fatalf("server count = %d, want %d", len(cfg.Servers), count)
	}
	for i := 0; i < count; i++ {
		base := fmt.Sprintf("https://api-%02d.test", i)
		want := fmt.Sprintf("token-%02d", i)
		if got, err := Token(base); err != nil || got != want {
			t.Fatalf("Token(%s) = %q, %v; want %q", base, got, err, want)
		}
	}
}

func TestConcurrentCredentialMutationsAcrossProcesses(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	const count = 12
	commands := make([]*exec.Cmd, 0, count)
	var outputs [count]strings.Builder
	for i := 0; i < count; i++ {
		command := exec.Command(os.Args[0], "-test.run=^TestCredentialMutationSubprocess$")
		command.Env = append(os.Environ(),
			"MIDNIGHT_CONFIG_HOME="+configHome,
			"MIDNIGHT_CONFIG_MUTATION_HELPER=1",
			fmt.Sprintf("MIDNIGHT_CONFIG_MUTATION_INDEX=%d", i),
			EnvAPIBase+"=", EnvBaseURL+"=", EnvBearerToken+"=", EnvAPIToken+"=",
		)
		command.Stdout = &outputs[i]
		command.Stderr = &outputs[i]
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	for i := range commands {
		if err := commands[i].Wait(); err != nil {
			t.Fatalf("subprocess %d: %v\n%s", i, err, outputs[i].String())
		}
	}
	cfg, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != count {
		t.Fatalf("server count = %d, want %d", len(cfg.Servers), count)
	}
}

func TestCredentialMutationSubprocess(t *testing.T) {
	if os.Getenv("MIDNIGHT_CONFIG_MUTATION_HELPER") != "1" { // not-a-failure-code
		t.Skip("subprocess helper")
	}
	index := os.Getenv("MIDNIGHT_CONFIG_MUTATION_INDEX") // not-a-failure-code
	if err := SetToken("https://process-"+index+".test", "process-token-"+index); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialErrorsRedactSecrets(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "secret-token-must-not-appear")
	if err := os.WriteFile(configHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", configHome)
	clearCredentialEnv(t)
	err := SetToken("https://api.test", "secret-token-must-not-appear")
	if err == nil {
		t.Fatal("SetToken succeeded with unusable config home")
	}
	if strings.Contains(err.Error(), "secret-token-must-not-appear") {
		t.Fatalf("error disclosed secret: %q", err)
	}
}
