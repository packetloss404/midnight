package build

import (
	"strings"
	"testing"
	"time"
)

func TestBuildStatementProducesGoldenVector(t *testing.T) {
	started := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	finished := started.Add(5 * time.Minute)

	stmt, err := BuildStatement(
		BuildSubject{
			Name:   "registry.midnight.local/midnight/web",
			Digest: "1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f",
		},
		BuildPredicate{
			Builder:   BuildBuilder{ID: "https://midnight.dev/buildkit"},
			BuildType: "https://midnight.dev/buildkit/v1",
			Invocation: BuildInvocation{
				InvocationID: "build-01",
				StartedOn:    started,
				FinishedOn:   finished,
			},
			Materials: []BuildMaterial{
				{URI: "docker.io/library/alpine", Digest: "sha256:aaaa"},
				{URI: "docker.io/library/golang", Digest: "bbbb"},
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildStatement() error = %v", err)
	}

	data, err := MarshalProvenanceStatementJSON(stmt)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	const golden = `{
  "_type": "https://in-toto.io/Statement/v1",
  "subject": [
    {
      "name": "registry.midnight.local/midnight/web",
      "digest": {
        "sha256": "1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f"
      }
    }
  ],
  "predicateType": "https://slsa.dev/provenance/v1",
  "predicate": {
    "buildDefinition": {
      "buildType": "https://midnight.dev/buildkit/v1",
      "resolvedDependencies": [
        {
          "uri": "docker.io/library/alpine",
          "digest": {
            "sha256": "aaaa"
          }
        },
        {
          "uri": "docker.io/library/golang",
          "digest": {
            "sha256": "bbbb"
          }
        }
      ]
    },
    "runDetails": {
      "builder": {
        "id": "https://midnight.dev/buildkit"
      },
      "metadata": {
        "invocationID": "build-01",
        "startedOn": "2026-04-26T10:00:00Z",
        "finishedOn": "2026-04-26T10:05:00Z"
      }
    }
  }
}`
	if string(data) != golden {
		t.Fatalf("provenance JSON mismatch\n--got--\n%s\n--want--\n%s", string(data), golden)
	}

	// Round-trip: parsing the marshaled bytes and re-marshalling must
	// produce the identical document.
	parsed, err := UnmarshalProvenanceStatementJSON(data)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	roundTripped, err := MarshalProvenanceStatementJSON(parsed)
	if err != nil {
		t.Fatalf("Marshal(round-trip) error = %v", err)
	}
	if string(roundTripped) != golden {
		t.Fatalf("round-trip mismatch")
	}
}

func TestBuildStatementRejectsEmptyFields(t *testing.T) {
	tests := []struct {
		name      string
		subject   BuildSubject
		predicate BuildPredicate
		want      string
	}{
		{
			name:      "missing name",
			subject:   BuildSubject{Digest: "sha256:abcd"},
			predicate: BuildPredicate{Builder: BuildBuilder{ID: "id"}, BuildType: "bt"},
			want:      "subject name",
		},
		{
			name:      "missing digest",
			subject:   BuildSubject{Name: "registry/x"},
			predicate: BuildPredicate{Builder: BuildBuilder{ID: "id"}, BuildType: "bt"},
			want:      "subject digest",
		},
		{
			name:      "missing builder",
			subject:   BuildSubject{Name: "x", Digest: "sha256:abcd"},
			predicate: BuildPredicate{BuildType: "bt"},
			want:      "builder id",
		},
		{
			name:      "missing buildType",
			subject:   BuildSubject{Name: "x", Digest: "sha256:abcd"},
			predicate: BuildPredicate{Builder: BuildBuilder{ID: "id"}},
			want:      "buildType",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildStatement(tt.subject, tt.predicate)
			if err == nil {
				t.Fatalf("BuildStatement() error = nil, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBuildStatementSortsMaterials(t *testing.T) {
	stmt, err := BuildStatement(
		BuildSubject{Name: "n", Digest: "sha256:abcd"},
		BuildPredicate{
			Builder:   BuildBuilder{ID: "id"},
			BuildType: "bt",
			Materials: []BuildMaterial{
				{URI: "z", Digest: "sha256:1"},
				{URI: "a", Digest: "sha256:2"},
				{URI: "m", Digest: "sha256:3"},
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildStatement() error = %v", err)
	}
	deps := stmt.Predicate.BuildDefinition.ResolvedDependencies
	if len(deps) != 3 || deps[0].URI != "a" || deps[1].URI != "m" || deps[2].URI != "z" {
		t.Fatalf("dependencies not sorted: %#v", deps)
	}
}

func TestBuildStatementRejectsInvalidInvocationWindow(t *testing.T) {
	started := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	_, err := BuildStatement(
		BuildSubject{Name: "n", Digest: "sha256:abcd"},
		BuildPredicate{
			Builder:    BuildBuilder{ID: "id"},
			BuildType:  "bt",
			Invocation: BuildInvocation{StartedOn: started, FinishedOn: started.Add(-time.Minute)},
		},
	)
	if err == nil {
		t.Fatal("BuildStatement() error = nil, want refusal for finishedOn < startedOn")
	}
}

func TestBuildStatementAcceptsBareDigestHex(t *testing.T) {
	stmt, err := BuildStatement(
		BuildSubject{Name: "n", Digest: "abcd"},
		BuildPredicate{Builder: BuildBuilder{ID: "id"}, BuildType: "bt"},
	)
	if err != nil {
		t.Fatalf("BuildStatement() error = %v", err)
	}
	if stmt.Subject[0].Digest["sha256"] != "abcd" {
		t.Fatalf("digest map = %#v", stmt.Subject[0].Digest)
	}
}
