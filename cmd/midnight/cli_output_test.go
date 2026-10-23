package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCLIProjectListOutputContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1alpha/projects" {
			t.Errorf("path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[{"id":"prj_z","workspace_id":"wrk_1","slug":"zulu","display_name":"Zulu"},{"id":"prj_a","workspace_id":"wrk_1","slug":"alpha","display_name":"Alpha"}]`))
	}))
	t.Cleanup(server.Close)
	setCLIEndpoint(t, server.URL)

	tests := []struct {
		name       string
		args       []string
		want       string
		validate   func(*testing.T, string)
		wantStderr string
	}{
		{
			name: "table is deterministic",
			args: []string{"project", "list"},
			validate: func(t *testing.T, output string) {
				if strings.Index(output, "prj_a") > strings.Index(output, "prj_z") {
					t.Fatalf("table is not sorted by id: %q", output)
				}
			},
		},
		{
			name: "json is one valid document",
			args: []string{"project", "list", "--output", "json"},
			validate: func(t *testing.T, output string) {
				var values []map[string]any
				if err := json.Unmarshal([]byte(output), &values); err != nil || len(values) != 2 {
					t.Fatalf("JSON output = %q, values=%#v err=%v", output, values, err)
				}
				if values[0]["id"] != "prj_a" || values[1]["id"] != "prj_z" {
					t.Fatalf("JSON output is not deterministic: %#v", values)
				}
			},
		},
		{
			name: "jsonl is sorted and one object per line",
			args: []string{"project", "list", "--output=jsonl"},
			validate: func(t *testing.T, output string) {
				lines := nonemptyLines(output)
				if len(lines) != 2 {
					t.Fatalf("JSONL lines = %#v", lines)
				}
				var identifiers []string
				for _, line := range lines {
					var value struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal([]byte(line), &value); err != nil {
						t.Fatalf("invalid JSONL line %q: %v", line, err)
					}
					identifiers = append(identifiers, value.ID)
				}
				if !reflect.DeepEqual(identifiers, []string{"prj_a", "prj_z"}) {
					t.Fatalf("JSONL ids = %#v", identifiers)
				}
			},
		},
		{
			name: "name emits stable identifiers only",
			args: []string{"project", "list", "--output", "name"},
			want: "prj_a\nprj_z\n",
		},
		{
			name: "quiet aliases primary identifiers",
			args: []string{"project", "list", "--quiet"},
			want: "prj_a\nprj_z\n",
		},
		{
			name: "legacy json remains compatible",
			args: []string{"project", "list", "--json"},
			validate: func(t *testing.T, output string) {
				if !json.Valid([]byte(output)) || !strings.Contains(output, `"id": "prj_z"`) {
					t.Fatalf("legacy JSON output = %q", output)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := ExecuteCLI(test.args, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			if test.want != "" && stdout.String() != test.want {
				t.Fatalf("stdout = %q, want %q", stdout.String(), test.want)
			}
			if test.validate != nil {
				test.validate(t, stdout.String())
			}
			if stderr.String() != test.wantStderr {
				t.Fatalf("stderr = %q, want no progress/warnings", stderr.String())
			}
		})
	}
}

func TestCLIRootTimeoutReachesGeneratedReadRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	setCLIEndpoint(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	started := time.Now()
	code := ExecuteCLI([]string{"project", "list", "--timeout", "20ms"}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if time.Since(started) > time.Second {
		t.Fatalf("root timeout did not bound request")
	}
	if !strings.Contains(stderr.String(), context.DeadlineExceeded.Error()) {
		t.Fatalf("stderr=%q, want context deadline", stderr.String())
	}
}

func TestCLIMachineOutputContainsNoTableOrProgressText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`[{"id":"svc_1","project_id":"prj_1","slug":"api","display_name":"API"}]`))
	}))
	t.Cleanup(server.Close)
	setCLIEndpoint(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := ExecuteCLI([]string{"service", "list", "--output", "jsonl"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "PROJECT") || strings.Contains(stdout.String(), "Fetching") || stderr.Len() != 0 {
		t.Fatalf("machine output contaminated: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	var value map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &value); err != nil || value["id"] != "svc_1" {
		t.Fatalf("stdout is not the requested machine result: value=%#v err=%v", value, err)
	}
}

func TestCLINoColorStripsANSIWithoutChangingLayout(t *testing.T) {
	execution := NewCLIExecutionContext(nil, io.Discard, io.Discard)
	execution.Options.NoColor = true
	var output bytes.Buffer
	writer := &cliOutputWriter{Writer: &output, execution: execution}
	_, _ = io.WriteString(writer, "before \x1b[31mred\x1b[0m after\n")
	if got, want := output.String(), "before red after\n"; got != want {
		t.Fatalf("no-color output = %q, want %q", got, want)
	}
}

func nonemptyLines(value string) []string {
	scanner := bufio.NewScanner(strings.NewReader(value))
	var lines []string
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
