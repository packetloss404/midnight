package preflight

import (
	"bufio"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

var packageReadinessEnvironmentNames = map[string]struct{}{
	"MIDNIGHT_DB_DSN_FILE":                {},
	"MIDNIGHT_CURSOR_KEY_PATH":            {},
	"MIDNIGHT_REGISTRY_CONFIG":            {},
	"MIDNIGHT_BUILDKITD_BIN":              {},
	"MIDNIGHT_BUILDCTL":                   {},
	"MIDNIGHT_REGISTRY_BIN":               {},
	"MIDNIGHT_WORKER_ID":                  {},
	"MIDNIGHT_WORKER_RUNTIME_BACKEND":     {},
	"MIDNIGHT_WORKER_DOCKER_HOST":         {},
	"DOCKER_HOST":                         {},
	"MIDNIGHT_WORKER_AGENT_ID":            {},
	"MIDNIGHT_AGENT_ID":                   {},
	"MIDNIGHT_WORKER_HOST_ID":             {},
	"MIDNIGHT_HOST_ID":                    {},
	"MIDNIGHT_WORKER_ROUTE_CONFIG_DIR":    {},
	"MIDNIGHT_WORKER_ROUTE_GROUP":         {},
	"MIDNIGHT_WORKER_ROUTE_PUBLIC_HOST":   {},
	"MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH":   {},
	"MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS":    {},
	"MIDNIGHT_WORKER_ROUTE_CERT_RESOLVER": {},
	"MIDNIGHT_WORKER_ROUTE_TARGET_PORT":   {},
}

// BackupDestinationHint is the maximum backup destination information Doctor
// retains. It is sufficient for the current provider/scheme readiness check
// and deliberately cannot carry credentials, hostnames, buckets, or paths.
type BackupDestinationHint struct {
	Configured bool
	URIValid   bool
	Scheme     string
}

// SanitizeBackupDestination returns only non-secret readiness metadata.
func SanitizeBackupDestination(raw string) BackupDestinationHint {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return BackupDestinationHint{}
	}
	hint := BackupDestinationHint{Configured: true}
	parsed, err := url.Parse(raw)
	if err != nil {
		return hint
	}
	hint.URIValid = true
	hint.Scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
	return hint
}

// ParsePackageEnv preserves the historical lenient API used by planning
// callers. Live/package wiring should use ParsePackageEnvStrict so malformed
// EnvironmentFile input cannot silently change readiness decisions.
func ParsePackageEnv(text string) map[string]string {
	out := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !validEnvironmentName(key) {
			continue
		}
		out[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return out
}

// ParsePackageEnvStrict parses the systemd EnvironmentFile subset used by the
// Debian package. Values are data: shell expansion, command substitution, and
// environment-variable expansion are never performed.
func ParsePackageEnvStrict(text string) (map[string]string, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("package environment is not valid UTF-8")
	}
	if strings.ContainsRune(text, '\x00') || strings.HasPrefix(text, "\ufeff") {
		return nil, fmt.Errorf("package environment contains a forbidden character")
	}

	lines := strings.Split(text, "\n")
	values := make(map[string]string)
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		keyText, valueText, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("package environment line %d is not a variable assignment", i+1)
		}
		key := strings.TrimSpace(keyText)
		if !validEnvironmentName(key) {
			return nil, fmt.Errorf("package environment line %d has an invalid variable name", i+1)
		}
		value, consumed, err := parsePackageEnvValue(lines[i:], valueText)
		if err != nil {
			return nil, fmt.Errorf("package environment line %d variable %s: %w", i+1, key, err)
		}
		i += consumed
		values[key] = value
	}
	return values, nil
}

// ParsePackageReadinessEnv returns only non-secret inputs consumed by doctor.
// Direct DSNs are represented by a presence bit and are never retained in the
// returned map.
func ParsePackageReadinessEnv(text string) (map[string]string, bool, BackupDestinationHint, error) {
	all, err := ParsePackageEnvStrict(text)
	if err != nil {
		return nil, false, BackupDestinationHint{}, err
	}
	configuredDSN := strings.TrimSpace(all["MIDNIGHT_DB_DSN"]) != "" || strings.TrimSpace(all["MIDNIGHT_DATABASE_URL"]) != ""
	backupHint := SanitizeBackupDestination(all["MIDNIGHT_BACKUP_DESTINATION_URI"])
	out := make(map[string]string)
	for name := range packageReadinessEnvironmentNames {
		if value, ok := all[name]; ok {
			out[name] = value
		}
	}
	return out, configuredDSN, backupHint, nil
}

func validEnvironmentName(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_' || (i > 0 && c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

// parsePackageEnvValue returns the decoded value and the number of additional
// physical lines consumed after lines[0].
func parsePackageEnvValue(lines []string, first string) (string, int, error) {
	first = strings.TrimLeft(first, " \t\r")
	if first == "" {
		return "", 0, nil
	}
	switch first[0] {
	case '\'':
		return parseQuotedPackageEnvValue(lines, first[1:], '\'')
	case '"':
		return parseQuotedPackageEnvValue(lines, first[1:], '"')
	default:
		return parseUnquotedPackageEnvValue(lines, first)
	}
}

func parseQuotedPackageEnvValue(lines []string, first string, quote byte) (string, int, error) {
	var out strings.Builder
	current := first
	for lineIndex := 0; ; lineIndex++ {
		for i := 0; i < len(current); i++ {
			c := current[i]
			if c == quote {
				if strings.TrimSpace(current[i+1:]) != "" {
					return "", lineIndex, fmt.Errorf("unexpected data after closing quote")
				}
				return out.String(), lineIndex, nil
			}
			if quote == '"' && c == '\\' {
				if i+1 == len(current) {
					// A trailing backslash consumes the physical newline.
					break
				}
				next := current[i+1]
				if strings.ContainsRune("\"\\`$", rune(next)) {
					out.WriteByte(next)
					i++
					continue
				}
				out.WriteByte(c)
				continue
			}
			out.WriteByte(c)
		}
		if lineIndex+1 >= len(lines) {
			return "", lineIndex, fmt.Errorf("unterminated quoted value")
		}
		if quote == '\'' || !strings.HasSuffix(current, "\\") {
			out.WriteByte('\n')
		}
		current = strings.TrimSuffix(lines[lineIndex+1], "\r")
	}
}

func parseUnquotedPackageEnvValue(lines []string, first string) (string, int, error) {
	var out strings.Builder
	current := first
	for lineIndex := 0; ; lineIndex++ {
		current = strings.TrimRight(current, " \t\r")
		continued := false
		for i := 0; i < len(current); i++ {
			if current[i] != '\\' {
				out.WriteByte(current[i])
				continue
			}
			if i+1 == len(current) {
				continued = true
				break
			}
			out.WriteByte(current[i+1])
			i++
		}
		if !continued {
			return strings.TrimRight(out.String(), " \t\r"), lineIndex, nil
		}
		if lineIndex+1 >= len(lines) {
			return "", lineIndex, fmt.Errorf("unterminated line continuation")
		}
		current = strings.TrimSuffix(lines[lineIndex+1], "\r")
	}
}
