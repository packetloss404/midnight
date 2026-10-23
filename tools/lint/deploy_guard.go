package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var systemdEnvRefPattern = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)

func checkDeploySystemdPackaging(root string) error {
	systemdDir := filepath.Join(root, "deploy", "systemd")
	rulesPath := filepath.Join(root, "deploy", "debian", "rules")
	if _, err := os.Stat(systemdDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat deploy/systemd: %w", err)
	}
	rulesData, err := os.ReadFile(rulesPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read deploy/debian/rules: %w", err)
	}
	rules := string(rulesData)
	installedBins := parseDebianInstalledUsrBin(rules)
	installedServices := parseDebianInstalledSystemdServices(rules)
	envDefaults, err := parseMidnightEnvFile(filepath.Join(root, "deploy", "debian", "midnight.env"))
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(systemdDir)
	if err != nil {
		return fmt.Errorf("read deploy/systemd: %w", err)
	}
	var failures []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".service") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("deploy", "systemd", entry.Name()))
		if !installedServices[entry.Name()] {
			failures = append(failures, fmt.Sprintf("%s is not installed by deploy/debian/rules", rel))
		}
		unitData, err := os.ReadFile(filepath.Join(systemdDir, entry.Name()))
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		for _, missing := range missingSystemdEnvDefaults(string(unitData), envDefaults) {
			failures = append(failures, fmt.Sprintf("%s references %s but deploy/debian/midnight.env does not declare it", rel, missing))
		}
		targets, err := execStartTargets(filepath.Join(systemdDir, entry.Name()))
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		for _, target := range targets {
			if !strings.HasPrefix(target, "/usr/bin/") {
				continue
			}
			name := strings.TrimPrefix(target, "/usr/bin/")
			if !installedBins[name] {
				failures = append(failures, fmt.Sprintf("%s ExecStart target %s is not installed by deploy/debian/rules", rel, target))
			}
		}
	}
	failures = append(failures, checkExternalBinaryEnvDefaults(envDefaults)...)
	if controlFailures, err := checkDebianControlExternalBinaryDeclarations(filepath.Join(root, "deploy", "debian", "control")); err != nil {
		return err
	} else {
		failures = append(failures, controlFailures...)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func parseMidnightEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read deploy/debian/midnight.env: %w", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
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
		if key != "" {
			out[key] = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func missingSystemdEnvDefaults(unit string, envDefaults map[string]string) []string {
	unitEnv := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(unit))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "Environment=") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Environment="))
		for _, field := range fields {
			name, _, ok := strings.Cut(field, "=")
			if ok && name != "" {
				unitEnv[name] = true
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, match := range systemdEnvRefPattern.FindAllStringSubmatch(unit, -1) {
		name := match[1]
		if seen[name] || unitEnv[name] || envDefaults[name] != "" {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func checkExternalBinaryEnvDefaults(envDefaults map[string]string) []string {
	var failures []string
	for _, name := range []string{"MIDNIGHT_BUILDKITD_BIN", "MIDNIGHT_BUILDCTL", "MIDNIGHT_REGISTRY_BIN"} {
		value := strings.TrimSpace(envDefaults[name])
		if value == "" {
			failures = append(failures, fmt.Sprintf("deploy/debian/midnight.env must declare %s", name))
			continue
		}
		if !strings.HasPrefix(value, "/") {
			failures = append(failures, fmt.Sprintf("deploy/debian/midnight.env %s must be an absolute operator-supplied binary path", name))
		}
	}
	return failures
}

func checkDebianControlExternalBinaryDeclarations(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read deploy/debian/control: %w", err)
	}
	fields := midnightPackageFields(string(data))
	var failures []string
	for _, pkg := range []string{"buildkit", "docker-registry"} {
		if relationContainsPackage(fields["Depends"], pkg) {
			failures = append(failures, fmt.Sprintf("deploy/debian/control Depends must not hard-depend on external binary package %s; declare it in Suggests", pkg))
		}
		if !relationContainsPackage(fields["Suggests"], pkg) {
			failures = append(failures, fmt.Sprintf("deploy/debian/control Suggests must declare external binary package %s", pkg))
		}
	}
	return failures, nil
}

func midnightPackageFields(control string) map[string]string {
	for _, stanza := range parseDebianControlStanzas(control) {
		if stanza["Package"] == "midnight" {
			return stanza
		}
	}
	return map[string]string{}
}

func parseDebianControlStanzas(control string) []map[string]string {
	var stanzas []map[string]string
	current := map[string]string{}
	lastKey := ""
	flush := func() {
		if len(current) > 0 {
			stanzas = append(stanzas, current)
			current = map[string]string{}
			lastKey = ""
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(control))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if lastKey != "" {
				current[lastKey] += "\n" + strings.TrimSpace(line)
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		lastKey = strings.TrimSpace(key)
		current[lastKey] = strings.TrimSpace(value)
	}
	flush()
	return stanzas
}

func relationContainsPackage(field, pkg string) bool {
	for _, rel := range strings.Split(field, ",") {
		for _, alt := range strings.Split(rel, "|") {
			name := strings.TrimSpace(alt)
			if idx := strings.IndexAny(name, " ("); idx >= 0 {
				name = name[:idx]
			}
			if name == pkg {
				return true
			}
		}
	}
	return false
}

func parseDebianInstalledUsrBin(rules string) map[string]bool {
	out := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(rules))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		idx := strings.Index(line, "$(CURDIR)/debian/midnight/usr/bin/")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[idx+len("$(CURDIR)/debian/midnight/usr/bin/"):])
		if name != "" && !strings.ContainsAny(name, " \t") {
			out[name] = true
		}
	}
	return out
}

func parseDebianInstalledSystemdServices(rules string) map[string]bool {
	out := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(rules))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		idx := strings.Index(line, "deploy/systemd/")
		if idx < 0 {
			continue
		}
		rest := line[idx+len("deploy/systemd/"):]
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		name := filepath.Base(fields[0])
		if strings.HasSuffix(name, ".service") {
			out[name] = true
		}
	}
	return out
}

func execStartTargets(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		target := strings.TrimSpace(strings.TrimPrefix(line, "ExecStart="))
		for strings.HasPrefix(target, "-") || strings.HasPrefix(target, "@") || strings.HasPrefix(target, "+") || strings.HasPrefix(target, "!") {
			target = strings.TrimSpace(target[1:])
		}
		fields := strings.Fields(target)
		if len(fields) > 0 {
			out = append(out, fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
