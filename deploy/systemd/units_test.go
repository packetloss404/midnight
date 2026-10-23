package systemd_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

var midnightUnitNames = []string{
	"midnight-server.service",
	"midnight-worker.service",
	"midnight-agent.service",
	"midnight-buildkit.service",
	"midnight-buildkit-worker.service",
	"midnight-registry.service",
	"midnight-github-status-worker.service",
}

func TestReadinessAndOrderingEdges(t *testing.T) {
	units := readUnits(t)
	requireWords(t, units["midnight-server.service"], "After", "postgresql.service")
	requireWords(t, units["midnight-worker.service"], "After", "postgresql.service", "midnight-server.service")
	requireWords(t, units["midnight-agent.service"], "After", "docker.service", "midnight-server.service")
	requireWords(t, units["midnight-agent.service"], "Requires", "docker.service")
	if containsWord(units["midnight-agent.service"]["Wants"], "midnight-server.service") {
		t.Fatal("midnight-agent.service must not pull in midnight-server.service")
	}
	requireWords(t, units["midnight-buildkit-worker.service"], "After", "postgresql.service", "midnight-server.service", "midnight-buildkit.service", "midnight-registry.service")
	requireWords(t, units["midnight-github-status-worker.service"], "After", "postgresql.service", "midnight-server.service")

	requireValue(t, units["midnight-server.service"], "ExecStartPost", "/usr/bin/midnight-doctor wait --tcp ${MIDNIGHT_SERVER_ADDR} --timeout 45s")
	requireValue(t, units["midnight-buildkit.service"], "ExecStartPost", "/usr/bin/midnight-doctor wait --unix ${MIDNIGHT_BUILDKIT_ADDR} --timeout 45s")
	requireValue(t, units["midnight-registry.service"], "ExecStartPost", "/usr/bin/midnight-doctor wait --tcp ${MIDNIGHT_REGISTRY_ADDR} --timeout 45s")
	for _, name := range []string{"midnight-server.service", "midnight-buildkit.service", "midnight-registry.service"} {
		requireValue(t, units[name], "TimeoutStartSec", "75s")
	}
}

func TestRemoteDatabaseDoesNotBecomeAHardDependency(t *testing.T) {
	units := readUnits(t)
	for name, unit := range units {
		for _, directive := range []string{"Requires", "Wants"} {
			if containsWord(unit[directive], "postgresql.service") {
				t.Fatalf("%s %s pulls in postgresql.service; remote database installs must remain supported", name, directive)
			}
		}
	}
}

func TestDockerRequiresIsLimitedToAgent(t *testing.T) {
	units := readUnits(t)
	for name, unit := range units {
		if name != "midnight-agent.service" && containsWord(unit["Requires"], "docker.service") {
			t.Fatalf("%s hard-requires docker.service", name)
		}
	}
}

func TestMidnightAfterGraphHasNoCycle(t *testing.T) {
	units := readUnits(t)
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("ordering cycle includes %s", name)
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		for _, dependency := range strings.Fields(units[name]["After"]) {
			if _, ok := units[dependency]; ok {
				if err := visit(dependency); err != nil {
					return err
				}
			}
		}
		delete(visiting, name)
		visited[name] = true
		return nil
	}
	for name := range units {
		if err := visit(name); err != nil {
			t.Fatal(err)
		}
	}
}

func readUnits(t *testing.T) map[string]map[string]string {
	t.Helper()
	units := make(map[string]map[string]string, len(midnightUnitNames))
	for _, name := range midnightUnitNames {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		unit := map[string]string{}
		section := ""
		for _, rawLine := range strings.Split(string(data), "\n") {
			line := strings.TrimSpace(rawLine)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if ok && (section == "Unit" || section == "Service") {
				if unit[key] != "" {
					unit[key] += " "
				}
				unit[key] += value
			}
		}
		units[name] = unit
	}
	return units
}

func requireWords(t *testing.T, unit map[string]string, directive string, words ...string) {
	t.Helper()
	for _, word := range words {
		if !containsWord(unit[directive], word) {
			t.Errorf("%s = %q, want word %q", directive, unit[directive], word)
		}
	}
}

func containsWord(value, word string) bool {
	for _, candidate := range strings.Fields(value) {
		if candidate == word {
			return true
		}
	}
	return false
}

func requireValue(t *testing.T, unit map[string]string, directive, want string) {
	t.Helper()
	if got := unit[directive]; got != want {
		t.Errorf("%s = %q, want %q", directive, got, want)
	}
}
