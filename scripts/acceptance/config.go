package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type config struct {
	values    map[string]string
	directory string
	deadline  time.Duration
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{3,160}$`)
var commitSHA = regexp.MustCompile(`^[a-f0-9]{40}$`)
var imageDigest = regexp.MustCompile(`^[^\s]+@sha256:[a-f0-9]{64}$`)
var allowedKeys = strings.Fields(`QA_API_BASE QA_EXPECTED_COMMIT QA_WORKSPACE_ID QA_PROJECT_ID QA_SERVICE_ID QA_ENVIRONMENT_ID QA_HUMAN_TOKEN_FILE DEADLINE_SECONDS TARGET_CLASS ACK_EXTERNAL_MUTATIONS ACK_PAID_CALLS ACK_DNS_MUTATIONS PROVIDER MODEL DIAGNOSIS_IMAGE GITHUB_APP_ID GITHUB_INSTALLATION_ID GITHUB_APP_SLUG GITHUB_PRIVATE_KEY_FILE GITHUB_QA_DRIVER_TOKEN_FILE GITHUB_QA_DRIVER_LOGIN GITHUB_REPOSITORY_ID GITHUB_REPOSITORY GITHUB_QA_BRANCH GITHUB_EXPECTED_HEAD GITHUB_STATUS_CONTEXT DNS_ZONE_ID DNS_ZONE_NAME DNS_QA_SUFFIX CLOUDFLARE_TOKEN_FILE ACME_EMAIL ACME_STAGING_ACCOUNT_DIR QA_RUNTIME_SSH_TARGET QA_RUNTIME_SSH_PORT QA_RUNTIME_SSH_KEY_FILE QA_RUNTIME_SSH_KNOWN_HOSTS_FILE QA_RUNTIME_SSH_SUDO`)

func loadConfig(path, lane string) (config, error) {
	if path == "" {
		return config{}, blocked("ENV_FILE_REQUIRED")
	}
	data, err := readPrivate(path, 64<<10)
	if err != nil {
		return config{}, err
	}
	values, err := parseEnv(bytes.NewReader(data))
	if err != nil {
		return config{}, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return config{}, blocked("INVALID_ENV_FILE")
	}
	c := config{values: values, directory: filepath.Dir(absolute), deadline: 10 * time.Minute}
	if value := values["DEADLINE_SECONDS"]; value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 15 || seconds > 1800 {
			return config{}, blocked("DEADLINE_OUT_OF_RANGE")
		}
		c.deadline = time.Duration(seconds) * time.Second
	}
	if values["TARGET_CLASS"] != "disposable-qa" {
		return config{}, blocked("DISPOSABLE_QA_TARGET_REQUIRED")
	}
	if lane == "dns-acme" {
		for _, key := range []string{"DNS_ZONE_ID", "DNS_ZONE_NAME", "DNS_QA_SUFFIX", "CLOUDFLARE_TOKEN_FILE", "ACME_EMAIL", "ACME_STAGING_ACCOUNT_DIR"} {
			if values[key] == "" {
				return config{}, blocked("MISSING_" + key)
			}
		}
		if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(values["DNS_ZONE_ID"]) || !validQASuffix(values["DNS_ZONE_NAME"], values["DNS_QA_SUFFIX"]) {
			return config{}, blocked("DISPOSABLE_DNS_SCOPE_INVALID")
		}
		return c, nil
	}
	if lane != "agent" && lane != "diagnosis" && lane != "github" {
		return config{}, blocked("UNKNOWN_LANE")
	}
	for _, key := range []string{"QA_API_BASE", "QA_EXPECTED_COMMIT", "QA_WORKSPACE_ID", "QA_PROJECT_ID", "QA_SERVICE_ID", "QA_ENVIRONMENT_ID", "QA_HUMAN_TOKEN_FILE"} {
		if values[key] == "" {
			return config{}, blocked("MISSING_" + key)
		}
	}
	if !safeAPIBase(values["QA_API_BASE"]) || !commitSHA.MatchString(values["QA_EXPECTED_COMMIT"]) {
		return config{}, blocked("QA_API_IDENTITY_INVALID")
	}
	for _, key := range []string{"QA_WORKSPACE_ID", "QA_PROJECT_ID", "QA_SERVICE_ID", "QA_ENVIRONMENT_ID"} {
		if !identifier.MatchString(values[key]) {
			return config{}, blocked("QA_SCOPE_INVALID")
		}
	}
	if lane == "agent" || lane == "diagnosis" {
		if values["PROVIDER"] != "anthropic" && values["PROVIDER"] != "openai" && values["PROVIDER"] != "minimax" {
			return config{}, blocked("PROVIDER_REQUIRED")
		}
		if values["MODEL"] == "" || len(values["MODEL"]) > 160 {
			return config{}, blocked("EXPLICIT_MODEL_REQUIRED")
		}
	}
	if lane == "diagnosis" && !imageDigest.MatchString(values["DIAGNOSIS_IMAGE"]) {
		return config{}, blocked("PINNED_FAILING_IMAGE_REQUIRED")
	}
	if lane == "github" {
		for _, key := range []string{"GITHUB_APP_ID", "GITHUB_INSTALLATION_ID", "GITHUB_APP_SLUG", "GITHUB_PRIVATE_KEY_FILE", "GITHUB_QA_DRIVER_TOKEN_FILE", "GITHUB_QA_DRIVER_LOGIN", "GITHUB_REPOSITORY_ID", "GITHUB_REPOSITORY", "GITHUB_QA_BRANCH", "GITHUB_EXPECTED_HEAD"} {
			if values[key] == "" {
				return config{}, blocked("MISSING_" + key)
			}
		}
		for _, key := range []string{"GITHUB_APP_ID", "GITHUB_INSTALLATION_ID", "GITHUB_REPOSITORY_ID"} {
			if n, err := strconv.ParseInt(values[key], 10, 64); err != nil || n < 1 {
				return config{}, blocked("GITHUB_SCOPE_INVALID")
			}
		}
		if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(values["GITHUB_REPOSITORY"]) || !regexp.MustCompile(`^qa-midnight-[A-Za-z0-9_-]+$`).MatchString(values["GITHUB_QA_BRANCH"]) || !commitSHA.MatchString(values["GITHUB_EXPECTED_HEAD"]) {
			return config{}, blocked("GITHUB_QA_BRANCH_OR_REPO_INVALID")
		}
	}
	return c, nil
}
func parseEnv(input io.Reader) (map[string]string, error) {
	allowed := map[string]bool{}
	for _, key := range allowedKeys {
		allowed[key] = true
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || !allowed[key] || values[key] != "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") || strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") || strings.HasPrefix(line, "export ") {
			return nil, blocked("INVALID_OR_DUPLICATE_ENV_ENTRY")
		}
		if _, exists := values[key]; exists {
			return nil, blocked("INVALID_OR_DUPLICATE_ENV_ENTRY")
		}
		values[key] = value
	}
	if scanner.Err() != nil {
		return nil, blocked("ENV_FILE_TOO_LARGE")
	}
	return values, nil
}
func readPrivate(path string, max int64) ([]byte, error) {
	if err := requirePrivateParent(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, blocked("PRIVATE_FILE_UNAVAILABLE")
	}
	if !info.Mode().IsRegular() || !trustedFileOwner(info) || info.Mode().Perm()&0077 != 0 || info.Size() > max {
		return nil, blocked("PRIVATE_FILE_PERMISSIONS_OR_SIZE")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, blocked("PRIVATE_FILE_UNAVAILABLE")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Mode().Perm()&0077 != 0 || !opened.Mode().IsRegular() {
		return nil, blocked("PRIVATE_FILE_CHANGED")
	}
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, blocked("PRIVATE_FILE_UNAVAILABLE")
	}
	return data, nil
}

// The immediate directory is private; all ancestors must be trusted and
// nonsymlinked. A trusted sticky ancestor such as /tmp is safe when its child
// boundary is owned/private. An ordinary writable ancestor can swap that child.
func requirePrivateParent(path string) error {
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if component == ".." {
			return blocked("PRIVATE_PATH_INVALID")
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return blocked("PRIVATE_PATH_INVALID")
	}
	parent := filepath.Dir(absolute)
	for directory := parent; ; directory = filepath.Dir(directory) {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !trustedFileOwner(info) {
			return blocked("PRIVATE_DIRECTORY_AUTHORITY_REQUIRED")
		}
		if directory == parent {
			if info.Mode().Perm()&0077 != 0 {
				return blocked("PRIVATE_PARENT_DIRECTORY_REQUIRED")
			}
		} else if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return blocked("UNSAFE_WRITABLE_PATH_ANCESTOR")
		}
		if filepath.Dir(directory) == directory {
			break
		}
	}
	return nil
}
func (c config) path(key string) string {
	value := c.values[key]
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(c.directory, value)
}
func (c config) secret(key string) (string, error) {
	data, err := readPrivate(c.path(key), 1<<20)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", blocked("SECRET_FILE_INVALID")
	}
	return value, nil
}
func (c config) authorize(lane string) error {
	if c.values["ACK_EXTERNAL_MUTATIONS"] != "disposable-scopes-only" {
		return blocked("EXTERNAL_MUTATION_ACK_REQUIRED")
	}
	if (lane == "agent" || lane == "diagnosis") && c.values["ACK_PAID_CALLS"] != "allow-bounded-calls" {
		return blocked("PAID_CALL_ACK_REQUIRED")
	}
	if lane == "dns-acme" && c.values["ACK_DNS_MUTATIONS"] != "staging-disposable-subdomain-only" {
		return blocked("DNS_MUTATION_ACK_REQUIRED")
	}
	return nil
}
func safeAPIBase(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
}
func validQASuffix(zone, suffix string) bool {
	return regexp.MustCompile(`^[a-z0-9.-]+$`).MatchString(zone) && regexp.MustCompile(`^[a-z0-9.-]+$`).MatchString(suffix) && strings.Contains(suffix, ".") && strings.HasSuffix(suffix, "."+zone) && !strings.HasPrefix(suffix, ".") && !strings.Contains(suffix, "..")
}
