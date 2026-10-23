package build

import "testing"

func TestDockerignoreMatchSimplePatterns(t *testing.T) {
	dockerignore := ParseDockerignore("# generated\n*.log\nbuild/\n!important.log\n")

	tests := map[string]bool{
		"app.log":           true,
		"nested/app.log":    true,
		"build/app.bin":     true,
		"important.log":     false,
		"src/important.log": false,
		"src/main.go":       false,
	}

	for file, want := range tests {
		if got := dockerignore.Match(file); got != want {
			t.Fatalf("Match(%q) = %v, want %v", file, got, want)
		}
	}
}

func TestPlanBuildContextFiltersExplicitFileList(t *testing.T) {
	dockerignore := ParseDockerignore("dist/\n*.tmp\n")

	plan, err := PlanBuildContext([]string{
		"cmd/app/main.go",
		"dist/app",
		"notes.tmp",
		"README.md",
	}, dockerignore)
	if err != nil {
		t.Fatalf("PlanBuildContext() error = %v", err)
	}

	want := []string{"cmd/app/main.go", "README.md"}
	if len(plan.Files) != len(want) {
		t.Fatalf("Files = %#v, want %#v", plan.Files, want)
	}
	for i := range want {
		if plan.Files[i] != want[i] {
			t.Fatalf("Files = %#v, want %#v", plan.Files, want)
		}
	}
}

func TestPlanBuildContextRejectsSecretLookingFiles(t *testing.T) {
	secretFiles := []string{".env", "certs/prod.pem", "config/private.key", "secrets/token.txt"}

	for _, file := range secretFiles {
		if _, err := PlanBuildContext([]string{"main.go", file}, Dockerignore{}); err == nil {
			t.Fatalf("PlanBuildContext(%q) error = nil, want secret-looking file rejection", file)
		}
	}
}

func TestPlanBuildContextAllowsIgnoredSecretLookingFiles(t *testing.T) {
	dockerignore := ParseDockerignore(".env\nsecrets/\n")

	plan, err := PlanBuildContext([]string{"main.go", ".env", "secrets/token.txt"}, dockerignore)
	if err != nil {
		t.Fatalf("PlanBuildContext() error = %v", err)
	}
	if len(plan.Files) != 1 || plan.Files[0] != "main.go" {
		t.Fatalf("Files = %#v, want [main.go]", plan.Files)
	}
}
