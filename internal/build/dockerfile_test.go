package build

import "testing"

func TestParseDockerfileFromDigests(t *testing.T) {
	contents := `# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.23@sha256:builder AS build
RUN go test ./...
from alpine:3.20@sha256:runtime as runtime
FROM scratch
`

	records := ParseDockerfileFromDigests(contents)
	if len(records) != 2 {
		t.Fatalf("ParseDockerfileFromDigests() len = %d, want 2: %#v", len(records), records)
	}
	if records[0].Line != 2 || records[0].Image != "golang:1.23@sha256:builder" || records[0].Digest != "sha256:builder" || records[0].Stage != "build" {
		t.Fatalf("records[0] = %#v", records[0])
	}
	if records[1].Line != 4 || records[1].Stage != "runtime" {
		t.Fatalf("records[1] = %#v", records[1])
	}
}
